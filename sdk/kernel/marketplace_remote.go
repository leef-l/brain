package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RemoteMarketplace 实现 Marketplace 接口，从远程 HTTP API 查询 package 索引。
// 内置本地缓存：首次查询从远程拉取，后续使用缓存直到过期。
type RemoteMarketplace struct {
	endpoint    string
	apiKey      string
	httpClient  *http.Client
	cachePath   string
	cacheMaxAge time.Duration

	mu    sync.RWMutex
	cache *MarketplaceIndex
}

// RemoteMarketplaceConfig 配置远程 Marketplace。
type RemoteMarketplaceConfig struct {
	Endpoint    string        // 远程 API 地址，如 "https://marketplace.example.com/api/v1"
	APIKey      string        // Bearer token（可选）
	CachePath   string        // 本地缓存路径，默认 ~/.brain/marketplace/cache.json
	CacheMaxAge time.Duration // 缓存有效期，默认 24h
	Timeout     time.Duration // HTTP 超时，默认 30s
}

// NewRemoteMarketplace 创建远程 Marketplace 客户端。
func NewRemoteMarketplace(cfg RemoteMarketplaceConfig) *RemoteMarketplace {
	if cfg.CachePath == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "."
		}
		cfg.CachePath = filepath.Join(home, ".brain", "marketplace", "cache.json")
	}
	if cfg.CacheMaxAge <= 0 {
		cfg.CacheMaxAge = 24 * time.Hour
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &RemoteMarketplace{
		endpoint:    strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:      cfg.APIKey,
		httpClient:  &http.Client{Timeout: timeout},
		cachePath:   cfg.CachePath,
		cacheMaxAge: cfg.CacheMaxAge,
	}
}

func (m *RemoteMarketplace) Search(ctx context.Context, query string) ([]MarketplaceEntry, error) {
	idx, err := m.ensureIndex(ctx, false)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var results []MarketplaceEntry
	for _, e := range idx.Entries {
		if fuzzyMatch(q, e) {
			results = append(results, e)
		}
	}
	return results, nil
}

func (m *RemoteMarketplace) Get(ctx context.Context, packageID string) (*MarketplaceEntry, error) {
	idx, err := m.ensureIndex(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range idx.Entries {
		if idx.Entries[i].PackageID == packageID {
			return &idx.Entries[i], nil
		}
	}
	return nil, fmt.Errorf("package %q not found", packageID)
}

func (m *RemoteMarketplace) List(ctx context.Context, filter MarketplaceFilter) ([]MarketplaceEntry, error) {
	idx, err := m.ensureIndex(ctx, false)
	if err != nil {
		return nil, err
	}
	var results []MarketplaceEntry
	for _, e := range idx.Entries {
		if matchFilter(e, filter) {
			results = append(results, e)
		}
	}
	return results, nil
}

// Sync 强制从远程拉取索引并更新本地缓存。返回条目数量。
func (m *RemoteMarketplace) Sync(ctx context.Context) (int, error) {
	idx, err := m.fetchRemote(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.cache = idx
	m.mu.Unlock()
	if err := m.saveCache(idx); err != nil {
		return len(idx.Entries), fmt.Errorf("save cache: %w (fetched %d entries)", err, len(idx.Entries))
	}
	return len(idx.Entries), nil
}

// ensureIndex 确保有可用的索引：优先内存缓存 → 文件缓存 → 远程拉取。
func (m *RemoteMarketplace) ensureIndex(ctx context.Context, force bool) (*MarketplaceIndex, error) {
	if !force {
		m.mu.RLock()
		if m.cache != nil && time.Since(m.cache.UpdatedAt) < m.cacheMaxAge {
			idx := m.cache
			m.mu.RUnlock()
			return idx, nil
		}
		m.mu.RUnlock()
	}

	// 尝试文件缓存
	if !force {
		if idx, err := m.loadCache(); err == nil && time.Since(idx.UpdatedAt) < m.cacheMaxAge {
			m.mu.Lock()
			m.cache = idx
			m.mu.Unlock()
			return idx, nil
		}
	}

	// 远程拉取
	idx, err := m.fetchRemote(ctx)
	if err != nil {
		// 远程失败时回退到过期缓存
		if cached, cacheErr := m.loadCache(); cacheErr == nil {
			m.mu.Lock()
			m.cache = cached
			m.mu.Unlock()
			return cached, nil
		}
		return nil, err
	}

	m.mu.Lock()
	m.cache = idx
	m.mu.Unlock()
	m.saveCache(idx)
	return idx, nil
}

func (m *RemoteMarketplace) fetchRemote(ctx context.Context) (*MarketplaceIndex, error) {
	if m.endpoint == "" {
		return nil, fmt.Errorf("remote marketplace endpoint not configured")
	}

	u, err := url.JoinPath(m.endpoint, "packages", "index")
	if err != nil {
		return nil, fmt.Errorf("build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch marketplace index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("marketplace API returned %d: %s", resp.StatusCode, string(body))
	}

	var idx MarketplaceIndex
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return nil, fmt.Errorf("decode marketplace index: %w", err)
	}
	if idx.UpdatedAt.IsZero() {
		idx.UpdatedAt = time.Now().UTC()
	}
	return &idx, nil
}

func (m *RemoteMarketplace) loadCache() (*MarketplaceIndex, error) {
	data, err := os.ReadFile(m.cachePath)
	if err != nil {
		return nil, err
	}
	var idx MarketplaceIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

func (m *RemoteMarketplace) saveCache(idx *MarketplaceIndex) error {
	dir := filepath.Dir(m.cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.cachePath, data, 0o600)
}

var _ Marketplace = (*RemoteMarketplace)(nil)
