package tui

import (
	"charm.land/bubbles/v2/key"
)

// ViewMode identifies the active screen in the TUI.
type ViewMode int

const (
	ViewFleet ViewMode = iota
	ViewDetail
	ViewCrashes
)

func (v ViewMode) ContextName() string {
	switch v {
	case ViewDetail:
		return "detail"
	case ViewCrashes:
		return "crashes"
	default:
		return "fleet"
	}
}

// KeyMap implements help.KeyMap and is surfaced both in-terminal and as Booba Terminal Actions.
type KeyMap struct {
	Up         key.Binding
	Down       key.Binding
	Enter      key.Binding
	Back       key.Binding
	Project    key.Binding
	Sort       key.Binding
	SortDir    key.Binding
	Crashes    key.Binding
	FixCrash   key.Binding
	Refresh    key.Binding
	More       key.Binding
	Visibility key.Binding
	Quit       key.Binding
	mode       ViewMode
}

// NewKeyMap returns the keybindings configured for the current view mode and help visibility.
func NewKeyMap(mode ViewMode, hideHelp bool) KeyMap {
	visLabel := "Hide help"
	if hideHelp {
		visLabel = "Show help"
	}
	crashesLabel := "Open crashes"
	if mode == ViewCrashes {
		crashesLabel = "Fleet targets"
	}

	return KeyMap{
		Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "Move up")),
		Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "Move down")),
		Enter:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "Target detail")),
		Back:       key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "Back")),
		Project:    key.NewBinding(key.WithKeys("p", "tab"), key.WithHelp("p/tab", "Filter project")),
		Sort:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "Sort column")),
		SortDir:    key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "Reverse sort")),
		Crashes:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", crashesLabel)),
		FixCrash:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "Mark crash fixed")),
		Refresh:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "Refresh now")),
		More:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "Full help")),
		Visibility: key.NewBinding(key.WithKeys("h"), key.WithHelp("h", visLabel)),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "Quit")),
		mode:       mode,
	}
}

// ShortHelp implements help.KeyMap.
func (k KeyMap) ShortHelp() []key.Binding {
	switch k.mode {
	case ViewDetail:
		return []key.Binding{k.Back, k.Up, k.Down, k.Refresh, k.Crashes, k.More}
	case ViewCrashes:
		return []key.Binding{k.Enter, k.Back, k.Up, k.Down, k.FixCrash, k.Crashes, k.Refresh}
	default:
		return []key.Binding{k.Enter, k.Up, k.Down, k.Sort, k.Project, k.Crashes, k.Refresh}
	}
}

// FullHelp implements help.KeyMap.
func (k KeyMap) FullHelp() [][]key.Binding {
	switch k.mode {
	case ViewDetail:
		return [][]key.Binding{
			{k.Back, k.Up, k.Down},
			{k.Refresh, k.Crashes, k.More, k.Visibility, k.Quit},
		}
	case ViewCrashes:
		return [][]key.Binding{
			{k.Enter, k.Back, k.Up, k.Down, k.FixCrash},
			{k.Crashes, k.Refresh, k.More, k.Visibility, k.Quit},
		}
	default:
		return [][]key.Binding{
			{k.Enter, k.Up, k.Down, k.Sort, k.SortDir, k.Project},
			{k.Crashes, k.Refresh, k.More, k.Visibility, k.Quit},
		}
	}
}
