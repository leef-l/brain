package main

import (
	"fmt"
	"os"
	"unicode/utf8"
)

// SelectorOption is one item in a selection menu.
type SelectorOption struct {
	Label   string // display text, e.g. "✓ Execute this plan"
	Value   string // machine value returned on select
	IsInput bool   // if true, this option has an inline text input field
}

// SelectorResult holds what the user chose.
type SelectorResult struct {
	Value     string // the Value of the selected option
	UserInput string // text the user typed (only if IsInput option was chosen)
	Cancelled bool   // true if user pressed Ctrl+C / Esc
}

// RunSelector renders a vertical selection menu with arrow-key navigation.
// It reads directly from stdin — use RunSelectorWithChan when stdin is
// managed by an async reader goroutine.
//
// Keybindings: Up/Down to move, Enter to confirm, Esc/Ctrl+C to cancel.
func RunSelector(options []SelectorOption) SelectorResult {
	if len(options) == 0 {
		return SelectorResult{Cancelled: true}
	}

	selected := 0
	var inputBuf []rune
	buf := make([]byte, 512)

	drawSelector(options, selected, inputBuf)

	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil {
			return SelectorResult{Cancelled: true}
		}
		result, done := processSelectorInput(buf[:n], options, &selected, &inputBuf)
		if done {
			return result
		}
	}
}

// RunSelectorWithChan renders a selector that receives input from a channel
// instead of reading stdin directly. This is used when the main loop owns
// the stdin reader goroutine.
func RunSelectorWithChan(options []SelectorOption, stdinCh <-chan []byte, stdinErrCh <-chan error) SelectorResult {
	if len(options) == 0 {
		return SelectorResult{Cancelled: true}
	}

	// Drain any buffered input that arrived before the selector was shown.
drainLoop:
	for {
		select {
		case _, ok := <-stdinCh:
			if !ok {
				return SelectorResult{Cancelled: true}
			}
		default:
			break drainLoop
		}
	}

	selected := 0
	var inputBuf []rune

	drawSelector(options, selected, inputBuf)

	for {
		select {
		case data, ok := <-stdinCh:
			if !ok {
				clearSelector(len(options))
				return SelectorResult{Cancelled: true}
			}
			result, done := processSelectorInput(data, options, &selected, &inputBuf)
			if done {
				return result
			}
		case err := <-stdinErrCh:
			_ = err
			clearSelector(len(options))
			return SelectorResult{Cancelled: true}
		}
	}
}

// processSelectorInput handles a chunk of input bytes for the selector.
// Returns (result, true) if a selection was made, or (_, false) to continue.
func processSelectorInput(data []byte, options []SelectorOption, selected *int, inputBuf *[]rune) (SelectorResult, bool) {
	for len(data) > 0 {
		ch := data[0]

		// --- Escape sequences ---
		if ch == 0x1B {
			if len(data) >= 3 && data[1] == '[' {
				switch data[2] {
				case 'A': // Up
					if *selected > 0 {
						*selected--
						*inputBuf = nil
					}
					data = data[3:]
					clearSelector(len(options))
					drawSelector(options, *selected, *inputBuf)
					continue
				case 'B': // Down
					if *selected < len(options)-1 {
						*selected++
						*inputBuf = nil
					}
					data = data[3:]
					clearSelector(len(options))
					drawSelector(options, *selected, *inputBuf)
					continue
				default:
					// Consume full CSI sequence
					consumed := 3
					for consumed < len(data) && data[consumed-1] < 0x40 {
						consumed++
					}
					data = data[consumed:]
					continue
				}
			}
			// Lone Esc = cancel
			clearSelector(len(options))
			return SelectorResult{Cancelled: true}, true
		}

		// --- Ctrl+C / Ctrl+D = cancel ---
		if ch == 0x03 || ch == 0x04 {
			clearSelector(len(options))
			return SelectorResult{Cancelled: true}, true
		}

		// --- Enter = confirm ---
		if ch == '\n' || ch == '\r' {
			clearSelector(len(options))
			opt := options[*selected]
			return SelectorResult{
				Value:     opt.Value,
				UserInput: string(*inputBuf),
			}, true
		}

		// --- Backspace (for input option) ---
		if (ch == 0x7F || ch == 0x08) && options[*selected].IsInput {
			if len(*inputBuf) > 0 {
				*inputBuf = (*inputBuf)[:len(*inputBuf)-1]
				clearSelector(len(options))
				drawSelector(options, *selected, *inputBuf)
			}
			data = data[1:]
			continue
		}

		// --- Printable chars (for input option) ---
		if options[*selected].IsInput && ch >= 0x20 {
			r, size := utf8.DecodeRune(data)
			if r != utf8.RuneError || size > 1 {
				*inputBuf = append(*inputBuf, r)
				data = data[size:]
				clearSelector(len(options))
				drawSelector(options, *selected, *inputBuf)
				continue
			}
		}

		data = data[1:]
	}
	return SelectorResult{}, false
}

// drawSelector prints the menu options, highlighting the selected one.
func drawSelector(options []SelectorOption, selected int, inputBuf []rune) {
	for i, opt := range options {
		if i == selected {
			// Highlighted: bold + cyan background.
			fmt.Printf("  \033[1;36m❯ %s\033[0m", opt.Label)
			if opt.IsInput {
				// Show text input inline.
				text := string(inputBuf)
				if text == "" {
					fmt.Print(" \033[2m(type your feedback...)\033[0m")
				} else {
					fmt.Printf(" \033[1m%s\033[0m\033[5m▌\033[0m", text)
				}
			}
		} else {
			fmt.Printf("  \033[2m  %s\033[0m", opt.Label)
		}
		fmt.Println()
	}
}

// clearSelector moves cursor up and clears the menu lines.
func clearSelector(count int) {
	for i := 0; i < count; i++ {
		fmt.Print("\033[A\033[K") // move up + clear line
	}
}
