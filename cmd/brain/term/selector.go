package term

import (
	"fmt"
	"os"
	"unicode/utf8"
)

type SelectorOption struct {
	Label   string
	Value   string
	IsInput bool
}

type SelectorResult struct {
	Value     string
	UserInput string
	Cancelled bool
}

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

func RunSelectorWithChan(options []SelectorOption, stdinCh <-chan []byte, stdinErrCh <-chan error) SelectorResult {
	if len(options) == 0 {
		return SelectorResult{Cancelled: true}
	}

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

func processSelectorInput(data []byte, options []SelectorOption, selected *int, inputBuf *[]rune) (SelectorResult, bool) {
	for len(data) > 0 {
		ch := data[0]

		if ch == 0x1B {
			if len(data) >= 3 && data[1] == '[' {
				switch data[2] {
				case 'A':
					if *selected > 0 {
						*selected--
						*inputBuf = nil
					}
					data = data[3:]
					clearSelector(len(options))
					drawSelector(options, *selected, *inputBuf)
					continue
				case 'B':
					if *selected < len(options)-1 {
						*selected++
						*inputBuf = nil
					}
					data = data[3:]
					clearSelector(len(options))
					drawSelector(options, *selected, *inputBuf)
					continue
				default:
					consumed := 3
					for consumed < len(data) && data[consumed-1] < 0x40 {
						consumed++
					}
					data = data[consumed:]
					continue
				}
			}
			clearSelector(len(options))
			return SelectorResult{Cancelled: true}, true
		}

		if ch == 0x03 || ch == 0x04 {
			clearSelector(len(options))
			return SelectorResult{Cancelled: true}, true
		}

		if ch == '\n' || ch == '\r' {
			clearSelector(len(options))
			opt := options[*selected]
			return SelectorResult{
				Value:     opt.Value,
				UserInput: string(*inputBuf),
			}, true
		}

		if (ch == 0x7F || ch == 0x08) && options[*selected].IsInput {
			if len(*inputBuf) > 0 {
				*inputBuf = (*inputBuf)[:len(*inputBuf)-1]
				clearSelector(len(options))
				drawSelector(options, *selected, *inputBuf)
			}
			data = data[1:]
			continue
		}

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

func drawSelector(options []SelectorOption, selected int, inputBuf []rune) {
	for i, opt := range options {
		if i == selected {
			fmt.Printf("  \033[1;36m❯ %s\033[0m", opt.Label)
			if opt.IsInput {
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

func clearSelector(count int) {
	for i := 0; i < count; i++ {
		fmt.Print("\033[A\033[K")
	}
}
