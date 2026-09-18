// Package session builds and destroys tmux sessions from a config.
//
// Error policy: structural failures (creating the session or a window,
// killing a session) return errors; per-pane failures (splits, commands,
// hooks, layout) print a warning to stderr and continue, so one broken
// command doesn't abort the rest of the layout.
package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// ErrSessionNotRunning is Kill's error when the config's session isn't
// currently running. It is the one Kill failure a caller can safely treat as
// "nothing to stop" — every other failure (a bad session.root, a tmux
// server/communication problem, kill-session itself failing) is a real
// problem that must not be swallowed the same way. Use errors.Is to check
// for it, since the returned error also names the session.
var ErrSessionNotRunning = errors.New("session is not running")

// Option configures Create and Kill.
type Option func(*options)

type options struct {
	dryRun     bool
	transcript io.Writer
	history    HookHistory
}

// HookHistory tells Create whether a project has started a session before,
// so it can choose between on_project_first_start and on_project_restart —
// a distinction Create has no way to make on its own, since FindSessionID
// only answers "is one running now", not "has one ever run". Implemented by
// *internal/state.Store; session takes the interface rather than importing
// that package directly, so the two stay decoupled.
type HookHistory interface {
	// Started reports whether dir has started a session before.
	Started(dir string) bool
	// MarkStarted records dir as started.
	MarkStarted(dir string) error
}

// WithHistory supplies the record Create consults to fire
// on_project_first_start or on_project_restart. Without it, neither hook
// ever fires — there is no sensible default for "has this ever started".
func WithHistory(h HookHistory) Option {
	return func(o *options) { o.history = h }
}

// DryRun makes Create and Kill describe the lifecycle hooks they would run —
// writing "# would run <hook>" to w — instead of executing them.
//
// It exists because a dry run's whole purpose is to let someone read a config's
// shell before it runs. Passing a recording tmux.Runner covers the tmux side,
// but hooks never go through the Runner: they are handed straight to $SHELL by
// runHook. `wyrm up -n` on an unread config therefore executed its
// on_project_start for real, which is precisely the thing -n is for avoiding.
//
// w is the transcript stream (the same writer the recording Runner prints to),
// not stderr, so the hook lands in reading order among the tmux commands it
// sits between.
func DryRun(w io.Writer) Option {
	return func(o *options) { o.dryRun, o.transcript = true, w }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.transcript == nil {
		o.transcript = io.Discard
	}
	return o
}

// Create builds the session described by cfg and returns its name and tmux
// session ID (e.g. "$3"). If a session with that name is already running it
// is left untouched — running panes keep running — and created is false so
// the caller can attach to it.
//
// Every tmux command below targets the session by ID once one is known
// (from FindSessionID, or captured off the initial new-session call), never
// by the raw config-derived name: tmux's -t target syntax treats "." as the
// window.pane separator, so a name containing "." (e.g. "wyrm.vim") would be
// misparsed by has-session, new-window, and friends. See tmux.FindSessionID.
//
// Per-window creation progress goes to stdout and per-pane warnings (see the
// package doc's error policy) go to stderr — passed in rather than hardcoded
// so callers can capture or redirect them, the same way main.run threads
// stdout/stderr throughout the CLI.
func Create(r tmux.Runner, cfg *config.Config, stdout, stderr io.Writer, opts ...Option) (name, sessionID string, created bool, err error) {
	o := newOptions(opts)

	name, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", "", false, err
	}
	if len(cfg.Windows) == 0 {
		return "", "", false, fmt.Errorf("no windows defined in config")
	}

	if id, ok, ferr := tmux.FindSessionID(r, name); ferr != nil {
		return "", "", false, ferr
	} else if ok {
		return name, id, false, nil
	}

	// Every root the build will need is resolved and validated before any
	// hook runs or lifecycle state is recorded. A malformed window or split
	// root — an unset $VAR, say — used to be discovered only after
	// on_project_start and on_project_first_start had already run (the
	// latter doing real, possibly expensive setup work) and first-start had
	// already been marked in history: fixing the config and retrying then
	// fired on_project_restart instead, even though nothing had actually
	// been built the first time.
	if err := ValidateRoots(cfg); err != nil {
		return "", "", false, err
	}

	if err := runHook(o, cfg.Session.OnProjectStart, root, "on_project_start", cfg.Session.Env, stderr); err != nil {
		warnf(stderr, "on_project_start failed: %v", err)
	}
	runFirstStartOrRestartHook(o, cfg, root, stderr)

	// Every window's root is resolved up front so a bad one fails before any
	// tmux state exists, rather than half way through a build.
	roots := make([]string, len(cfg.Windows))
	winEnvs := make([]map[string]string, len(cfg.Windows))
	initEnvs := make([]map[string]string, len(cfg.Windows))
	for i, w := range cfg.Windows {
		wr, rerr := config.ResolveRoot(root, w.Root)
		if rerr != nil {
			return "", "", false, fmt.Errorf("window %q: %w", w.Name, rerr)
		}
		roots[i] = wr

		we := mergeEnv(cfg.Session.Env, w.Env)
		winEnvs[i] = we
		ie := we
		if len(w.Splits) > 0 && w.Splits[0].Type == "" {
			_, ie, _ = paneAttributes(w.Splits[0], wr, we)
		}
		initEnvs[i] = ie
	}

	// Commands are collected while the layout is built and typed in one tmux
	// process afterwards — see keyBatch. In a three-window, six-pane build that
	// is twelve send-keys calls collapsed into one.
	keys := &keyBatch{}

	initRoots := make([]string, len(cfg.Windows))
	for i, w := range cfg.Windows {
		initRoot := roots[i]
		// A first split's own root always overrides the window's — Split.Root
		// is documented to override the window directory for its pane
		// regardless of whether the window set one itself. Gating this on
		// w.Root being empty silently ignored splits[0].root whenever the
		// window also had a root, and started the initial pane in the wrong
		// directory.
		if len(w.Splits) > 0 && w.Splits[0].Type == "" {
			initRoot, _, _ = paneAttributes(w.Splits[0], roots[i], winEnvs[i])
		}
		initRoots[i] = initRoot
	}

	first, err := newSession(r, name, cfg.Windows[0], initRoots[0], envArgs(initEnvs[0]), stderr)
	if err != nil {
		return "", "", false, err
	}
	if first.name != name {
		// tmux does not always name the session what we asked for: some builds
		// replace "." and ":" with "_", and a config in a directory called
		// "example.com" hits that. Reporting the config's name instead of the
		// real one makes the *next* run fail to find the session and try to
		// create a duplicate.
		warnf(stderr, "tmux named the session %q, not %q", first.name, name)
		name = first.name
	}
	id := first.sessionID
	windowIDs := make([]string, len(cfg.Windows))
	syncStates := make([]string, len(cfg.Windows))

	for i, w := range cfg.Windows {
		windowID, paneID := first.windowID, first.paneID
		if i > 0 {
			var werr error
			windowID, paneID, werr = newWindow(r, id, w, initRoots[i], envArgs(initEnvs[i]))
			if werr != nil {
				return "", "", false, rollback(r, id, stderr, werr)
			}
		}
		windowIDs[i] = windowID
		// send-keys obeys synchronize-panes, including an inherited global
		// setting. Temporarily disable it throughout initialization. Only the
		// window's own value is recorded (no -A): an inherited value must be
		// restored by unsetting, not by copying it onto the window, or a later
		// change to the global setting would stop reaching this window.
		out, serr := r.Run("show-options", "-w", "-v", "-t", windowID, "synchronize-panes")
		if serr != nil {
			return "", "", false, rollback(r, id, stderr, tmux.CmdErr(serr, out))
		}
		syncStates[i] = out
		if out, serr := r.Run("set-window-option", "-t", windowID, "synchronize-panes", "off"); serr != nil {
			return "", "", false, rollback(r, id, stderr, tmux.CmdErr(serr, out))
		}
		_, _ = fmt.Fprintf(stdout, "window %s: %s\n", windowID, w.Name)
		buildWindow(r, windowID, paneID, w, splitCtx{
			root: roots[i], envMap: winEnvs[i], preWindow: w.PreWindow, keys: keys,
		}, stderr)
		enablePaneTitles(r, windowID, cfg.Session, stderr)
	}

	// Every pane now exists, so every target is known: type the lot.
	keys.flush(r, stderr)
	for i, w := range cfg.Windows {
		syncState := syncStates[i]
		setting := w.Synchronize
		if setting == nil {
			setting = w.SynchronizePanes
		}
		if setting != nil {
			syncState = "off"
			if *setting {
				syncState = "on"
			}
		}
		args := []string{"set-window-option", "-t", windowIDs[i], "synchronize-panes", syncState}
		if syncState == "" {
			args = []string{"set-window-option", "-u", "-t", windowIDs[i], "synchronize-panes"}
		}
		if out, err := r.Run(args...); err != nil {
			warnf(stderr, "restoring synchronize-panes: %v", tmux.CmdErr(err, out))
		}
	}

	// post_window runs only now — a real subprocess, unlike pre_window's
	// typed command, so it needs every pane's own command to have actually
	// been sent first, not just the pane to exist. Sequential and in window
	// order, matching the order windows were built in.
	for i, w := range cfg.Windows {
		if err := runHook(o, w.PostWindow, roots[i], "post_window", winEnvs[i], stderr); err != nil {
			warnf(stderr, "post_window failed for window %q: %v", w.Name, err)
		}
	}

	if cfg.Session.StartupWindow != "" {
		selectStartup(r, id, cfg.Session.StartupWindow, cfg.Session.StartupPane, stderr)
	} else if first.windowID != "" {
		// Every window was created with -d, so window 0 is still current — but
		// say so explicitly rather than relying on that, and land on its first
		// pane too (splits are also created with -d).
		if _, err := r.Run("select-window", "-t", first.windowID); err != nil {
			warnf(stderr, "failed to select the first window: %v", err)
		}
	}
	if cfg.Session.OnProjectDetach != "" {
		if _, err := r.Run("set-hook", "-t", id, "client-detached", detachHookCommand(cfg.Session.OnProjectDetach)); err != nil {
			warnf(stderr, "failed to configure on_project_detach hook: %v", err)
		}
	}
	return name, id, true, nil
}

// ValidateRoots resolves every root a Create build would need — the
// session's own name and root, plus every window and (nested) split root —
// without touching tmux. `wyrm validate` uses it to catch a failure Create
// would otherwise be the first to hit, such as session.root or a window's
// root referencing an environment variable that isn't set: Config.validate
// only checks the decoded structure, not what these paths actually resolve
// to once interpolation has run.
func ValidateRoots(cfg *config.Config) error {
	_, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return err
	}
	for _, w := range cfg.Windows {
		wr, err := config.ResolveRoot(root, w.Root)
		if err != nil {
			return fmt.Errorf("window %q: %w", w.Name, err)
		}
		if err := validateSplitRoots(wr, w.Splits); err != nil {
			return fmt.Errorf("window %q: %w", w.Name, err)
		}
	}
	return nil
}

// validateSplitRoots mirrors applySplits' own root resolution: each split's
// root resolves against its parent's (the window's, for a top-level split),
// and children inherit their own parent split's resolved root in turn.
func validateSplitRoots(base string, splits []config.Split) error {
	for i, s := range splits {
		root, err := config.ResolveRoot(base, s.Root)
		if err != nil {
			return fmt.Errorf("split %d: %w", i, err)
		}
		if err := validateSplitRoots(root, s.Children); err != nil {
			return err
		}
	}
	return nil
}

// detachHookCommand renders on_project_detach as the tmux command stored
// against the session's client-detached hook.
//
// Two layers of tmux syntax sit between the config and the shell, and Go's %q
// satisfies neither:
//
//   - tmux's command lexer re-parses the stored string when the hook fires, so
//     the body is wrapped in single quotes, which tmux takes literally.
//   - run-shell then expands its argument as a FORMAT. That happens whatever
//     the quoting, so a hook merely *mentioning* "#{session_name}" was rewritten
//     before the shell saw it, and "#(...)" was executed by tmux at expansion
//     time. "##" is the documented way to write a literal "#" in a format, so
//     every "#" is doubled first.
//
// Verified against tmux 3.7: without the doubling, `#{session_name}` reaches
// the shell as the session's name and `#(id -un)` runs as a tmux job.
func detachHookCommand(hook string) string {
	return "run-shell " + tmuxQuote(strings.ReplaceAll(hook, "#", "##"))
}

// tmuxQuote wraps s so tmux's command lexer reads it back verbatim. tmux
// single quotes are literal with no escape mechanism, exactly like sh, so an
// embedded quote has to leave and re-enter the quoted run.
func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RunAttachHook executes cfg.Session.OnProjectAttach if non-empty.
//
// Create deliberately does not call this. The hook is documented as running
// when you attach, and Create has no idea whether its caller is about to:
// `wyrm up -d`, `wyrm restart -d` and `wyrm restart -all` all build a session
// and hand it to nobody. Firing it from Create meant on_project_attach ran for
// every one of them. Callers run it immediately before handing the terminal
// over — see app.attachSession.
func RunAttachHook(cfg *config.Config, stderr io.Writer, opts ...Option) error {
	if cfg == nil || cfg.Session.OnProjectAttach == "" {
		return nil
	}
	o := newOptions(opts)
	_, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		root = cfg.Dir()
	}
	if err := runHook(o, cfg.Session.OnProjectAttach, root, "on_project_attach", cfg.Session.Env, stderr); err != nil {
		warnf(stderr, "on_project_attach failed: %v", err)
	}
	return nil
}

// newIDs is what a session- or window-creating tmux command reports back.
type newIDs struct {
	sessionID string
	// name is what tmux actually called the session, which is not always what
	// was asked for — see Create.
	name     string
	windowID string
	paneID   string
}

// newSession creates the session together with its first window.
//
// -c is that *first window's* root, not necessarily the session's: new-session
// sets both at once, and the pane's directory is the one a user can see. They
// differ only when window 0 sets its own root.
func newSession(r tmux.Runner, name string, w config.Window, root string, env []string, stderr io.Writer) (newIDs, error) {
	args := []string{"new-session", "-d", "-P", "-F",
		"#{session_id}|#{window_id}|#{pane_id}",
		"-s", name, "-n", w.Name, "-c", root}
	args = append(args, env...)
	args = append(args, paneProcess(w)...)

	out, err := r.Run(args...)
	if err != nil {
		return newIDs{}, fmt.Errorf("creating session: %w", tmux.CmdErr(err, out))
	}
	// Only IDs share this delimited response — never the session name: tmux
	// accepts "|" in a session name, and a name field sitting between two more
	// fields could not be told apart from them. See tmux.SessionName.
	parts := strings.Split(out, "|")
	if len(parts) != 3 {
		return newIDs{}, fmt.Errorf("unexpected tmux output %q", out)
	}
	ids := newIDs{sessionID: parts[0], windowID: parts[1], paneID: parts[2]}
	if err := tmux.CheckIDs(ids.sessionID, ids.windowID, ids.paneID); err != nil {
		return newIDs{}, fmt.Errorf("creating session: %w", err)
	}
	// tmux does not always name a session what was asked for (see Create's
	// rename warning), so the real name has to be queried separately. A
	// session now genuinely exists, so a failure here is rolled back like any
	// later build failure rather than left running with no ID ever reported
	// to the caller.
	actualName, nerr := tmux.SessionName(r, ids.sessionID)
	if nerr != nil {
		return newIDs{}, rollback(r, ids.sessionID, stderr, fmt.Errorf("creating session: %w", nerr))
	}
	ids.name = actualName
	return ids, nil
}

// newWindow adds a window to an existing session.
//
// -d keeps the session's active window where it started. Without it tmux makes
// each new window current, so a freshly built session opens on the *last*
// window in the config rather than the first — which is what startup_window's
// default documents.
func newWindow(r tmux.Runner, sessionID string, w config.Window, root string, env []string) (windowID, paneID string, err error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}|#{pane_id}",
		"-t", sessionID, "-n", w.Name, "-c", root}
	args = append(args, env...)
	args = append(args, paneProcess(w)...)

	out, err := r.Run(args...)
	if err != nil {
		return "", "", fmt.Errorf("creating window %q: %w", w.Name, tmux.CmdErr(err, out))
	}
	windowID, paneID, ok := strings.Cut(out, "|")
	if !ok {
		return "", "", fmt.Errorf("unexpected tmux output %q", out)
	}
	if err := tmux.CheckIDs("", windowID, paneID); err != nil {
		return "", "", fmt.Errorf("creating window %q: %w", w.Name, err)
	}
	return windowID, paneID, nil
}

// rollback destroys a session whose build failed partway through and returns
// cause unchanged. Without it the half-built session stays running, and the
// next `wyrm` finds it, reports "already running, attaching", and hands over a
// session missing most of its windows with no sign anything went wrong.
func rollback(r tmux.Runner, sessionID string, stderr io.Writer, cause error) error {
	if out, err := r.Run("kill-session", "-t", sessionID); err != nil {
		warnf(stderr, "could not clean up the partially built session: %v (%s)", err, out)
	}
	return cause
}

// Kill runs the on_project_exit hook and destroys the session. The hook is
// skipped when the session isn't running. Hook-failure warnings go to
// stderr, passed in rather than hardcoded — see Create.
//
// Under DryRun the session lookup still happens for real — it is what names the
// session being described — but the hook and the kill-session are printed
// rather than performed.
func Kill(r tmux.Runner, cfg *config.Config, stderr io.Writer, opts ...Option) (string, error) {
	o := newOptions(opts)

	name, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", err
	}
	id, ok, err := tmux.FindSessionID(r, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q is not running: %w", name, ErrSessionNotRunning)
	}
	if err := runHook(o, cfg.Session.OnProjectExit, root, "on_project_exit", cfg.Session.Env, stderr); err != nil {
		warnf(stderr, "on_project_exit failed: %v", err)
	}
	if o.dryRun {
		// The lookup above ran against the real server on purpose — "which
		// session would this kill" has no answer otherwise — so only the
		// destructive command is withheld.
		_, _ = fmt.Fprintf(o.transcript, "tmux kill-session -t %s\n", id)
		return name, nil
	}
	if out, err := r.Run("kill-session", "-t", id); err != nil {
		return "", fmt.Errorf("killing session %q: %w", name, tmux.CmdErr(err, out))
	}
	return name, nil
}

// envArgs renders a session's env map as repeated "-e KEY=VALUE" arguments, in
// sorted order so a build is reproducible and a dry-run transcript is stable.
// tmux takes -e on new-session, new-window, and split-window; setting it per
// command rather than once on the session is what makes it reach the panes,
// since set-environment only affects processes started afterward.
func envArgs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	return args
}

// paneProcess returns the trailing command arguments for a window whose first
// split sets `run`, so tmux starts that process directly instead of a shell.
// Only a leading entry with no type owns the window's initial pane; anything
// else creates its own pane and is handled in applySplits.
func paneProcess(w config.Window) []string {
	if len(w.Splits) == 0 {
		return nil
	}
	first := w.Splits[0]
	_, _, command := paneAttributes(first, "", nil)
	if first.Type != "" || command == "" {
		return nil
	}
	// "--" so a command starting with "-" isn't read as more tmux flags.
	return []string{"--", startupWaitCommand}
}

// Keep direct-process panes alive until options and the complete layout exist.
// respawn-pane replaces this process after remain-on-exit is installed.
const startupWaitCommand = "exec sleep 2147483647"

// Leading children reuse their parent's physical pane. Resolve their creation
// attributes before creating that pane, without changing sibling inheritance.
func paneAttributes(s config.Split, base string, env map[string]string) (string, map[string]string, string) {
	root, _ := config.ResolveRoot(base, s.Root) // ValidateRoots already checked it.
	env = mergeEnv(env, s.Env)
	command := s.Run
	for len(s.Children) > 0 && s.Children[0].Type == "" {
		s = s.Children[0]
		root, _ = config.ResolveRoot(root, s.Root)
		env = mergeEnv(env, s.Env)
		if s.Run != "" {
			command = s.Run
		}
	}
	return root, env, command
}

func buildWindow(r tmux.Runner, windowID, initialPane string, w config.Window, ctx splitCtx, stderr io.Writer) {
	// done tracks the panes pre_window has already been typed into, so it runs
	// exactly once per pane across the whole window — see sendPreWindow. The
	// caller supplies everything else; this is the one thing scoped to a single
	// window, so it is created here rather than passed in.
	ctx.done = map[string]bool{}

	// Window-level remain-on-exit
	if w.RemainOnExit != nil {
		if out, err := r.Run("set-window-option", "-t", windowID, "remain-on-exit", boolOption(*w.RemainOnExit)); err != nil {
			warnf(stderr, "failed to enable remain-on-exit for window %q: %v", w.Name, tmux.CmdErr(err, out))
		}
	}

	var paneToZoom string
	ctx.onPaneCreated = func(paneID string, s config.Split) {
		if s.RemainOnExit != nil {
			if out, err := r.Run("set-option", "-p", "-t", paneID, "remain-on-exit", boolOption(*s.RemainOnExit)); err != nil {
				warnf(stderr, "failed to set remain-on-exit on pane %s: %v", paneID, tmux.CmdErr(err, out))
			}
		}
		if (s.Zoomed != nil && *s.Zoomed) || (s.Zoom != nil && *s.Zoom) {
			paneToZoom = paneID
		}
	}

	if len(w.Splits) > 0 && w.Splits[0].Type == "" {
		ctx.onPaneCreated(initialPane, w.Splits[0])
	}

	switch {
	case len(w.Splits) > 0:
		applySplits(r, initialPane, w.Splits, ctx, stderr)
	case len(w.Panes) > 0:
		applyPanes(r, windowID, initialPane, w, ctx, stderr)
	case w.PreWindow != "":
		sendPreWindow(ctx.keys, initialPane, w.PreWindow, ctx.done)
	}

	if paneToZoom != "" {
		if out, err := r.Run("resize-pane", "-Z", "-t", paneToZoom); err != nil {
			warnf(stderr, "failed to zoom pane %s: %v", paneToZoom, tmux.CmdErr(err, out))
		}
	}
}

func boolOption(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// splitCtx is what a level of the split tree inherits from the one above it:
// the directory new panes open in, the environment they get, the window's
// pre_window command, and the per-pane record of where it has already run.
type splitCtx struct {
	root          string
	envMap        map[string]string
	preWindow     string
	done          map[string]bool
	keys          *keyBatch
	onPaneCreated func(paneID string, s config.Split)
}

// applySplits walks a split tree. Each entry with a type splits the pane of
// the previous entry at the same level (the base pane for the first entry);
// entries without a type reuse that pane. Children operate within their
// parent's pane. Panes are addressed by tmux pane ID (%N), so the result is
// independent of the user's pane-base-index setting.
//
// Every sibling at a level is created before descending into any of their
// children. tmux's own layout tree nests containers inside cells, so a
// sibling's size is a share of the *container* — not of whatever is left after
// an earlier sibling's children already subdivided it. Splitting breadth-first
// is what makes `size` mean that, and what lets a layout captured by
// `wyrm save` rebuild to the geometry it was captured from.
func applySplits(r tmux.Runner, basePane string, splits []config.Split, ctx splitCtx, stderr io.Writer) {
	panes := make([]string, len(splits))
	roots := make([]string, len(splits))
	splitEnvs := make([]map[string]string, len(splits))
	current := basePane
	for i, s := range splits {
		root, err := config.ResolveRoot(ctx.root, s.Root)
		if err != nil {
			warnf(stderr, "split %d: %v", i, err)
			root = ctx.root
		}
		roots[i] = root
		splitEnv := mergeEnv(ctx.envMap, s.Env)
		splitEnvs[i] = splitEnv

		pane := current
		paneRoot, paneEnv, command := paneAttributes(s, ctx.root, ctx.envMap)
		if s.Type != "" {
			process := s
			process.Run = command
			newPane, err := splitPane(r, current, process, paneRoot, envArgs(paneEnv))
			if err != nil {
				warnf(stderr, "failed to split pane: %v", err)
				continue // panes[i] stays "": skipped below
			}
			pane = newPane
			if ctx.onPaneCreated != nil {
				ctx.onPaneCreated(pane, s)
			}
		}
		panes[i] = pane
		current = pane
		if command != "" {
			ctx.keys.launch(pane, paneRoot, paneEnv, command)
		}
	}

	// A first entry with a type splits basePane and lands its command in the
	// *new* pane, so the loop below never touches basePane — but it is still a
	// pane of this window, so pre_window still applies to it. At nested levels
	// basePane is the parent's pane, already in done, so this is a no-op there.
	// When basePane instead runs a process directly (a typeless first entry
	// with `run`, possibly via a chain of typeless children), keyBatch.flush
	// drops the send, so there is no need to detect that case here.
	sendPreWindow(ctx.keys, basePane, ctx.preWindow, ctx.done)

	for i, s := range splits {
		pane := panes[i]
		if pane == "" {
			continue
		}
		// A pane created with `run` has no shell to type into: the process is
		// already what the pane is. splitPane (or paneProcess, for the window's
		// initial pane) has started it, so neither pre_window nor Command can
		// be typed into it — both would land as literal input to that process.
		if s.Run == "" {
			sendPreWindow(ctx.keys, pane, ctx.preWindow, ctx.done)
			ctx.keys.add(pane, s.Command)
		} else {
			// No shell, but it can still parent children.
			ctx.done[pane] = true
		}
		child := ctx
		child.root = roots[i]
		child.envMap = splitEnvs[i]
		if len(s.Children) > 0 && s.Children[0].Type == "" && ctx.onPaneCreated != nil {
			ctx.onPaneCreated(pane, s.Children[0])
		}
		applySplits(r, pane, s.Children, child, stderr)
	}
}

func splitPane(r tmux.Runner, target string, s config.Split, root string, env []string) (string, error) {
	dir := "-v"
	if t := strings.ToLower(s.Type); t == "h" || t == "horizontal" {
		dir = "-h"
	}
	// -d leaves the active pane where it was, so a finished window is focused
	// on its first pane rather than on whichever split happened to be created
	// last. startup_pane still overrides this.
	args := []string{"split-window", "-d", "-t", target, dir, "-P", "-F", "#{pane_id}"}
	// -c explicitly, always. Without it tmux starts the pane in the *invoking
	// client's* working directory rather than anywhere related to the session:
	// `wyrm api` run from ~ built a session rooted at ~/work/api whose every
	// split pane sat in ~. Only the window's initial pane, created with
	// new-session/new-window -c, was ever in the right place.
	if root != "" {
		args = append(args, "-c", root)
	}
	if s.Size > 0 {
		// -l N% rather than -p N: -p was deprecated in tmux 3.1 and removed
		// from newer builds; -l with a percentage works on 3.1+.
		args = append(args, "-l", fmt.Sprintf("%d%%", s.Size))
	}
	args = append(args, env...)
	if s.Run != "" {
		// "--" so a command starting with "-" isn't read as more tmux flags.
		args = append(args, "--", startupWaitCommand)
	}
	out, err := r.Run(args...)
	if err != nil {
		return "", tmux.CmdErr(err, out)
	}
	if err := tmux.CheckID(tmux.PaneSigil, "pane", out); err != nil {
		return "", err
	}
	return out, nil
}

// applyPanes implements the legacy flat pane list: panes split alternately
// h/v off the previously created pane, then a layout evens them out.
func applyPanes(r tmux.Runner, windowID, initialPane string, w config.Window, ctx splitCtx, stderr io.Writer) {
	sendPreWindow(ctx.keys, initialPane, w.PreWindow, ctx.done)
	ctx.keys.add(initialPane, w.Panes[0].Command)

	current := initialPane
	for i, p := range w.Panes[1:] {
		dir := "-h"
		if i%2 == 1 {
			dir = "-v"
		}
		args := []string{"split-window", "-d", "-t", current, dir, "-P", "-F", "#{pane_id}"}
		if ctx.root != "" {
			args = append(args, "-c", ctx.root) // see splitPane
		}
		args = append(args, envArgs(ctx.envMap)...)
		out, err := r.Run(args...)
		if err != nil {
			warnf(stderr, "failed to split pane: %v (%s)", err, out)
			continue
		}
		if err := tmux.CheckID(tmux.PaneSigil, "pane", out); err != nil {
			warnf(stderr, "failed to split pane: %v", err)
			continue
		}
		current = out
		sendPreWindow(ctx.keys, current, w.PreWindow, ctx.done)
		ctx.keys.add(current, p.Command)
	}

	layout := w.Layout
	if layout == "" && len(w.Panes) > 1 {
		layout = "tiled"
	}
	if layout != "" {
		if out, err := r.Run("select-layout", "-t", windowID, layout); err != nil {
			warnf(stderr, "failed to apply layout %q: %v (%s)", layout, err, out)
		}
	}
}

// sendPreWindow types the window's pre_window command into a pane, at most
// once per pane. pre_window is documented as running in every pane of the
// window, and tracking panes rather than split-tree entries is what makes that
// true: the tree can visit one pane more than once (a nested container reuses
// its parent's pane as its own first entry) and can leave one unvisited (a
// first entry with a type splits the window's initial pane and lands
// everything in the new one).
func sendPreWindow(keys *keyBatch, target, preWindow string, done map[string]bool) {
	if preWindow == "" || target == "" || done[target] {
		return
	}
	done[target] = true
	keys.add(target, preWindow)
}

// keySend is one command to type into one pane: the literal text and the Enter
// that submits it.
type keySend struct {
	target, command string
}

// keyBatch collects every command a build will type, so they can all be issued
// in one tmux process at the end instead of two processes per pane.
//
// Deferring them is safe because a pane's ID is known the moment it is created
// and never changes — nothing later in the build alters where a command should
// land. It is also slightly *safer* than typing as we go: the shells have had
// longer to start by the time anything is sent.
type keyBatch struct {
	sends        []keySend
	processes    map[string][]string
	processOrder []string
}

func (k *keyBatch) launch(pane, root string, env map[string]string, command string) {
	if k.processes == nil {
		k.processes = make(map[string][]string)
	}
	if _, exists := k.processes[pane]; !exists {
		k.processOrder = append(k.processOrder, pane)
	}
	args := []string{"respawn-pane", "-k", "-t", pane, "-c", root}
	args = append(args, envArgs(env)...)
	k.processes[pane] = append(args, "--", command)
}

// add queues a command. Commands starting with "#" are comments and are
// skipped, as is the empty string.
func (k *keyBatch) add(target, command string) {
	if command == "" || strings.HasPrefix(command, "#") || target == "" {
		return
	}
	k.sends = append(k.sends, keySend{target: target, command: command})
}

// flush issues everything queued and warns about whatever failed, one warning
// per command rather than one per tmux call.
func (k *keyBatch) flush(r tmux.Runner, stderr io.Writer) {
	for _, pane := range k.processOrder {
		if out, err := r.Run(k.processes[pane]...); err != nil {
			warnf(stderr, "starting process in %s: %v", pane, tmux.CmdErr(err, out))
		}
	}
	// No typed setup may land in a direct process, even when a container
	// higher in the tree contributed a command for the same physical pane.
	sends := k.sends[:0]
	for _, s := range k.sends {
		if _, direct := k.processes[s.target]; !direct {
			sends = append(sends, s)
		}
	}
	k.sends = sends
	if len(k.sends) == 0 {
		return
	}
	// -l types the argument literally and "--" ends the flag list. Without
	// them tmux first looks the argument up as a key name, so a command that
	// happens to be one ("up", "space", "tab", "c-c") is sent as that key
	// instead of typed, and a command starting with "-" is taken for a flag.
	// tmux.LiteralText covers the one thing "-l --" does not: a trailing ";",
	// which tmux's argument parser strips as a command separator. Enter is then
	// sent separately, as an actual key.
	cmds := make([][]string, 0, len(k.sends)*2)
	for _, s := range k.sends {
		cmds = append(cmds,
			[]string{"send-keys", "-t", s.target, "-l", "--", tmux.LiteralText(s.command)},
			[]string{"send-keys", "-t", s.target, "Enter"})
	}

	errs := tmux.RunEach(r, cmds)
	for i, s := range k.sends {
		// Either half failing means the command didn't run as typed; report it
		// once, naming the command rather than the tmux call.
		if err := errs[i*2]; err != nil {
			warnf(stderr, "failed to run %q in %s: %v", s.command, s.target, err)
			continue
		}
		if err := errs[i*2+1]; err != nil {
			warnf(stderr, "failed to run %q in %s: %v", s.command, s.target, err)
		}
	}
	k.sends = nil
}

// defaultPaneTitleFormat mirrors tmux's own pane-border-format default
// closely enough to be useful without configuring anything beyond
// enable_pane_titles = true.
const defaultPaneTitleFormat = "#{pane_index}: #{pane_current_command}"

// enablePaneTitles turns on tmux's live pane-border status line for the
// window, if the config asked for it. It's cosmetic — a failure here
// leaves a fully usable session — so it warns and continues rather than
// aborting the build, same as every other per-pane failure in this package.
func enablePaneTitles(r tmux.Runner, windowID string, s config.Session, stderr io.Writer) {
	if s.EnablePaneTitles == nil || !*s.EnablePaneTitles {
		return
	}
	position := s.PaneTitlePosition
	if position == "" {
		position = "top"
	}
	if out, err := r.Run("set-option", "-t", windowID, "pane-border-status", position); err != nil {
		warnf(stderr, "failed to enable pane titles: %v", tmux.CmdErr(err, out))
		return
	}
	format := s.PaneTitleFormat
	if format == "" {
		format = defaultPaneTitleFormat
	}
	if out, err := r.Run("set-option", "-t", windowID, "pane-border-format", format); err != nil {
		warnf(stderr, "failed to set pane title format: %v", tmux.CmdErr(err, out))
	}
}

// selectStartup focuses the session's startup window (given by name or index)
// and, optionally, a pane within it. Both are resolved to tmux object IDs
// (@window, %pane) via list-windows/list-panes rather than assembled into a
// "session:window.pane" target string: tmux's -t syntax treats "." as the
// window.pane separator, so a window whose name contains "." would otherwise
// be misparsed — the same hazard tmux.FindSessionID avoids for session names.
// Resolving against the live window/pane list also means an unknown
// startup_window simply isn't found (and is warned about) rather than being
// smuggled into a target string.
func selectStartup(r tmux.Runner, session, window string, pane *int, stderr io.Writer) {
	windows, err := tmux.ListWindows(r, session)
	if err != nil {
		warnf(stderr, "failed to list windows for startup_window %q: %v", window, err)
		return
	}
	windowID, ok := findStartupWindow(windows, window)
	if !ok {
		warnf(stderr, "startup_window %q not found", window)
		return
	}
	if _, err := r.Run("select-window", "-t", windowID); err != nil {
		warnf(stderr, "failed to select window %q: %v", window, err)
		return
	}
	if pane == nil {
		return
	}
	panes, err := tmux.ListPanes(r, windowID)
	if err != nil {
		warnf(stderr, "failed to list panes for startup_pane in window %q: %v", window, err)
		return
	}
	paneID, ok := findStartupPane(panes, *pane)
	if !ok {
		warnf(stderr, "startup_pane %d not found in window %q", *pane, window)
		return
	}
	if _, err := r.Run("select-pane", "-t", paneID); err != nil {
		warnf(stderr, "failed to select pane %d in window %q: %v", *pane, window, err)
	}
}

// findStartupWindow resolves a startup_window value — a window name, or a
// window index written as a string — to its tmux window ID. A name match wins
// over an index match, mirroring tmux's own "session:window" resolution.
func findStartupWindow(windows []tmux.WindowInfo, spec string) (string, bool) {
	for _, w := range windows {
		if w.Name == spec {
			return w.ID, true
		}
	}
	if idx, err := strconv.Atoi(spec); err == nil {
		for _, w := range windows {
			if w.Index == idx {
				return w.ID, true
			}
		}
	}
	return "", false
}

// findStartupPane resolves a startup_pane index (honoring the user's
// pane-base-index, since it matches against tmux's reported pane_index) to its
// tmux pane ID.
func findStartupPane(panes []tmux.PaneInfo, index int) (string, bool) {
	for _, p := range panes {
		if p.Index == index {
			return p.ID, true
		}
	}
	return "", false
}

// runHook executes a lifecycle hook via the user's shell in the given
// directory. It honors $SHELL (the same shell wyrm's pane commands land in),
// falling back to sh, rather than hardcoding bash — which isn't present on
// every system and isn't necessarily the user's shell where it is.
//
// The hook's output is streamed to stderr rather than captured, and the fact
// that it's running is announced first. Hooks are routinely slow (a
// `git pull && npm install` is the documented example) and blocking with a
// blank screen and no output looks indistinguishable from a hang. stderr
// rather than stdout so hook chatter can't be confused with wyrm's own
// progress lines.
func runHook(o options, hook, dir, label string, env map[string]string, stderr io.Writer) error {
	if hook == "" {
		return nil
	}
	if o.dryRun {
		// Into the transcript rather than stderr, so it reads in sequence with
		// the tmux commands it sits between. Two lines, because the directory a
		// hook runs in is as much a part of "what would happen" as the command.
		_, _ = fmt.Fprintf(o.transcript, "# would run %s, in %s:\n#   %s\n", label, dir, hook)
		return nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	_, _ = fmt.Fprintf(stderr, "wyrm: running %s: %s\n", label, hook)
	cmd := exec.Command(shell, "-c", hook)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = hookEnv(env)
	}
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// runFirstStartOrRestartHook fires exactly one of on_project_first_start /
// on_project_restart, alongside the on_project_start that always fires — see
// HookHistory. It is a no-op without a history (nothing to consult) or for a
// config with no on-disk identity (cfg.Dir() == ""; the built-in default or
// one built in memory), since there is no meaningful "has this project
// started before" for either.
//
// History is keyed by root — the resolved, absolute session directory —
// rather than cfg.Dir(), the directory the config *file* lives in. The two
// differ for a wildcard project: every directory a [[wildcard]] pattern
// matches shares one template file (and so one cfg.Dir()), but each has its
// own root. Keying by cfg.Dir() collapsed them into a single lifecycle
// identity, so only the first directory ever matched by a given template
// saw on_project_first_start; every other one was told it had already
// started.
//
// The MarkStarted write is skipped under dry-run: describing what would
// happen must not itself change what "first start" means for the real run
// that follows.
func runFirstStartOrRestartHook(o options, cfg *config.Config, root string, stderr io.Writer) {
	if o.history == nil || cfg.Dir() == "" {
		return
	}
	if o.history.Started(root) {
		if err := runHook(o, cfg.Session.OnProjectRestart, root, "on_project_restart", cfg.Session.Env, stderr); err != nil {
			warnf(stderr, "on_project_restart failed: %v", err)
		}
		return
	}
	if err := runHook(o, cfg.Session.OnProjectFirstStart, root, "on_project_first_start", cfg.Session.Env, stderr); err != nil {
		warnf(stderr, "on_project_first_start failed: %v", err)
	}
	if o.dryRun {
		return
	}
	if err := o.history.MarkStarted(root); err != nil {
		warnf(stderr, "failed to record project start: %v", err)
	}
}

// hookEnv merges the process environment with the session's configured env map,
// sorted for deterministic ordering.
func hookEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := append([]string(nil), os.Environ()...)
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func warnf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "wyrm: warning: "+format+"\n", args...)
}

func mergeEnv(parent, child map[string]string) map[string]string {
	if len(parent) == 0 && len(child) == 0 {
		return nil
	}
	res := make(map[string]string, len(parent)+len(child))
	for k, v := range parent {
		res[k] = v
	}
	for k, v := range child {
		res[k] = v
	}
	return res
}
