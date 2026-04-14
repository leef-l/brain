package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// BrowserSession manages a Chrome/Chromium browser process and its CDP connection.
//
// It supports any Chromium-based browser (Chrome, Edge, Brave, Opera, Vivaldi).
// Set the BROWSER_PATH environment variable to use a specific browser binary.
type BrowserSession struct {
	cmd       *exec.Cmd
	client    *Client
	sessionID string // current page session
	targetID  string // current page target
	userDir   string // temporary user data dir
	mu        sync.Mutex
	port      int
}

// browserTarget is a CDP target (tab/page).
type browserTarget struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Title              string `json:"title"`
	URL                string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// NewBrowserSession launches a browser in headless mode and connects via CDP.
// It auto-detects Chrome, Edge, or other Chromium-based browsers.
func NewBrowserSession(ctx context.Context) (*BrowserSession, error) {
	// Find browser binary.
	browserPath := findBrowser()
	if browserPath == "" {
		return nil, fmt.Errorf("cdp: no Chromium-based browser found. " +
			"Install Chrome/Chromium/Edge or set BROWSER_PATH env var")
	}

	// Create temp user data dir.
	userDir, err := os.MkdirTemp("", "brain-cdp-*")
	if err != nil {
		return nil, fmt.Errorf("cdp: create temp dir: %w", err)
	}

	// Find a free port.
	port := 0
	for p := 9222; p < 9322; p++ {
		if isPortFree(p) {
			port = p
			break
		}
	}
	if port == 0 {
		os.RemoveAll(userDir)
		return nil, fmt.Errorf("cdp: no free port in range 9222-9321")
	}

	// Launch browser.
	args := []string{
		"--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", userDir),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-client-side-phishing-detection",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-hang-monitor",
		"--disable-popup-blocking",
		"--disable-prompt-on-repost",
		"--disable-sync",
		"--disable-translate",
		"--metrics-recording-only",
		"--safebrowsing-disable-auto-update",
		"--no-sandbox",
		"--disable-gpu",
		"--window-size=1920,1080",
		"about:blank",
	}

	cmd := exec.CommandContext(ctx, browserPath, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		os.RemoveAll(userDir)
		return nil, fmt.Errorf("cdp: start browser %s: %w", browserPath, err)
	}

	s := &BrowserSession{
		cmd:     cmd,
		userDir: userDir,
		port:    port,
	}

	// Wait for CDP endpoint to become available.
	if err := s.waitForCDP(ctx); err != nil {
		s.Close()
		return nil, err
	}

	return s, nil
}

// Client returns the underlying CDP client.
func (s *BrowserSession) Client() *Client {
	return s.client
}

// SessionID returns the current page session ID.
func (s *BrowserSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// TargetID returns the current page target ID.
func (s *BrowserSession) TargetID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetID
}

// Navigate navigates the current page to the given URL and waits for load.
func (s *BrowserSession) Navigate(ctx context.Context, url string) error {
	var result struct {
		FrameID string `json:"frameId"`
	}
	return s.client.CallSession(ctx, s.SessionID(), "Page.navigate", map[string]string{
		"url": url,
	}, &result)
}

// NewTab creates a new tab, attaches a session, and makes it the active target.
func (s *BrowserSession) NewTab(ctx context.Context, url string) error {
	if url == "" {
		url = "about:blank"
	}

	var result struct {
		TargetID string `json:"targetId"`
	}
	if err := s.client.Call(ctx, "Target.createTarget", map[string]string{
		"url": url,
	}, &result); err != nil {
		return fmt.Errorf("cdp: create target: %w", err)
	}

	return s.attachToTarget(ctx, result.TargetID)
}

// SwitchTab switches to an existing tab by target ID.
func (s *BrowserSession) SwitchTab(ctx context.Context, targetID string) error {
	// Activate the target in the browser.
	if err := s.client.Call(ctx, "Target.activateTarget", map[string]interface{}{
		"targetId": targetID,
	}, nil); err != nil {
		return fmt.Errorf("cdp: activate target: %w", err)
	}
	return s.attachToTarget(ctx, targetID)
}

// CloseTab closes a tab by target ID.
func (s *BrowserSession) CloseTab(ctx context.Context, targetID string) error {
	return s.client.Call(ctx, "Target.closeTarget", map[string]interface{}{
		"targetId": targetID,
	}, nil)
}

// ListTabs returns all page targets.
func (s *BrowserSession) ListTabs(ctx context.Context) ([]browserTarget, error) {
	var result struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := s.client.Call(ctx, "Target.getTargets", nil, &result); err != nil {
		return nil, err
	}

	var tabs []browserTarget
	for _, t := range result.TargetInfos {
		if t.Type == "page" {
			tabs = append(tabs, browserTarget{
				ID:    t.TargetID,
				Type:  t.Type,
				Title: t.Title,
				URL:   t.URL,
			})
		}
	}
	return tabs, nil
}

// Exec sends a CDP command on the current page session.
func (s *BrowserSession) Exec(ctx context.Context, method string, params interface{}, result interface{}) error {
	return s.client.CallSession(ctx, s.SessionID(), method, params, result)
}

// Close shuts down the browser and cleans up.
func (s *BrowserSession) Close() error {
	if s.client != nil {
		// Try graceful close.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		s.client.Call(ctx, "Browser.close", nil, nil)
		cancel()
		s.client.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	if s.userDir != "" {
		os.RemoveAll(s.userDir)
	}
	return nil
}

// --- internal ---

// waitForCDP polls the CDP /json/version endpoint until the browser is ready.
func (s *BrowserSession) waitForCDP(ctx context.Context) error {
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("cdp: browser didn't start within 15s (port %d)", s.port)
		case <-ticker.C:
			wsURL, err := s.getWebSocketURL()
			if err != nil {
				continue
			}
			client, err := Dial(wsURL)
			if err != nil {
				continue
			}
			s.client = client

			// Attach to the first page target.
			if err := s.attachToFirstPage(ctx); err != nil {
				return err
			}
			return nil
		}
	}
}

// getWebSocketURL fetches the browser's WebSocket debugger URL.
func (s *BrowserSession) getWebSocketURL() (string, error) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", s.port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("cdp: empty webSocketDebuggerUrl")
	}
	return info.WebSocketDebuggerURL, nil
}

// attachToFirstPage finds the first page target and attaches a session.
func (s *BrowserSession) attachToFirstPage(ctx context.Context) error {
	// List targets.
	var result struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := s.client.Call(ctx, "Target.getTargets", nil, &result); err != nil {
		return fmt.Errorf("cdp: list targets: %w", err)
	}

	for _, t := range result.TargetInfos {
		if t.Type == "page" {
			return s.attachToTarget(ctx, t.TargetID)
		}
	}

	// No page target — create one.
	var createResult struct {
		TargetID string `json:"targetId"`
	}
	if err := s.client.Call(ctx, "Target.createTarget", map[string]string{
		"url": "about:blank",
	}, &createResult); err != nil {
		return fmt.Errorf("cdp: create target: %w", err)
	}
	return s.attachToTarget(ctx, createResult.TargetID)
}

// attachToTarget attaches a flat CDP session to the given target.
func (s *BrowserSession) attachToTarget(ctx context.Context, targetID string) error {
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := s.client.Call(ctx, "Target.attachToTarget", map[string]interface{}{
		"targetId": targetID,
		"flatten":  true,
	}, &result); err != nil {
		return fmt.Errorf("cdp: attach to target %s: %w", targetID, err)
	}

	s.mu.Lock()
	s.sessionID = result.SessionID
	s.targetID = targetID
	s.mu.Unlock()

	// Enable necessary domains on this session.
	for _, domain := range []string{"Page", "DOM", "Runtime", "Network", "Input"} {
		if err := s.client.CallSession(ctx, result.SessionID, domain+".enable", nil, nil); err != nil {
			// Some domains may not be available — log but don't fail.
			fmt.Fprintf(os.Stderr, "cdp: enable %s: %v\n", domain, err)
		}
	}

	return nil
}

// findBrowser searches for a Chromium-based browser on the system.
// Priority: BROWSER_PATH env → Chrome → Chromium → Edge → Brave → Opera.
func findBrowser() string {
	// 1. Environment variable override.
	if p := os.Getenv("BROWSER_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 2. Platform-specific search.
	candidates := browserCandidates()
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
		// Try absolute path.
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	return ""
}

// browserCandidates returns browser binary names/paths in priority order.
func browserCandidates() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{
			// Google Chrome
			"google-chrome-stable",
			"google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/opt/google/chrome/google-chrome",
			// Chromium
			"chromium-browser",
			"chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/snap/bin/chromium",
			// Edge
			"microsoft-edge-stable",
			"microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/opt/microsoft/msedge/msedge",
			// Brave
			"brave-browser-stable",
			"brave-browser",
			"/usr/bin/brave-browser-stable",
			"/opt/brave.com/brave/brave-browser",
			// Opera
			"opera",
			"/usr/bin/opera",
			// Vivaldi
			"vivaldi-stable",
			"vivaldi",
			"/usr/bin/vivaldi-stable",
		}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Opera.app/Contents/MacOS/Opera",
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
			"google-chrome",
			"chromium",
		}
	case "windows":
		// Common Windows paths.
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("PROGRAMFILES")
		programFilesX86 := os.Getenv("PROGRAMFILES(X86)")
		var paths []string
		for _, base := range []string{programFiles, programFilesX86, localAppData} {
			if base == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				filepath.Join(base, "Opera Software", "Opera Stable", "opera.exe"),
				filepath.Join(base, "Vivaldi", "Application", "vivaldi.exe"),
			)
		}
		return paths
	default:
		return []string{"chromium", "google-chrome", "chrome"}
	}
}

// isPortFree checks if a TCP port is available.
func isPortFree(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).Dial("tcp", addr)
	if err != nil {
		return true // can't connect → port is free
	}
	conn.Close()
	return false
}

