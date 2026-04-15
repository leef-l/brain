//go:build !windows

package netutil

import (
	"net/http"
	"net/url"
)

// systemProxy is a no-op on non-Windows platforms.
// On Linux/macOS, HTTP_PROXY/HTTPS_PROXY env vars are the standard mechanism
// and are already handled by http.ProxyFromEnvironment.
func systemProxy(_ *http.Request) (*url.URL, error) {
	return nil, nil
}
