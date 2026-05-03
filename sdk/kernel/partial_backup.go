// partial_backup.go — Replan 触发时的 partial 文件备份/恢复
//
// 设计动机:
//   sub agent 被 abort 时可能留下半成品文件(如 server/handler.go 写到 30%
//   就被打断)。Replan 不应该让新 sub 看到这些半成品(LLM 容易接续错误的代码)。
//
//   方案:abort 时把 partial 文件移动到 .brain/partial/<run_id>/<original_path>,
//   工作目录恢复干净状态。用户后悔可用 /restore <run_id> 恢复。
//
// 调用关系:
//   PlanOrchestrator.snapshotState 收集 PartialFiles 后,调 BackupPartialFiles
//   chat /restore <run_id> 命令调 RestorePartialFiles
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §3.5

package kernel

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PartialBackupRoot 默认的 partial 文件备份根目录(相对工作目录)。
// 使用 .brain 前缀让 git ignore 它(若用户工作目录是 git repo)。
const PartialBackupRoot = ".brain/partial"

// BackupPartialFiles 把指定 runID 的所有 partial 文件移动到备份目录。
//
// 操作逻辑:
//  1. 创建 backupDir = workdir/.brain/partial/<runID>/
//  2. 对每个 file:
//     - 计算 backupPath = backupDir/<相对 workdir 的路径>
//     - mkdir -p backupPath 父目录
//     - os.Rename file → backupPath(原子,跨 fs 失败时退化为 copy+remove)
//  3. 返回成功备份的文件数 + 错误数
//
// 失败处理:单个文件失败不阻塞其他文件,记录到返回的 err map。
// 工作目录之外的文件路径会被跳过(安全检查)。
func BackupPartialFiles(workdir, runID string, files []string) (backedUp int, errs map[string]error) {
	errs = make(map[string]error)
	if workdir == "" || runID == "" || len(files) == 0 {
		return 0, errs
	}

	backupDir := filepath.Join(workdir, PartialBackupRoot, runID)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		errs["__mkdir__"] = fmt.Errorf("create backup dir %q: %w", backupDir, err)
		return 0, errs
	}

	for _, raw := range files {
		// 安全:解析为绝对路径并校验在 workdir 内
		absPath := raw
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(workdir, raw)
		}
		clean := filepath.Clean(absPath)
		rel, err := filepath.Rel(workdir, clean)
		if err != nil || filepath.IsAbs(rel) || hasParentTraversal(rel) {
			errs[raw] = fmt.Errorf("file %q outside workdir, skipped", raw)
			continue
		}

		// 原文件不存在 → 跳过(可能 sub 已自己清理)
		if _, err := os.Stat(clean); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs[raw] = fmt.Errorf("stat: %w", err)
			continue
		}

		dst := filepath.Join(backupDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs[raw] = fmt.Errorf("mkdir parent: %w", err)
			continue
		}
		if err := moveFile(clean, dst); err != nil {
			errs[raw] = err
			continue
		}
		backedUp++
	}
	return backedUp, errs
}

// RestorePartialFiles 把 backupDir/<runID>/ 下所有文件恢复到工作目录原位置。
//
// 操作逻辑:
//  1. backupDir = workdir/.brain/partial/<runID>/
//  2. 遍历 backupDir 下所有文件
//  3. 对每个 backup file:
//     - 计算原路径 = workdir/<相对 backupDir 的路径>
//     - 如果原路径已存在(用户已修改) → 跳过 + 记 error
//     - 否则 mv backup → 原路径
//  4. 全部成功后删除 backupDir
//
// 失败处理:同 Backup,单个失败不阻塞。
func RestorePartialFiles(workdir, runID string) (restored int, errs map[string]error) {
	errs = make(map[string]error)
	if workdir == "" || runID == "" {
		return 0, errs
	}

	backupDir := filepath.Join(workdir, PartialBackupRoot, runID)
	if _, err := os.Stat(backupDir); err != nil {
		if os.IsNotExist(err) {
			errs["__missing__"] = fmt.Errorf("no backup found for run %s", runID)
		} else {
			errs["__stat__"] = err
		}
		return 0, errs
	}

	// 收集要 restore 的文件
	var toRestore []string
	walkErr := filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		toRestore = append(toRestore, path)
		return nil
	})
	if walkErr != nil {
		errs["__walk__"] = walkErr
		return 0, errs
	}

	for _, src := range toRestore {
		rel, err := filepath.Rel(backupDir, src)
		if err != nil {
			errs[src] = err
			continue
		}
		dst := filepath.Join(workdir, rel)
		// 如果原位置已有文件,不覆盖(用户可能已自己改过)
		if _, err := os.Stat(dst); err == nil {
			errs[rel] = fmt.Errorf("destination %q exists, not overwriting", rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs[rel] = fmt.Errorf("mkdir parent: %w", err)
			continue
		}
		if err := moveFile(src, dst); err != nil {
			errs[rel] = err
			continue
		}
		restored++
	}

	// 全部成功才删除 backupDir,有失败的保留供再试
	if len(errs) == 0 {
		_ = os.RemoveAll(backupDir)
	}
	return restored, errs
}

// ListPartialBackups 列出工作目录下所有 partial 备份的 runID。
// 给 chat /restore 命令在用户没指定 runID 时提供选项。
func ListPartialBackups(workdir string) ([]string, error) {
	root := filepath.Join(workdir, PartialBackupRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 没有备份 = 空列表
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// moveFile 跨 fs 安全移动文件:先尝试 os.Rename(同 fs 原子),
// 失败则 fallback 为 copy + remove(跨 fs 场景)。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// fallback: copy + remove
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy %q to %q: %w", src, dst, err)
	}
	if err := os.Remove(src); err != nil {
		// dst 已成功,记 warning 但不报错
		return nil
	}
	return nil
}

// copyFile 简单文件复制。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	stat, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, stat.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// hasParentTraversal 检测 rel 路径是否含 ".." 父目录穿越尝试。
func hasParentTraversal(rel string) bool {
	parts := splitPath(rel)
	for _, p := range parts {
		if p == ".." {
			return true
		}
	}
	return false
}

// splitPath 跨平台拆分路径(用 filepath.Separator)。
func splitPath(p string) []string {
	var parts []string
	for {
		dir, file := filepath.Split(p)
		if file != "" {
			parts = append([]string{file}, parts...)
		}
		if dir == "" || dir == string(filepath.Separator) {
			break
		}
		p = filepath.Clean(dir)
		if p == "." || p == string(filepath.Separator) {
			break
		}
	}
	return parts
}
