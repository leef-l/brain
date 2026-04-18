package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/persistence"
)

type DoctorDeps struct {
	ConfigPath  func() string
	LoadConfig  func() (*config.Config, error)
	NewRuntime  func(brainKind string) (*cliruntime.Runtime, error)
	BinResolver func() func(kind agent.Kind) (string, error)
}

type checkStatus int

const (
	checkOK checkStatus = iota
	checkFail
	checkSkip
)

type checkResult struct {
	name   string
	status checkStatus
	msg    string
	hint   string
}

func RunDoctor(args []string, deps DoctorDeps) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fixFlag := fs.Bool("fix", false, "attempt to repair issues when supported")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if *fixFlag {
		fmt.Fprintf(os.Stderr, "brain doctor: --fix has no automatic repairs in v%s\n", brain.CLIVersion)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "brain doctor: unexpected argument %q\n", fs.Arg(0))
		return cli.ExitUsage
	}

	fmt.Println("Checking brain environment...")
	fmt.Println()

	results := []checkResult{
		checkWorkspace(),
		checkConfigFile(deps),
		checkDatabase(deps),
		checkCredentials(deps),
		checkSidecars(deps),
		checkLLMReachable(deps),
		checkDiskSpace(deps),
		checkClockDrift(),
	}

	failed := 0
	skipped := 0
	for _, r := range results {
		switch r.status {
		case checkOK:
			fmt.Printf("✓ %s: %s\n", r.name, r.msg)
		case checkFail:
			fmt.Printf("✗ %s: %s\n", r.name, r.msg)
			if r.hint != "" {
				fmt.Printf("  → %s\n", r.hint)
			}
			failed++
		case checkSkip:
			fmt.Printf("- %s: skipped (%s)\n", r.name, r.msg)
			skipped++
		}
	}

	fmt.Println()
	switch {
	case failed > 0:
		fmt.Printf("%d issue(s) found", failed)
		if skipped > 0 {
			fmt.Printf(", %d skipped", skipped)
		}
		fmt.Println(". Run with --fix to attempt repair where supported.")
		return cli.ExitFailed
	case skipped > 0:
		fmt.Printf("All active checks passed (%d skipped in current build).\n", skipped)
	default:
		fmt.Println("All checks passed.")
	}
	return cli.ExitOK
}

func checkWorkspace() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{"workspace", checkFail, err.Error(), "set $HOME"}
	}
	ws := filepath.Join(home, ".brain")
	st, err := os.Stat(ws)
	if os.IsNotExist(err) {
		return checkResult{"workspace", checkOK, fmt.Sprintf("%s (not present, will be created on first run)", ws), ""}
	}
	if err != nil {
		return checkResult{"workspace", checkFail, err.Error(), ""}
	}
	if !st.IsDir() {
		return checkResult{"workspace", checkFail, ws + " exists but is not a directory", ""}
	}
	probe, err := os.CreateTemp(ws, ".brain-doctor-*")
	if err != nil {
		return checkResult{"workspace", checkFail, "not writable: " + err.Error(), "chmod u+w " + ws}
	}
	name := probe.Name()
	probe.Close()
	_ = os.Remove(name)
	return checkResult{"workspace", checkOK, fmt.Sprintf("%s (writable)", ws), ""}
}

func checkConfigFile(deps DoctorDeps) checkResult {
	path := deps.ConfigPath()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return checkResult{name: "config", status: checkOK, msg: fmt.Sprintf("%s (not present yet)", path)}
	}
	if err != nil {
		return checkResult{name: "config", status: checkFail, msg: err.Error()}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return checkResult{name: "config", status: checkFail, msg: fmt.Sprintf("%s permissions are %04o", path, info.Mode().Perm()), hint: "chmod 600 " + path}
	}
	if _, err := deps.LoadConfig(); err != nil {
		return checkResult{name: "config", status: checkFail, msg: err.Error()}
	}
	return checkResult{name: "config", status: checkOK, msg: fmt.Sprintf("%s (valid)", path)}
}

func checkDatabase(deps DoctorDeps) checkResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rt, err := deps.NewRuntime("central")
	if err != nil {
		return checkResult{"database", checkFail, "open runtime: " + err.Error(), ""}
	}
	if rt.Kernel == nil || rt.Kernel.PlanStore == nil || rt.Kernel.ArtifactStore == nil || rt.RunStore == nil {
		return checkResult{"database", checkFail, "runtime initialized with missing stores", ""}
	}
	snap, _ := json.Marshal(map[string]string{"probe": "doctor"})
	plan := &persistence.BrainPlan{
		BrainID:      "doctor",
		Version:      1,
		CurrentState: snap,
	}
	id, err := rt.Kernel.PlanStore.Create(ctx, plan)
	if err != nil {
		return checkResult{"database", checkFail, "PlanStore.Create: " + err.Error(), ""}
	}
	got, err := rt.Kernel.PlanStore.Get(ctx, id)
	if err != nil {
		return checkResult{"database", checkFail, "PlanStore.Get: " + err.Error(), ""}
	}
	if got == nil || got.ID != id {
		return checkResult{"database", checkFail, "PlanStore.Get returned nil or mismatched plan", ""}
	}
	if err := rt.Kernel.PlanStore.Archive(ctx, id); err != nil {
		return checkResult{"database", checkFail, "PlanStore.Archive: " + err.Error(), ""}
	}
	return checkResult{
		name:   "database",
		status: checkOK,
		msg:    fmt.Sprintf("file-backed runtime OK (plan=%d)", id),
	}
}

func checkCredentials(deps DoctorDeps) checkResult {
	cfg, err := deps.LoadConfig()
	if err != nil {
		return checkResult{name: "credentials", status: checkFail, msg: err.Error()}
	}
	if cfg == nil {
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			return checkResult{name: "credentials", status: checkOK, msg: "ANTHROPIC_API_KEY (set)"}
		}
		return checkResult{name: "credentials", status: checkSkip, msg: "no config file and no ANTHROPIC_API_KEY"}
	}
	resolved, err := provider.ResolveWithInput(cfg, "central", nil, "", "", "", "")
	if err != nil {
		return checkResult{name: "credentials", status: checkFail, msg: err.Error(), hint: "brain config set providers.<name>.api_key <key>"}
	}
	if strings.TrimSpace(resolved.APIKey) == "" {
		return checkResult{name: "credentials", status: checkFail, msg: "no API key configured", hint: "brain config set providers." + resolved.Name + ".api_key <key>"}
	}
	return checkResult{name: "credentials", status: checkOK, msg: fmt.Sprintf("%s credentials resolved", resolved.Name)}
}

func checkSidecars(deps DoctorDeps) checkResult {
	resolve := deps.BinResolver()
	required := []agent.Kind{
		agent.KindCentral,
		agent.KindCode,
		agent.KindBrowser,
		agent.KindVerifier,
		agent.KindFault,
	}
	var missing []string
	for _, kind := range required {
		if _, err := resolve(kind); err != nil {
			missing = append(missing, string(kind))
		}
	}
	if len(missing) > 0 {
		return checkResult{
			name:   "sidecars",
			status: checkFail,
			msg:    "missing: " + strings.Join(missing, ", "),
			hint:   "rebuild bin/ or place sidecar binaries next to brain",
		}
	}
	return checkResult{name: "sidecars", status: checkOK, msg: "all built-in sidecars found"}
}

func checkLLMReachable(deps DoctorDeps) checkResult {
	cfg, err := deps.LoadConfig()
	if err != nil {
		return checkResult{name: "llm reachable", status: checkFail, msg: err.Error()}
	}
	if cfg == nil {
		return checkResult{name: "llm reachable", status: checkSkip, msg: "no config file"}
	}
	resolved, err := provider.ResolveWithInput(cfg, "central", nil, "", "", "", "")
	if err != nil {
		return checkResult{name: "llm reachable", status: checkSkip, msg: err.Error()}
	}
	if strings.TrimSpace(resolved.BaseURL) == "" {
		return checkResult{name: "llm reachable", status: checkSkip, msg: "no provider base_url configured"}
	}
	u, err := url.Parse(resolved.BaseURL)
	if err != nil {
		return checkResult{name: "llm reachable", status: checkFail, msg: "invalid base_url: " + err.Error()}
	}
	host := u.Host
	if host == "" {
		return checkResult{name: "llm reachable", status: checkFail, msg: "invalid base_url host"}
	}
	if !strings.Contains(host, ":") {
		switch u.Scheme {
		case "http":
			host += ":80"
		default:
			host += ":443"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return checkResult{name: "llm reachable", status: checkFail, msg: err.Error()}
	}
	_ = conn.Close()
	return checkResult{name: "llm reachable", status: checkOK, msg: fmt.Sprintf("%s (%dms)", resolved.Name, time.Since(start).Milliseconds())}
}

func checkDiskSpace(deps DoctorDeps) checkResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rt, err := deps.NewRuntime("central")
	if err != nil {
		return checkResult{"disk space", checkFail, "open runtime: " + err.Error(), ""}
	}
	if rt.Kernel.ArtifactStore == nil {
		return checkResult{"disk space", checkFail, "runtime returned nil ArtifactStore", ""}
	}
	payload := []byte("brain doctor CAS probe")
	ref, err := rt.Kernel.ArtifactStore.Put(ctx, 0, persistence.Artifact{
		Kind:    "doctor-probe",
		Content: payload,
		Caption: "brain doctor smoke probe",
	})
	if err != nil {
		return checkResult{"disk space", checkFail, "ArtifactStore.Put: " + err.Error(), ""}
	}
	ok, err := rt.Kernel.ArtifactStore.Exists(ctx, ref)
	if err != nil || !ok {
		return checkResult{"disk space", checkFail, fmt.Sprintf("ArtifactStore.Exists: ok=%v err=%v", ok, err), ""}
	}
	rc, err := rt.Kernel.ArtifactStore.Get(ctx, ref)
	if err != nil {
		return checkResult{"disk space", checkFail, "ArtifactStore.Get: " + err.Error(), ""}
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		return checkResult{"disk space", checkFail, "ArtifactStore.Read: " + err.Error(), ""}
	}
	if string(got) != string(payload) {
		return checkResult{"disk space", checkFail, "CAS content mismatch", ""}
	}
	return checkResult{
		name:   "disk space",
		status: checkOK,
		msg:    fmt.Sprintf("artifact store round-trip OK (ref=%s)", string(ref)[:doctorMin(20, len(string(ref)))]),
	}
}

func doctorMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func checkClockDrift() checkResult {
	_ = time.Now
	return checkResult{
		name:   "clock drift",
		status: checkSkip,
		msg:    "NTP probe not configured",
	}
}
