package term

import (
	"fmt"
	"sync"
	"time"
)

var (
	spinnerMu   sync.Mutex
	spinnerDone chan struct{}
	spinnerMsg  string
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func StartSpinner(msg string) {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()

	if spinnerDone != nil {
		return
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

func UpdateSpinner(msg string) {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	spinnerMsg = msg
}

func StopSpinner() {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()

	if spinnerDone == nil {
		return
	}
	close(spinnerDone)
	spinnerDone = nil
	fmt.Print("\r\033[K")
}
