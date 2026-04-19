package kernel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// RemoteEndpoint 描述一个远程 brain 服务端点。
type RemoteEndpoint struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Weight int    `json:"weight,omitempty"`
}

func (e RemoteEndpoint) Address() string {
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// ServiceDiscovery 定义服务发现接口。
// 实现可以是 DNS SRV、Consul、etcd、静态列表等。
type ServiceDiscovery interface {
	Resolve(ctx context.Context, serviceName string) ([]RemoteEndpoint, error)
}

// DNSDiscovery 基于 DNS SRV 记录的服务发现。
// 查询格式：_brain-{kind}._tcp.{domain}
type DNSDiscovery struct {
	Domain   string
	resolver *net.Resolver
}

func NewDNSDiscovery(domain string) *DNSDiscovery {
	return &DNSDiscovery{
		Domain:   domain,
		resolver: net.DefaultResolver,
	}
}

func (d *DNSDiscovery) Resolve(ctx context.Context, serviceName string) ([]RemoteEndpoint, error) {
	srvName := fmt.Sprintf("_brain-%s._tcp.%s", serviceName, d.Domain)
	_, addrs, err := d.resolver.LookupSRV(ctx, "", "", srvName)
	if err != nil {
		return nil, fmt.Errorf("dns discovery: lookup %s: %w", srvName, err)
	}
	endpoints := make([]RemoteEndpoint, 0, len(addrs))
	for _, addr := range addrs {
		endpoints = append(endpoints, RemoteEndpoint{
			Host:   addr.Target,
			Port:   int(addr.Port),
			Weight: int(addr.Weight),
		})
	}
	return endpoints, nil
}

// StaticDiscovery 静态端点列表，用于测试和简单部署。
type StaticDiscovery struct {
	endpoints map[string][]RemoteEndpoint
}

func NewStaticDiscovery(m map[string][]RemoteEndpoint) *StaticDiscovery {
	return &StaticDiscovery{endpoints: m}
}

func (s *StaticDiscovery) Resolve(_ context.Context, serviceName string) ([]RemoteEndpoint, error) {
	eps, ok := s.endpoints[serviceName]
	if !ok || len(eps) == 0 {
		return nil, fmt.Errorf("static discovery: no endpoints for %q", serviceName)
	}
	return eps, nil
}

// CircuitState 熔断器状态。
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // 正常
	CircuitOpen                         // 熔断（拒绝请求）
	CircuitHalfOpen                     // 半开（允许试探）
)

// CircuitBreaker 实现简单的熔断器模式。
type CircuitBreaker struct {
	failureThreshold int
	successThreshold int
	timeout          time.Duration

	mu               sync.Mutex
	state            CircuitState
	failureCount     int
	successCount     int
	lastFailureTime  time.Time
}

func NewCircuitBreaker(failureThreshold, successThreshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
		state:            CircuitClosed,
	}
}

// Allow 检查是否允许请求通过。
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(cb.lastFailureTime) > cb.timeout {
			cb.state = CircuitHalfOpen
			cb.successCount = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	}
	return true
}

// RecordSuccess 记录成功调用。
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	if cb.state == CircuitHalfOpen {
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = CircuitClosed
		}
	}
}

// RecordFailure 记录失败调用。
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()
	if cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitOpen
	}
}

// State 返回当前状态。
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == CircuitOpen && time.Since(cb.lastFailureTime) > cb.timeout {
		return CircuitHalfOpen
	}
	return cb.state
}

// DiscoverableBrainPool 在 RemoteBrainPool 之上增加服务发现和熔断能力。
type DiscoverableBrainPool struct {
	discovery ServiceDiscovery
	scheme    string
	apiKey    string
	timeout   time.Duration

	mu       sync.Mutex
	pools    map[agent.Kind]*RemoteBrainPool
	breakers map[agent.Kind]*CircuitBreaker
}

// NewDiscoverableBrainPool 创建支持服务发现的远程 BrainPool。
func NewDiscoverableBrainPool(discovery ServiceDiscovery, scheme, apiKey string, timeout time.Duration) *DiscoverableBrainPool {
	if scheme == "" {
		scheme = "http"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &DiscoverableBrainPool{
		discovery: discovery,
		scheme:    scheme,
		apiKey:    apiKey,
		timeout:   timeout,
		pools:     make(map[agent.Kind]*RemoteBrainPool),
		breakers:  make(map[agent.Kind]*CircuitBreaker),
	}
}

func (p *DiscoverableBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()
	cb, ok := p.breakers[kind]
	if !ok {
		cb = NewCircuitBreaker(3, 2, 30*time.Second)
		p.breakers[kind] = cb
	}
	p.mu.Unlock()

	if !cb.Allow() {
		return nil, fmt.Errorf("circuit breaker open for %s", kind)
	}

	// 尝试已有连接
	p.mu.Lock()
	if pool, ok := p.pools[kind]; ok {
		p.mu.Unlock()
		ag, err := pool.GetBrain(ctx, kind)
		if err == nil {
			cb.RecordSuccess()
			return ag, nil
		}
		cb.RecordFailure()
		// 连接失败，尝试重新发现
	} else {
		p.mu.Unlock()
	}

	// 服务发现
	endpoints, err := p.discovery.Resolve(ctx, string(kind))
	if err != nil {
		cb.RecordFailure()
		return nil, fmt.Errorf("discover %s: %w", kind, err)
	}
	if len(endpoints) == 0 {
		cb.RecordFailure()
		return nil, fmt.Errorf("no endpoints for %s", kind)
	}

	// 用第一个端点创建连接（简单轮转）
	ep := endpoints[0]
	endpoint := fmt.Sprintf("%s://%s", p.scheme, ep.Address())
	configs := []*RemoteBrainConfig{{
		Kind:     kind,
		Endpoint: endpoint,
		APIKey:   p.apiKey,
		Timeout:  p.timeout,
	}}
	pool, err := NewRemoteBrainPool(configs)
	if err != nil {
		cb.RecordFailure()
		return nil, err
	}

	ag, err := pool.GetBrain(ctx, kind)
	if err != nil {
		cb.RecordFailure()
		return nil, err
	}

	p.mu.Lock()
	p.pools[kind] = pool
	p.mu.Unlock()
	cb.RecordSuccess()
	return ag, nil
}

func (p *DiscoverableBrainPool) Status() map[agent.Kind]BrainStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[agent.Kind]BrainStatus)
	for kind, pool := range p.pools {
		for k, v := range pool.Status() {
			_ = k
			result[kind] = v
		}
	}
	return result
}

func (p *DiscoverableBrainPool) AutoStart(ctx context.Context) {
	// 服务发现模式下 AutoStart 是 no-op，按需连接
}

func (p *DiscoverableBrainPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	pools := make(map[agent.Kind]*RemoteBrainPool, len(p.pools))
	for k, v := range p.pools {
		pools[k] = v
	}
	p.pools = make(map[agent.Kind]*RemoteBrainPool)
	p.mu.Unlock()

	var lastErr error
	for _, pool := range pools {
		if err := pool.Shutdown(ctx); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
