package tmux

import (
	"fmt"
	"strconv"
	"strings"
)

// WindowInfo is one window of a running session, as reported by tmux.
type WindowInfo struct {
	Index  int
	ID     string
	Active bool
	Layout string // #{window_layout}: the split tree, see internal/freeze
	Name   string
}

// PaneInfo is one pane of a running window, as reported by tmux.
type PaneInfo struct {
	ID      string
	Index   int
	Active  bool
	Command string // #{pane_current_command}: the pane's foreground process
	Path    string // #{pane_current_path}: the pane's current working directory
}

// SessionPath returns a session's working directory (#{session_path}) — the
// directory tmux opens new windows in, and the closest thing tmux records to
// "where this project lives".
func SessionPath(r Runner, sessionID string) (string, error) {
	out, err := r.Run("display-message", "-p", "-t", sessionID, "-F", "#{session_path}")
	if err != nil {
		return "", fmt.Errorf("reading session path: %w", CmdErr(err, out))
	}
	return strings.TrimSpace(out), nil
}

// records splits tmux "-F" output into one field slice per line, each with
// exactly n pipe-separated fields.
//
// The last field is the one allowed to contain the delimiter — a window or
// session name may hold a "|" — which is why SplitN's cap matters and why every
// format string here puts the free-form field last. Blank lines are skipped and
// a trailing "\r" trimmed, both of which some tmux builds emit.
//
// A line with the wrong field count is an error rather than a skipped row: it
// means the format string and this parser disagree, which is a wyrm bug and not
// something to paper over.
func records(out string, n int, what string) ([][]string, error) {
	return ParseRecords(out, n, false, what)
}

// ParseRecords validates pipe-delimited tmux output. optionalLast accepts
// older pane records with no current-path field.
func ParseRecords(out string, n int, optionalLast bool, what string) ([][]string, error) {
	var recs [][]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "|", n)
		if optionalLast && len(fields) == n-1 {
			fields = append(fields, "")
		}
		if len(fields) != n {
			return nil, fmt.Errorf("unexpected %s output %q", what, line)
		}
		recs = append(recs, fields)
	}
	return recs, nil
}

// atoiField converts a numeric field, naming what it was.
func atoiField(field, what string) (int, error) {
	return AtoiField(field, what)
}

// AtoiField parses a tmux numeric field with a useful diagnostic.
func AtoiField(field, what string) (int, error) {
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0, fmt.Errorf("unexpected %s %q", what, field)
	}
	return n, nil
}

const windowListFormat = "#{window_index}|#{window_id}|#{?window_active,1,0}|#{window_layout}|#{window_name}"

// ListWindows returns the windows of sessionID (a tmux session ID such as
// "$3"), in tmux's own window-index order.
func ListWindows(r Runner, sessionID string) ([]WindowInfo, error) {
	out, err := r.Run("list-windows", "-t", sessionID, "-F", windowListFormat)
	if err != nil {
		return nil, fmt.Errorf("listing windows: %w", CmdErr(err, out))
	}
	recs, err := records(out, 5, "list-windows")
	if err != nil {
		return nil, err
	}
	windows := make([]WindowInfo, 0, len(recs))
	for _, f := range recs {
		index, err := atoiField(f[0], "window index")
		if err != nil {
			return nil, err
		}
		if err := CheckID(WindowSigil, "window", f[1]); err != nil {
			return nil, fmt.Errorf("listing windows: %w", err)
		}
		windows = append(windows, WindowInfo{
			Index:  index,
			ID:     f[1],
			Active: f[2] == "1",
			Layout: f[3],
			Name:   f[4],
		})
	}
	return windows, nil
}

// PaneRef locates one pane within the whole tmux server, alongside the command
// it's running and enough naming context (session/window name and index) to
// show it meaningfully in a flat, server-wide list. It's what a server-wide
// `list-panes -a` yields: enough to decide which panes are worth inspecting,
// which window and session to attribute the result to, and how to label a row
// that's lost the visual hierarchy a per-session or per-window list has.
type PaneRef struct {
	SessionID   string
	SessionName string
	WindowID    string
	WindowName  string
	WindowIndex int
	PaneID      string
	PaneIndex   int
	Command     string
}

// allPanesSep separates allPanesFormat's fields. Unlike every other format
// string in this file, this one has *three* free-form, user-controlled
// fields (session name, window name, and pane_current_command) rather than
// one — records' "only the last field may contain the delimiter" contract
// only protects one of them, so a session or window named with a literal
// "|" would misparse (at best erroring the whole list, at worst — if the
// field count still happened to come out right — silently misattributing
// fields). A control character essentially never present in a typed name
// sidesteps the problem instead of just moving it, which is why this
// function parses its own output rather than going through records.
const allPanesSep = "\x01"

const allPanesFormat = "#{session_id}" + allPanesSep + "#{session_name}" + allPanesSep +
	"#{window_id}" + allPanesSep + "#{window_index}" + allPanesSep + "#{window_name}" + allPanesSep +
	"#{pane_id}" + allPanesSep + "#{pane_index}" + allPanesSep + "#{pane_current_command}"

// ListAllPanes returns every pane on the tmux server. The TUI uses it to find
// agent panes across all sessions in one round trip — the alternative, walking
// list-windows and list-panes per session, costs a tmux call per window and is
// run on a timer — and to back the flat, whole-server pane search ("f" in
// `wyrm tui`).
//
// Like sessions.List, no server running is not an error: it just means
// there are no panes.
func ListAllPanes(r Runner) ([]PaneRef, error) {
	out, err := r.Run("list-panes", "-a", "-F", allPanesFormat)
	if err != nil {
		if NoServerRunning(err, out) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing panes: %w", CmdErr(err, out))
	}
	const nFields = 8
	var recs [][]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, allPanesSep)
		if len(fields) != nFields {
			return nil, fmt.Errorf("unexpected list-panes output %q", line)
		}
		recs = append(recs, fields)
	}
	refs := make([]PaneRef, 0, len(recs))
	for _, f := range recs {
		// These IDs are used directly as capture-pane targets, so they get the
		// same validation every other parsed ID gets — see CheckID. This parser
		// was the one that skipped it.
		if err := CheckIDs(f[0], f[2], f[5]); err != nil {
			return nil, fmt.Errorf("listing panes: %w", err)
		}
		windowIndex, err := atoiField(f[3], "window index")
		if err != nil {
			return nil, err
		}
		paneIndex, err := atoiField(f[6], "pane index")
		if err != nil {
			return nil, err
		}
		refs = append(refs, PaneRef{
			SessionID:   f[0],
			SessionName: f[1],
			WindowID:    f[2],
			WindowIndex: windowIndex,
			WindowName:  f[4],
			PaneID:      f[5],
			PaneIndex:   paneIndex,
			Command:     f[7],
		})
	}
	return refs, nil
}

// CheckIDs validates a session/window/pane ID triple parsed out of one tmux
// response. An empty argument is skipped, so callers can check only the ones
// they actually parsed. See CheckID for why a malformed ID has to be caught at
// the parse site rather than left to misdirect a later command.
func CheckIDs(sessionID, windowID, paneID string) error {
	for _, c := range []struct {
		sigil byte
		kind  string
		id    string
	}{
		{SessionSigil, "session", sessionID},
		{WindowSigil, "window", windowID},
		{PaneSigil, "pane", paneID},
	} {
		if c.id == "" {
			continue
		}
		if err := CheckID(c.sigil, c.kind, c.id); err != nil {
			return err
		}
	}
	return nil
}

const paneListFormat = "#{pane_id}|#{pane_index}|#{?pane_active,1,0}|#{pane_current_command}|#{pane_current_path}"

// ListPanes returns the panes of target (a window ID such as "@2", or a
// session ID to list every pane in the session).
func ListPanes(r Runner, target string) ([]PaneInfo, error) {
	out, err := r.Run("list-panes", "-t", target, "-F", paneListFormat)
	if err != nil {
		return nil, fmt.Errorf("listing panes: %w", CmdErr(err, out))
	}
	recs, err := ParseRecords(out, 5, true, "list-panes")
	if err != nil {
		return nil, err
	}
	panes := make([]PaneInfo, 0, len(recs))
	for _, f := range recs {
		index, err := atoiField(f[1], "pane index")
		if err != nil {
			return nil, err
		}
		if err := CheckID(PaneSigil, "pane", f[0]); err != nil {
			return nil, fmt.Errorf("listing panes: %w", err)
		}
		panes = append(panes, PaneInfo{
			ID:      f[0],
			Index:   index,
			Active:  f[2] == "1",
			Command: f[3],
			Path:    f[4],
		})
	}
	return panes, nil
}
