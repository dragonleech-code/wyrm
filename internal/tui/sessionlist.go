package tui

import (
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// This file describes what the Sessions panel lists.
//
// By default it lists what tmux is running, and that is the whole answer. With
// "all" toggled on ("a") it also lists every discoverable project that is
// *not* running, so "reach the session I already have" and "start the one I
// don't" are one list instead of two panels — which is the only way `wyrm
// pick`, whose compact form has no Projects panel, can start anything at all.
//
// The two kinds of row are one type rather than two lists because every
// consumer — the cursor, the filter, the renderer, the mouse hit test — indexes
// rows positionally and must not care which kind it landed on.

// sessionEntry is one row of the Sessions panel.
type sessionEntry struct {
	// Name is what the row is filtered and rendered by: the tmux session name
	// for a running row, the session name the config would produce for a
	// stopped one.
	Name string

	// Session is the live tmux session. Only meaningful when Running — a
	// stopped row has no session ID, which is precisely why every
	// session-targeting action guards on currentSession's ok.
	Session sessions.Session
	Running bool

	// Project is the config this row can be started from, and HasProject
	// whether there is one. A stopped row always has one (nothing else could
	// have put it in the list); a running row has one only when a discovered
	// project shares its name, which is what lets a session started outside
	// wyrm still appear here with nothing to start.
	Project    Project
	HasProject bool
}

// sessionEntries builds the panel's rows: the running sessions in tmux's own
// most-recently-active order, then — with allSessions on — the discovered
// projects that aren't among them, in discovery order.
//
// Stopped rows come last rather than being interleaved by name: the running
// ones are the ones with live state behind them (windows, panes, agent
// markers), and sorting a stopped row between them would move the row a user
// was about to press Enter on every time a session's activity changed.
func (m Model) sessionEntries() []sessionEntry {
	entries := make([]sessionEntry, 0, len(m.sessions))
	if !m.allSessions {
		for _, s := range m.sessions {
			entries = append(entries, sessionEntry{Name: s.Name, Session: s, Running: true})
		}
		return entries
	}

	// First project wins a name collision, matching listProjects' own
	// precedence (a real config over a zoxide directory of the same name).
	byName := make(map[string]Project, len(m.projects))
	for _, p := range m.projects {
		if _, seen := byName[p.Name]; !seen {
			byName[p.Name] = p
		}
	}

	running := make(map[string]string, len(m.sessions))
	for _, s := range m.sessions {
		running[s.Name] = s.ID
		e := sessionEntry{Name: s.Name, Session: s, Running: true}
		if p, ok := byName[s.Name]; ok {
			e.Project, e.HasProject = p, true
		} else {
			for _, p := range m.projects {
				if tmux.SanitizeName(p.Name) == s.Name {
					e.Project, e.HasProject = p, true
					break
				}
			}
		}
		entries = append(entries, e)
	}

	// Running is decided against m.sessions rather than Project.Running:
	// the two are separate snapshots (loadProjects and loadSessions land as
	// separate messages), and trusting the annotation could list a project
	// as stopped in the same frame its session is listed as running.
	seen := make(map[string]bool, len(m.projects))
	for _, p := range m.projects {
		if runningProjectID(running, p.Name) != "" || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		entries = append(entries, sessionEntry{Name: p.Name, Project: p, HasProject: true})
	}
	return entries
}
