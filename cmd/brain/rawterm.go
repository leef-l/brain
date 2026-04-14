package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// inputAction indicates what the user did to end their input line.
type inputAction int

const (
	actionEnter  inputAction = iota // pressed Enter — submit line
	actionQueue                     // pressed Tab — queue/send current line
	actionCycle                     // pressed cycle-mode key (e.g. Ctrl+W)
	actionCancel                    // pressed cancel key (Escape) — cancel running task or clear input
	actionQuit                      // pressed quit key (e.g. Ctrl+D) or EOF — exit chat
	actionEscape                    // pressed Escape — context-dependent (clear input / cancel task)
)

// lineEditor holds the state for editing a single input line with cursor.
type lineEditor struct {
	runes       []rune // the line content as runes
	pos         int    // cursor position (index into runes, 0 = before first rune)
	promptWidth int    // display width of the prompt prefix
}

// lineReadSession incrementally reads and edits one input line. Unlike
// readLineRaw, Poll returns control to the caller when no input is available,
// which lets chat mode handle AI completion and approval prompts without
// losing the partially typed line.
type lineReadSession struct {
	kb      *keybindings
	ed      *lineEditor
	pending []byte
	pasting bool // inside a bracketed paste sequence
	history []string

	// historyIndex == len(history) means "not currently browsing history".
	historyIndex int

	// frameLines is the number of terminal lines occupied by the chat prompt
	// frame (queued messages + input line + footer). It is managed by chat mode.
	frameLines int
}

func newLineReadSession(kb *keybindings, promptWidth int) *lineReadSession {
	return &lineReadSession{
		kb:           kb,
		ed:           &lineEditor{promptWidth: promptWidth},
		historyIndex: 0,
	}
}

func (s *lineReadSession) editor() *lineEditor {
	return s.ed
}

// Poll consumes any available stdin bytes and updates the line editor state.
// It returns done=false when no terminating action has happened yet.
func (s *lineReadSession) Poll() (line string, action inputAction, done bool, err error) {
	buf := make([]byte, 512)
	n, readErr := os.Stdin.Read(buf)
	if readErr != nil {
		if readErr == syscall.EINTR || readErr == syscall.EAGAIN {
			return "", actionEnter, false, nil
		}
		if n == 0 {
			return "", actionQuit, true, nil
		}
	}
	if n == 0 {
		return "", actionEnter, false, nil
	}

	return s.consume(buf[:n])
}

func (s *lineReadSession) consume(chunk []byte) (line string, action inputAction, done bool, err error) {
	data := append(append([]byte(nil), s.pending...), chunk...)
	s.pending = nil

	// Resolve configurable keys to byte values.
	cycleCh, _ := keyToChar(s.kb.CycleMode)
	cancelCh, _ := keyToChar(s.kb.Cancel)
	quitCh, _ := keyToChar(s.kb.Quit)
	clearCh, _ := keyToChar(s.kb.ClearScreen)
	lineStartCh, _ := keyToChar(s.kb.LineStart)
	lineEndCh, _ := keyToChar(s.kb.LineEnd)
	delToStartCh, _ := keyToChar(s.kb.DeleteToStart)
	delToEndCh, _ := keyToChar(s.kb.DeleteToEnd)
	delWordCh, _ := keyToChar(s.kb.DeleteWord)

	for len(data) > 0 {
		ch := data[0]

		// --- Escape sequences (arrow keys, Home, End, Delete) ---
		if ch == 0x1B {
			// Only ESC in buffer → lone Escape keypress.
			if len(data) == 1 {
				return s.ed.string(), actionEscape, true, nil
			}
			// CSI/SS3 sequence prefix.
			if data[1] == '[' || data[1] == 'O' {
				if escapeSequenceIncomplete(data) {
					// Incomplete sequence — store as pending for next chunk.
					s.pending = append(s.pending, data...)
					return "", actionEnter, false, nil
				}
				act, consumed := handleEscape(data, s)
				data = data[consumed:]
				if act >= 0 {
					return s.ed.string(), inputAction(act), true, nil
				}
				continue
			}
			// ESC followed by non-sequence byte → lone Escape.
			data = data[1:]
			return s.ed.string(), actionEscape, true, nil
		}

		// --- Ctrl+C (0x03): ignored, matching Claude Code behavior ---
		// Copy is handled by the terminal emulator, not the application.
		if ch == 0x03 {
			data = data[1:]
			continue
		}

		// --- Ctrl+V (0x16): in raw mode, terminal handles paste ---
		// If terminal doesn't intercept Ctrl+V, just ignore the control byte.
		// Actual paste content arrives via bracketed paste (ESC[200~ ... ESC[201~).
		if ch == 0x16 {
			data = data[1:]
			continue
		}

		// --- Configurable action keys ---
		if ch == cycleCh {
			redrawClear(s.ed)
			return "", actionCycle, true, nil
		}
		if cancelCh != 0x03 && ch == cancelCh {
			line := s.ed.string()
			redrawClear(s.ed)
			return line, actionCancel, true, nil
		}
		if ch == quitCh {
			return "", actionQuit, true, nil
		}

		// --- Enter ---
		if ch == '\n' || ch == '\r' {
			if s.pasting {
				// During bracketed paste, newlines are part of pasted text.
				// Insert a space instead (single-line input only).
				s.leaveHistoryBrowse()
				s.ed.insert(' ')
				data = data[1:]
				continue
			}
			return s.ed.string(), actionEnter, true, nil
		}

		// --- Tab queues the current draft when the caller wants it ---
		if ch == '\t' {
			if len(s.ed.runes) == 0 {
				data = data[1:]
				continue
			}
			return s.ed.string(), actionQueue, true, nil
		}

		// --- Backspace ---
		if ch == 0x7F || ch == 0x08 {
			s.leaveHistoryBrowse()
			w := s.ed.backspace()
			if w > 0 {
				redrawFull(s.ed)
			}
			data = data[1:]
			continue
		}

		// --- Line editing shortcuts ---
		if ch == clearCh {
			fmt.Print("\033[2J\033[H")
			data = data[1:]
			continue
		}
		if ch == lineStartCh {
			moveCursorTo(s.ed, 0)
			data = data[1:]
			continue
		}
		if ch == lineEndCh {
			moveCursorTo(s.ed, len(s.ed.runes))
			data = data[1:]
			continue
		}
		if ch == delToStartCh {
			s.leaveHistoryBrowse()
			if s.ed.pos > 0 {
				s.ed.runes = s.ed.runes[s.ed.pos:]
				s.ed.pos = 0
				redrawFull(s.ed)
			}
			data = data[1:]
			continue
		}
		if ch == delToEndCh {
			s.leaveHistoryBrowse()
			if s.ed.pos < len(s.ed.runes) {
				s.ed.runes = s.ed.runes[:s.ed.pos]
				redrawFull(s.ed)
			}
			data = data[1:]
			continue
		}
		if ch == delWordCh {
			s.leaveHistoryBrowse()
			deleteWordBack(s.ed)
			data = data[1:]
			continue
		}

		// --- Ignore other control chars ---
		if ch < 0x20 {
			data = data[1:]
			continue
		}

		// --- Printable: decode UTF-8 rune ---
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size <= 1 {
			// Incomplete UTF-8 sequence — store as pending for next chunk.
			s.pending = append(s.pending, data...)
			return "", actionEnter, false, nil
		}

		s.leaveHistoryBrowse()
		s.ed.insert(r)
		if s.ed.pos == len(s.ed.runes) {
			fmt.Print(string(r))
		} else {
			redrawFull(s.ed)
		}
		data = data[size:]
	}

	return "", actionEnter, false, nil
}

func (s *lineReadSession) addHistory(line string) {
	if line == "" {
		return
	}
	if n := len(s.history); n > 0 && s.history[n-1] == line {
		s.historyIndex = len(s.history)
		return
	}
	s.history = append(s.history, line)
	s.historyIndex = len(s.history)
}

func (s *lineReadSession) leaveHistoryBrowse() {
	s.historyIndex = len(s.history)
}

func (s *lineReadSession) browseHistoryPrev() bool {
	if len(s.history) == 0 {
		return false
	}
	if s.historyIndex == len(s.history) {
		if !editorBlank(s.ed) {
			return false
		}
		s.historyIndex = len(s.history) - 1
	} else if s.historyIndex > 0 {
		s.historyIndex--
	}
	s.replaceInput(s.history[s.historyIndex])
	return true
}

func (s *lineReadSession) browseHistoryNext() bool {
	if len(s.history) == 0 || s.historyIndex == len(s.history) {
		return false
	}
	if s.historyIndex < len(s.history)-1 {
		s.historyIndex++
		s.replaceInput(s.history[s.historyIndex])
		return true
	}
	s.historyIndex = len(s.history)
	s.replaceInput("")
	return true
}

func (s *lineReadSession) replaceInput(text string) {
	s.ed.runes = []rune(text)
	s.ed.pos = len(s.ed.runes)
	redrawFull(s.ed)
}

func editorBlank(ed *lineEditor) bool {
	for _, r := range ed.runes {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func escapeSequenceIncomplete(data []byte) bool {
	if len(data) < 2 {
		return true
	}

	switch data[1] {
	case '[':
		_, ok := csiSequenceLength(data)
		return !ok
	case 'O':
		return len(data) < 3
	default:
		return false
	}
}

func csiSequenceLength(data []byte) (int, bool) {
	if len(data) < 3 || data[0] != 0x1B || data[1] != '[' {
		return 0, false
	}
	for i := 2; i < len(data); i++ {
		if data[i] >= 0x40 && data[i] <= 0x7E {
			return i + 1, true
		}
	}
	return 0, false
}

// insert inserts a rune at the cursor position.
func (e *lineEditor) insert(r rune) {
	e.runes = append(e.runes, 0)
	copy(e.runes[e.pos+1:], e.runes[e.pos:])
	e.runes[e.pos] = r
	e.pos++
}

// backspace deletes the rune before the cursor. Returns the deleted rune width.
func (e *lineEditor) backspace() int {
	if e.pos == 0 {
		return 0
	}
	e.pos--
	w := runeWidth(e.runes[e.pos])
	e.runes = append(e.runes[:e.pos], e.runes[e.pos+1:]...)
	return w
}

// delete deletes the rune at the cursor (forward delete).
func (e *lineEditor) delete() {
	if e.pos >= len(e.runes) {
		return
	}
	e.runes = append(e.runes[:e.pos], e.runes[e.pos+1:]...)
}

// displayWidth returns the total display width of the line.
func (e *lineEditor) displayWidth() int {
	w := 0
	for _, r := range e.runes {
		w += runeWidth(r)
	}
	return w
}

// displayWidthRange returns the display width of runes[from:to].
func (e *lineEditor) displayWidthRange(from, to int) int {
	w := 0
	for i := from; i < to && i < len(e.runes); i++ {
		w += runeWidth(e.runes[i])
	}
	return w
}

// string returns the line content as a string.
func (e *lineEditor) string() string {
	return string(e.runes)
}

// readLineRaw reads a line with full cursor editing, intercepting configurable
// key bindings. Supports UTF-8, arrow keys, Home/End, Delete, and readline-
// style shortcuts. Returns the line text and what action ended the input.
//
// promptWidth is the display width of the prompt already printed by the caller,
// used to correctly redraw the line.
func readLineRaw(kb *keybindings, promptWidth int) (line string, action inputAction) {
	session := newLineReadSession(kb, promptWidth)
	for {
		line, act, done, err := session.Poll()
		if err != nil {
			return session.editor().string(), actionQuit
		}
		if done {
			return line, act
		}
	}
}

// handleEscape processes escape sequences (CSI). Returns (action, bytesConsumed).
// action is -1 if no inputAction was triggered (just cursor movement).
func handleEscape(data []byte, s *lineReadSession) (int, int) {
	ed := s.ed
	if len(data) < 2 {
		return -1, 1 // lone ESC, ignore
	}

	// CSI sequences: ESC [ ...
	if data[1] == '[' {
		seqLen, ok := csiSequenceLength(data)
		if !ok {
			return -1, len(data)
		}
		final := data[seqLen-1]
		params := string(data[2 : seqLen-1])

		switch final {
		case 'A': // Up arrow — recall previous input when prompt is blank.
			if s.browseHistoryPrev() {
				return -1, seqLen
			}
			return -1, seqLen
		case 'B': // Down arrow — move forward in recalled input history.
			if s.browseHistoryNext() {
				return -1, seqLen
			}
			return -1, seqLen
		case 'C': // Right arrow
			if strings.HasSuffix(params, ";5") {
				moveWordForward(ed)
				return -1, seqLen
			}
			if ed.pos < len(ed.runes) {
				w := runeWidth(ed.runes[ed.pos])
				ed.pos++
				fmt.Printf("\033[%dC", w)
			}
			return -1, seqLen
		case 'D': // Left arrow
			if strings.HasSuffix(params, ";5") {
				moveWordBack(ed)
				return -1, seqLen
			}
			if ed.pos > 0 {
				ed.pos--
				w := runeWidth(ed.runes[ed.pos])
				fmt.Printf("\033[%dD", w)
			}
			return -1, seqLen
		case 'H': // Home
			moveCursorTo(ed, 0)
			return -1, seqLen
		case 'F': // End
			moveCursorTo(ed, len(ed.runes))
			return -1, seqLen
		case '~':
			switch csiPrimaryParam(params) {
			case "1", "7":
				moveCursorTo(ed, 0)
				return -1, seqLen
			case "3":
				s.leaveHistoryBrowse()
				ed.delete()
				redrawFull(ed)
				return -1, seqLen
			case "4", "8":
				moveCursorTo(ed, len(ed.runes))
				return -1, seqLen
			case "200":
				// Bracketed paste start — consume the marker, remaining
				// bytes in data are the pasted content (handled normally
				// as printable chars until we see the end marker).
				s.pasting = true
				return -1, seqLen
			case "201":
				// Bracketed paste end.
				s.pasting = false
				return -1, seqLen
			}
			return -1, seqLen
		}

		// Unknown CSI — consume ESC [ X.
		return -1, seqLen
	}

	// ESC O sequences (some terminals send these for Home/End).
	if data[1] == 'O' {
		if len(data) < 3 {
			return -1, 2
		}
		switch data[2] {
		case 'H': // Home
			moveCursorTo(ed, 0)
			return -1, 3
		case 'F': // End
			moveCursorTo(ed, len(ed.runes))
			return -1, 3
		}
		return -1, 3
	}

	return -1, 1
}

func csiPrimaryParam(params string) string {
	if params == "" {
		return ""
	}
	if i := strings.IndexByte(params, ';'); i >= 0 {
		return params[:i]
	}
	return params
}

// --- Display helpers ---

// moveCursorTo moves the cursor to a new rune position, emitting ANSI escapes.
func moveCursorTo(ed *lineEditor, newPos int) {
	if newPos == ed.pos {
		return
	}
	if newPos < ed.pos {
		w := ed.displayWidthRange(newPos, ed.pos)
		fmt.Printf("\033[%dD", w)
	} else {
		w := ed.displayWidthRange(ed.pos, newPos)
		fmt.Printf("\033[%dC", w)
	}
	ed.pos = newPos
}

// redrawFromCursor redraws everything from the cursor position to the end,
// then moves the cursor back to its correct position. Used after insert/delete
// in the middle of the line.
func redrawFromCursor(ed *lineEditor) {
	// Print from cursor to end, clear trailing, move cursor back.
	for i := ed.pos; i < len(ed.runes); i++ {
		fmt.Print(string(ed.runes[i]))
	}
	fmt.Print("\033[K") // clear to end of line
	// Move cursor back to correct position.
	tailW := ed.displayWidthRange(ed.pos, len(ed.runes))
	if tailW > 0 {
		fmt.Printf("\033[%dD", tailW)
	}
}

// redrawFull redraws the entire line from the start (after prompt).
// It moves to column 0, skips past the prompt, reprints all text, and
// repositions the cursor.
func redrawFull(ed *lineEditor) {
	// Go to column 0, move forward past prompt, clear rest of line.
	fmt.Print("\r")
	if ed.promptWidth > 0 {
		fmt.Printf("\033[%dC", ed.promptWidth)
	}
	for _, r := range ed.runes {
		fmt.Print(string(r))
	}
	fmt.Print("\033[K") // clear any leftover chars

	// Move cursor back to correct position.
	tailW := ed.displayWidthRange(ed.pos, len(ed.runes))
	if tailW > 0 {
		fmt.Printf("\033[%dD", tailW)
	}
}

// redrawClear erases the current input line completely.
func redrawClear(ed *lineEditor) {
	if len(ed.runes) > 0 {
		fmt.Print("\r\033[K")
	}
}

// --- Word movement/deletion ---

// moveWordBack moves cursor back to start of previous word.
func moveWordBack(ed *lineEditor) {
	if ed.pos == 0 {
		return
	}
	// Skip trailing spaces.
	for ed.pos > 0 && unicode.IsSpace(ed.runes[ed.pos-1]) {
		ed.pos--
	}
	// Skip word chars.
	for ed.pos > 0 && !unicode.IsSpace(ed.runes[ed.pos-1]) {
		ed.pos--
	}
	redrawFull(ed)
}

// moveWordForward moves cursor forward to end of next word.
func moveWordForward(ed *lineEditor) {
	n := len(ed.runes)
	if ed.pos >= n {
		return
	}
	// Skip current word chars.
	for ed.pos < n && !unicode.IsSpace(ed.runes[ed.pos]) {
		ed.pos++
	}
	// Skip spaces.
	for ed.pos < n && unicode.IsSpace(ed.runes[ed.pos]) {
		ed.pos++
	}
	redrawFull(ed)
}

// deleteWordBack deletes from cursor back to the start of the previous word.
func deleteWordBack(ed *lineEditor) {
	if ed.pos == 0 {
		return
	}
	orig := ed.pos
	// Skip trailing spaces.
	for ed.pos > 0 && unicode.IsSpace(ed.runes[ed.pos-1]) {
		ed.pos--
	}
	// Skip word chars.
	for ed.pos > 0 && !unicode.IsSpace(ed.runes[ed.pos-1]) {
		ed.pos--
	}
	ed.runes = append(ed.runes[:ed.pos], ed.runes[orig:]...)
	redrawFull(ed)
}

// --- Rune width ---

// runeWidth returns how many terminal columns a rune occupies.
func runeWidth(r rune) int {
	if isCJK(r) {
		return 2
	}
	return 1
}

// isCJK returns true if the rune is a CJK/fullwidth character (2 columns).
func isCJK(r rune) bool {
	return (r >= 0x2E80 && r <= 0x9FFF) || // CJK radicals, unified ideographs
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE30 && r <= 0xFE4F) || // CJK compatibility forms
		(r >= 0xFF01 && r <= 0xFF60) || // Fullwidth ASCII
		(r >= 0xFFE0 && r <= 0xFFE6) || // Fullwidth signs
		(r >= 0x20000 && r <= 0x2FA1F) // CJK unified ext B-F, compat supplement
}
