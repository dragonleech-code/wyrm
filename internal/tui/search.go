package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// Search results retain typed identities independently of their display labels.
type searchEntry struct {
	panel                                    panel
	label                                    string
	project                                  Project
	sessionID, sessionName, windowID, paneID string
}

type searchEntriesMsg struct {
	id      int
	entries []searchEntry
	scope   string
	err     error
}
type searchResultMsg struct {
	entry *searchEntry
	err   error
}
type searchLocationMsg struct {
	id       int
	entry    searchEntry
	sessions []sessions.Session
	windows  []tmux.WindowInfo
	panes    []tmux.PaneInfo
	err      error
}

func searchLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (m Model) searchPanelEntries(p panel) []searchEntry {
	var out []searchEntry
	switch p {
	case panelProjects:
		for _, project := range m.projects {
			out = append(out, searchEntry{panel: p, project: project, label: "Project  " + project.Name + "  " + project.Path + "  " + project.Root})
		}
	case panelSessions:
		for _, s := range m.sessionEntries() {
			label := "Session  " + s.Name
			if !s.Running {
				label += "  (stopped)"
			}
			out = append(out, searchEntry{panel: p, sessionID: s.Session.ID, sessionName: s.Name, project: s.Project, label: label})
		}
	case panelWindows:
		s, ok := m.currentSession()
		if !ok {
			break
		}
		for _, w := range m.windows {
			out = append(out, searchEntry{panel: p, sessionID: s.ID, windowID: w.ID, label: fmt.Sprintf("Window  %s / %d:%s  %s", s.Name, w.Index, w.Name, w.ID)})
		}
	case panelPanes:
		s, sok := m.currentSession()
		w, wok := m.currentWindow()
		if !sok || !wok {
			break
		}
		for _, pane := range m.panes {
			out = append(out, searchEntry{panel: p, sessionID: s.ID, windowID: w.ID, paneID: pane.ID, label: fmt.Sprintf("Pane  %s / %d:%s / %d  %s  %s  %s", s.Name, w.Index, w.Name, pane.Index, pane.ID, pane.Command, pane.Path)})
		}
	}
	return out
}

func (m Model) startSearch(all bool) (tea.Model, tea.Cmd) {
	m.err, m.info = nil, ""
	if m.mode != modeSearch {
		m.searchReturnMode = m.mode
		m.searchQuery = m.filter
	}
	m.mode = modeSearch
	m.searchID++
	m.searchCur = 0
	m.searchEntries = nil
	m.searchLoading, m.searchResolving = all, false
	m.searchScope = m.focus.spec().title
	if all {
		m.searchScope = "All"
	}
	if !all {
		return m.handleSearchEntries(searchEntriesMsg{id: m.searchID, entries: m.searchPanelEntries(m.focus), scope: m.searchScope})
	}
	return m, func() tea.Msg {
		list, err := sessions.List(m.runner)
		if err != nil {
			return searchEntriesMsg{id: m.searchID, err: err}
		}
		snapshot := m
		snapshot.sessions = list
		snapshot.projects = projectsFromSessions(m.settings, list)
		var entries []searchEntry
		for _, p := range snapshot.panels() {
			if p == panelProjects || p == panelSessions {
				entries = append(entries, snapshot.searchPanelEntries(p)...)
			}
		}
		refs, err := tmux.ListAllPanes(m.runner)
		if err != nil {
			return searchEntriesMsg{id: m.searchID, err: err}
		}
		// Linked windows can appear in multiple sessions; retain each location.
		seen := map[string]bool{}
		for _, ref := range refs {
			id := ref.SessionID + "/" + ref.WindowID
			if !seen[id] {
				seen[id] = true
				entries = append(entries, searchEntry{panel: panelWindows, sessionID: ref.SessionID, windowID: ref.WindowID, label: fmt.Sprintf("Window  %s / %d:%s  %s", ref.SessionName, ref.WindowIndex, ref.WindowName, ref.WindowID)})
			}
			if !m.compact {
				entries = append(entries, searchEntry{panel: panelPanes, sessionID: ref.SessionID, windowID: ref.WindowID, paneID: ref.PaneID, label: fmt.Sprintf("Pane  %s / %d:%s / %d  %s  %s", ref.SessionName, ref.WindowIndex, ref.WindowName, ref.PaneIndex, ref.PaneID, ref.Command)})
			}
		}
		return searchEntriesMsg{id: m.searchID, entries: entries, scope: "All"}
	}
}

func (m Model) handleSearchEntries(msg searchEntriesMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeSearch || msg.id != m.searchID {
		return m, nil
	}
	m.searchLoading = false
	if msg.err != nil {
		m.mode = m.searchReturnMode
		m.err = msg.err
		return m, nil
	}
	m.searchEntries, m.searchScope = msg.entries, msg.scope
	m.searchCur = clamp(m.searchCur, len(m.visibleSearchEntries()))
	return m, nil
}

func (m Model) visibleSearchEntries() []searchEntry {
	if m.searchQuery == "" {
		return m.searchEntries
	}
	type match struct {
		entry searchEntry
		score int
	}
	var matches []match
	for _, entry := range m.searchEntries {
		if score, ok := sessions.FuzzyMatch(m.searchQuery, searchLabel(entry.label)); ok {
			matches = append(matches, match{entry, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	out := make([]searchEntry, len(matches))
	for i, match := range matches {
		out[i] = match.entry
	}
	return out
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = m.searchReturnMode
		return m, nil
	}
	if m.searchResolving {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlF:
		return m.startSearch(false)
	case tea.KeyCtrlZ:
		return m.startSearch(true)
	case tea.KeyEnter:
		list := m.visibleSearchEntries()
		if m.searchLoading || len(list) == 0 {
			return m, nil
		}
		entry := list[clamp(m.searchCur, len(list))]
		return m.handleSearchResult(searchResultMsg{entry: &entry})
	case tea.KeyUp, tea.KeyCtrlP:
		m.searchCur = clamp(m.searchCur-1, len(m.visibleSearchEntries()))
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.searchCur = clamp(m.searchCur+1, len(m.visibleSearchEntries()))
		return m, nil
	case tea.KeyPgUp:
		m.searchCur = clamp(m.searchCur-max(1, m.height-5), len(m.visibleSearchEntries()))
		return m, nil
	case tea.KeyPgDown:
		m.searchCur = clamp(m.searchCur+max(1, m.height-5), len(m.visibleSearchEntries()))
		return m, nil
	case tea.KeyBackspace:
		m.searchQuery = trimLastRune(m.searchQuery)
	case tea.KeyCtrlU:
		m.searchQuery = ""
	case tea.KeyRunes:
		m.searchQuery += string(msg.Runes)
	case tea.KeySpace:
		m.searchQuery += " "
	default:
		return m, nil
	}
	m.searchCur = 0
	return m, nil
}

func (m Model) handleSearchResult(msg searchResultMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeSearch {
		return m, nil
	}
	if msg.err != nil || msg.entry == nil {
		m.mode = m.searchReturnMode
		m.err = msg.err
		return m, nil
	}
	entry := *msg.entry
	if entry.panel == panelProjects || entry.sessionID == "" {
		return m.handleSearchLocation(searchLocationMsg{id: m.searchID, entry: entry})
	}
	m.searchResolving = true
	return m, func() tea.Msg {
		location := searchLocationMsg{id: m.searchID, entry: entry}
		location.sessions, location.err = sessions.List(m.runner)
		if location.err != nil {
			return location
		}
		location.windows, location.err = tmux.ListWindows(m.runner, entry.sessionID)
		if location.err != nil {
			return location
		}
		idx := activeOrClamp(-1, location.windows)
		if entry.windowID != "" {
			idx = indexOfWindow(location.windows, entry.windowID)
		}
		if idx < 0 || idx >= len(location.windows) {
			location.err = errors.New("search result window is no longer available")
			return location
		}
		location.panes, location.err = tmux.ListPanes(m.runner, location.windows[idx].ID)
		return location
	}
}

func (m Model) handleSearchLocation(msg searchLocationMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeSearch || msg.id != m.searchID {
		return m, nil
	}
	if msg.err != nil {
		m.mode = m.searchReturnMode
		m.err = msg.err
		return m, nil
	}
	// Apply the full hierarchy atomically; normal cascade responses may still
	// be in flight from before search opened.
	next := m.rebaseFilteredCursor()
	next.filter, next.filtering, next.mode = "", false, modeNormal
	entry := msg.entry
	next.focus = entry.panel
	if entry.panel == panelProjects || entry.sessionID == "" {
		found := -1
		for i, p := range next.projects {
			if p.Path == entry.project.Path && p.Root == entry.project.Root && p.Name == entry.project.Name {
				found = i
				break
			}
		}
		if found < 0 {
			next.projects = append(next.projects, entry.project)
			found = len(next.projects) - 1
		}
		if entry.panel == panelProjects {
			next.cur[panelProjects] = found
		} else {
			next.allSessions = true
			next.cur[panelSessions] = indexOfSession(next.sessionEntries(), entry.sessionName)
			if next.cur[panelSessions] < 0 {
				m.mode = m.searchReturnMode
				m.info = "search result is no longer available"
				return m, nil
			}
			next.cur[panelWindows], next.cur[panelPanes] = -1, -1
			next.windows, next.panes = nil, nil
		}
	} else {
		// Every running session is visible regardless of allSessions, which
		// only adds stopped projects. Refresh before locating the target ID.
		next.sessions = msg.sessions
		found := -1
		for i, s := range next.sessionEntries() {
			if s.Session.ID == entry.sessionID {
				found = i
				break
			}
		}
		if found < 0 {
			m.mode = m.searchReturnMode
			m.info = "search result session is no longer available"
			return m, nil
		}
		next.cur[panelSessions] = found
		next.windows, next.panes = msg.windows, msg.panes
		next.cur[panelWindows] = activeOrClamp(-1, next.windows)
		if entry.windowID != "" {
			next.cur[panelWindows] = indexOfWindow(next.windows, entry.windowID)
		}
		next.cur[panelPanes] = activePaneOrClamp(-1, next.panes)
		if entry.paneID != "" {
			next.cur[panelPanes] = indexOfPane(next.panes, entry.paneID)
			if next.cur[panelPanes] < 0 {
				m.mode = m.searchReturnMode
				m.info = "search result pane is no longer available"
				return m, nil
			}
		}
	}
	return next, next.updatePreview()
}
