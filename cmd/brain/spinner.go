package main

import (
	"fmt"
	"sync"
	"time"
)

// spinner shows an animated status indicator while the AI is working.
// Example output: "⠋ Thinking..."
//
// Usage:
//
//	startSpinner("Thinking...")
//	// ... long operation ...
//	stopSpinner()
var (
	spinnerMu   sync.Mutex
	spinnerDone chan struct{}
	spinnerMsg  string
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// startSpinner begins showing an animated spinner with the given message.
// Call stopSpinner() to stop and clear the spinner line.
func startSpinner(msg string) {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()

	if spinnerDone != nil {
		return // already running
	}

	spinnerMsg = msg
	spinnerDone = make(chan struct{})

	go func() {
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-spinnerDone:
				return
			case <-ticker.C:
				spinnerMu.Lock()
				msg := spinnerMsg
				spinnerMu.Unlock()
				frame := spinnerFrames[i%len(spinnerFrames)]
				fmt.Printf("\r\033[2m%s %s\033[0m\033[K", frame, msg)
				i++
			}
		}
	}()
}

// updateSpinner changes the spinner message while it's running.
func updateSpinner(msg string) {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	spinnerMsg = msg
}

// stopSpinner stops the spinner and clears the line.
func stopSpinner() {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()

	if spinnerDone == nil {
		return
	}
	close(spinnerDone)
	spinnerDone = nil
	fmt.Print("\r\033[K") // clear spinner line
}
