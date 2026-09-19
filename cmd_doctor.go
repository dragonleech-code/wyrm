package main

// wyrm doctor: one command that answers "why isn't this working".
//
// Almost everything wyrm can get wrong, it gets wrong *quietly*. A wildcard
// pattern that matches nothing, an agent busy_pattern that fails to compile, a
// theme file with a typo, no clipboard tool installed, a settings key that was
// misspelled and therefore ignored, a tmux too old for the sizes in a config —
// each of these degrades into "it just doesn't do the thing" with nothing on
// screen to explain it. Individually none of them justifies a startup check
// that everyone pays for on every run; together they justify one place a user
// can go to see the whole picture at once.
//
// Every check reads state and reports; none of them repair anything.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/clipboard"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/editor"
	"github.com/jskoll/wyrm/internal/selfupdate"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/jskoll/wyrm/internal/tmux"
	"github.com/jskoll/wyrm/internal/tui"
	"github.com/jskoll/wyrm/internal/zoxide"
)

// minTmuxVersion is the oldest tmux wyrm supports: `split-window -l N%` — how
// every `size` in a config is applied — needs 3.1. Below that, sizes fail one
// warning at a time and the layout silently comes out even.
const minTmuxMajor, minTmuxMinor = 3, 1

// checkLevel is how much attention a finding deserves.
type checkLevel int

const (
	levelOK checkLevel = iota
	// levelNote is a fact worth stating that is not a problem: a feature
	// switched off, a file that doesn't exist yet.
	levelNote
	levelWarn
	levelError
)

func (l checkLevel) label() string {
	switch l {
	case levelNote:
		return "note"
	case levelWarn:
		return "warn"
	case levelError:
		return "err "
	}
	return "ok  "
}

// finding is one line of the report: what was checked, what was found, and —
// when something is wrong — what to do about it. Hints are separate strings
// rather than one joined blob so a config with three warnings reads as three
// lines instead of one that wraps off the screen.
type finding struct {
	level  checkLevel
	name   string
	detail string
	fixes  []string
}

// doctorReport accumulates findings so the summary can count them.
type doctorReport struct {
	findings []finding
}

func (d *doctorReport) add(level checkLevel, name, detail string, fixes ...string) {
	var kept []string
	for _, f := range fixes {
		if f != "" {
			kept = append(kept, f)
		}
	}
	d.findings = append(d.findings, finding{level: level, name: name, detail: detail, fixes: kept})
}

func (d *doctorReport) ok(name, detail string)   { d.add(levelOK, name, detail) }
func (d *doctorReport) note(name, detail string) { d.add(levelNote, name, detail) }
func (d *doctorReport) warn(name, detail string, fixes ...string) {
	d.add(levelWarn, name, detail, fixes...)
}
func (d *doctorReport) error(name, detail string, fixes ...string) {
	d.add(levelError, name, detail, fixes...)
}

// count returns how many findings are at exactly this level. Levels are
// counted separately rather than cumulatively so the summary can say
// "1 error, 3 warnings" without the warning count swallowing the error.
func (d *doctorReport) count(level checkLevel) int {
	n := 0
	for _, f := range d.findings {
		if f.level == level {
			n++
		}
	}
	return n
}

// doctor inspects wyrm's environment and configuration and reports anything
// that is broken or silently doing nothing.
func (a *app) doctor(args []string) error {
	fs := a.newFlagSet("doctor")
	strict := fs.Bool("strict", false, "exit non-zero for warnings as well as errors")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	var d doctorReport

	// Settings are loaded before anything is reported — every check below
	// depends on what they say — but reported after tmux, which is the more
	// fundamental thing to establish and the one people scroll to first.
	settings, settingsErr := config.LoadSettings()
	if settingsErr != nil {
		// Carry on with the defaults so the rest of the report is still useful.
		settings = &config.Settings{Storage: config.StorageLocal}
	}

	a.checkTmux(&d, settings)
	if settingsErr != nil {
		d.error("settings", settingsErr.Error(),
			"fix the file, or move it aside to fall back to the defaults")
	} else {
		a.checkSettings(&d, settings)
	}
	a.checkStorage(&d, settings)
	a.checkWildcards(&d, settings)
	a.checkConfig(&d, settings)
	a.checkState(&d)
	a.checkAgent(&d, settings)
	a.checkAgentNotify(&d, settings)
	a.checkTUI(&d, settings)
	a.checkExternalTools(&d, settings)
	a.checkSelfupdate(&d)

	a.printDoctor(&d)

	if d.count(levelError) > 0 {
		return silent(1)
	}
	if *strict && d.count(levelWarn) > 0 {
		return silent(1)
	}
	return nil
}

// printDoctor renders the report: one aligned line per finding, with an
// indented "→" hint under anything actionable. Plain text and no color, like
// `wyrm list` — this is output people paste into bug reports.
func (a *app) printDoctor(d *doctorReport) {
	width := 0
	for _, f := range d.findings {
		if len(f.name) > width {
			width = len(f.name)
		}
	}
	for _, f := range d.findings {
		_, _ = fmt.Fprintf(a.stdout, "%s  %-*s  %s\n", f.level.label(), width, f.name, f.detail)
		for _, fix := range f.fixes {
			_, _ = fmt.Fprintf(a.stdout, "      %-*s  → %s\n", width, "", fix)
		}
	}

	errs, warns := d.count(levelError), d.count(levelWarn)
	_, _ = fmt.Fprintln(a.stdout)
	switch {
	case errs == 0 && warns == 0:
		_, _ = fmt.Fprintln(a.stdout, "no problems found")
	default:
		_, _ = fmt.Fprintf(a.stdout, "%s, %s\n", plural(errs, "error"), plural(warns, "warning"))
	}
}

// plural renders a count with its noun. The irregular plural is given
// explicitly where "+s" is wrong ("directory" -> "directories").
func plural(n int, singular string, irregular ...string) string {
	if n == 1 {
		return "1 " + singular
	}
	form := singular + "s"
	if len(irregular) > 0 {
		form = irregular[0]
	}
	return strconv.Itoa(n) + " " + form
}

// tildePath abbreviates the user's home directory to "~", so a report full of
// absolute paths stays readable at terminal width.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == home {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return p
}

// checkTmux reports which tmux binary wyrm will run, its version, and whether
// a server is up.
func (a *app) checkTmux(d *doctorReport, settings *config.Settings) {
	bin := settings.TmuxCommand()
	if bin == "" {
		bin = "tmux"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		d.error("tmux", bin+" not found on PATH",
			"install tmux, or set [tmux].command / WYRM_TMUX_COMMAND to its path")
		return
	}

	// -V through the Runner rather than exec directly, so this reports the
	// version of the binary wyrm actually invokes, socket override included.
	out, err := a.runner.Run("-V")
	if err != nil {
		d.warn("tmux", tildePath(path)+" (could not read version: "+err.Error()+")")
		return
	}
	version := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "tmux "))
	major, minor, ok := parseTmuxVersion(version)
	switch {
	case !ok && version == "":
		d.note("tmux", fmt.Sprintf("%s (reported no version)", tildePath(path)))
	case !ok:
		d.note("tmux", fmt.Sprintf("%s at %s (unrecognized version)", version, tildePath(path)))
	case major < minTmuxMajor || (major == minTmuxMajor && minor < minTmuxMinor):
		d.warn("tmux", fmt.Sprintf("%s at %s", version, tildePath(path)),
			fmt.Sprintf("wyrm needs %d.%d or newer: `split-window -l N%%%%` is how every "+
				"`size` in a config is applied, and older builds ignore it", minTmuxMajor, minTmuxMinor))
	default:
		d.ok("tmux", fmt.Sprintf("%s at %s", version, tildePath(path)))
	}

	socket := settings.TmuxSocket()
	label := "default server"
	if socket != "" {
		label = "socket " + socket
	}
	sessions, err := listSessionNames(a.runner)
	switch {
	case err != nil:
		d.warn("tmux server", label+": "+err.Error())
	case len(sessions) == 0:
		d.note("tmux server", label+", no sessions running")
	default:
		d.ok("tmux server", fmt.Sprintf("%s, %s running", label, plural(len(sessions), "session")))
	}
}

// listSessionNames is sessions.List reduced to what doctor needs, without
// importing the package for one count.
func listSessionNames(r tmux.Runner) ([]string, error) {
	out, err := r.Run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if tmux.NoServerRunning(err, out) {
			return nil, nil
		}
		return nil, tmux.CmdErr(err, out)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// parseTmuxVersion reads the major and minor numbers out of a tmux version
// string. tmux appends a letter to patch releases ("3.5a") and prefixes
// development builds ("next-3.6"), neither of which a plain numeric split
// survives — "3.5a" parsed as 3.0 would report a current tmux as too old.
func parseTmuxVersion(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(v, "next-")
	majorStr, rest, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return 0, 0, false
	}
	// Stop at the first non-digit: the trailing letter on a patch release.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(rest[:end])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// checkSettings reports where the global settings file is and any keys in it
// that wyrm ignored.
func (a *app) checkSettings(d *doctorReport, settings *config.Settings) {
	path, err := config.SettingsPath()
	if err != nil {
		d.error("settings", err.Error())
		return
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		d.note("settings", tildePath(path)+" (none; using defaults)")
		return
	}
	warnings := settings.Warnings()
	if len(warnings) == 0 {
		d.ok("settings", tildePath(path))
		return
	}
	// An unknown key is not a parse error, so it takes effect as "nothing at
	// all" — e.g. a "[[widcard]]" section (note the missing "l"), which parses
	// clean and then never matches anything.
	d.warn("settings", fmt.Sprintf("%s (%s)", tildePath(path), plural(len(warnings), "ignored key")),
		warnings...)
}

// checkStorage reports where project configs are looked for.
func (a *app) checkStorage(d *doctorReport, settings *config.Settings) {
	if settings.Storage != config.StorageShared {
		d.ok("storage", "local (.wyrm.toml in the project directory)")
	} else {
		dir, err := settings.ResolvedSharedDir()
		if err != nil {
			d.error("storage", "shared, but shared_dir is unusable: "+err.Error(),
				"fix [shared_dir] in the settings file")
			return
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*"+config.DefaultFileName))
		if len(matches) == 0 {
			d.warn("storage", fmt.Sprintf("shared → %s (no configs)", tildePath(dir)),
				"run `wyrm migrate-config` in a project to move its config here")
		} else {
			d.ok("storage", fmt.Sprintf("shared → %s (%s)", tildePath(dir), plural(len(matches), "config")))
		}
	}

	if settings.UpwardDiscoveryEnabled() {
		d.ok("discovery", "upward (searches parent directories up to a git root)")
	} else {
		d.note("discovery", "current directory only (discovery.upward = false)")
	}
}

// checkWildcards reports each [[wildcard]] pattern and how many directories it
// currently matches. A pattern matching nothing is the classic silent no-op:
// the config parses, the feature is on, and the Projects panel simply stays
// empty.
func (a *app) checkWildcards(d *doctorReport, settings *config.Settings) {
	if len(settings.Wildcard) == 0 {
		return
	}
	for i, w := range settings.Wildcard {
		name := fmt.Sprintf("wildcard[%d]", i)
		if w.Config == "" {
			d.error(name, fmt.Sprintf("%q has no config", tildePath(w.Pattern)), "set `config` to a template file")
			continue
		}
		cfgPath, err := config.ExpandPath(w.Config)
		if err != nil {
			d.error(name, fmt.Sprintf("%q: %v", tildePath(w.Pattern), err))
			continue
		}
		if _, err := config.Load(cfgPath); err != nil {
			d.error(name, fmt.Sprintf("%q → template %s: %v", tildePath(w.Pattern), tildePath(cfgPath), err),
				"every directory this pattern matches is unbuildable until the template loads")
			continue
		}
		dirs, err := config.WildcardMatches(w)
		if err != nil {
			d.error(name, fmt.Sprintf("%q: %v", tildePath(w.Pattern), err))
			continue
		}
		if len(dirs) == 0 {
			d.warn(name, fmt.Sprintf("%q matches no directories", tildePath(w.Pattern)),
				"check the pattern; \"*\" matches one path segment, \"/**\" recurses")
			continue
		}
		d.ok(name, fmt.Sprintf("%q → %s", tildePath(w.Pattern), plural(len(dirs), "directory", "directories")))
	}
}

// checkConfig reports which config a bare `wyrm` would build here, and what is
// wrong with it. This is the same resolution app.resolveConfig prints, without
// building anything.
func (a *app) checkConfig(d *doctorReport, settings *config.Settings) {
	cfg, source, err := config.ResolveEffective(settings, "")
	if err != nil {
		d.error("config", err.Error(), "run `wyrm validate` for the full message")
		return
	}
	name, root, rerr := cfg.Session.Resolve(cfg.Dir())
	detail := source
	if rerr == nil {
		detail = fmt.Sprintf("%s → session %q in %s", source, name, tildePath(root))
	}
	warnings := cfg.Warnings()
	switch {
	case rerr != nil:
		d.error("config", fmt.Sprintf("%s: %v", source, rerr))
	case len(warnings) > 0:
		d.warn("config", fmt.Sprintf("%s (%s)", detail, plural(len(warnings), "warning")),
			warnings...)
	default:
		d.ok("config", detail)
	}
}

// checkState reports the first-start record on_project_first_start consults.
func (a *app) checkState(d *doctorReport) {
	store, err := state.Load()
	if err != nil {
		d.warn("state", err.Error(),
			"on_project_first_start and on_project_restart cannot tell each other apart until this loads")
		return
	}
	d.ok("state", fmt.Sprintf("%s (%s recorded as started)", tildePath(store.Path()), plural(store.Len(), "project")))
}

// checkAgent reports whether agent detection is on and whether the profiles
// driving it actually compile. A bad busy_pattern disables the markers exactly
// as thoroughly as switching the feature off, and says nothing.
func (a *app) checkAgent(d *doctorReport, settings *config.Settings) {
	if !settings.AgentEnabled() {
		d.note("agent", "disabled (tui.agent.enabled = false)")
		return
	}
	profiles, err := agent.ProfilesFrom(settings)
	if err != nil {
		d.error("agent", err.Error(), "agent markers stay off until this compiles")
		return
	}
	var commands []string
	for _, p := range profiles {
		commands = append(commands, p.Commands...)
	}
	source := "built-in profile"
	if len(settings.AgentProfiles()) > 0 {
		source = plural(len(profiles), "custom profile")
	}
	d.ok("agent", fmt.Sprintf("%s, watching %s", source, strings.Join(commands, ", ")))
}

// checkAgentNotify reports notification channels. It is separate from checkAgent so a profile that fails
// to compile does not also hide whatever is wrong with the notifications.
func (a *app) checkAgentNotify(d *doctorReport, settings *config.Settings) {
	if !settings.AgentNotifyEnabled() {
		d.note("agent notify", "disabled")
		return
	}
	// Deliberately exclusive, mirroring agent.Dispatch: a configured command
	// returns before the desktop notification, so listing both here would
	// promise a desktop popup that never arrives.
	var channels []string
	if settings.AgentNotifyCommand() != "" {
		channels = append(channels, "command")
	} else if settings.AgentNotifyDesktop() {
		channels = append(channels, "desktop")
	}
	if settings.AgentNotifyBell() {
		channels = append(channels, "bell")
	}
	if settings.AgentNotifyOSC() {
		channels = append(channels, "osc")
	}
	if len(channels) == 0 {
		d.warn("agent notify", "enabled, but every channel is off",
			"set one of desktop, bell, osc, or command")
		return
	}
	detail := strings.Join(channels, ", ")
	d.ok("agent notify", "enabled: "+detail)
}

// checkTUI reports on the optional theme file, which the TUI refuses to start
// without when it is malformed.
func (a *app) checkTUI(d *doctorReport, settings *config.Settings) {
	path, err := config.ThemePath()
	if err == nil {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			d.note("theme", tildePath(path)+" (none; using the built-in palette)")
		} else if _, terr := tui.LoadTheme(); terr != nil {
			d.error("theme", terr.Error(), "`wyrm tui` and `wyrm pick` will not start until this loads")
		} else {
			d.ok("theme", tildePath(path))
		}
	}
	if settings.MouseEnabled() {
		d.ok("tui mouse", "enabled")
	} else {
		d.note("tui mouse", "disabled (tui.mouse = false)")
	}
}

// checkExternalTools reports the optional binaries wyrm can use: the editor
// `wyrm edit` opens, a clipboard backend for the TUI's "y", and zoxide.
func (a *app) checkExternalTools(d *doctorReport, settings *config.Settings) {
	parts, err := editor.Resolve()
	switch {
	case err != nil:
		d.warn("editor", err.Error(), "fix $EDITOR; `wyrm edit` falls back to "+editor.Fallback)
	case len(parts) == 0:
		d.warn("editor", "unresolved")
	default:
		if _, lookErr := exec.LookPath(parts[0]); lookErr != nil {
			d.warn("editor", strings.Join(parts, " ")+" (not on PATH)",
				"`wyrm edit` will fail; set $EDITOR to something installed")
		} else {
			d.ok("editor", strings.Join(parts, " "))
		}
	}

	if err := clipboard.Available(); err != nil {
		d.warn("clipboard", "no backend found",
			"install one of "+clipboard.Backends()+"; until then \"y\" in the TUI cannot copy")
	} else {
		d.ok("clipboard", "available")
	}

	switch {
	case !settings.ZoxideEnabled():
		d.note("zoxide", "disabled (zoxide.enabled = false)")
	case !zoxide.Available():
		d.warn("zoxide", "enabled, but the zoxide binary is not on PATH",
			"install zoxide, or set zoxide.enabled = false")
	default:
		d.ok("zoxide", "enabled")
	}
}

// checkSelfupdate reports whether this build can verify the releases it
// installs. A build with no signing key compiled in can tell a corrupted
// download from a good one, but not a genuine release from a tampered one.
func (a *app) checkSelfupdate(d *doctorReport) {
	if selfupdate.DefaultSigningKey.Valid() {
		d.ok("release signing", "public key compiled in; releases are signature-verified")
		return
	}
	d.note("release signing", "no key compiled in; `wyrm selfupdate` verifies checksums only")
}
