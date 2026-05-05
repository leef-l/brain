package diaglog

import (
	"os"
	"sync"
)

// maxLogBytes 是 diagnostics 日志文件单文件容量上限,
// 达到后旋转一次到 path+".1"(覆盖旧 .1),保证占用 ≤ 2× maxLogBytes。
const maxLogBytes = 64 * 1024 * 1024 // 64 MiB

// rotatingFile 是带容量上限的 io.WriteCloser,只保留 1 个旋转副本。
//
// 写入语义:每次 Write 累计 size,超过 max 时:
//  1. 关闭当前 fd
//  2. rename(path, path+".1")(覆盖旧 .1)
//  3. 重新 open 新空文件
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func newRotatingFile(path string, max int64) *rotatingFile {
	rf := &rotatingFile{path: path, max: max}
	rf.openLocked()
	return rf
}

func (r *rotatingFile) openLocked() {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		r.f = nil
		r.size = 0
		return
	}
	r.f = f
	if st, err := f.Stat(); err == nil {
		r.size = st.Size()
	}
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		r.openLocked()
		if r.f == nil {
			return len(p), nil // best-effort:打不开 fd 时静默丢,不阻塞业务
		}
	}
	if r.size+int64(len(p)) > r.max {
		r.rotateLocked()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotateLocked() {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	_ = os.Rename(r.path, r.path+".1")
	r.openLocked()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
