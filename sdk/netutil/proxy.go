// Package netutil provides cross-platform network utilities.
package netutil

import (
	"net/http"
	"net/url"
)

// ProxyFunc returns a proxy function suitable for http.Transport or
// websocket.Dialer. It checks (in order):
//
//  1. HTTP_PROXY / HTTPS_PROXY / NO_PROXY environment variables
//  2. OS-level system proxy (Windows registry, macOS scutil, etc.)
//
// If no proxy is found, returns nil (direct connection).
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		// 1. Environment variables (standard Go behavior).
		if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
			return u, err
		}

		// 2. OS-specific system proxy detection.
		return systemProxy(req)
	}
}
