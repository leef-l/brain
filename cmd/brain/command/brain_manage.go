package command

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"text/template"
	"unicode"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/kernel/manifest"
)

//go:embed templates/*
var templateFS embed.FS

// BrainManageDeps 是 brain brain 子命令所需的依赖。
type BrainManageDeps struct {
	// BrainsDir 返回 brain 安装根目录，通常为 ~/.brain/brains
	BrainsDir func() string
}

// RunBrainManage 分发 brain brain <subcommand>。
func RunBrainManage(args []string, deps BrainManageDeps) int {
	if len(args) == 0 {
		printBrainManageUsage()
		return cli.ExitUsage
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		return runBrainList(rest, deps)
	case "init":
		return runBrainInit(rest)
	case "install":
		return runBrainInstall(rest, deps)
	case "pack":
		return runBrainPack(rest)
	case "activate":
		return runBrainActivate(rest, deps)
	case "deactivate":
		return runBrainDeactivate(rest, deps)
	case "uninstall":
		return runBrainUninstall(rest, deps)
	case "upgrade":
		return runBrainUpgrade(rest, deps)
	case "rollback":
		return runBrainRollback(rest, deps)
	case "search":
		return runBrainSearch(rest, MarketplaceDeps{})
	case "info":
		return runBrainInfo(rest, MarketplaceDeps{})
	case "-h", "--help", "help":
		printBrainManageUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain brain: unknown subcommand %q\n", sub)
		printBrainManageUsage()
		return cli.ExitUsage
	}
}

func printBrainManageUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain brain <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  init         Initialize a new brain project from template")
	fmt.Fprintln(os.Stderr, "  list         List all installed brains")
	fmt.Fprintln(os.Stderr, "  install      Install a brain from a local path, kind, or .brainpkg file")
	fmt.Fprintln(os.Stderr, "  pack         Pack current directory into a .brainpkg file")
	fmt.Fprintln(os.Stderr, "  activate     Activate an installed brain")
	fmt.Fprintln(os.Stderr, "  deactivate   Deactivate an installed brain")
	fmt.Fprintln(os.Stderr, "  uninstall    Uninstall an installed brain")
	fmt.Fprintln(os.Stderr, "  upgrade      Upgrade an installed brain (not implemented yet)")
	fmt.Fprintln(os.Stderr, "  rollback     Rollback an installed brain (not implemented yet)")
	fmt.Fprintln(os.Stderr, "  search       Search marketplace for available brains")
	fmt.Fprintln(os.Stderr, "  info         Show detailed info for a marketplace package")
}

// brainEntry 代表列表中的一个 brain 条目。
type brainEntry struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	RuntimeType string `json:"runtime_type"`
}

func runBrainList(args []string, deps BrainManageDeps) int {
	fs := flag.NewFlagSet("brain brain list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	brainsDir := deps.BrainsDir()
	entries, err := os.ReadDir(brainsDir)
	if os.IsNotExist(err) {
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]interface{}{"brains": []brainEntry{}, "total": 0})
		} else {
			fmt.Println("No brains installed yet.")
			fmt.Fprintf(os.Stdout, "Install directory: %s\n", brainsDir)
		}
		return cli.ExitOK
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain brain list: %v\n", err)
		return cli.ExitSoftware
	}

	var brains []brainEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(brainsDir, e.Name())
		m, loadErr := manifest.LoadFromDir(dir)
		if loadErr != nil {
			continue
		}
		status := "inactive"
		if _, err := os.Stat(filepath.Join(dir, ".active")); err == nil {
			status = "active"
		}
		brains = append(brains, brainEntry{
			Kind:        m.Kind,
			Name:        m.Name,
			Version:     m.BrainVersion,
			Status:      status,
			RuntimeType: string(m.Runtime.Type),
		})
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]interface{}{"brains": brains, "total": len(brains)})
	} else {
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "KIND\tNAME\tVERSION\tSTATUS\tRUNTIME\n")
		for _, b := range brains {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", b.Kind, b.Name, b.Version, b.Status, b.RuntimeType)
		}
		tw.Flush()
		fmt.Fprintf(os.Stdout, "\n%d brain(s) installed.\n", len(brains))
	}
	return cli.ExitOK
}

func runBrainInstall(args []string, deps BrainManageDeps) int {
	fs := flag.NewFlagSet("brain brain install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain install <path-or-kind-or-.brainpkg>")
		return cli.ExitUsage
	}

	srcPath := fs.Arg(0)
	brainsDir := deps.BrainsDir()

	// 如果是 .brainpkg 文件，走 PackageInstaller 安装流程
	if strings.HasSuffix(srcPath, ".brainpkg") {
		if _, err := os.Stat(srcPath); err != nil {
			fmt.Fprintf(os.Stderr, "brain brain install: %q not found\n", srcPath)
			return cli.ExitUsage
		}
		installer := kernel.NewPackageInstaller()
		if err := installer.Install(srcPath, brainsDir); err != nil {
			fmt.Fprintf(os.Stderr, "brain brain install: %v\n", err)
			return cli.ExitSoftware
		}
		fmt.Println("Status: active")
		return cli.ExitOK
	}

	// 如果不是有效目录，当作 kind 尝试在当前目录的 brains/<kind> 下查找
	info, err := os.Stat(srcPath)
	if err != nil || !info.IsDir() {
		// 尝试在常见位置查找
		candidates := []string{
			filepath.Join("brains", srcPath),
			srcPath,
		}
		found := false
		for _, c := range candidates {
			if fi, ferr := os.Stat(c); ferr == nil && fi.IsDir() {
				srcPath = c
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "brain brain install: %q is not a valid directory or .brainpkg file\n", fs.Arg(0))
			return cli.ExitUsage
		}
	}

	// 加载并验证 manifest
	m, err := manifest.LoadFromDir(srcPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain brain install: %v\n", err)
		return cli.ExitDataErr
	}

	errs := manifest.Validate(m)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "brain brain install: manifest validation failed:\n")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
		}
		return cli.ExitDataErr
	}

	// 目标目录
	destDir := filepath.Join(brainsDir, m.Kind)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain install: create dir: %v\n", err)
		return cli.ExitSoftware
	}

	// 递归复制目录（支持子目录如 bin/、bindings/ 等）
	if err := copyDirRecursive(srcPath, destDir); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain install: %v\n", err)
		return cli.ExitSoftware
	}

	// 标记为 active
	activeFile := filepath.Join(destDir, ".active")
	if err := os.WriteFile(activeFile, []byte(""), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain install: create .active: %v\n", err)
		return cli.ExitSoftware
	}

	fmt.Printf("Installed brain %q (%s v%s) to %s\n", m.Kind, m.Name, m.BrainVersion, destDir)
	fmt.Println("Status: active")
	return cli.ExitOK
}

// runBrainPack 将当前目录打包为 .brainpkg 文件。
func runBrainPack(args []string) int {
	fs := flag.NewFlagSet("brain brain pack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	srcDir := "."
	if fs.NArg() > 0 {
		srcDir = fs.Arg(0)
	}

	installer := kernel.NewPackageInstaller()
	pkg, err := installer.Pack(srcDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain brain pack: %v\n", err)
		return cli.ExitSoftware
	}

	fmt.Printf("Package ID:      %s\n", pkg.PackageID)
	fmt.Printf("Version:         %s\n", pkg.PackageVersion)
	fmt.Printf("Files:           %d\n", len(pkg.Files))
	fmt.Printf("Checksum (SHA256): %s\n", pkg.Checksum)
	return cli.ExitOK
}

// runBrainUninstall 卸载一个已安装的 brain。
func runBrainUninstall(args []string, deps BrainManageDeps) int {
	fs := flag.NewFlagSet("brain brain uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain uninstall <kind>")
		return cli.ExitUsage
	}

	kind := fs.Arg(0)
	if err := validateBrainKind(kind); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain uninstall: %v\n", err)
		return cli.ExitUsage
	}
	installer := kernel.NewPackageInstaller()
	if err := installer.Uninstall(kind, deps.BrainsDir()); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain uninstall: %v\n", err)
		return cli.ExitSoftware
	}
	return cli.ExitOK
}

func runBrainActivate(args []string, deps BrainManageDeps) int {
	fs := flag.NewFlagSet("brain brain activate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain activate <kind>")
		return cli.ExitUsage
	}

	kind := fs.Arg(0)
	if err := validateBrainKind(kind); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain activate: %v\n", err)
		return cli.ExitUsage
	}
	brainDir := filepath.Join(deps.BrainsDir(), kind)

	if _, err := os.Stat(brainDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "brain brain activate: brain %q not installed\n", kind)
		return cli.ExitNotFound
	}

	activeFile := filepath.Join(brainDir, ".active")
	if _, err := os.Stat(activeFile); err == nil {
		fmt.Fprintf(os.Stderr, "brain brain activate: brain %q is already active\n", kind)
		return cli.ExitOK
	}

	if err := os.WriteFile(activeFile, []byte(""), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain activate: %v\n", err)
		return cli.ExitSoftware
	}

	fmt.Printf("Brain %q activated.\n", kind)
	return cli.ExitOK
}

func runBrainDeactivate(args []string, deps BrainManageDeps) int {
	fs := flag.NewFlagSet("brain brain deactivate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain deactivate <kind>")
		return cli.ExitUsage
	}

	kind := fs.Arg(0)
	if err := validateBrainKind(kind); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain deactivate: %v\n", err)
		return cli.ExitUsage
	}
	brainDir := filepath.Join(deps.BrainsDir(), kind)

	if _, err := os.Stat(brainDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "brain brain deactivate: brain %q not installed\n", kind)
		return cli.ExitNotFound
	}

	activeFile := filepath.Join(brainDir, ".active")
	if _, err := os.Stat(activeFile); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "brain brain deactivate: brain %q is already inactive\n", kind)
		return cli.ExitOK
	}

	if err := os.Remove(activeFile); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain deactivate: %v\n", err)
		return cli.ExitSoftware
	}

	fmt.Printf("Brain %q deactivated.\n", kind)
	return cli.ExitOK
}

func runBrainUpgrade(_ []string, _ BrainManageDeps) int {
	fmt.Fprintln(os.Stderr, "brain brain upgrade: not implemented yet")
	return cli.ExitOK
}

func runBrainRollback(_ []string, _ BrainManageDeps) int {
	fmt.Fprintln(os.Stderr, "brain brain rollback: not implemented yet")
	return cli.ExitOK
}

// copyDirRecursive 递归复制 src 目录内容到 dst 目录。
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		if relPath == "." {
			return nil
		}
		target := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// copyFile 复制单个文件，保留权限。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return nil
}

// brainInitData 是渲染模板时的上下文数据。
type brainInitData struct {
	Kind string // brain kind 标识符，如 "image"
	Name string // brain 显示名称，如 "Image Brain"
}

// runBrainInit 在当前目录生成新 brain 项目骨架。
func runBrainInit(args []string) int {
	fs := flag.NewFlagSet("brain brain init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain init <kind>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "在当前目录生成第三方专精大脑项目骨架。")
		fmt.Fprintln(os.Stderr, "  <kind>  brain 类型标识，如 image、mobile、security-audit")
		return cli.ExitUsage
	}

	kind := fs.Arg(0)
	if err := validateBrainKind(kind); err != nil {
		fmt.Fprintf(os.Stderr, "brain brain init: %v\n", err)
		return cli.ExitUsage
	}

	// 构建显示名称：首字母大写 + " Brain"
	name := upperFirst(kind) + " Brain"

	data := brainInitData{
		Kind: kind,
		Name: name,
	}

	// 要渲染的模板文件 -> 输出文件名
	files := map[string]string{
		"templates/brain.json.tmpl":  "brain.json",
		"templates/main.go.tmpl":     "main.go",
		"templates/handler.go.tmpl":  "handler.go",
	}

	for tmplPath, outName := range files {
		// 检查目标文件是否已存在
		if _, err := os.Stat(outName); err == nil {
			fmt.Fprintf(os.Stderr, "brain brain init: %s already exists, skipping\n", outName)
			continue
		}

		content, err := templateFS.ReadFile(tmplPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain brain init: read template %s: %v\n", tmplPath, err)
			return cli.ExitSoftware
		}

		tmpl, err := template.New(outName).Parse(string(content))
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain brain init: parse template %s: %v\n", tmplPath, err)
			return cli.ExitSoftware
		}

		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			fmt.Fprintf(os.Stderr, "brain brain init: execute template %s: %v\n", tmplPath, err)
			return cli.ExitSoftware
		}

		if err := os.WriteFile(outName, []byte(buf.String()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "brain brain init: write %s: %v\n", outName, err)
			return cli.ExitSoftware
		}
		fmt.Printf("  created %s\n", outName)
	}

	fmt.Printf("\nCreated brain project for %s. Run `go build` to compile.\n", kind)
	return cli.ExitOK
}

// upperFirst 将字符串首字母大写。
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// validateBrainKind 校验 kind 参数，防止路径遍历攻击。
// kind 不能为空、不能包含路径分隔符、不能是 ".." 或 "."。
func validateBrainKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("kind 不能为空")
	}
	if strings.ContainsAny(kind, "/\\") {
		return fmt.Errorf("kind %q 包含非法路径分隔符", kind)
	}
	if kind == ".." || kind == "." {
		return fmt.Errorf("kind %q 是非法路径", kind)
	}
	return nil
}

// brainManageDir 返回默认的 brain 安装目录。
func BrainManageDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".brain", "brains")
	}
	return filepath.Join(home, ".brain", "brains")
}
