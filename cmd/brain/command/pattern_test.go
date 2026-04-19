package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRunPatternUsageHelpAndUnknownSubcommand(t *testing.T) {
	_, stderr, code := capturePatternIO(t, func() int {
		return RunPattern(nil, PatternDeps{})
	})
	if code != cli.ExitUsage {
		t.Fatalf("code=%d, want ExitUsage(%d)", code, cli.ExitUsage)
	}
	if !strings.Contains(stderr, "Usage: brain pattern <subcommand>") {
		t.Fatalf("stderr=%q, want usage text", stderr)
	}

	_, stderr, code = capturePatternIO(t, func() int {
		return RunPattern([]string{"unknown-subcommand"}, PatternDeps{})
	})
	if code != cli.ExitUsage {
		t.Fatalf("code=%d, want ExitUsage(%d)", code, cli.ExitUsage)
	}
	if !strings.Contains(stderr, `brain pattern: unknown subcommand "unknown-subcommand"`) {
		t.Fatalf("stderr=%q, want unknown subcommand error", stderr)
	}

	_, stderr, code = capturePatternIO(t, func() int {
		return RunPattern([]string{"help"}, PatternDeps{})
	})
	if code != cli.ExitOK {
		t.Fatalf("code=%d, want ExitOK(%d)", code, cli.ExitOK)
	}
	if !strings.Contains(stderr, "Subcommands:") {
		t.Fatalf("stderr=%q, want help text", stderr)
	}
}

func TestRunPatternExportImportDispatch(t *testing.T) {
	srcDSN := filepath.Join(t.TempDir(), "source.db")
	dstDSN := filepath.Join(t.TempDir(), "target.db")
	exportPath := filepath.Join(t.TempDir(), "patterns.json")

	mustUpsertPattern(t, srcDSN, &tool.UIPattern{
		ID:       "cli-export-auth",
		Category: "auth",
		Source:   "user",
		Enabled:  true,
		AppliesWhen: tool.MatchCondition{
			URLPattern: "/login",
		},
		ElementRoles: map[string]tool.ElementDescriptor{
			"submit": {CSS: "button[type=submit]"},
		},
		ActionSequence: []tool.ActionStep{
			{Tool: "browser.click", TargetRole: "submit"},
		},
	})

	_, stderr, code := capturePatternIO(t, func() int {
		return RunPattern([]string{
			"export",
			"--ids", "cli-export-auth",
			"--origin", "cli-test-pack",
			"-o", exportPath,
			"auth",
		}, patternDepsForDSN(srcDSN))
	})
	if code != cli.ExitOK {
		t.Fatalf("export code=%d, want ExitOK(%d), stderr=%q", code, cli.ExitOK, stderr)
	}
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var env tool.PatternExport
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if env.Origin != "cli-test-pack" {
		t.Fatalf("origin=%q, want cli-test-pack", env.Origin)
	}
	if env.Count != 1 || len(env.Patterns) != 1 || env.Patterns[0].ID != "cli-export-auth" {
		t.Fatalf("export envelope=%+v, want single cli-export-auth pattern", env)
	}

	stdout, stderr, code := capturePatternIO(t, func() int {
		return RunPattern([]string{
			"import",
			"--mode=merge",
			"--category", "auth",
			exportPath,
		}, patternDepsForDSN(dstDSN))
	})
	if code != cli.ExitOK {
		t.Fatalf("import code=%d, want ExitOK(%d), stderr=%q", code, cli.ExitOK, stderr)
	}
	if !strings.Contains(stdout, `"written": 1`) {
		t.Fatalf("stdout=%q, want import report with written=1", stdout)
	}

	lib, err := tool.NewPatternLibrary(dstDSN)
	if err != nil {
		t.Fatalf("open target library: %v", err)
	}
	defer lib.Close()
	got := lib.GetAny("cli-export-auth")
	if got == nil {
		t.Fatal("imported pattern missing from target library")
	}
	if got.Category != "auth" || got.Source != "user" {
		t.Fatalf("imported pattern=%+v, want auth/user", got)
	}
}

func TestRunPatternImportExitPaths(t *testing.T) {
	t.Run("missing file returns data err", func(t *testing.T) {
		_, stderr, code := capturePatternIO(t, func() int {
			return RunPattern([]string{"import", filepath.Join(t.TempDir(), "missing.json")}, patternDepsForDSN(filepath.Join(t.TempDir(), "unused.db")))
		})
		if code != cli.ExitDataErr {
			t.Fatalf("code=%d, want ExitDataErr(%d)", code, cli.ExitDataErr)
		}
		if !strings.Contains(stderr, "brain pattern import: read") {
			t.Fatalf("stderr=%q, want read error", stderr)
		}
	})

	t.Run("protected overwrite returns software", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "target.db")
		importPath := filepath.Join(t.TempDir(), "overwrite.json")

		mustUpsertPattern(t, dsn, &tool.UIPattern{
			ID:       "login_username_password",
			Category: "auth",
			Source:   "seed",
			Enabled:  true,
			AppliesWhen: tool.MatchCondition{
				URLPattern: "/login",
			},
			ElementRoles: map[string]tool.ElementDescriptor{
				"submit": {CSS: "button[type=submit]"},
			},
			ActionSequence: []tool.ActionStep{
				{Tool: "browser.click", TargetRole: "submit"},
			},
		})
		writePatternExportFile(t, importPath, tool.PatternExport{
			SchemaVersion: tool.PatternExportSchemaVersion,
			ExportedAt:    time.Now().UTC(),
			Count:         1,
			Patterns: []tool.UIPattern{
				{
					ID:          "login_username_password",
					Category:    "auth",
					Source:      "seed",
					Description: "override attempt",
					Enabled:     true,
					AppliesWhen: tool.MatchCondition{URLPattern: "/login"},
					ElementRoles: map[string]tool.ElementDescriptor{
						"submit": {CSS: "button[type=submit]"},
					},
					ActionSequence: []tool.ActionStep{
						{Tool: "browser.click", TargetRole: "submit"},
					},
				},
			},
		})

		stdout, stderr, code := capturePatternIO(t, func() int {
			return RunPattern([]string{"import", "--mode=overwrite", importPath}, patternDepsForDSN(dsn))
		})
		if code != cli.ExitSoftware {
			t.Fatalf("code=%d, want ExitSoftware(%d), stdout=%q stderr=%q", code, cli.ExitSoftware, stdout, stderr)
		}
		if !strings.Contains(stdout, `"rejected": 1`) || !strings.Contains(stdout, `"written": 0`) {
			t.Fatalf("stdout=%q, want rejected=1 and written=0 report", stdout)
		}
	})

	t.Run("dry run keeps ok exit even when nothing written", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "target.db")
		importPath := filepath.Join(t.TempDir(), "dry-run.json")

		mustUpsertPattern(t, dsn, &tool.UIPattern{
			ID:       "login_username_password",
			Category: "auth",
			Source:   "seed",
			Enabled:  true,
			AppliesWhen: tool.MatchCondition{
				URLPattern: "/login",
			},
			ElementRoles: map[string]tool.ElementDescriptor{
				"submit": {CSS: "button[type=submit]"},
			},
			ActionSequence: []tool.ActionStep{
				{Tool: "browser.click", TargetRole: "submit"},
			},
		})
		writePatternExportFile(t, importPath, tool.PatternExport{
			SchemaVersion: tool.PatternExportSchemaVersion,
			ExportedAt:    time.Now().UTC(),
			Count:         1,
			Patterns: []tool.UIPattern{
				{
					ID:          "login_username_password",
					Category:    "auth",
					Source:      "seed",
					Description: "dry-run override attempt",
					Enabled:     true,
					AppliesWhen: tool.MatchCondition{URLPattern: "/login"},
					ElementRoles: map[string]tool.ElementDescriptor{
						"submit": {CSS: "button[type=submit]"},
					},
					ActionSequence: []tool.ActionStep{
						{Tool: "browser.click", TargetRole: "submit"},
					},
				},
			},
		})

		stdout, stderr, code := capturePatternIO(t, func() int {
			return RunPattern([]string{"import", "--mode=dry-run", importPath}, patternDepsForDSN(dsn))
		})
		if code != cli.ExitOK {
			t.Fatalf("code=%d, want ExitOK(%d), stdout=%q stderr=%q", code, cli.ExitOK, stdout, stderr)
		}
		if !strings.Contains(stdout, `"mode": "dry-run"`) || !strings.Contains(stdout, `"written": 0`) {
			t.Fatalf("stdout=%q, want dry-run report with written=0", stdout)
		}
	})
}

func patternDepsForDSN(dsn string) PatternDeps {
	return PatternDeps{
		NewLibrary: func() (*tool.PatternLibrary, error) {
			return tool.NewPatternLibrary(dsn)
		},
	}
}

func mustUpsertPattern(t *testing.T, dsn string, p *tool.UIPattern) {
	t.Helper()
	lib, err := tool.NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("open pattern library: %v", err)
	}
	defer lib.Close()
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("upsert pattern %q: %v", p.ID, err)
	}
}

func writePatternExportFile(t *testing.T, path string, env tool.PatternExport) {
	t.Helper()
	blob, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal export envelope: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatalf("write export envelope: %v", err)
	}
}

func capturePatternIO(t *testing.T, fn func() int) (stdout string, stderr string, code int) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr

	stdoutFile, err := os.CreateTemp(t.TempDir(), "pattern-stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "pattern-stderr-*")
	if err != nil {
		t.Fatalf("create stderr temp: %v", err)
	}

	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		stdoutFile.Close()
		stderrFile.Close()
	}()

	code = fn()

	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	if _, err := stderrFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stderr: %v", err)
	}
	stdoutBytes, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout temp: %v", err)
	}
	stderrBytes, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatalf("read stderr temp: %v", err)
	}
	return string(stdoutBytes), string(stderrBytes), code
}
