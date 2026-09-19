// Command wyrm creates repeatable tmux session layouts from a TOML config.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// version is stamped at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Settings are loaded here, once, only to build the tmux runner: its
	// [tmux].socket/command decide which server and binary every subsequent
	// tmux invocation targets, runner.Attach included. Every verb still loads
	// its own Settings for its own purposes (shared dir, TUI prefs) — this
	// isn't a substitute for that, just the one load early enough to shape
	// the runner before any verb runs.
	settings, err := config.LoadSettings()
	if err != nil {
		if !settingsOptional(os.Args[1:]) {
			_, _ = fmt.Fprintln(os.Stderr, "wyrm: "+err.Error())
			os.Exit(1)
		}
		settings = &config.Settings{Storage: config.StorageLocal}
	}
	for _, w := range settings.Warnings() {
		_, _ = fmt.Fprintln(os.Stderr, "wyrm: warning: "+w)
	}
	runner := runnerFromSettings(settings)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, runner, tmux.InsideTmux, runner.Attach))
}

// Diagnostics and basic help must remain available when settings are broken.
func settingsOptional(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "doctor", "help", "--help", "-help", "-h", "version", "--version", "-version", "-v":
		return true
	}
	return false
}

// runnerFromSettings builds the tmux.Exec every tmux invocation for this
// process goes through, from [tmux].socket/command (WYRM_TMUX_SOCKET/COMMAND
// take priority — see Settings.TmuxSocket/TmuxCommand). Split out from main
// so it's a plain, directly testable function despite main itself calling
// os.Exit.
func runnerFromSettings(settings *config.Settings) tmux.Exec {
	return tmux.Exec{SocketName: settings.TmuxSocket(), Command: settings.TmuxCommand()}
}

// app carries the process-level dependencies every subcommand needs. Verbs take
// one of these rather than the five separate parameters they used to, which is
// what lets each of them return a plain error instead of hand-rolling its own
// "print wyrm: <err> and return 1" — see run's report.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	runner         tmux.Runner
	insideTmux     func() bool
	attach         func(string) error

	watchCtx context.Context

	// httpClient is used only by selfupdate. Left nil in the normal
	// construction path below, where it defaults to http.DefaultClient;
	// tests point it at an httptest server instead.
	httpClient *http.Client
}

func (a *app) in() io.Reader {
	if a.stdin != nil {
		return a.stdin
	}
	return os.Stdin
}

func (a *app) promptConfirm(msg string) bool {
	_, _ = fmt.Fprint(a.stdout, msg)
	var resp string
	if _, err := fmt.Fscanln(a.in(), &resp); err != nil {
		return false
	}
	resp = strings.ToLower(strings.TrimSpace(resp))
	return resp == "y" || resp == "yes"
}

// exitErr is a subcommand failure carrying an explicit exit status. A nil Err
// means the failure has already been reported — the flag package prints its own
// parse errors and -h output — so only the code is left to convey.
type exitErr struct {
	Err  error
	Code int
}

func (e exitErr) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e exitErr) Unwrap() error { return e.Err }

// usageErrf reports a usage mistake: a bad flag value, an unknown format, a
// stray positional argument. Exit 2, matching the flag package's own convention
// for "you typed the command wrong" as distinct from "the command failed".
func usageErrf(format string, args ...any) error {
	return exitErr{fmt.Errorf(format, args...), 2}
}

// silent returns an error that sets the exit code without printing anything.
func silent(code int) error { return exitErr{nil, code} }

// report turns a verb's error into an exit code, printing it with wyrm's
// prefix. This is the single place that formatting lives; before it, the same
// two lines were spelled out at every one of ~37 failure sites.
func (a *app) report(err error) int {
	if err == nil {
		return 0
	}
	var ex exitErr
	if errors.As(err, &ex) {
		if ex.Err != nil {
			_, _ = fmt.Fprintln(a.stderr, "wyrm: "+ex.Err.Error())
		}
		return ex.Code
	}
	_, _ = fmt.Fprintln(a.stderr, "wyrm: "+err.Error())
	return 1
}

// run implements the CLI. It takes its dependencies as parameters (rather
// than reaching for globals like os.Stdout or the default flag.CommandLine)
// so tests can drive it without touching real stdio or a real tmux server.
//
// The command surface is git-style subcommands: bare `wyrm` (or `wyrm up`)
// builds or attaches the current folder's session, `wyrm <name>` attaches to
// a running session by name, and every other mode is its own verb (kill,
// pick, tui, save, edit, validate, list, ...). Because each verb is a
// separate branch that parses only its own flags, the old problem of
// silently-ignored or mutually-exclusive top-level flags can't arise.
//
// The verb implementations live in cmd_*.go, grouped by what they act on.
// runOptions carries the dependencies only some callers need, so they do not
// have to be package globals.
//
// run used to read appStdin and appWatchCtx from two mutable package-level
// variables written only by tests. That contradicted run's own contract of
// taking its dependencies as parameters, forced every test touching them to be
// non-parallel, and left two mutable globals in the shipped binary. They are
// options because the common case passes neither.
type runOptions struct {
	// stdin is what prompts read from. nil means os.Stdin.
	stdin io.Reader
	// watchCtx bounds the lifetime of a --watch loop. nil means run until
	// interrupted.
	watchCtx context.Context
}

func run(args []string, stdout, stderr io.Writer, runner tmux.Runner, insideTmux func() bool, attach func(string) error) int {
	return runWith(runOptions{}, args, stdout, stderr, runner, insideTmux, attach)
}

// runWith is run with the optional dependencies supplied explicitly.
func runWith(opts runOptions, args []string, stdout, stderr io.Writer, runner tmux.Runner, insideTmux func() bool, attach func(string) error) int {
	a := &app{stdin: opts.stdin, stdout: stdout, stderr: stderr, runner: runner, insideTmux: insideTmux, attach: attach, watchCtx: opts.watchCtx}

	if len(args) == 0 {
		return a.report(a.up(nil))
	}

	switch cmd := args[0]; cmd {
	case "version", "--version", "-version", "-v":
		_, _ = fmt.Fprintln(stdout, "wyrm "+versionString())
		return 0
	case "help", "--help", "-help", "-h":
		printUsage(stdout)
		return 0
	case "up":
		return a.report(a.up(args[1:]))
	case "restart":
		return a.report(a.restart(args[1:]))
	case "kill":
		return a.report(a.kill(args[1:]))
	case "pick":
		return a.report(a.pick(args[1:]))
	case "tui":
		return a.report(a.tui(args[1:]))
	case "save":
		return a.report(a.save(args[1:]))
	case "edit":
		return a.report(a.edit(args[1:]))
	case "validate":
		return a.report(a.validate(args[1:]))
	case "list":
		return a.report(a.list(args[1:]))
	case "list-configs":
		return a.report(a.listConfigs(args[1:]))
	case "migrate-config":
		return a.report(a.migrateConfig(args[1:]))
	case "clone":
		return a.report(a.clone(args[1:]))
	case "selfupdate":
		return a.report(a.selfupdate(args[1:]))
	case "status":
		return a.report(a.status(args[1:]))
	case "send":
		return a.report(a.send(args[1:]))
	case "export":
		return a.report(a.export(args[1:]))
	case "init":
		return a.report(a.init(args[1:]))
	case "setup-tmux":
		return a.report(a.setupTmux(args[1:]))
	case "doctor":
		return a.report(a.doctor(args[1:]))
	default:
		if strings.HasPrefix(cmd, "-") {
			// A bare flag with no subcommand (e.g. `wyrm -config x`) drives the
			// default build/attach, so the common `wyrm -config foo` still works.
			return a.report(a.up(args))
		}
		// Anything else is a running session name to attach to, or a known
		// project to start. This is what shell completion (see completions/)
		// completes a bare argument to.
		return a.report(a.attachByName(cmd, args[1:]))
	}
}

// versionString returns the release version stamped at build time, or — for an
// unstamped `go install`/`go build` where version is still "dev" — the VCS
// revision the Go toolchain records in the build info, so bug reports carry
// something more useful than a bare "dev". The unstamped form uses semver's
// "+build-metadata" separator (dev+<rev>) rather than a parenthetical, so
// tooling that greps for a semver-ish token isn't thrown by stray parens.
func versionString() string {
	return computeVersionString(version, debug.ReadBuildInfo)
}

// computeVersionString is versionString's logic with debug.ReadBuildInfo
// passed in, so tests can supply a fake *debug.BuildInfo — a real `go test`
// binary doesn't carry VCS settings the way `go build`/`go install` do, so
// the build-info branch below isn't otherwise reachable from a test.
func computeVersionString(version string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if version != "dev" {
		return version
	}
	info, ok := readBuildInfo()
	if !ok {
		return version
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return "dev+" + rev
}

const usage = `wyrm — repeatable tmux session layouts from a TOML config.

Usage:
  wyrm [-config PATH]        build or attach the current folder's session (default)
  wyrm up [-config PATH]     same as bare wyrm, spelled explicitly (-n to dry-run, -d to skip attaching)
  wyrm <name>                attach to a running session, or start a known project, by name
  wyrm restart [-config P]   stop the session and build it again (-all, -n, -d, -y)
  wyrm kill [name]           destroy the session (runs on_project_exit first; -all, -n, -y)
  wyrm pick                  fuzzy-pick a running session and attach to it
  wyrm tui                   full-screen session manager (browse, preview, manage)
  wyrm save [-config PATH]   save the running session's layout as this folder's config (-stdout, -n)
  wyrm edit [-config PATH]   open the resolved config in $EDITOR, creating one if needed
  wyrm validate [-config P]  check the effective config parses and validates (-strict)
  wyrm status [-format FMT]  print agent status across sessions (FMT: text, json, tmux, waybar, sketchybar)
  wyrm send [target] [cmd]   send command or keys to target session/window/pane (-l, -n, -r)
  wyrm export [-o file]      write running sessions as JSON (-n N, -current)
  wyrm list [-format FMT]    list running sessions (FMT: table, json, toml, names)
  wyrm list-configs [-names] list config paths, or project names and aliases
  wyrm migrate-config        move the local config into the shared config directory
  wyrm clone REPO [DEST]     git clone, then confirm and build a session for it (-y, -no-start)
  wyrm init [-template T]    scaffold a project config interactively or with -template (-force)
  wyrm selfupdate            download and install the latest release (-check, -version V)
  wyrm setup-tmux            generate or append recommended tmux popup configuration (-a)
  wyrm doctor                check tmux, settings, configs, and optional tools (-strict)
  wyrm version               print version and exit
  wyrm help                  show this help

Run a subcommand with -h for its own flags.
`

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, usage)
}

// newFlagSet builds a subcommand flag set that reports parse errors to stderr
// and returns them (ContinueOnError) rather than calling os.Exit, so run stays
// testable. Its Usage prints the subcommand's one-line synopsis (reusing the
// same top-level usage string printUsage does, so there's nothing new to keep
// in sync) ahead of the flag list, rather than a bare stdlib flag dump with no
// context.
func (a *app) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("wyrm "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(a.stderr, usageSynopsis(name))
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			_, _ = fmt.Fprintln(a.stderr, "\nFlags:")
			fs.PrintDefaults()
		}
	}
	return fs
}

// usageSynopsis returns the one-line description of a subcommand from the
// top-level usage string (e.g. "wyrm kill [-config PATH]   destroy the
// session (runs on_project_exit first)"), falling back to a bare "wyrm
// <name>" if the string doesn't mention it (which TestSubcommandsListedInUsage
// guards against).
func usageSynopsis(name string) string {
	re := regexp.MustCompile(`(?m)^  (wyrm ` + regexp.QuoteMeta(name) + `\b.*)$`)
	if m := re.FindStringSubmatch(usage); m != nil {
		return strings.TrimRight(m[1], " ")
	}
	return "wyrm " + name
}

// parseFlags parses a subcommand's flags. flag has already printed whatever the
// user needs to see, so both outcomes are silent: nil (exit 0) for -h/-help,
// and a silent exit 2 for a genuine parse error.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return silent(0)
		}
		return silent(2)
	}
	return nil
}

// requireNoArgs rejects leftover positional arguments after a subcommand's
// flags. Without it a mistake like `wyrm list json` (meaning `-format json`)
// is silently ignored and prints the default format instead.
func requireNoArgs(fs *flag.FlagSet) error {
	if fs.NArg() == 0 {
		return nil
	}
	err := usageErrf("unexpected argument %q for %s", fs.Arg(0), fs.Name())
	fs.Usage()
	return err
}

// printWarnings reports a config's non-fatal problems (see
// config.Config.Warnings) on stderr, so they're visible without being mixed
// into output a script might be parsing.
func (a *app) printWarnings(cfg *config.Config) {
	for _, w := range cfg.Warnings() {
		_, _ = fmt.Fprintln(a.stderr, "wyrm: warning: "+w)
	}
}

// resolveConfig loads the config wyrm would use and reports where it came
// from. With five discovery layers (explicit path, local file, shared file,
// user default, built-in), a user who gets an unexpected session otherwise has
// no way to find out which one was picked — so say so, on stderr, always.
func (a *app) resolveConfig(settings *config.Settings, explicitPath string) (*config.Config, string, error) {
	cfg, source, err := config.ResolveEffective(settings, explicitPath)
	if err != nil {
		return nil, "", err
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: using config %s\n", source)
	a.printWarnings(cfg)
	return cfg, source, nil
}

// knownSubcommands lists the verbs run()'s switch dispatches on (mirrored by
// TestKnownSubcommandsMatchDispatch, which parses the switch itself so this
// list can't silently drift from the real dispatch table). Used only to
// power the "did you mean" hint in attachByName.
var knownSubcommands = []string{
	"up", "restart", "kill", "pick", "tui", "save", "edit", "validate", "status", "send", "setup-tmux",
	"list", "list-configs", "migrate-config", "clone", "init", "selfupdate", "doctor", "version", "help",
}

// nearestSubcommand returns the known subcommand closest to name by edit
// distance, when it's close enough to plausibly be a typo (distance <= 2,
// and not so close to name's own length that nearly anything would "match").
func nearestSubcommand(name string) (string, bool) {
	best := ""
	bestDist := -1
	for _, verb := range knownSubcommands {
		d := levenshtein(name, verb)
		if bestDist == -1 || d < bestDist {
			best, bestDist = verb, d
		}
	}
	if bestDist >= 0 && bestDist <= 2 && bestDist < len(best) {
		return best, true
	}
	return "", false
}

// levenshtein returns the edit distance between a and b.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// attachOrSwitch hands the terminal to the session identified by sessionID
// (a tmux session ID such as "$3" — see tmux.FindSessionID for why a raw
// session name isn't used here), switching the client instead of nesting
// when wyrm is already running inside tmux.
func (a *app) attachOrSwitch(sessionID string) error {
	if a.insideTmux() {
		if out, err := a.runner.Run("switch-client", "-t", sessionID); err != nil {
			return fmt.Errorf("switching to session: %w", tmux.CmdErr(err, out))
		}
		return nil
	}
	if err := a.attach(sessionID); err != nil {
		return fmt.Errorf("attaching to session: %w", err)
	}
	return nil
}
