// Package sessions is the data layer over the running tmux world: listing
// sessions, killing one, formatting a row, and the fuzzy matcher every list in
// wyrm filters with.
//
// It exists as its own package because these are the things both user
// interfaces and the plain `wyrm list` need, and they used to live in
// internal/picker — so the TUI imported a UI package to get at a struct
// definition, and a change to either one reached further than it should.
// Nothing here draws anything or touches a terminal.
package sessions

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// Session is a running tmux session.
type Session struct {
	ID       string    `json:"id" toml:"id"`
	Name     string    `json:"name" toml:"name"`
	Windows  int       `json:"windows" toml:"windows"`
	Attached bool      `json:"attached" toml:"attached"`
	Activity time.Time `json:"activity" toml:"activity"`
}

// FindProject matches a live session against discovered project identities.
// Aliases are command lookup names and never identify an existing session.
func FindProject(index config.ProjectIndex, name string) (config.Project, bool) {
	for _, p := range index.Projects() {
		if p.Name == name {
			return p, true
		}
	}
	for _, p := range index.Projects() {
		if tmux.SanitizeName(p.Name) == name {
			return p, true
		}
	}
	return config.Project{}, false
}

// listFormat mirrors the pipe-separated fields parseSession expects. tmux
// rewrites control characters (including tabs) in -F output to "_", so a
// printable delimiter is required. The session name — the only field that may
// contain the delimiter — is emitted last so a fixed-count SplitN keeps it
// whole even when it holds a "|". The session ID (e.g. "$3") never contains
// "|" and is used to target this session unambiguously afterward — see
// tmux.FindSessionID for why the name itself isn't a safe tmux target.
const listFormat = "#{session_id}|#{session_windows}|#{?session_attached,1,0}|#{session_activity}|#{session_name}"

// List returns the running tmux sessions, most-recently-active first.
// When no tmux server is running it returns an empty slice and no error, so
// callers can treat "nothing to show" as an ordinary outcome.
func List(r tmux.Runner) ([]Session, error) {
	out, err := r.Run("list-sessions", "-F", listFormat)
	if err != nil {
		// No server up isn't an error here — it just means nothing is running.
		if tmux.NoServerRunning(err, out) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing sessions: %w", tmux.CmdErr(err, out))
	}
	if tmux.NoServerRunning(nil, out) {
		return nil, nil
	}
	recs, err := tmux.ParseRecords(out, 5, false, "list-sessions")
	if err != nil {
		return nil, err
	}
	list := make([]Session, 0, len(recs))
	for _, fields := range recs {
		s, err := parseSession(fields)
		if err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	sortSessions(list)
	return list, nil
}

func parseSession(f []string) (Session, error) {
	if err := tmux.CheckID(tmux.SessionSigil, "session", f[0]); err != nil {
		return Session{}, err
	}
	windows, err := tmux.AtoiField(f[1], "session window count")
	if err != nil {
		return Session{}, err
	}
	epoch, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("unexpected session activity %q: %w", f[3], err)
	}
	return Session{
		ID:       f[0],
		Name:     f[4],
		Windows:  windows,
		Attached: f[2] == "1",
		Activity: time.Unix(epoch, 0),
	}, nil
}

// sortSessions orders by most recent activity, then name for a stable tie-break.
func sortSessions(s []Session) {
	sort.SliceStable(s, func(i, j int) bool {
		if !s[i].Activity.Equal(s[j].Activity) {
			return s[i].Activity.After(s[j].Activity)
		}
		return s[i].Name < s[j].Name
	})
}

// Kill destroys a session by its tmux session ID (e.g. "$3") — see
// tmux.FindSessionID for why a raw session name isn't used as a tmux target.
// Unlike session.Kill it runs no lifecycle hooks: the interfaces operate on
// arbitrary running sessions whose config we don't have, so this is a plain
// tmux kill.
func Kill(r tmux.Runner, id string) error {
	if out, err := r.Run("kill-session", "-t", id); err != nil {
		return fmt.Errorf("killing session %q: %w", id, tmux.CmdErr(err, out))
	}
	return nil
}

// FuzzyMatch reports whether query is a subsequence of target (case-insensitive)
// along with a score; higher is better. Contiguous runs and matches at a word
// boundary score higher, so "dev" ranks "dev-api" above "d-e-v". An empty query
// matches everything with score 0, preserving the caller's input order.
//
// Every filterable list in wyrm ranks through this one function, so the
// Sessions panel, the Projects panel, and the picker can't disagree about what
// "matches" means.
func FuzzyMatch(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := strings.ToLower(query)
	t := strings.ToLower(target)

	score, streak, qi, prevTi := 0, 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		score++ // base hit
		if ti == prevTi+1 {
			streak++
			score += 3 + streak // consecutive matches dominate
		} else {
			streak = 0
			if ti == 0 || isBoundary(t[ti-1]) {
				score += 2 // start-of-word bonus
			}
		}
		prevTi = ti
		qi++
	}
	if qi != len(q) {
		return 0, false
	}
	return score, true
}

func isBoundary(b byte) bool {
	switch b {
	case '-', '_', ' ', '.', '/', ':', '@':
		return true
	}
	return false
}

// FormatRow renders a session as "name  N window(s)[  (attached)]" — the plain,
// awk-able shape `wyrm list` prints. The interactive views build their own rows
// with styled spans instead, so there is no colored variant here.
func FormatRow(s Session) string {
	unit := "windows"
	if s.Windows == 1 {
		unit = "window"
	}
	att := ""
	if s.Attached {
		att = "  (attached)"
	}
	return padName(s.Name, nameColumn) + " " + fmt.Sprintf("%d %s", s.Windows, unit) + att
}

// nameColumn is the width the session-name column is padded to.
const nameColumn = 24

// padName fits a session name into a fixed display-width column, truncating
// with an ellipsis when it doesn't fit.
//
// fmt's "%-24s" pads by *rune* count, not display width, so a CJK or emoji
// name — twice as wide on screen as it is long in runes — pushed the window
// count out of alignment for every row. It also never truncated, so a long
// name ran over the count and the "(attached)" marker.
func padName(name string, w int) string {
	if width := ansi.StringWidth(name); width <= w {
		return name + strings.Repeat(" ", w-width)
	}
	truncated := ansi.Truncate(name, w, "…")
	if gap := w - ansi.StringWidth(truncated); gap > 0 {
		truncated += strings.Repeat(" ", gap)
	}
	return truncated
}
