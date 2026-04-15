//go:build windows

package netutil

import (
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"unsafe"
)

var (
	modAdvapi32        = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW  = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueW = modAdvapi32.NewProc("RegQueryValueExW")
	procRegCloseKey    = modAdvapi32.NewProc("RegCloseKey")
)

const (
	_HKEY_CURRENT_USER = 0x80000001
	_KEY_READ          = 0x20019
	_REG_DWORD         = 4
	_REG_SZ            = 1
)

// systemProxy reads the Windows system proxy from the registry:
// HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings
func systemProxy(req *http.Request) (*url.URL, error) {
	subkey, _ := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
	var hkey syscall.Handle
	r, _, _ := procRegOpenKeyExW.Call(
		uintptr(_HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(subkey)),
		0,
		uintptr(_KEY_READ),
		uintptr(unsafe.Pointer(&hkey)),
	)
	if r != 0 {
		return nil, nil
	}
	defer procRegCloseKey.Call(uintptr(hkey))

	// Check ProxyEnable (DWORD).
	enabled := readRegDWORD(hkey, "ProxyEnable")
	if enabled == 0 {
		return nil, nil
	}

	// Read ProxyServer (string).
	server := readRegString(hkey, "ProxyServer")
	if server == "" {
		return nil, nil
	}

	// Check ProxyOverride (NO_PROXY equivalent).
	override := readRegString(hkey, "ProxyOverride")
	if override != "" {
		host := req.URL.Hostname()
		for _, pattern := range strings.Split(override, ";") {
			pattern = strings.TrimSpace(pattern)
			if pattern == "<local>" {
				if !strings.Contains(host, ".") {
					return nil, nil
				}
				continue
			}
			if matchWildcard(pattern, host) {
				return nil, nil
			}
		}
	}

	proxyURL := pickProxy(server, req.URL.Scheme)
	if proxyURL == "" {
		return nil, nil
	}
	if !strings.Contains(proxyURL, "://") {
		proxyURL = "http://" + proxyURL
	}
	return url.Parse(proxyURL)
}

func readRegDWORD(hkey syscall.Handle, name string) uint32 {
	valName, _ := syscall.UTF16PtrFromString(name)
	var valType, size uint32
	size = 4
	var data uint32
	r, _, _ := procRegQueryValueW.Call(
		uintptr(hkey),
		uintptr(unsafe.Pointer(valName)),
		0,
		uintptr(unsafe.Pointer(&valType)),
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 || valType != _REG_DWORD {
		return 0
	}
	return data
}

func readRegString(hkey syscall.Handle, name string) string {
	valName, _ := syscall.UTF16PtrFromString(name)
	var valType, size uint32

	// First call to get the size.
	r, _, _ := procRegQueryValueW.Call(
		uintptr(hkey),
		uintptr(unsafe.Pointer(valName)),
		0,
		uintptr(unsafe.Pointer(&valType)),
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 || valType != _REG_SZ || size == 0 {
		return ""
	}

	buf := make([]uint16, size/2)
	r, _, _ = procRegQueryValueW.Call(
		uintptr(hkey),
		uintptr(unsafe.Pointer(valName)),
		0,
		uintptr(unsafe.Pointer(&valType)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// pickProxy extracts the proxy URL for the given scheme from a Windows proxy
// server string. Handles both simple ("host:port") and per-protocol
// ("http=host:port;https=host:port") formats.
func pickProxy(server, scheme string) string {
	if !strings.Contains(server, "=") {
		return server
	}

	// Per-protocol: try exact scheme match first.
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i > 0 {
			proto := strings.ToLower(part[:i])
			addr := part[i+1:]
			if proto == scheme ||
				(proto == "https" && scheme == "wss") ||
				(proto == "http" && scheme == "ws") {
				return addr
			}
		}
	}

	// Fallback: try https then http.
	for _, try := range []string{"https", "http"} {
		for _, part := range strings.Split(server, ";") {
			part = strings.TrimSpace(part)
			if i := strings.Index(part, "="); i > 0 {
				if strings.ToLower(part[:i]) == try {
					return part[i+1:]
				}
			}
		}
	}
	return ""
}

// matchWildcard matches a simple wildcard pattern (only leading *).
func matchWildcard(pattern, host string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix)
	}
	return strings.EqualFold(pattern, host)
}
