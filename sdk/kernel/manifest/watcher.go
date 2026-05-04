// watcher.go — Manifest 文件变更监听
//
// 目的:开发期 / 第三方 brain 热更新场景下,brain.json 修改后无需重启 kernel,
// Watcher 检测变更后自动 reload + 通知 registry 更新。
//
// 实现选择:走 mtime 轮询而非 fsnotify。原因:
//   1. 仓库当前不依赖 fsnotify,引入新依赖需要 go mod tidy 在离线环境麻烦
//   2. brain.json 不是高频更新场景,轮询足够(2s 间隔可感知 < 5s 内变更)
//   3. 跨平台无需写 darwin/linux/windows 三套代码
//
// 使用:
//
//	w := NewWatcher(2 * time.Second)
//	w.OnChange(func(m *manifest.Manifest, sourcePath string) {
//	    registry.Replace(m)
//	})
//	w.Watch("/path/to/brain.json")  // 可多次调用监听多个文件
//	defer w.Stop()
//
// 文件不存在 / 解析失败时,记录错误但不退出 goroutine,等待文件恢复。

package manifest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ChangeHandler 是文件变更回调,接收新解析的 Manifest 和源路径。
// 当解析失败但需要通知调用方时,m 为 nil,err 不为 nil。
type ChangeHandler func(m *Manifest, sourcePath string, err error)

// Watcher 监听一组 manifest 文件的 mtime 变化,变更时调用 ChangeHandler。
type Watcher struct {
	interval time.Duration

	mu       sync.Mutex
	tracked  map[string]int64 // path → 上次 mtime UnixNano
	handler  ChangeHandler
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// NewWatcher 创建 Watcher。interval 是轮询间隔,< 100ms 时强制为 100ms 避免空转。
func NewWatcher(interval time.Duration) *Watcher {
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	return &Watcher{
		interval: interval,
		tracked:  make(map[string]int64),
	}
}

// OnChange 注册回调。同一时间只允许一个 handler;再次调用覆盖旧值。
func (w *Watcher) OnChange(h ChangeHandler) {
	w.mu.Lock()
	w.handler = h
	w.mu.Unlock()
}

// Watch 加入要监听的文件。可在 Start 之前或之后调用。
// 路径不存在时记录初始 mtime=0,等待文件出现时也会触发 ChangeHandler。
func (w *Watcher) Watch(path string) {
	if path == "" {
		return
	}
	w.mu.Lock()
	if _, ok := w.tracked[path]; !ok {
		w.tracked[path] = 0 // 等首次轮询时填充实际 mtime
	}
	w.mu.Unlock()
}

// Unwatch 移除监听。
func (w *Watcher) Unwatch(path string) {
	w.mu.Lock()
	delete(w.tracked, path)
	w.mu.Unlock()
}

// Start 启动后台 goroutine 开始轮询。重复调用是 no-op(只启一次)。
// 阻塞期间(典型 < 1ms)只持锁拍快照,真正的 stat / 解析 / 回调在锁外执行。
func (w *Watcher) Start(parent context.Context) {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return // 已启动
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.mu.Unlock()

	go w.loop(ctx)
}

// Stop 停止 watcher。可多次调用,只生效一次。
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		if w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		w.mu.Unlock()
	})
}

func (w *Watcher) loop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.scan()
		}
	}
}

// scan 拍快照后逐个 stat 比较 mtime,变更则解析 + 回调。
func (w *Watcher) scan() {
	w.mu.Lock()
	paths := make([]string, 0, len(w.tracked))
	prev := make(map[string]int64, len(w.tracked))
	for p, t := range w.tracked {
		paths = append(paths, p)
		prev[p] = t
	}
	handler := w.handler
	w.mu.Unlock()

	for _, p := range paths {
		newMtime, exists := statMtime(p)
		oldMtime := prev[p]

		if !exists {
			// 文件被删除,且我们之前见过它
			if oldMtime != 0 {
				w.updateMtime(p, 0)
				if handler != nil {
					handler(nil, p, errors.New("manifest file removed"))
				}
			}
			continue
		}

		if newMtime == oldMtime {
			continue // 无变化
		}

		// 变更:解析新内容
		w.updateMtime(p, newMtime)
		m, err := LoadFromFile(p)
		if handler != nil {
			handler(m, p, err)
		}
	}
}

func (w *Watcher) updateMtime(path string, mtime int64) {
	w.mu.Lock()
	if _, ok := w.tracked[path]; ok { // 仅当仍在监听中才更新
		w.tracked[path] = mtime
	}
	w.mu.Unlock()
}

// statMtime 返回文件 mtime UnixNano,文件不存在返回 (_, false)。
// 其他 stat 错误(权限等)视为不存在,不暴露细节给上层。
func statMtime(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if info.IsDir() {
		return 0, false
	}
	return info.ModTime().UnixNano(), true
}

// 兼容性占位:即便不引入 fmt 也保留可读 String 视图供 debug。
func (w *Watcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fmt.Sprintf("Watcher{interval=%s, tracking=%d files}", w.interval, len(w.tracked))
}
