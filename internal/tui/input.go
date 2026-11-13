package tui

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// input keeps text editing local to the TUI, without a terminal clipboard or
// any additional module dependencies. Cursor positions are Unicode code points.
type input struct {
	Prompt, Placeholder string
	CharLimit           int
	value               []rune
	cursor              int
	focused             bool
}

func newInput() input              { return input{} }
func (t *input) SetValue(v string) { t.value = []rune(v); t.cursor = len(t.value) }
func (t input) Value() string      { return string(t.value) }
func (t *input) Focus() tea.Cmd    { t.focused = true; return nil }
func (t *input) Blur()             { t.focused = false }
func (t input) View() string {
	if len(t.value) == 0 && !t.focused {
		return t.Prompt + t.Placeholder
	}
	value := string(t.value)
	if t.focused {
		value = string(t.value[:t.cursor]) + "▏" + string(t.value[t.cursor:])
	}
	return t.Prompt + value
}
func (t input) Update(message tea.Msg) (input, tea.Cmd) {
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return t, nil
	}
	switch key.String() {
	case "left", "ctrl+b":
		if t.cursor > 0 {
			t.cursor--
		}
	case "right", "ctrl+f":
		if t.cursor < len(t.value) {
			t.cursor++
		}
	case "home", "ctrl+a":
		t.cursor = 0
	case "end", "ctrl+e":
		t.cursor = len(t.value)
	case "backspace", "ctrl+h":
		if t.cursor > 0 {
			t.value = append(t.value[:t.cursor-1], t.value[t.cursor:]...)
			t.cursor--
		}
	case "delete", "ctrl+d":
		if t.cursor < len(t.value) {
			t.value = append(t.value[:t.cursor], t.value[t.cursor+1:]...)
		}
	case "ctrl+u":
		t.value = append([]rune(nil), t.value[t.cursor:]...)
		t.cursor = 0
	case "ctrl+k":
		t.value = t.value[:t.cursor]
	case "ctrl+w":
		start := t.cursor
		for start > 0 && t.value[start-1] == ' ' {
			start--
		}
		for start > 0 && t.value[start-1] != ' ' {
			start--
		}
		t.value = append(t.value[:start], t.value[t.cursor:]...)
		t.cursor = start
	default:
		if len(key.Runes) > 0 && key.Type == tea.KeyRunes {
			if t.CharLimit > 0 && len(t.value)+len(key.Runes) > t.CharLimit {
				break
			}
			// Control characters and invalid text are not part of a text field.
			valid := true
			for _, r := range key.Runes {
				if r < 32 || r == utf8.RuneError {
					valid = false
					break
				}
			}
			if !valid {
				break
			}
			next := make([]rune, 0, len(t.value)+len(key.Runes))
			next = append(next, t.value[:t.cursor]...)
			next = append(next, key.Runes...)
			next = append(next, t.value[t.cursor:]...)
			t.value = next
			t.cursor += len(key.Runes)
		}
	}
	return t, nil
}
