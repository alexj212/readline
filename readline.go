package readline

import (
	"bytes"
	"os"
	"regexp"
)

var rxMultiline = regexp.MustCompile(`[\r\n]+`)

// Readline displays the readline prompt.
// It will return a string (user entered data) or an error.
func (rl *Instance) Readline() (string, error) {
	fd := int(os.Stdin.Fd())
	state, err := MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer Restore(fd, state)

	// Here we have to either print prompt and return new line (multiline)
	// Or use the prompt value as multiline and therefore not printing anything here
	// print(rl.prompt)

	rl.line = []rune{}
	rl.viUndoHistory = []undoItem{{line: "", pos: 0}}
	rl.pos = 0
	rl.histPos = rl.History.Len()
	rl.modeViMode = vimInsert

	rl.computePrompt() // initialise the prompt for first print

	rl.resetHintText()
	rl.resetTabCompletion()

	if len(rl.multisplit) > 0 {
		r := []rune(rl.multisplit[0])
		rl.editorInput(r)
		rl.carridgeReturn()
		if len(rl.multisplit) > 1 {
			rl.multisplit = rl.multisplit[1:]
		} else {
			rl.multisplit = []string{}
		}
		return string(rl.line), nil
	}

	rl.getHintText()
	rl.renderHelpers()

	for {
		rl.viUndoSkipAppend = false
		b := make([]byte, 1024)
		var i int

		if !rl.skipStdinRead {
			var err error
			i, err = os.Stdin.Read(b)
			if err != nil {
				return "", err
			}
		}

		rl.skipStdinRead = false
		r := []rune(string(b))

		if isMultiline(r[:i]) || len(rl.multiline) > 0 {
			rl.multiline = append(rl.multiline, b[:i]...)
			if i == len(b) {
				continue
			}

			if !rl.allowMultiline(rl.multiline) {
				rl.multiline = []byte{}
				continue
			}

			s := string(rl.multiline)
			rl.multisplit = rxMultiline.Split(s, -1)

			r = []rune(rl.multisplit[0])
			rl.modeViMode = vimInsert
			rl.editorInput(r)
			rl.carridgeReturn()
			rl.multiline = []byte{}
			if len(rl.multisplit) > 1 {
				rl.multisplit = rl.multisplit[1:]
			} else {
				rl.multisplit = []string{}
			}
			return string(rl.line), nil
		}

		s := string(r[:i])
		if rl.evtKeyPress[s] != nil {
			rl.clearHelpers()

			ret := rl.evtKeyPress[s](s, rl.line, rl.pos)

			rl.clearLine()
			rl.line = append(ret.NewLine, []rune{}...)
			rl.echo()
			rl.pos = ret.NewPos

			if ret.ClearHelpers {
				rl.resetHelpers()
			} else {
				rl.updateHelpers()
				rl.renderHelpers()
			}

			if len(ret.HintText) > 0 {
				rl.hintText = ret.HintText
				rl.clearHelpers()
				rl.renderHelpers()
			}
			if !ret.ForwardKey {
				continue
			}
			if ret.CloseReadline {
				rl.clearHelpers()
				return string(rl.line), nil
			}
		}

		switch b[0] {
		case charCtrlC:
			rl.clearHelpers()
			return "", CtrlC

		case charEOF:
			rl.clearHelpers()
			return "", EOF

		case charCtrlF:

			// Both these settings apply to when we already
			// are in completion mode and when we are not.
			rl.searchMode = CompletionFind
			rl.modeAutoFind = true

			// Switch from history to completion search
			if rl.modeTabCompletion && rl.searchMode == HistoryFind {
				rl.searchMode = CompletionFind
			}

			rl.updateTabFind([]rune{})
			rl.viUndoSkipAppend = true

		case charCtrlR:
			rl.searchMode = HistoryFind
			rl.modeAutoFind = true
			rl.tcOffset = 0
			rl.modeTabCompletion = true

			rl.modeTabFind = true
			rl.updateTabFind([]rune{})
			rl.viUndoSkipAppend = true

		case charCtrlU:
			rl.clearLine()
			rl.resetHelpers()

		case charTab:
			if rl.modeTabCompletion {
				rl.moveTabCompletionHighlight(1, 0)
			} else {
				rl.getTabCompletion()
			}

			rl.renderHelpers()
			rl.viUndoSkipAppend = true

		case '\r':
			fallthrough
		case '\n':

			if rl.modeTabCompletion {
				cur := rl.getCurrentGroup()
				cell := (cur.tcMaxX * (cur.tcPosY - 1)) + cur.tcOffset + cur.tcPosX - 1
				rl.clearHelpers()
				rl.resetTabCompletion()
				rl.renderHelpers()
				rl.insert([]rune(cur.Suggestions[cell]))

				continue
			}
			rl.carridgeReturn()
			return string(rl.line), nil

		case charBackspace, charBackspace2:
			if rl.modeTabFind || rl.modeAutoFind {
				rl.backspaceTabFind()
				rl.viUndoSkipAppend = true
			} else {
				rl.backspace()
				rl.renderHelpers()
			}

		case charEscape:
			rl.escapeSeq(r[:i])

		default:
			// Not sure that CompletionFind is useful, nor one of the other two
			if rl.modeAutoFind || rl.modeTabFind && rl.searchMode == CompletionFind {
				rl.updateTabFind(r[:i])
				rl.viUndoSkipAppend = true
			} else {
				rl.editorInput(r[:i])
				if len(rl.multiline) > 0 && rl.modeViMode == vimKeys {
					rl.skipStdinRead = true
				}
			}
		}

		//if !rl.viUndoSkipAppend {
		//	rl.viUndoHistory = append(rl.viUndoHistory, rl.line)
		//}
		rl.undoAppendHistory()
	}
}

func (rl *Instance) escapeSeq(r []rune) {
	switch string(r) {
	case string(charEscape):
		switch {
		case rl.modeAutoFind:
			rl.resetTabFind()
			rl.clearHelpers()
			rl.resetTabCompletion()
			rl.renderHelpers()

		case rl.modeTabFind:
			rl.resetTabFind()

		case rl.modeTabCompletion:
			rl.clearHelpers()
			rl.resetTabCompletion()
			rl.renderHelpers()

		default:
			if rl.pos == len(rl.line) && len(rl.line) > 0 {
				rl.pos--
				moveCursorBackwards(1)
			}
			rl.modeViMode = vimKeys
			rl.viIteration = ""
			//rl.viHintVimKeys()
			// rl.viHintMessage()
			rl.refreshVimStatus()

			// Added by me, to refresh Vim status in prompt
			rl.clearHelpers()
			rl.renderHelpers()
		}
		rl.viUndoSkipAppend = true

	case seqDelete:
		if rl.modeTabFind {
			rl.backspaceTabFind()
		} else {
			rl.delete()
		}

	case seqUp:
		if rl.modeTabCompletion {
			rl.moveTabCompletionHighlight(0, -1)
			rl.renderHelpers()
			return
		}
		rl.walkHistory(-1)

	case seqDown:
		if rl.modeTabCompletion {
			rl.moveTabCompletionHighlight(0, 1)
			rl.renderHelpers()
			return
		}
		rl.walkHistory(1)

	case seqBackwards:
		if rl.modeTabCompletion {
			rl.moveTabCompletionHighlight(-1, 0)
			rl.renderHelpers()
			return
		}
		if rl.pos > 0 {
			moveCursorBackwards(1)
			rl.pos--
		}
		rl.viUndoSkipAppend = true

	case seqForwards:
		if rl.modeTabCompletion {
			rl.moveTabCompletionHighlight(1, 0)
			rl.renderHelpers()
			return
		}
		if (rl.modeViMode == vimInsert && rl.pos < len(rl.line)) ||
			(rl.modeViMode != vimInsert && rl.pos < len(rl.line)-1) {
			moveCursorForwards(1)
			rl.pos++
		}
		rl.viUndoSkipAppend = true

	case seqHome, seqHomeSc:
		if rl.modeTabCompletion {
			return
		}
		moveCursorBackwards(rl.pos)
		rl.pos = 0
		rl.viUndoSkipAppend = true

	case seqEnd, seqEndSc:
		if rl.modeTabCompletion {
			return
		}
		moveCursorForwards(len(rl.line) - rl.pos)
		rl.pos = len(rl.line)
		rl.viUndoSkipAppend = true

	case seqShiftTab:
		if rl.modeTabCompletion {
			rl.moveTabCompletionHighlight(-1, 0)
			rl.renderHelpers()
			return
		}

	default:
		if rl.modeTabFind {
			return
		}
		// alt+numeric append / delete
		if len(r) == 2 && '1' <= r[1] && r[1] <= '9' {
			if rl.modeViMode == vimDelete {
				rl.vimDelete(r)
				return
			}

			line, err := rl.History.GetLine(rl.History.Len() - 1)
			if err != nil {
				return
			}

			tokens, _, _ := tokeniseSplitSpaces([]rune(line), 0)
			pos := int(r[1]) - 48 // convert ASCII to integer
			if pos > len(tokens) {
				return
			}
			rl.insert([]rune(tokens[pos-1]))
		} else {
			rl.viUndoSkipAppend = true
		}
	}
}

// editorInput is an unexported function used to determine what mode of text
// entry readline is currently configured for and then update the line entries
// accordingly.
func (rl *Instance) editorInput(r []rune) {
	switch rl.modeViMode {
	case vimKeys:
		rl.vi(r[0])
		// rl.viHintMessage()
		rl.refreshVimStatus()

	case vimDelete:
		rl.vimDelete(r)
		// rl.viHintMessage()
		rl.refreshVimStatus()

	case vimReplaceOnce:
		rl.modeViMode = vimKeys
		rl.delete()
		rl.insert([]rune{r[0]})
		// rl.viHintMessage()
		rl.refreshVimStatus()

	case vimReplaceMany:
		for _, char := range r {
			rl.delete()
			rl.insert([]rune{char})
		}
		// rl.viHintMessage()
		rl.refreshVimStatus()

	default:
		rl.insert(r)
	}

	if len(rl.multisplit) == 0 {
		rl.syntaxCompletion()
	}
}

// SetPrompt will define the readline prompt string.
// It also calculates the runes in the string as well as any non-printable
// escape codes.
func (rl *Instance) SetPrompt(s string) {
	rl.prompt = s
	rl.promptLen = strLen(s)
}

func (rl *Instance) carridgeReturn() {
	rl.clearHelpers()
	print("\r\n")
	if rl.HistoryAutoWrite {
		var err error
		rl.histPos, err = rl.History.Write(string(rl.line))
		if err != nil {
			print(err.Error() + "\r\n")
		}
	}
}

func isMultiline(r []rune) bool {
	for i := range r {
		if (r[i] == '\r' || r[i] == '\n') && i != len(r)-1 {
			return true
		}
	}
	return false
}

func (rl *Instance) allowMultiline(data []byte) bool {
	rl.clearHelpers()
	printf("\r\nWARNING: %d bytes of multiline data was dumped into the shell!", len(data))
	for {
		print("\r\nDo you wish to proceed (yes|no|preview)? [y/n/p] ")

		b := make([]byte, 1024)

		i, err := os.Stdin.Read(b)
		if err != nil {
			return false
		}

		s := string(b[:i])
		print(s)

		switch s {
		case "y", "Y":
			print("\r\n" + rl.prompt)
			return true

		case "n", "N":
			print("\r\n" + rl.prompt)
			return false

		case "p", "P":
			preview := string(bytes.Replace(data, []byte{'\r'}, []byte{'\r', '\n'}, -1))
			if rl.SyntaxHighlighter != nil {
				preview = rl.SyntaxHighlighter([]rune(preview))
			}
			print("\r\n" + preview)

		default:
			print("\r\nInvalid response. Please answer `y` (yes), `n` (no) or `p` (preview)")
		}
	}
}
