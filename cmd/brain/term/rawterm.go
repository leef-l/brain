package term

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

type InputAction int

const (
	ActionEnter  InputAction = iota
	ActionQueue
	ActionCycle
	ActionCancel
	ActionQuit
	ActionEscape
)

type LineEditor struct {
	Runes       []rune
	Pos         int
	PromptWidth int
	// LastEndRow:上次 RedrawFull 后内容末尾所在行相对 prompt 起始行的
	// 偏移(0=同一行,1=第二行,...)。用于清屏时算要清的行数。
	LastEndRow int
	// LastCursorRow:上次 RedrawFull 后光标所在行相对 prompt 起始行的偏移。
	// 光标不一定在末尾行(Pos 可以在内容中间),清屏前要按这个值先上移回
	// prompt 首行,再从那里清到屏幕底。
	LastCursorRow int
}

type LineReadSession struct {
	Kb      *Keybindings
	Ed      *LineEditor
	Pending []byte
	Pasting bool
	History []string

	HistoryIndex int

	FrameLines int
}

func NewLineReadSession(kb *Keybindings, promptWidth int) *LineReadSession {
	return &LineReadSession{
		Kb:           kb,
		Ed:           &LineEditor{PromptWidth: promptWidth},
		HistoryIndex: 0,
	}
}

func (s *LineReadSession) Editor() *LineEditor {
	return s.Ed
}

func (s *LineReadSession) Poll() (line string, action InputAction, done bool, err error) {
	buf := make([]byte, 512)
	n, readErr := os.Stdin.Read(buf)
	if readErr != nil {
		if readErr == syscall.EINTR || readErr == syscall.EAGAIN {
			return "", ActionEnter, false, nil
		}
		if n == 0 {
			return "", ActionQuit, true, nil
		}
	}
	if n == 0 {
		return "", ActionEnter, false, nil
	}

	return s.Consume(buf[:n])
}

func (s *LineReadSession) Consume(chunk []byte) (line string, action InputAction, done bool, err error) {
	data := append(append([]byte(nil), s.Pending...), chunk...)
	s.Pending = nil

	cycleCh, _ := KeyToChar(s.Kb.CycleMode)
	cancelCh, _ := KeyToChar(s.Kb.Cancel)
	quitCh, _ := KeyToChar(s.Kb.Quit)
	clearCh, _ := KeyToChar(s.Kb.ClearScreen)
	lineStartCh, _ := KeyToChar(s.Kb.LineStart)
	lineEndCh, _ := KeyToChar(s.Kb.LineEnd)
	delToStartCh, _ := KeyToChar(s.Kb.DeleteToStart)
	delToEndCh, _ := KeyToChar(s.Kb.DeleteToEnd)
	delWordCh, _ := KeyToChar(s.Kb.DeleteWord)

	for len(data) > 0 {
		ch := data[0]

		if ch == 0x1B {
			if len(data) == 1 {
				return s.Ed.String(), ActionEscape, true, nil
			}
			if data[1] == '[' || data[1] == 'O' {
				if EscapeSequenceIncomplete(data) {
					s.Pending = append(s.Pending, data...)
					return "", ActionEnter, false, nil
				}
				act, consumed := HandleEscape(data, s)
				data = data[consumed:]
				if act >= 0 {
					return s.Ed.String(), InputAction(act), true, nil
				}
				continue
			}
			data = data[1:]
			return s.Ed.String(), ActionEscape, true, nil
		}

		if ch == 0x03 {
			data = data[1:]
			continue
		}

		if ch == 0x16 {
			data = data[1:]
			continue
		}

		if ch == cycleCh {
			RedrawClear(s.Ed)
			return "", ActionCycle, true, nil
		}
		if cancelCh != 0x03 && ch == cancelCh {
			line := s.Ed.String()
			RedrawClear(s.Ed)
			return line, ActionCancel, true, nil
		}
		if ch == quitCh {
			return "", ActionQuit, true, nil
		}

		if ch == '\n' || ch == '\r' {
			if s.Pasting {
				s.LeaveHistoryBrowse()
				s.Ed.Insert(' ')
				data = data[1:]
				continue
			}
			return s.Ed.String(), ActionEnter, true, nil
		}

		if ch == '\t' {
			if len(s.Ed.Runes) == 0 {
				data = data[1:]
				continue
			}
			return s.Ed.String(), ActionQueue, true, nil
		}

		if ch == 0x7F || ch == 0x08 {
			s.LeaveHistoryBrowse()
			w := s.Ed.Backspace()
			if w > 0 {
				RedrawFull(s.Ed)
			}
			data = data[1:]
			continue
		}

		if ch == clearCh {
			fmt.Print("\033[2J\033[H")
			data = data[1:]
			continue
		}
		if ch == lineStartCh {
			MoveCursorTo(s.Ed, 0)
			data = data[1:]
			continue
		}
		if ch == lineEndCh {
			MoveCursorTo(s.Ed, len(s.Ed.Runes))
			data = data[1:]
			continue
		}
		if ch == delToStartCh {
			s.LeaveHistoryBrowse()
			if s.Ed.Pos > 0 {
				s.Ed.Runes = s.Ed.Runes[s.Ed.Pos:]
				s.Ed.Pos = 0
				RedrawFull(s.Ed)
			}
			data = data[1:]
			continue
		}
		if ch == delToEndCh {
			s.LeaveHistoryBrowse()
			if s.Ed.Pos < len(s.Ed.Runes) {
				s.Ed.Runes = s.Ed.Runes[:s.Ed.Pos]
				RedrawFull(s.Ed)
			}
			data = data[1:]
			continue
		}
		if ch == delWordCh {
			s.LeaveHistoryBrowse()
			DeleteWordBack(s.Ed)
			data = data[1:]
			continue
		}

		if ch < 0x20 {
			data = data[1:]
			continue
		}

		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size <= 1 {
			s.Pending = append(s.Pending, data...)
			return "", ActionEnter, false, nil
		}

		s.LeaveHistoryBrowse()
		s.Ed.Insert(r)
		if s.Ed.Pos == len(s.Ed.Runes) {
			// fast path:追加一个字符,光标 == 末尾。追加后若跨越终端
			// 宽度,同步更新 LastEndRow 和 LastCursorRow(它们相等,
			// 因为光标就在末尾),下次清屏才能找到真正的 prompt 首行。
			fmt.Print(string(r))
			cols := termCols()
			totalW := s.Ed.PromptWidth + s.Ed.DisplayWidthRange(0, len(s.Ed.Runes))
			row := totalW / cols
			s.Ed.LastEndRow = row
			s.Ed.LastCursorRow = row
		} else {
			RedrawFull(s.Ed)
		}
		data = data[size:]
	}

	return "", ActionEnter, false, nil
}

func (s *LineReadSession) AddHistory(line string) {
	if line == "" {
		return
	}
	if n := len(s.History); n > 0 && s.History[n-1] == line {
		s.HistoryIndex = len(s.History)
		return
	}
	s.History = append(s.History, line)
	s.HistoryIndex = len(s.History)
}

func (s *LineReadSession) LeaveHistoryBrowse() {
	s.HistoryIndex = len(s.History)
}

func (s *LineReadSession) BrowseHistoryPrev() bool {
	if len(s.History) == 0 {
		return false
	}
	if s.HistoryIndex == len(s.History) {
		if !EditorBlank(s.Ed) {
			return false
		}
		s.HistoryIndex = len(s.History) - 1
	} else if s.HistoryIndex > 0 {
		s.HistoryIndex--
	}
	s.ReplaceInput(s.History[s.HistoryIndex])
	return true
}

func (s *LineReadSession) BrowseHistoryNext() bool {
	if len(s.History) == 0 || s.HistoryIndex == len(s.History) {
		return false
	}
	if s.HistoryIndex < len(s.History)-1 {
		s.HistoryIndex++
		s.ReplaceInput(s.History[s.HistoryIndex])
		return true
	}
	s.HistoryIndex = len(s.History)
	s.ReplaceInput("")
	return true
}

func (s *LineReadSession) ReplaceInput(text string) {
	s.Ed.Runes = []rune(text)
	s.Ed.Pos = len(s.Ed.Runes)
	RedrawFull(s.Ed)
}

func EditorBlank(ed *LineEditor) bool {
	for _, r := range ed.Runes {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func EscapeSequenceIncomplete(data []byte) bool {
	if len(data) < 2 {
		return true
	}

	switch data[1] {
	case '[':
		_, ok := CsiSequenceLength(data)
		return !ok
	case 'O':
		return len(data) < 3
	default:
		return false
	}
}

func CsiSequenceLength(data []byte) (int, bool) {
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

func (e *LineEditor) Insert(r rune) {
	e.Runes = append(e.Runes, 0)
	copy(e.Runes[e.Pos+1:], e.Runes[e.Pos:])
	e.Runes[e.Pos] = r
	e.Pos++
}

func (e *LineEditor) Backspace() int {
	if e.Pos == 0 {
		return 0
	}
	e.Pos--
	w := RuneWidth(e.Runes[e.Pos])
	e.Runes = append(e.Runes[:e.Pos], e.Runes[e.Pos+1:]...)
	return w
}

func (e *LineEditor) Delete() {
	if e.Pos >= len(e.Runes) {
		return
	}
	e.Runes = append(e.Runes[:e.Pos], e.Runes[e.Pos+1:]...)
}

func (e *LineEditor) DisplayWidth() int {
	w := 0
	for _, r := range e.Runes {
		w += RuneWidth(r)
	}
	return w
}

func (e *LineEditor) DisplayWidthRange(from, to int) int {
	w := 0
	for i := from; i < to && i < len(e.Runes); i++ {
		w += RuneWidth(e.Runes[i])
	}
	return w
}

func (e *LineEditor) String() string {
	return string(e.Runes)
}

func ReadLineRaw(kb *Keybindings, promptWidth int) (line string, action InputAction) {
	session := NewLineReadSession(kb, promptWidth)
	for {
		line, act, done, err := session.Poll()
		if err != nil {
			return session.Editor().String(), ActionQuit
		}
		if done {
			return line, act
		}
	}
}

func HandleEscape(data []byte, s *LineReadSession) (int, int) {
	ed := s.Ed
	if len(data) < 2 {
		return -1, 1
	}

	if data[1] == '[' {
		seqLen, ok := CsiSequenceLength(data)
		if !ok {
			return -1, len(data)
		}
		final := data[seqLen-1]
		params := string(data[2 : seqLen-1])

		switch final {
		case 'A':
			if s.BrowseHistoryPrev() {
				return -1, seqLen
			}
			return -1, seqLen
		case 'B':
			if s.BrowseHistoryNext() {
				return -1, seqLen
			}
			return -1, seqLen
		case 'C':
			if strings.HasSuffix(params, ";5") {
				MoveWordForward(ed)
				return -1, seqLen
			}
			if ed.Pos < len(ed.Runes) {
				w := RuneWidth(ed.Runes[ed.Pos])
				ed.Pos++
				fmt.Printf("\033[%dC", w)
			}
			return -1, seqLen
		case 'D':
			if strings.HasSuffix(params, ";5") {
				MoveWordBack(ed)
				return -1, seqLen
			}
			if ed.Pos > 0 {
				ed.Pos--
				w := RuneWidth(ed.Runes[ed.Pos])
				fmt.Printf("\033[%dD", w)
			}
			return -1, seqLen
		case 'H':
			MoveCursorTo(ed, 0)
			return -1, seqLen
		case 'F':
			MoveCursorTo(ed, len(ed.Runes))
			return -1, seqLen
		case '~':
			switch CsiPrimaryParam(params) {
			case "1", "7":
				MoveCursorTo(ed, 0)
				return -1, seqLen
			case "3":
				s.LeaveHistoryBrowse()
				ed.Delete()
				RedrawFull(ed)
				return -1, seqLen
			case "4", "8":
				MoveCursorTo(ed, len(ed.Runes))
				return -1, seqLen
			case "200":
				s.Pasting = true
				return -1, seqLen
			case "201":
				s.Pasting = false
				return -1, seqLen
			}
			return -1, seqLen
		}

		return -1, seqLen
	}

	if data[1] == 'O' {
		if len(data) < 3 {
			return -1, 2
		}
		switch data[2] {
		case 'H':
			MoveCursorTo(ed, 0)
			return -1, 3
		case 'F':
			MoveCursorTo(ed, len(ed.Runes))
			return -1, 3
		}
		return -1, 3
	}

	return -1, 1
}

func CsiPrimaryParam(params string) string {
	if params == "" {
		return ""
	}
	if i := strings.IndexByte(params, ';'); i >= 0 {
		return params[:i]
	}
	return params
}

func MoveCursorTo(ed *LineEditor, newPos int) {
	if newPos == ed.Pos {
		return
	}
	if newPos < ed.Pos {
		w := ed.DisplayWidthRange(newPos, ed.Pos)
		fmt.Printf("\033[%dD", w)
	} else {
		w := ed.DisplayWidthRange(ed.Pos, newPos)
		fmt.Printf("\033[%dC", w)
	}
	ed.Pos = newPos
}

func RedrawFromCursor(ed *LineEditor) {
	for i := ed.Pos; i < len(ed.Runes); i++ {
		fmt.Print(string(ed.Runes[i]))
	}
	fmt.Print("\033[K")
	tailW := ed.DisplayWidthRange(ed.Pos, len(ed.Runes))
	if tailW > 0 {
		fmt.Printf("\033[%dD", tailW)
	}
}

// termCols returns the terminal width, falling back to 120 when unknown.
func termCols() int {
	w := TerminalColumns()
	if w <= 0 {
		return 120
	}
	return w
}

// clearCurrentAndBelow 把光标先回到 prompt 起始行,然后清除该行到屏幕底
// 部(包括上次绘制占用的所有下方行)。
//
// 必须处理两个独立的"行偏移":
//   - LastCursorRow:上次绘制后光标所在行(可能在内容中间,例如用户按了
//     Home/方向键让 Pos < len(Runes))。进入 clear 时光标就在这一行。
//   - LastEndRow:上次绘制的内容末尾行。清屏时要确保从 prompt 行一路
//     清到 LastEndRow,否则末尾行及其后的旧内容残留,就是"重影"。
//
// 步骤:
//   1. 按 LastCursorRow 上移到 prompt 起始行(\033[NA 仅用于上移到我们
//      确知已经打出过内容的行,不会越界到 prompt 之前)。
//   2. \r + \033[K 清 prompt 起始行。
//   3. 对剩下的 LastEndRow 行,\n + \033[K 逐行往下清。
//   4. \033[NA 原路回到 prompt 起始行首,作为新内容起点。
func clearCurrentAndBelow(ed *LineEditor) {
	if ed.LastCursorRow > 0 {
		fmt.Printf("\033[%dA", ed.LastCursorRow)
	}
	fmt.Print("\r\033[K")
	for i := 0; i < ed.LastEndRow; i++ {
		fmt.Print("\n\033[K")
	}
	if ed.LastEndRow > 0 {
		fmt.Printf("\033[%dA\r", ed.LastEndRow)
	}
	ed.LastEndRow = 0
	ed.LastCursorRow = 0
}

func RedrawFull(ed *LineEditor) {
	clearCurrentAndBelow(ed)
	if ed.PromptWidth > 0 {
		fmt.Printf("\033[%dC", ed.PromptWidth)
	}
	for _, r := range ed.Runes {
		fmt.Print(string(r))
	}

	// 计算光标最终位置的行/列,供下次重绘使用。
	cols := termCols()
	totalW := ed.PromptWidth + ed.DisplayWidthRange(0, len(ed.Runes))
	endRow := totalW / cols

	// 把光标从内容末尾移到 ed.Pos 的位置。
	cursorW := ed.PromptWidth + ed.DisplayWidthRange(0, ed.Pos)
	curRow, curCol := cursorW/cols, cursorW%cols

	if endRow > curRow {
		fmt.Printf("\033[%dA", endRow-curRow)
	}
	fmt.Print("\r")
	if curCol > 0 {
		fmt.Printf("\033[%dC", curCol)
	}

	ed.LastEndRow = endRow
	ed.LastCursorRow = curRow
}

func RedrawClear(ed *LineEditor) {
	clearCurrentAndBelow(ed)
}

func MoveWordBack(ed *LineEditor) {
	if ed.Pos == 0 {
		return
	}
	for ed.Pos > 0 && unicode.IsSpace(ed.Runes[ed.Pos-1]) {
		ed.Pos--
	}
	for ed.Pos > 0 && !unicode.IsSpace(ed.Runes[ed.Pos-1]) {
		ed.Pos--
	}
	RedrawFull(ed)
}

func MoveWordForward(ed *LineEditor) {
	n := len(ed.Runes)
	if ed.Pos >= n {
		return
	}
	for ed.Pos < n && !unicode.IsSpace(ed.Runes[ed.Pos]) {
		ed.Pos++
	}
	for ed.Pos < n && unicode.IsSpace(ed.Runes[ed.Pos]) {
		ed.Pos++
	}
	RedrawFull(ed)
}

func DeleteWordBack(ed *LineEditor) {
	if ed.Pos == 0 {
		return
	}
	orig := ed.Pos
	for ed.Pos > 0 && unicode.IsSpace(ed.Runes[ed.Pos-1]) {
		ed.Pos--
	}
	for ed.Pos > 0 && !unicode.IsSpace(ed.Runes[ed.Pos-1]) {
		ed.Pos--
	}
	ed.Runes = append(ed.Runes[:ed.Pos], ed.Runes[orig:]...)
	RedrawFull(ed)
}

func RuneWidth(r rune) int {
	if IsCJK(r) {
		return 2
	}
	return 1
}

func IsCJK(r rune) bool {
	return (r >= 0x2E80 && r <= 0x9FFF) ||
		(r >= 0xAC00 && r <= 0xD7AF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE4F) ||
		(r >= 0xFF01 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x20000 && r <= 0x2FA1F)
}
