package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

func TestSearchPanelEntries(t *testing.T) {
	m := New(nopRunner(), nil)
	m.projects = []Project{{Name: "app", Path: "/app/.wyrm.toml"}}
	m.sessions = []sessions.Session{{ID: "$1", Name: "app"}}
	m.windows = []tmux.WindowInfo{{ID: "@1", Name: "code", Index: 2}}
	m.panes = []tmux.PaneInfo{{ID: "%1", Command: "nvim", Path: "/app"}}
	m.cur = [numPanels]int{}
	for _, p := range allPanels {
		m.focus, m.filter = p, "does-not-match"
		entries := m.searchPanelEntries(p)
		if len(entries) != 1 || entries[0].panel != p {
			t.Fatalf("panel %v: %+v", p, entries)
		}
		if p >= panelSessions && entries[0].sessionID != "$1" {
			t.Fatalf("lost session identity: %+v", entries[0])
		}
	}
}

func TestSearchGlobalEntries(t *testing.T) {
	m := New(funcRunner{fn: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "$1|1|0|1|one\n$2|1|0|1|two\n", nil
		case "list-panes":
			return strings.Join([]string{"$2", "two", "@3", "0", "code", "%4", "0", "nvim"}, "\x01") + "\n" + strings.Join([]string{"$2", "two", "@3", "0", "code", "%5", "1", "zsh"}, "\x01"), nil
		}
		return "", nil
	}}, nil)
	_, cmd := m.startSearch(true)
	msg := cmd().(searchEntriesMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	counts := map[panel]int{}
	for _, entry := range msg.entries {
		counts[entry.panel]++
	}
	if counts[panelSessions] != 2 || counts[panelWindows] != 1 || counts[panelPanes] != 2 {
		t.Fatalf("counts: %v", counts)
	}
}

func TestSearchLocationAcrossSessions(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.filter = "old"
	entry := searchEntry{panel: panelPanes, sessionID: "$9", windowID: "@9", paneID: "%9"}
	next, cmd := m.handleSearchLocation(searchLocationMsg{entry: entry,
		sessions: []sessions.Session{{ID: "$1", Name: "old"}, {ID: "$9", Name: "new"}},
		windows:  []tmux.WindowInfo{{ID: "@8", Active: true}, {ID: "@9"}},
		panes:    []tmux.PaneInfo{{ID: "%8", Active: true}, {ID: "%9", Command: "nvim"}},
	})
	result := next.(Model)
	s, _ := result.currentSession()
	w, _ := result.currentWindow()
	p, _ := result.currentPane()
	if result.mode != modeNormal || result.focus != panelPanes || result.filter != "" || s.ID != "$9" || w.ID != "@9" || p.ID != "%9" || cmd == nil {
		t.Fatalf("selection: %v %v %v", s, w, p)
	}
	if result.pendingAttach != "" {
		t.Fatal("search attached instead of focusing")
	}
}

func TestSearchCancelPreservesFilter(t *testing.T) {
	m := mouseModel(t)
	m.mode, m.searchReturnMode, m.filtering, m.filter = modeSearch, modeFilter, true, "web"
	next, cmd := m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	result := next.(Model)
	if result.mode != modeFilter || !result.filtering || result.filter != "web" || cmd != nil {
		t.Fatalf("cancel: %+v", result)
	}
}

func TestSearchBindingsWithoutExternalExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, focus := range allPanels {
		for _, searchKey := range []tea.KeyMsg{{Type: tea.KeyCtrlF}, {Type: tea.KeyCtrlZ}} {
			m := mouseModel(t)
			m.focus = focus
			next, cmd := update(m, searchKey)
			if next.mode != modeSearch || next.err != nil {
				t.Fatalf("panel %v key %s: mode %v error %v", focus, searchKey.String(), next.mode, next.err)
			}
			if searchKey.Type == tea.KeyCtrlZ && cmd == nil {
				t.Fatal("all search did not load server entries")
			}
		}
	}
}

func TestSearchProjectAndStoppedSessionSelection(t *testing.T) {
	for _, p := range []panel{panelProjects, panelSessions} {
		m := mouseModel(t)
		m.mode = modeSearch
		project := Project{Name: "new-project", Root: "/new-project", Zoxide: true}
		next, _ := m.handleSearchResult(searchResultMsg{entry: &searchEntry{panel: p, project: project, sessionName: project.Name}})
		result := next.(Model)
		if result.mode != modeNormal || result.focus != p {
			t.Fatalf("mode/focus: %v %v", result.mode, result.focus)
		}
		if p == panelProjects {
			selected, ok := result.currentProject()
			if !ok || selected.Root != project.Root {
				t.Fatalf("project: %+v", selected)
			}
		} else {
			selected, ok := result.currentSessionEntry()
			if !ok || selected.Running || selected.Project.Root != project.Root || len(result.windows) != 0 || len(result.panes) != 0 {
				t.Fatalf("stopped session: %+v", selected)
			}
		}
	}
}

func TestSearchVanishedPanePreservesSelection(t *testing.T) {
	m := mouseModel(t)
	m.mode, m.searchReturnMode = modeSearch, modeNormal
	original, _ := m.currentSession()
	next, cmd := m.handleSearchLocation(searchLocationMsg{
		entry:    searchEntry{panel: panelPanes, sessionID: "$9", windowID: "@9", paneID: "%9"},
		sessions: []sessions.Session{{ID: "$9", Name: "new"}},
		windows:  []tmux.WindowInfo{{ID: "@9"}},
		panes:    []tmux.PaneInfo{{ID: "%8"}},
	})
	result := next.(Model)
	selected, _ := result.currentSession()
	if result.mode != modeNormal || result.info == "" || selected.ID != original.ID || cmd != nil {
		t.Fatalf("vanished result changed selection: %+v", selected)
	}
}

func TestSearchTypingRankingAndUnicodeBackspace(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.searchEntries = []searchEntry{{label: "d-e-v", paneID: "%1"}, {label: "dev", paneID: "%2"}, {label: "café", paneID: "%3"}}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("dev")})
	matches := m.visibleSearchEntries()
	if len(matches) != 2 || matches[0].paneID != "%2" {
		t.Fatalf("ranking: %+v", matches)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.searchCur != 1 {
		t.Fatalf("cursor %d", m.searchCur)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("café")})
	if matches = m.visibleSearchEntries(); len(matches) != 1 || matches[0].paneID != "%3" {
		t.Fatalf("unicode: %+v", matches)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.searchQuery != "caf" || m.searchCur != 0 {
		t.Fatalf("query %q cursor %d", m.searchQuery, m.searchCur)
	}
}

func TestSearchSelectionAfterFiltering(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.searchEntries = []searchEntry{
		{panel: panelProjects, label: "same", project: Project{Name: "same", Root: "/first", Zoxide: true}},
		{panel: panelProjects, label: "same", project: Project{Name: "same", Root: "/second", Zoxide: true}},
	}
	m.searchQuery = "same"
	m.searchCur = 1
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	selected, ok := m.currentProject()
	if !ok || selected.Root != "/second" || m.mode != modeNormal {
		t.Fatalf("selected %+v mode %v", selected, m.mode)
	}
}

func TestSearchEmptyResultsAndLoading(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.searchEntries = []searchEntry{{label: "hello"}}
	m.searchQuery = "xyz"
	for _, keyType := range []tea.KeyType{tea.KeyDown, tea.KeyUp, tea.KeyEnter} {
		var cmd tea.Cmd
		m, cmd = update(m, tea.KeyMsg{Type: keyType})
		if m.mode != modeSearch || m.searchCur != 0 || cmd != nil {
			t.Fatalf("empty search: key %v", keyType)
		}
	}
	m.searchQuery = ""
	m.searchLoading = true
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeSearch || cmd != nil {
		t.Fatal("selected while loading")
	}
}

func TestSearchIgnoresStaleResponses(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.searchID = 2
	m.searchLoading = true
	m, _ = update(m, searchEntriesMsg{id: 1, entries: []searchEntry{{label: "stale"}}})
	if !m.searchLoading || len(m.searchEntries) != 0 {
		t.Fatal("accepted old candidates")
	}
	m, _ = update(m, searchLocationMsg{id: 1, entry: searchEntry{panel: panelProjects, project: Project{Name: "stale"}}})
	if m.mode != modeSearch || m.focus != panelSessions {
		t.Fatal("accepted old selection")
	}
	m, _ = update(m, searchEntriesMsg{id: 2, entries: []searchEntry{{label: "current"}}, scope: "All"})
	if m.searchLoading || len(m.searchEntries) != 1 {
		t.Fatal("ignored current candidates")
	}
}

func TestSearchScopeSwitchPreservesQueryAndReturnMode(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeFilter
	m.filtering = true
	m.filter = "web"
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlF})
	firstID := m.searchID
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.searchQuery != "web" || m.searchReturnMode != modeFilter || m.searchID == firstID || !m.searchLoading || cmd == nil {
		t.Fatal("scope switch lost search state")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeFilter || m.filter != "web" || !m.filtering {
		t.Fatal("cancel lost filter")
	}
}

func TestSearchOverlayFitsTerminal(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeSearch
	m.searchScope = "All"
	m.searchEntries = []searchEntry{{label: "Project  café\x1b[31m\n\tremote"}}
	for _, size := range [][2]int{{100, 40}, {30, 10}, {10, 6}, {3, 3}} {
		m.width, m.height = size[0], size[1]
		rendered := m.View()
		if lipgloss.Width(rendered) > m.width || lipgloss.Height(rendered) > m.height {
			t.Fatalf("overlay does not fit %v: %dx%d", size, lipgloss.Width(rendered), lipgloss.Height(rendered))
		}
	}
	m.width, m.height = 100, 40
	m.searchQuery = "missing"
	if !strings.Contains(m.View(), "no matching results") {
		t.Fatal("missing empty-state hint")
	}
}
