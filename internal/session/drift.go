package session

import (
	"fmt"
	"io"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// Drift kinds: how a running session differs from its config.
const (
	// DriftMissing is a window the config defines that isn't running.
	DriftMissing = "missing"
	// DriftExtra is a running window the config doesn't define.
	DriftExtra = "extra"
	// DriftPanes is a window present in both, with a different pane count.
	DriftPanes = "panes"
)

// WindowDrift is one difference between a config and its running session.
// Windows are matched by name, the only identity a config gives them.
type WindowDrift struct {
	Window      string `json:"window"`
	Kind        string `json:"kind"`
	ConfigPanes int    `json:"config_panes,omitempty"`
	LivePanes   int    `json:"live_panes,omitempty"`
}

// Diff compares cfg with the session it builds, which must be running. Pane
// commands are not compared: tmux only reports a pane's current foreground
// program, which rarely matches the command line that started it.
func Diff(r tmux.Runner, cfg *config.Config) (name, sessionID string, drift []WindowDrift, err error) {
	name, _, err = cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", "", nil, err
	}
	id, ok, err := tmux.FindSessionID(r, name)
	if err != nil {
		return "", "", nil, err
	}
	if !ok {
		return name, "", nil, fmt.Errorf("session %q is not running: %w", name, ErrSessionNotRunning)
	}
	live, err := tmux.ListWindows(r, id)
	if err != nil {
		return "", "", nil, err
	}
	liveByName := make(map[string]tmux.WindowInfo, len(live))
	for _, w := range live {
		if _, dup := liveByName[w.Name]; !dup {
			liveByName[w.Name] = w
		}
	}
	configured := make(map[string]bool, len(cfg.Windows))
	for _, w := range cfg.Windows {
		configured[w.Name] = true
		lw, ok := liveByName[w.Name]
		if !ok {
			drift = append(drift, WindowDrift{Window: w.Name, Kind: DriftMissing, ConfigPanes: PaneCount(w)})
			continue
		}
		panes, err := tmux.ListPanes(r, lw.ID)
		if err != nil {
			return "", "", nil, err
		}
		if want := PaneCount(w); want != len(panes) {
			drift = append(drift, WindowDrift{Window: w.Name, Kind: DriftPanes, ConfigPanes: want, LivePanes: len(panes)})
		}
	}
	for _, w := range live {
		if !configured[w.Name] {
			drift = append(drift, WindowDrift{Window: w.Name, Kind: DriftExtra})
		}
	}
	return name, id, drift, nil
}

// PaneCount is how many panes Create builds for w: the initial pane, plus one
// for every split entry with a type, at any depth. A typeless entry reuses the
// pane before it.
func PaneCount(w config.Window) int {
	if len(w.Splits) == 0 {
		if len(w.Panes) > 1 {
			return len(w.Panes)
		}
		return 1
	}
	var count func([]config.Split) int
	count = func(splits []config.Split) int {
		n := 0
		for _, s := range splits {
			if s.Type != "" {
				n++
			}
			n += count(s.Children)
		}
		return n
	}
	return 1 + count(w.Splits)
}

// Reconcile adds to a running session every window its config defines that
// the session lacks, matched by name, and leaves every existing window — and
// whatever is running in it — alone. Lifecycle hooks other than the new
// windows' post_window do not run: the project has already started.
//
// It returns the names of the windows it added. A config window with no name
// can't be matched to a running one, so it is skipped with a warning.
func Reconcile(r tmux.Runner, cfg *config.Config, stdout, stderr io.Writer, opts ...Option) (name, sessionID string, added []string, err error) {
	o := newOptions(opts)
	o.hookTimeout, _ = cfg.Session.HookTimeoutDuration()

	name, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", "", nil, err
	}
	id, ok, err := tmux.FindSessionID(r, name)
	if err != nil {
		return "", "", nil, err
	}
	if !ok {
		return name, "", nil, fmt.Errorf("session %q is not running: %w", name, ErrSessionNotRunning)
	}
	if err := ValidateRoots(cfg); err != nil {
		return "", "", nil, err
	}
	live, err := tmux.ListWindows(r, id)
	if err != nil {
		return "", "", nil, err
	}
	running := make(map[string]bool, len(live))
	for _, w := range live {
		running[w.Name] = true
	}
	plan, err := prepareWindows(cfg, root)
	if err != nil {
		return "", "", nil, err
	}

	keys := &keyBatch{}
	waitCommand := o.resolveWaitCommand(cfg)
	type built struct {
		index               int
		windowID, syncState string
	}
	var made []built
	for i, w := range cfg.Windows {
		if w.Name == "" {
			warnf(stderr, "window %d has no name, so it can't be matched to a running window; skipped", i)
			continue
		}
		if running[w.Name] {
			continue
		}
		windowID, paneID, werr := newWindow(r, id, w, plan.initRoots[i], envArgs(plan.initEnvs[i]))
		if werr != nil {
			return name, id, added, werr
		}
		syncState, serr := populateWindow(r, cfg, plan, i, windowID, paneID, keys, waitCommand, stdout, stderr)
		if serr != nil {
			warnf(stderr, "window %q: %v", w.Name, serr)
		}
		made = append(made, built{i, windowID, syncState})
		added = append(added, w.Name)
	}
	keys.flush(r, stderr)
	for _, b := range made {
		w := cfg.Windows[b.index]
		restoreSync(r, w, b.windowID, b.syncState, stderr)
		if err := runHook(o, w.PostWindow, plan.roots[b.index], "post_window", plan.envs[b.index], stderr); err != nil {
			warnf(stderr, "post_window failed for window %q: %v", w.Name, err)
		}
	}
	return name, id, added, nil
}
