//go:build windows

package tool

import (
	"os"
	"strings"
)

func resolveSignal(name string) os.Signal {
	switch strings.ToUpper(name) {
	case "INT", "SIGINT", "2":
		return os.Interrupt
	default:
		return os.Kill
	}
}
