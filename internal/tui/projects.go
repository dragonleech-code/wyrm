package tui

import (
	"bytes"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/editor"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/jskoll/wyrm/internal/tmux"
	"github.com/jskoll/wyrm/internal/zoxide"
)

// Project is a discoverable wyrm config the TUI can start, attach to, edit, or
// stop. Name is the session name the config would produce; Running/SessionID
// are filled by joining against the live session list.
type Project struct {
	Name      string
	Path      string
	Shared    bool
	Running   bool
	SessionID string
	// Root and Wildcard mirror config.Project — see DiscoverWildcardProjects.
	// Root is the matched directory for a wildcard-discovered project
	// (nonempty only when Wildcard is true), which overrides the template
	// config's own session.root when the project is started.
	Root     string
	Wildcard bool
	// Zoxide is true for a directory zoxide knows about that has no wyrm
	// config of its own — see appendZoxideProjects. Path is empty for these;
	// starting one builds a session from the user's default config (or the
	// built-in one) rooted at Root.
	Zoxide bool
}

// listProjects annotates config.DiscoverProjects — the shared discovery rules,
// used identically by `wyrm <name>` and `wyrm list-configs` — with whether a
// session by each project's name is currently running, then folds in
// zoxide-known directories that don't already have a project of their own
// (opt-in — see appendZoxideProjects).
func listProjects(r tmux.Runner, settings *config.Settings) ([]Project, error) {
	// sessions.List already treats "no server running" as an empty list, not
	// an error — so any error it does return is a real one (permission,
	// a broken tmux config, ...). Ignoring it here used to mean every
	// project displayed as stopped whenever the list call failed, silently
	// misrepresenting what was actually running rather than surfacing the
	// failure.
	sessionList, err := sessions.List(r)
	if err != nil {
		return nil, err
	}
	return projectsFromSessions(settings, sessionList), nil
}

func projectsFromSessions(settings *config.Settings, sessionList []sessions.Session) []Project {
	running := map[string]string{}
	for _, s := range sessionList {
		running[s.Name] = s.ID
	}

	discovered := config.DiscoverProjects(settings)
	projects := make([]Project, 0, len(discovered))
	names := make(map[string]bool, len(discovered))
	for _, d := range discovered {
		p := Project{Name: d.Name, Path: d.Path, Shared: d.Shared, Root: d.Root, Wildcard: d.Wildcard}
		if id := runningProjectID(running, d.Name); id != "" {
			p.Running, p.SessionID = true, id
		}
		projects = append(projects, p)
		names[d.Name] = true
	}
	projects = appendZoxideProjects(projects, names, running, settings)
	return projects
}

// appendZoxideProjects folds zoxide's directory list into projects, skipping
// any directory whose basename collides with a project already discovered —
// a duplicate row with the same name would be confusing regardless of
// whether the two technically point at different directories, and a
// same-named wyrm project is the more specific, more likely intended match
// anyway. Absent entirely unless both settings.ZoxideEnabled() and the
// zoxide binary itself are available — see internal/zoxide's package doc.
func appendZoxideProjects(projects []Project, names map[string]bool, running map[string]string, settings *config.Settings) []Project {
	if !settings.ZoxideEnabled() || !zoxide.Available() {
		return projects
	}
	entries, err := zoxide.Query(0)
	if err != nil {
		return projects
	}
	for _, e := range entries {
		name, _, err := (config.Session{Root: e.Path}).Resolve("")
		if err != nil {
			continue
		}
		if name == "" || names[name] {
			continue
		}
		names[name] = true
		p := Project{Name: name, Root: e.Path, Zoxide: true}
		if id := runningProjectID(running, name); id != "" {
			p.Running, p.SessionID = true, id
		}
		projects = append(projects, p)
	}
	return projects
}

func runningProjectID(running map[string]string, name string) string {
	if id := running[name]; id != "" {
		return id
	}
	return running[tmux.SanitizeName(name)]
}

// --- messages ---

type projectsMsg struct {
	projects []Project
	err      error
	// warnings carries non-fatal stderr output from the command that
	// triggered this reload (e.g. killProjectCmd's on_project_exit), when
	// the operation itself otherwise succeeded. See projectStartedMsg for
	// why this can't just reuse err.
	warnings string
}

type configPreviewMsg struct {
	path    string
	content string
	err     error
}

type projectStartedMsg struct {
	sessionID string
	err       error
	// warnings carries non-fatal stderr output from building the session or
	// running on_project_attach (a failed pane split, a hook that exited
	// non-zero, ...). Kept apart from err — which, on this message, aborts
	// the attach Update is about to make (see its handler) — so a warning is
	// still visible without blocking the attach the user is already
	// quitting into.
	warnings string
}

// --- commands ---

func loadConfigPreview(path string) tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		return configPreviewMsg{path: path, content: string(data), err: err}
	}
}

// startProjectCmd builds (or, if already running, reuses) the session for a
// project and resolves to a projectStartedMsg carrying the session ID to
// attach to. session.Create is idempotent, so this doubles as "attach".
func startProjectCmd(r tmux.Runner, settings *config.Settings, p Project) tea.Cmd {
	return func() tea.Msg {
		cfg, err := projectConfig(p)
		if err != nil {
			return projectStartedMsg{err: err}
		}
		hist, err := state.Load()
		if err != nil {
			return projectStartedMsg{err: err}
		}
		// Captured rather than discarded: Create's own per-pane warnings (a
		// failed split, a hook that exited non-zero, ...) reach the CLI's
		// stderr directly, but discarding them here left the TUI the only
		// place a project could be started with something silently having
		// gone wrong.
		var warnings bytes.Buffer
		_, id, _, err := session.Create(r, cfg, io.Discard, &warnings, session.WithHistory(hist))
		if err == nil {
			// projectStartedMsg always quits into an attach (see Update), so
			// this is an attach — session.Create no longer runs the hook.
			_ = session.RunAttachHook(cfg, &warnings)
		}
		if err == nil && p.Root != "" && settings.ZoxideTrack() && zoxide.Available() {
			// Best-effort: teaching zoxide about a directory wyrm just
			// built a session for is a nice-to-have, never worth failing
			// the session build over.
			_ = zoxide.Add(p.Root)
		}
		msg := projectStartedMsg{sessionID: id, err: err}
		if err == nil && warnings.Len() > 0 {
			msg.warnings = strings.TrimSpace(warnings.String())
		}
		return msg
	}
}

// configProject is the discovery-layer view of this project — the identity
// config.Project.LoadConfig needs to turn it into a buildable config. A zoxide
// directory has no config file of its own, which LoadConfig recognises by the
// empty Path.
func (p Project) configProject() config.Project {
	return config.Project{
		Name:     p.Name,
		Path:     p.Path,
		Shared:   p.Shared,
		Root:     p.Root,
		Wildcard: p.Wildcard,
	}
}

// projectConfig loads the config a project should build from. The rules live
// in config.Project.LoadConfig so that starting a project here and killing it
// from the CLI cannot disagree about which config a project means.
func projectConfig(p Project) (*config.Config, error) {
	return p.configProject().LoadConfig()
}

// killProjectCmd stops a project's session, running its on_project_exit hook
// (unlike the hook-less session kills), then re-lists projects to refresh the
// running annotation.
//
// It takes the project rather than its config path because a path alone does
// not identify a project: a wildcard project's path is the shared template,
// and a zoxide directory has no path at all. Loading by path killed the wrong
// session for the first and failed outright for the second.
func killProjectCmd(r tmux.Runner, settings *config.Settings, p config.Project) tea.Cmd {
	return func() tea.Msg {
		cfg, err := p.LoadConfig()
		if err != nil {
			return actionErrMsg{err}
		}
		// Captured rather than discarded: a failed on_project_exit reaches
		// the CLI's stderr directly, but discarding it here left the TUI the
		// only place a project could be stopped with its exit hook silently
		// having failed.
		var warnings bytes.Buffer
		if _, err := session.Kill(r, cfg, &warnings); err != nil {
			return actionErrMsg{err}
		}
		ps, lerr := listProjects(r, settings)
		msg := projectsMsg{projects: ps, err: lerr}
		if lerr == nil && warnings.Len() > 0 {
			msg.warnings = strings.TrimSpace(warnings.String())
		}
		return msg
	}
}

// editConfigCmd opens the config in $EDITOR (suspending the TUI via
// tea.ExecProcess and resuming after), then re-lists projects. Editor
// resolution is shared with `wyrm edit`.
func editConfigCmd(r tmux.Runner, settings *config.Settings, path string) tea.Cmd {
	c, err := editor.Command(path)
	if err != nil {
		return func() tea.Msg { return actionErrMsg{err} }
	}
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return actionErrMsg{err}
		}
		ps, lerr := listProjects(r, settings)
		return projectsMsg{projects: ps, err: lerr}
	})
}
