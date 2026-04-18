package main

import "github.com/leef-l/brain/cmd/brain/term"

type inputAction = term.InputAction
type lineEditor = term.LineEditor
type lineReadSession = term.LineReadSession
type keybindings = term.Keybindings

const (
	actionEnter  = term.ActionEnter
	actionQueue  = term.ActionQueue
	actionCycle  = term.ActionCycle
	actionCancel = term.ActionCancel
	actionQuit   = term.ActionQuit
	actionEscape = term.ActionEscape
)

type SelectorOption = term.SelectorOption
type SelectorResult = term.SelectorResult

var (
	newLineReadSession = term.NewLineReadSession
	readLineRaw        = term.ReadLineRaw
	runeWidth          = term.RuneWidth
	redrawFull         = term.RedrawFull
	redrawClear        = term.RedrawClear
	editorBlank        = term.EditorBlank

	defaultKeybindings = term.DefaultKeybindings
	keybindingsPath    = term.KeybindingsPath
	loadKeybindings    = term.LoadKeybindings
	keyToChar          = term.KeyToChar
	keybindingsHelp    = term.KeybindingsHelp

	startSpinner  = term.StartSpinner
	updateSpinner = term.UpdateSpinner
	stopSpinner   = term.StopSpinner

	RunSelector         = term.RunSelector
	RunSelectorWithChan = term.RunSelectorWithChan

	enableRawInput    = term.EnableRawInput
	waitForStdinReady = term.WaitForStdinReady
	terminalColumns   = term.TerminalColumns
	terminalRows      = term.TerminalRows
)
