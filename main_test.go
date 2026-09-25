package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/config"
)

// TestMain isolates every test in this package from the developer's real
// global wyrm settings file (~/.config/wyrm/config.toml), so run()'s call
// to config.LoadSettings() always sees defaults unless a test overrides
// HOME/XDG_CONFIG_HOME itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wyrm-test-config")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeRunner is a minimal tmux.Runner double, mirroring the one in
// internal/session's tests: it fabricates -P -F output for commands that
// need it and can be told to fail specific commands by name.
type fakeRunner struct {
	calls [][]string
	seq   int
	fail  map[string]bool
	// listOutput is returned verbatim for any "list-sessions" call: it backs
	// both tmux.FindSessionID's existence check (id|name pairs) and -list's
	// picker.ListSessions (the fuller id|windows|attached|activity|name
	// format) — whichever a given test actually exercises. Empty means "no
	// matching/running sessions", matching real tmux's "no server running".
	listOutput string
	// displayMessageOutput backs a target-less "display-message" call
	// (tmux.CurrentSession), as "$id|name".
	displayMessageOutput string
	// lastSessionName is what the most recent new-session call resolved to,
	// echoed back by newSession's targeted display-message lookup of the
	// session's real name — see tmux.SessionName.
	lastSessionName string
	// listWindowsOutput backs any "list-windows" call, regardless of target.
	listWindowsOutput string
	// listPanesOutput backs "list-panes", keyed by the -t target (a window
	// ID such as "@1").
	listPanesOutput map[string]string
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.fail[args[0]] {
		return "boom", errors.New("exit status 1")
	}
	switch args[0] {
	case "kill-session":
		// Reflect the kill in what list-sessions reports next, so a sequence
		// like kill-then-create behaves the way it would against real tmux
		// rather than still finding the session it just destroyed.
		f.listOutput = ""
		return "", nil
	case "new-session":
		f.seq++
		name := ""
		for i, a := range args {
			if a == "-s" && i+1 < len(args) {
				name = args[i+1]
			}
		}
		f.lastSessionName = name
		return fmt.Sprintf("$%d|@%d|%%%d", f.seq, f.seq, f.seq), nil
	case "new-window":
		f.seq++
		return fmt.Sprintf("@%d|%%%d", f.seq, f.seq), nil
	case "split-window":
		f.seq++
		return fmt.Sprintf("%%%d", f.seq), nil
	case "list-sessions":
		if len(args) >= 3 && args[1] == "-F" && args[2] == "#{session_id}|#{session_name}" {
			var lines []string
			for _, line := range strings.Split(f.listOutput, "\n") {
				parts := strings.Split(line, "|")
				if len(parts) >= 2 {
					lines = append(lines, parts[0]+"|"+parts[len(parts)-1])
				}
			}
			return strings.Join(lines, "\n"), nil
		}
		// The fuller sessions.List format. A fixture written as the short
		// "id|name" pair is padded out to it, so one listOutput can serve both
		// consumers — several tests exercise a verb that calls each in turn.
		var lines []string
		for _, line := range strings.Split(f.listOutput, "\n") {
			if line == "" {
				continue
			}
			if parts := strings.Split(line, "|"); len(parts) == 2 {
				line = parts[0] + "|1|0|0|" + parts[1]
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n"), nil
	case "display-message":
		for _, a := range args {
			if a == "-t" {
				// newSession's targeted lookup of the session's real name —
				// see tmux.SessionName — not the target-less current-session
				// query tmux.CurrentSession makes.
				return f.lastSessionName, nil
			}
		}
		return f.displayMessageOutput, nil
	case "list-windows":
		return f.listWindowsOutput, nil
	case "list-panes":
		return f.listPanesOutput[args[2]], nil
	}
	return "", nil
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wyrm.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFakeEditor writes a shell script that overwrites its target path
// (passed as $1, wyrm's -edit convention) with content, and returns its path
// for use as $EDITOR.
func writeFakeEditor(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-editor.sh")
	script := "#!/bin/sh\ncat > \"$1\" <<'WYRM_EOF'\n" + content + "\nWYRM_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	// t.Chdir restores the previous directory itself, and refuses to run in a
	// parallel test — which is the bug the hand-rolled version could not catch.
	t.Chdir(dir)
}

const validConfig = `
[session]
name = "proj"
root = "/tmp/proj"
[[windows]]
name = "w"
`

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"version"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "wyrm "+version) {
		t.Errorf("stdout = %q, want containing version string", stdout.String())
	}
}

func TestRunVersionAlias(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-version", "-v"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", arg, code)
		}
		if !strings.Contains(stdout.String(), "wyrm "+version) {
			t.Errorf("%s: stdout = %q, want the version string", arg, stdout.String())
		}
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", arg, code)
		}
		if !strings.Contains(stdout.String(), "Usage:") || !strings.Contains(stdout.String(), "wyrm kill") {
			t.Errorf("%s: stdout = %q, want the usage overview", arg, stdout.String())
		}
	}
}

func TestSubcommandHelpShowsSynopsisAndFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"kill", "-h"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "wyrm kill [name]") {
		t.Errorf("stderr = %q, want the subcommand's usage-line synopsis", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Flags:") || !strings.Contains(stderr.String(), "-config") {
		t.Errorf("stderr = %q, want a Flags section listing -config", stderr.String())
	}
}

func TestSubcommandHelpOmitsFlagsSectionWhenNone(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"pick", "-h"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "wyrm pick") {
		t.Errorf("stderr = %q, want the subcommand's usage-line synopsis", stderr.String())
	}
	if strings.Contains(stderr.String(), "Flags:") {
		t.Errorf("stderr = %q, want no Flags section for a flagless subcommand", stderr.String())
	}
}

func TestRunUpExplicit(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	attachCalled := ""
	code := run([]string{"up", "-config", path}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, func(name string) error { attachCalled = name; return nil })
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session proj") {
		t.Errorf("stdout = %q, want a created-session message", stdout.String())
	}
	if attachCalled != "$1" {
		t.Errorf("attach called with %q, want $1", attachCalled)
	}
}

func TestRunUpRejectsExtraArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"up", "somename"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Errorf("stderr = %q, want an unexpected-argument error", stderr.String())
	}
}

func TestRunSubcommandRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-bogus"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestRunFlagParseError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-bogus-flag"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestReportExitCodes covers the one place a verb's error becomes an exit code
// and a message. Every subcommand routes through it, so the prefix and the
// exit-status policy are pinned here rather than at each of the sites that used
// to spell them out by hand.
func TestReportExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOut  string
	}{
		{"nil is success", nil, 0, ""},
		{"a plain error exits 1", errors.New("boom"), 1, "wyrm: boom\n"},
		{"a usage mistake exits 2", usageErrf("bad %s", "flag"), 2, "wyrm: bad flag\n"},
		{"an already-reported failure prints nothing", silent(2), 2, ""},
		{"an already-reported help exits 0", silent(0), 0, ""},
		{"a wrapped exitErr keeps its code", fmt.Errorf("ctx: %w", usageErrf("inner")), 2, "wyrm: inner\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			a := &app{stdout: io.Discard, stderr: &stderr}
			if got := a.report(tt.err); got != tt.wantCode {
				t.Errorf("report = %d, want %d", got, tt.wantCode)
			}
			if got := stderr.String(); got != tt.wantOut {
				t.Errorf("stderr = %q, want %q", got, tt.wantOut)
			}
		})
	}
}

func TestRunUpSettingsLoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	settingsDir := filepath.Join(home, ".config", "wyrm")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, config.SettingsFileName), []byte("storage = \"bogus\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	code := run([]string{"up"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "storage must be") {
		t.Errorf("stderr = %q, want the settings load error surfaced", stderr.String())
	}
}

func TestComputeVersionStringStamped(t *testing.T) {
	got := computeVersionString("1.2.3", func() (*debug.BuildInfo, bool) {
		t.Fatal("readBuildInfo should not be called when version is already stamped")
		return nil, false
	})
	if got != "1.2.3" {
		t.Errorf("got %q, want %q", got, "1.2.3")
	}
}

func TestComputeVersionStringNoBuildInfo(t *testing.T) {
	got := computeVersionString("dev", func() (*debug.BuildInfo, bool) {
		return nil, false
	})
	if got != "dev" {
		t.Errorf("got %q, want %q", got, "dev")
	}
}

func TestComputeVersionStringNoRevision(t *testing.T) {
	got := computeVersionString("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{}, true
	})
	if got != "dev" {
		t.Errorf("got %q, want %q", got, "dev")
	}
}

func TestComputeVersionStringWithRevision(t *testing.T) {
	got := computeVersionString("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef0123456789"},
		}}, true
	})
	if got != "dev+abcdef012345" {
		t.Errorf("got %q, want %q", got, "dev+abcdef012345")
	}
}

func TestComputeVersionStringDirty(t *testing.T) {
	got := computeVersionString("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	})
	if got != "dev+abc123-dirty" {
		t.Errorf("got %q, want %q", got, "dev+abc123-dirty")
	}
}

func TestRunConfigLoadError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.toml")
	code := run([]string{"-config", missing}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "wyrm:") {
		t.Errorf("stderr = %q, want a wyrm: prefixed error", stderr.String())
	}
}

func TestRunValidateValid(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate", "-config", path}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "config valid: "+path) {
		t.Errorf("stdout = %q, want it to name the validated path", stdout.String())
	}
}

func TestRunValidateInvalid(t *testing.T) {
	path := writeConfig(t, "not valid toml [[[")

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate", "-config", path}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "wyrm:") {
		t.Errorf("stderr = %q, want a wyrm: prefixed error", stderr.String())
	}
}

// TestRunValidateCatchesUnresolvedRoot is the regression test for validate
// checking only decoded structure, not what session.root/a window's root
// actually resolve to: an unset environment variable there used to report
// "config valid" and then fail in `wyrm up`.
func TestRunValidateCatchesUnresolvedRoot(t *testing.T) {
	t.Setenv("WYRM_TEST_UNDEFINED_VAR_ABC123", "")
	os.Unsetenv("WYRM_TEST_UNDEFINED_VAR_ABC123")
	content := "[session]\nname = \"proj\"\nroot = \"$WYRM_TEST_UNDEFINED_VAR_ABC123/api\"\n\n[[windows]]\nname = \"w\"\n"
	path := writeConfig(t, content)

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate", "-config", path}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code == 0 {
		t.Fatalf("exit code = 0, want a nonzero exit for an unresolved root; stdout=%q", stdout.String())
	}
	if strings.Contains(stdout.String(), "config valid") {
		t.Errorf("stdout = %q, falsely reported the config valid", stdout.String())
	}
}

func TestRunValidateFallsBackToBuiltInDefault(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "config valid: built-in default") {
		t.Errorf("stdout = %q, want it to name the built-in default", stdout.String())
	}
}

func TestRunEditExisting(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile(config.DefaultFileName, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", writeFakeEditor(t, validConfig))

	var stdout, stderr bytes.Buffer
	code := run([]string{"edit"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	got, err := os.ReadFile(config.DefaultFileName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `name = "proj"`) {
		t.Errorf("file content = %q, want the fake editor's written content", got)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want no warning for a valid save", stderr.String())
	}
}

func TestRunEditCreatesLocal(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("EDITOR", writeFakeEditor(t, validConfig))

	var stdout, stderr bytes.Buffer
	code := run([]string{"edit"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(config.DefaultFileName); err != nil {
		t.Errorf("expected %s to be created: %v", config.DefaultFileName, err)
	}
}

func TestRunEditCreatesShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	projectDir := t.TempDir()
	chdir(t, projectDir)

	sharedDir := t.TempDir()
	settingsDir := filepath.Join(home, ".config", "wyrm")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsContent := "storage = \"shared\"\nshared_dir = \"" + sharedDir + "\"\n"
	if err := os.WriteFile(filepath.Join(settingsDir, config.SettingsFileName), []byte(settingsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", writeFakeEditor(t, validConfig))

	var stdout, stderr bytes.Buffer
	code := run([]string{"edit"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	wantPath := filepath.Join(sharedDir, filepath.Base(projectDir)+config.DefaultFileName)
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected %s to be created: %v", wantPath, err)
	}
}

func TestRunEditWarnsOnInvalidSave(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("EDITOR", writeFakeEditor(t, "not valid toml [[["))

	var stdout, stderr bytes.Buffer
	code := run([]string{"edit"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (warn, don't fail): stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("stderr = %q, want a warning about the invalid save", stderr.String())
	}
}

func TestRunSaveInsideTmux(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "saved session myproj to "+config.DefaultFileName) {
		t.Errorf("stdout = %q, want a saved-session message", stdout.String())
	}
	got, err := os.ReadFile(config.DefaultFileName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `name = 'myproj'`) || !strings.Contains(string(got), `command = 'nvim'`) {
		t.Errorf("file content = %q, want the captured session name and pane command", got)
	}
}

func TestRunSaveOutsideTmux(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	name := filepath.Base(dir)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput:        "$7|" + name,
		listWindowsOutput: "0|@2|1|abcd,80x24,0,0,3|shell",
		listPanesOutput:   map[string]string{"@2": "%3|0|1|zsh"},
	}
	code := run([]string{"save"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "saved session "+name+" to "+config.DefaultFileName) {
		t.Errorf("stdout = %q, want a saved-session message for %q", stdout.String(), name)
	}
	if _, err := os.Stat(config.DefaultFileName); err != nil {
		t.Errorf("expected %s to be created: %v", config.DefaultFileName, err)
	}
}

func TestRunSaveOutsideTmuxNoRunningSession(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{} // no sessions running
	code := run([]string{"save"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running session") {
		t.Errorf("stderr = %q, want a no-running-session message", stderr.String())
	}
	if _, err := os.Stat(config.DefaultFileName); err == nil {
		t.Error("expected no config file to be created")
	}
}

func TestRunSaveRefusesExistingConfig(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile(config.DefaultFileName, []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want an already-exists message", stderr.String())
	}
	got, err := os.ReadFile(config.DefaultFileName)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != validConfig {
		t.Error("existing config was overwritten, want it left untouched")
	}
}

func TestRunSaveWithConfigFlag(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save", "-config", "custom.toml"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "saved session myproj to custom.toml") {
		t.Errorf("stdout = %q, want a saved-session message naming custom.toml", stdout.String())
	}
	if _, err := os.Stat("custom.toml"); err != nil {
		t.Errorf("expected custom.toml to be created: %v", err)
	}
	if _, err := os.Stat(config.DefaultFileName); err == nil {
		t.Errorf("expected %s not to be created when -config overrides the destination", config.DefaultFileName)
	}
}

func TestRunSaveWithConfigFlagRefusesExisting(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile("custom.toml", []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save", "-config", "custom.toml"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want an already-exists message", stderr.String())
	}
}

func TestRunSaveCurrentSessionError(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{fail: map[string]bool{"display-message": true}}
	code := run([]string{"save"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "wyrm:") {
		t.Errorf("stderr = %q, want a wyrm: prefixed error", stderr.String())
	}
}

func TestRunSaveStdout(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save", "-stdout"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `name = 'myproj'`) || !strings.Contains(out, `command = 'nvim'`) {
		t.Errorf("stdout = %q, want TOML containing session name and command", out)
	}
	if _, err := os.Stat(config.DefaultFileName); err == nil {
		t.Errorf("expected %s not to be created when -stdout is used", config.DefaultFileName)
	}
}

func TestRunSaveOutputDash(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save", "-o", "-"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `name = 'myproj'`) || !strings.Contains(out, `command = 'nvim'`) {
		t.Errorf("stdout = %q, want TOML containing session name and command", out)
	}
}

func TestRunSaveDryRun(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
		listWindowsOutput:    "0|@1|1|abcd,80x24,0,0,0|main",
		listPanesOutput:      map[string]string{"@1": "%0|0|1|nvim"},
	}
	code := run([]string{"save", "-n"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "# Dry run: would save session myproj to") {
		t.Errorf("stdout = %q, want dry-run header", out)
	}
	if !strings.Contains(out, `name = 'myproj'`) {
		t.Errorf("stdout = %q, want generated TOML in dry run preview", out)
	}
	if _, err := os.Stat(config.DefaultFileName); err == nil {
		t.Errorf("expected %s not to be created under -n", config.DefaultFileName)
	}
}

func TestRunSaveIncompatibleFlags(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		displayMessageOutput: "$3|myproj",
	}

	// -stdout with -config
	code := run([]string{"save", "-stdout", "-config", "custom.toml"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for incompatible flags", code)
	}

	// -o mismatch with -config
	stderr.Reset()
	code = run([]string{"save", "-config", "a.toml", "-o", "b.toml"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for conflicting config and -o", code)
	}
}

func TestRunListTable(t *testing.T) {
	r := &fakeRunner{listOutput: strings.Join([]string{
		"$1|3|1|2000|alpha",
		"$2|1|0|1000|beta",
	}, "\n")}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "3 windows") || !strings.Contains(out, "(attached)") {
		t.Errorf("stdout = %q, want alpha row with window count and attached marker", out)
	}
	if !strings.Contains(out, "beta") || !strings.Contains(out, "1 window") {
		t.Errorf("stdout = %q, want beta row with singular window", out)
	}
	if strings.Index(out, "alpha") > strings.Index(out, "beta") {
		t.Errorf("stdout = %q, want alpha (more recent) before beta", out)
	}
}

func TestRunListTableEmpty(t *testing.T) {
	r := &fakeRunner{listOutput: "no server running on /tmp/tmux-1000/default"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty output for an empty session list", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no running tmux sessions") {
		t.Errorf("stderr = %q, want a no-sessions notice", stderr.String())
	}
}

func TestRunListJSON(t *testing.T) {
	r := &fakeRunner{listOutput: "$1|2|1|1000|alpha"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-format", "json"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	var got []struct {
		Name     string `json:"name"`
		Windows  int    `json:"windows"`
		Attached bool   `json:"attached"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v (stdout=%q)", err, stdout.String())
	}
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Windows != 2 || !got[0].Attached {
		t.Errorf("got %+v, want one alpha session with 2 windows, attached", got)
	}
}

func TestRunListJSONEmpty(t *testing.T) {
	r := &fakeRunner{listOutput: "no server running on /tmp/tmux-1000/default"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-format", "json"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Errorf("stdout = %q, want []", stdout.String())
	}
}

func TestRunListTOML(t *testing.T) {
	r := &fakeRunner{listOutput: "$1|2|0|1000|alpha"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-format", "toml"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "name = 'alpha'") && !strings.Contains(stdout.String(), `name = "alpha"`) {
		t.Errorf("stdout = %q, want a sessions entry named alpha", stdout.String())
	}
}

func TestRunListNames(t *testing.T) {
	r := &fakeRunner{listOutput: strings.Join([]string{
		"$1|3|1|2000|alpha",
		"$2|1|0|1000|beta",
	}, "\n")}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-format", "names"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "alpha\nbeta" {
		t.Errorf("stdout = %q, want bare names \"alpha\\nbeta\"", got)
	}
}

func TestRunListNamesEmpty(t *testing.T) {
	r := &fakeRunner{listOutput: "no server running on /tmp/tmux-1000/default"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "-format", "names"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty output", stdout.String())
	}
}

func TestRunListUnknownFormat(t *testing.T) {
	r := &fakeRunner{listOutput: "$1|2|0|1000|alpha"}

	var stdout, stderr bytes.Buffer
	// An unknown -format is a usage error, so it exits 2 like any other bad
	// flag rather than 1.
	code := run([]string{"list", "-format", "yaml"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown -format") {
		t.Errorf("stderr = %q, want an unknown-format error", stderr.String())
	}
}

func TestRunDiscoverFallsBackToDefault(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run(nil, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if attachCalled == "" {
		t.Error("attach was not called; want the default config's session to be created and attached")
	}
}

func TestRunDiscoverBuildsDefaultDespiteUnrelatedSessions(t *testing.T) {
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	// A session is running, but for a different folder: the default session
	// for this folder should still be built and attached directly, not
	// routed through the interactive picker.
	r := &fakeRunner{listOutput: "$9|some-other-project"}
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run(nil, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session") {
		t.Errorf("stdout = %q, want a new session to be created for this folder", stdout.String())
	}
	if attachCalled == "" {
		t.Error("attach was not called; want the default config's session to be created and attached")
	}
}

func TestRunDiscoverFallsBackToUserDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	chdir(t, t.TempDir())

	dir := filepath.Join(home, ".config", "wyrm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "[session]\nname = \"user-default\"\nroot = \".\"\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(dir, "default.wyrm.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	code := run(nil, &stdout, &stderr, r, func() bool { return false }, func(string) error { return nil })
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session user-default") {
		t.Errorf("stdout = %q, want the user default config's session name", stdout.String())
	}
}

// TestRunnerFromSettings guards main's use of [tmux].socket/command to build
// the process-wide runner — the one construction site that shapes every tmux
// invocation for the run, attach included.
func TestRunnerFromSettings(t *testing.T) {
	settings := &config.Settings{Tmux: config.Tmux{Socket: "mysocket", Command: "/opt/tmux/bin/tmux"}}
	r := runnerFromSettings(settings)
	if r.SocketName != "mysocket" {
		t.Errorf("SocketName = %q, want %q", r.SocketName, "mysocket")
	}
	if r.Command != "/opt/tmux/bin/tmux" {
		t.Errorf("Command = %q, want %q", r.Command, "/opt/tmux/bin/tmux")
	}
}

func TestRunnerFromSettingsEnvOverride(t *testing.T) {
	t.Setenv("WYRM_TMUX_SOCKET", "env-socket")
	t.Setenv("WYRM_TMUX_COMMAND", "env-tmux")

	r := runnerFromSettings(&config.Settings{Tmux: config.Tmux{Socket: "file-socket", Command: "file-tmux"}})
	if r.SocketName != "env-socket" {
		t.Errorf("SocketName = %q, want env override %q", r.SocketName, "env-socket")
	}
	if r.Command != "env-tmux" {
		t.Errorf("Command = %q, want env override %q", r.Command, "env-tmux")
	}
}

func TestRunCreateAndAttach(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session proj") {
		t.Errorf("stdout = %q, want mention of created session", stdout.String())
	}
	if attachCalled != "$1" {
		t.Errorf("attach called with %q, want the session's tmux ID $1", attachCalled)
	}
}

// TestRunUpDetachSkipsAttach guards `wyrm up -d`: the session must still be
// built for real (unlike -n), but attach must never be called.
func TestRunUpDetachSkipsAttach(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	attachCalled := false
	attach := func(string) error { attachCalled = true; return nil }

	code := run([]string{"up", "-config", path, "-d"}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session proj") {
		t.Errorf("stdout = %q, want mention of created session", stdout.String())
	}
	if attachCalled {
		t.Error("wyrm up -d attached; want it to skip attaching")
	}
	if !strings.Contains(joinCalls(r), "new-session") {
		t.Errorf("wyrm up -d did not build the session:\n%s", joinCalls(r))
	}
}

func TestRunCreateErrorPropagates(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{fail: map[string]bool{"new-session": true}}

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "wyrm:") {
		t.Errorf("stderr = %q, want a wyrm: prefixed error", stderr.String())
	}
}

func TestRunAttachError(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	attach := func(_ string) error { return errors.New("boom") }

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "attaching to session") {
		t.Errorf("stderr = %q, want attach failure message", stderr.String())
	}
}

func TestRunInsideTmuxSwitchesClient(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	found := false
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "switch-client" {
			found = true
			if len(c) < 3 || c[1] != "-t" || c[2] != "$1" {
				t.Errorf("switch-client args = %v, want -t targeting the session's tmux ID $1", c)
			}
		}
	}
	if !found {
		t.Error("switch-client was not called when inside tmux")
	}
}

// TestRunInsideTmuxSwitchesClientNameWithDot guards against the bug where a
// session name containing "." (e.g. "wyrm.vim") got misparsed by tmux's -t
// target syntax, which treats "." as the window.pane separator: switching
// or attaching must always target the session by its tmux ID, never by the
// raw (possibly dotted) name, regardless of what the session is called.
func TestRunInsideTmuxSwitchesClientNameWithDot(t *testing.T) {
	path := writeConfig(t, `
[session]
name = "wyrm.vim"
root = "/tmp/proj"
[[windows]]
name = "w"
`)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	found := false
	for _, c := range r.calls {
		for i, arg := range c {
			if i > 0 && c[i-1] == "-t" && arg == "wyrm.vim" {
				t.Errorf("call %v targets the raw dotted name instead of a session ID", c)
			}
		}
		if len(c) > 0 && c[0] == "switch-client" {
			found = true
			if len(c) < 3 || c[2] != "$1" {
				t.Errorf("switch-client args = %v, want -t targeting the session's tmux ID $1", c)
			}
		}
	}
	if !found {
		t.Error("switch-client was not called when inside tmux")
	}
}

func TestRunInsideTmuxSwitchClientError(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{fail: map[string]bool{"switch-client": true}}

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "switching to session") {
		t.Errorf("stderr = %q, want switch failure message", stderr.String())
	}
}

func TestRunAlreadyRunningAttaches(t *testing.T) {
	path := writeConfig(t, validConfig)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{listOutput: "$5|proj"}
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run([]string{"-config", path}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already running, attaching") {
		t.Errorf("stdout = %q, want already-running message", stdout.String())
	}
	if attachCalled != "$5" {
		t.Errorf("attach called with %q, want the existing session's tmux ID $5", attachCalled)
	}
}

func TestRunAttachByNameFound(t *testing.T) {
	r := &fakeRunner{listOutput: "$5|myproj"}
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	var stdout, stderr bytes.Buffer
	code := run([]string{"myproj"}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if attachCalled != "$5" {
		t.Errorf("attach called with %q, want the session's tmux ID $5", attachCalled)
	}
}

// TestRunAttachByAlias covers session.aliases end to end: `wyrm <alias>`
// resolves to the project declaring it, the same way an exact project name
// already does — with no fuzzy matching involved.
func TestRunAttachByAlias(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	content := "[session]\nname = \"dotfiles\"\nroot = \".\"\naliases = [\"dot\"]\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(dir, config.DefaultFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run([]string{"dot"}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session dotfiles") {
		t.Errorf("stdout = %q, want the aliased project's session to be built", stdout.String())
	}
	if attachCalled == "" {
		t.Error("attach was not called after resolving the alias")
	}
}

// TestRunAttachByWildcardName covers a [[wildcard]] end to end: `wyrm
// <matched-directory-basename>` resolves through config.FindProject to the
// wildcard's template, building a session rooted at the matched directory
// rather than at whatever the template's own (normally unset) session.root
// says.
func TestRunAttachByWildcardName(t *testing.T) {
	settingsPath, err := config.SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(settingsPath) })

	base := t.TempDir()
	matched := filepath.Join(base, "widget")
	if err := os.MkdirAll(matched, 0o755); err != nil {
		t.Fatal(err)
	}
	// root = "." is a placeholder to satisfy config.Load's "name or root
	// required" validation; startProject overrides it with the matched
	// directory regardless of what's here — see config.DiscoverWildcardProjects.
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[session]\nroot = \".\"\n\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settingsBody := "[[wildcard]]\npattern = \"" + filepath.Join(base, "*") + "\"\nconfig = \"" + template + "\"\n"
	if err := os.WriteFile(settingsPath, []byte(settingsBody), 0o644); err != nil {
		t.Fatal(err)
	}

	chdir(t, t.TempDir()) // standing well outside the matched directory

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	attachCalled := ""
	attach := func(name string) error { attachCalled = name; return nil }

	code := run([]string{"widget"}, &stdout, &stderr, r, func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session widget") {
		t.Errorf("stdout = %q, want the wildcard-matched project's session to be built", stdout.String())
	}
	if attachCalled == "" {
		t.Error("attach was not called after resolving the wildcard match")
	}
	var newSessionRoot string
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "new-session" {
			for i, a := range c {
				if a == "-c" && i+1 < len(c) {
					newSessionRoot = c[i+1]
				}
			}
		}
	}
	if newSessionRoot != matched {
		t.Errorf("new-session -c = %q, want the matched directory %q", newSessionRoot, matched)
	}
}

func TestRunAttachByNameInsideTmux(t *testing.T) {
	r := &fakeRunner{listOutput: "$5|myproj"}

	var stdout, stderr bytes.Buffer
	code := run([]string{"myproj"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	found := false
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "switch-client" {
			found = true
			if len(c) < 3 || c[2] != "$5" {
				t.Errorf("switch-client args = %v, want -t $5", c)
			}
		}
	}
	if !found {
		t.Error("switch-client was not called when inside tmux")
	}
}

func TestRunAttachByNameNotFound(t *testing.T) {
	r := &fakeRunner{}

	var stdout, stderr bytes.Buffer
	code := run([]string{"nonexistent"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `no running session or known project named "nonexistent"`) {
		t.Errorf("stderr = %q, want a not-found message", stderr.String())
	}
}

func TestRunAttachByNameTypoHint(t *testing.T) {
	r := &fakeRunner{}

	var stdout, stderr bytes.Buffer
	code := run([]string{"klil"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `no running session or known project named "klil"`) {
		t.Errorf("stderr = %q, want a not-found message", stderr.String())
	}
	if !strings.Contains(stderr.String(), `did you mean the subcommand "kill"?`) {
		t.Errorf("stderr = %q, want a did-you-mean hint for %q", stderr.String(), "kill")
	}
}

func TestRunAttachByNameNoHintForUnrelatedName(t *testing.T) {
	r := &fakeRunner{}

	var stdout, stderr bytes.Buffer
	code := run([]string{"my-project-session"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), "did you mean") {
		t.Errorf("stderr = %q, want no did-you-mean hint for an unrelated name", stderr.String())
	}
}

func TestRunPickNoSessions(t *testing.T) {
	// With no running sessions, picker.Run returns ("", nil) without ever
	// touching a real tty, so this is safe and deterministic to test.
	var stdout, stderr bytes.Buffer
	code := run([]string{"pick"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "no running tmux sessions") {
		t.Errorf("stderr = %q, want a no-sessions message", stderr.String())
	}
}

func TestRunPickRejectsExtraArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"pick", "-bogus"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestRunListConfigsLocal(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile(config.DefaultFileName, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list-configs"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != config.DefaultFileName {
		t.Errorf("stdout = %q, want %q", got, config.DefaultFileName)
	}
}

func TestRunListConfigsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	chdir(t, t.TempDir())

	sharedDir := filepath.Join(home, ".config", "wyrm", "settings")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedConfig := filepath.Join(sharedDir, "otherproj"+config.DefaultFileName)
	if err := os.WriteFile(sharedConfig, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list-configs"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != sharedConfig {
		t.Errorf("stdout = %q, want %q", got, sharedConfig)
	}
}

func TestRunListConfigsNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	chdir(t, t.TempDir())

	var stdout, stderr bytes.Buffer
	code := run([]string{"list-configs"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty output", stdout.String())
	}
}

func TestRunMigrateConfig(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		projectDir := t.TempDir()
		chdir(t, projectDir)
		if err := os.WriteFile(".wyrm.toml", []byte(validConfig), 0o644); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		code := run([]string{"migrate-config"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}

		want := filepath.Join(home, ".config", "wyrm", "settings", filepath.Base(projectDir)+".wyrm.toml")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected migrated file at %s: %v", want, err)
		}
		if _, err := os.Stat(".wyrm.toml"); !os.IsNotExist(err) {
			t.Errorf("local .wyrm.toml still present after migration")
		}
		if !strings.Contains(stdout.String(), "moved") {
			t.Errorf("stdout = %q, want mention of moved file", stdout.String())
		}
		if !strings.Contains(stdout.String(), "note:") {
			t.Errorf("stdout = %q, want a note about enabling shared storage", stdout.String())
		}
	})

	// TestRunMigrateConfig/relative_root_is_canonicalized is the regression
	// test for a config storage location becoming project-root provenance:
	// root = "." resolves against the directory the config file lives in, so
	// moving it into the shared directory used to silently reroot the
	// session it builds at the shared directory instead of the project.
	t.Run("relative root is canonicalized", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		projectDir := t.TempDir()
		chdir(t, projectDir)
		content := "[session]\nname = \"proj\"\nroot = \".\"\n\n[[windows]]\nname = \"w\"\n"
		if err := os.WriteFile(".wyrm.toml", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		code := run([]string{"migrate-config"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "rewritten to") {
			t.Errorf("stdout = %q, want a note about the rewritten root", stdout.String())
		}

		want := filepath.Join(home, ".config", "wyrm", "settings", filepath.Base(projectDir)+".wyrm.toml")
		cfg, err := config.Load(want)
		if err != nil {
			t.Fatalf("Load(%s): %v", want, err)
		}
		if cfg.Session.Root == "." || cfg.Session.Root == "" {
			t.Fatalf("session.root = %q, want it canonicalized to the project's absolute path", cfg.Session.Root)
		}
		wantRoot, err := filepath.EvalSymlinks(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		if gotRoot, err := filepath.EvalSymlinks(cfg.Session.Root); err != nil || gotRoot != wantRoot {
			t.Errorf("session.root = %q, want the project directory %q", cfg.Session.Root, wantRoot)
		}
	})

	// TestRunMigrateConfig/a_relative_root_other_than_.__preserves_its_subpath
	// is the regression test for canonicalizing every relative root to cwd
	// unconditionally: root = "backend" names a subdirectory of the project,
	// not the project root itself, so the rewritten absolute value has to be
	// cwd joined with "backend" — collapsing it to bare cwd silently
	// rerooted the session at the wrong directory.
	t.Run("a relative root other than . preserves its subpath", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		projectDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(projectDir, "backend"), 0o755); err != nil {
			t.Fatal(err)
		}
		chdir(t, projectDir)
		content := "[session]\nname = \"proj\"\nroot = \"backend\"\n\n[[windows]]\nname = \"w\"\n"
		if err := os.WriteFile(".wyrm.toml", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		code := run([]string{"migrate-config"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}

		want := filepath.Join(home, ".config", "wyrm", "settings", filepath.Base(projectDir)+".wyrm.toml")
		cfg, err := config.Load(want)
		if err != nil {
			t.Fatalf("Load(%s): %v", want, err)
		}
		wantRoot, err := filepath.EvalSymlinks(filepath.Join(projectDir, "backend"))
		if err != nil {
			t.Fatal(err)
		}
		if gotRoot, err := filepath.EvalSymlinks(cfg.Session.Root); err != nil || gotRoot != wantRoot {
			t.Errorf("session.root = %q, want the backend subdirectory %q (not just the project root)", cfg.Session.Root, wantRoot)
		}
	})

	t.Run("no local config", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		chdir(t, t.TempDir())

		var stdout, stderr bytes.Buffer
		code := run([]string{"migrate-config"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "no local config") {
			t.Errorf("stderr = %q, want no-local-config message", stderr.String())
		}
	})

	t.Run("destination already exists", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		projectDir := t.TempDir()
		chdir(t, projectDir)
		if err := os.WriteFile(".wyrm.toml", []byte(validConfig), 0o644); err != nil {
			t.Fatal(err)
		}

		sharedDir := filepath.Join(home, ".config", "wyrm", "settings")
		if err := os.MkdirAll(sharedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(sharedDir, filepath.Base(projectDir)+".wyrm.toml")
		if err := os.WriteFile(dst, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		code := run([]string{"migrate-config"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "already exists") {
			t.Errorf("stderr = %q, want already-exists message", stderr.String())
		}
	})
}

func TestRunKill(t *testing.T) {
	path := writeConfig(t, validConfig)

	t.Run("success", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		r := &fakeRunner{listOutput: "$5|proj"}
		code := run([]string{"kill", "-config", path}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "killed session proj") {
			t.Errorf("stdout = %q, want killed session message", stdout.String())
		}
	})

	t.Run("not running", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		r := &fakeRunner{}
		code := run([]string{"kill", "-config", path}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "wyrm:") {
			t.Errorf("stderr = %q, want a wyrm: prefixed error", stderr.String())
		}
	})
}

// TestRunKillByNamePrefersExactRunningSession is the regression test for
// alias precedence: killByName used to resolve a configured project or alias
// before checking for an exact live tmux session, so a project's alias
// colliding with an unrelated running session's exact name made `wyrm kill
// <name>` destroy the wrong session. attachByName already resolved in the
// safe order (exact session first); killByName now matches it.
func TestRunKillByNamePrefersExactRunningSession(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	// Project "alpha" aliases "beta" — but there's also a real, unrelated
	// running session actually named "beta".
	content := "[session]\nname = \"alpha\"\nroot = \".\"\naliases = [\"beta\"]\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(dir, config.DefaultFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{listOutput: "$1|beta"}
	var stdout, stderr bytes.Buffer
	code := run([]string{"kill", "beta"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "killed session beta") {
		t.Errorf("stdout = %q, want the exact-named session killed", stdout.String())
	}
	if strings.Contains(stdout.String(), "alpha") || strings.Contains(stderr.String(), "alpha") {
		t.Errorf("output = %q / %q, resolved the alias instead of the exact running session", stdout.String(), stderr.String())
	}
	killed := false
	for _, c := range r.calls {
		if len(c) >= 3 && c[0] == "kill-session" && c[2] == "$1" {
			killed = true
		}
	}
	if !killed {
		t.Errorf("no kill-session for $1; calls = %v", r.calls)
	}
}

func TestRunUpWithVarFlag(t *testing.T) {
	templ := `
[session]
name = "proj-{{.port}}"
root = "/tmp/proj"

[[windows]]
name = "win-{{.branch}}"
`
	path := writeConfig(t, templ)

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	code := run([]string{"up", "-config", path, "-n", "--var", "port=8080", "--var", "branch=feat"},
		&stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "proj-8080") {
		t.Errorf("expected interpolated session name proj-8080, got:\n%s", out)
	}
	if !strings.Contains(out, "win-feat") {
		t.Errorf("expected interpolated window name win-feat, got:\n%s", out)
	}
}

func TestRunValidateWithVarFlag(t *testing.T) {
	templ := `
[session]
name = "proj-{{.env}}"
root = "/tmp/proj"

[[windows]]
name = "main"
`
	path := writeConfig(t, templ)

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate", "-config", path, "--var", "env=prod"},
		&stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "config valid") {
		t.Errorf("expected config valid in stdout, got %q", stdout.String())
	}
}

func TestRunKillAllFlagYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: strings.Join([]string{
			"$1|1|0|1000|alpha",
			"$2|1|0|1000|beta",
		}, "\n"),
	}
	code := run([]string{"kill", "-all", "-y"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "killed session alpha") || !strings.Contains(out, "killed session beta") {
		t.Errorf("stdout = %q, want killed messages for alpha and beta", out)
	}
}

func TestRunKillAllConfirmed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: "$1|1|0|1000|alpha",
	}
	code := runWith(runOptions{stdin: strings.NewReader("y\n")},
		[]string{"kill", "--all"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "killed session alpha") {
		t.Errorf("stdout = %q, want killed session alpha", stdout.String())
	}
}

func TestRunKillAllAborted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: "$1|1|0|1000|alpha",
	}
	code := runWith(runOptions{stdin: strings.NewReader("n\n")},
		[]string{"kill", "--all"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aborted") {
		t.Errorf("stdout = %q, want aborted message", stdout.String())
	}
}

func TestRunKillAllDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: "$1|1|0|1000|alpha",
	}
	code := run([]string{"kill", "--all", "-n"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tmux kill-session -t $1") {
		t.Errorf("stdout = %q, want tmux kill-session command", stdout.String())
	}
}

func TestRunKillAllNoSessions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	code := run([]string{"kill", "--all"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no active sessions to kill") {
		t.Errorf("stdout = %q, want no active sessions message", stdout.String())
	}
}

func TestRunKillAllIncompatibleFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &fakeRunner{}
	code := run([]string{"kill", "--all", "-config", "foo.toml"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	stderr.Reset()
	code = run([]string{"kill", "--all", "somesession"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestRunRestartAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	settingsDir := filepath.Join(home, ".config", "wyrm")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedDir := filepath.Join(settingsDir, "settings")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgContent := "[session]\nname = 'alpha'\n[[windows]]\nname = 'w'\n"
	if err := os.WriteFile(filepath.Join(sharedDir, "alpha.wyrm.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: "$1|1|0|1000|alpha",
	}
	code := run([]string{"restart", "--all", "-y"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "killed session alpha") || !strings.Contains(out, "created session alpha") {
		t.Errorf("stdout = %q, want killed and created messages for alpha", out)
	}
}

func TestRunRestartAllMatchesInterpolatedName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	shared := filepath.Join(home, ".config", "wyrm", "settings")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "[session]\nname = 'api-$ENV'\n[[windows]]\nname = 'w'\n"
	if err := os.WriteFile(filepath.Join(shared, "api.wyrm.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{listOutput: "$1|1|0|1000|api-dev"}
	var stdout, stderr bytes.Buffer
	code := run([]string{"restart", "-all", "-y", "-var", "ENV=dev"}, &stdout, &stderr, r,
		func() bool { return false }, nil)
	if code != 0 || !strings.Contains(stdout.String(), "created session api-dev") {
		t.Fatalf("restart = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunRestartAllDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	settingsDir := filepath.Join(home, ".config", "wyrm")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedDir := filepath.Join(settingsDir, "settings")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgContent := "[session]\nname = 'alpha'\n[[windows]]\nname = 'w'\n"
	if err := os.WriteFile(filepath.Join(sharedDir, "alpha.wyrm.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := &fakeRunner{
		listOutput: "$1|1|0|1000|alpha",
	}
	code := run([]string{"restart", "--all", "-n"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Restarting session alpha") {
		t.Errorf("stdout = %q, want Restarting session alpha header", stdout.String())
	}
}

// setupWildcardProject writes a [[wildcard]] template covering base/<name> and
// returns the matched directory. The template's own root = "." is a
// placeholder — the matched directory is the project's real root.
func setupWildcardProject(t *testing.T, name string) string {
	t.Helper()
	settingsPath, err := config.SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(settingsPath) })

	base := t.TempDir()
	matched := filepath.Join(base, name)
	if err := os.MkdirAll(matched, 0o755); err != nil {
		t.Fatal(err)
	}
	// The template deliberately lives in a directory with a different
	// basename: loading it by path names the session after *this* directory,
	// which is the bug being guarded against.
	tmplDir := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(tmplDir, "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[session]\nroot = \".\"\n\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "[[wildcard]]\npattern = \"" + filepath.Join(base, "*") + "\"\nconfig = \"" + template + "\"\n"
	if err := os.WriteFile(settingsPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return matched
}

// Every teardown path has to resolve a wildcard project the same way the start
// path does. Three of them loaded the template by path and lost the matched
// directory, so the session name came from the template's own folder:
// `wyrm kill widget` reported `session "templates" is not running`, and
// `wyrm restart -all` built a spurious session called "templates" rooted in the
// config directory while leaving the real one untouched.
func TestWildcardProjectTeardownUsesTheMatchedDirectory(t *testing.T) {
	t.Run("kill by name", func(t *testing.T) {
		setupWildcardProject(t, "widget")
		chdir(t, t.TempDir())

		r := &fakeRunner{listOutput: "$1|widget"}
		var stdout, stderr bytes.Buffer
		code := run([]string{"kill", "widget"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "killed session widget") {
			t.Errorf("stdout = %q, want the matched project's session killed", stdout.String())
		}
		if strings.Contains(stderr.String(), "templates") {
			t.Errorf("stderr = %q, resolved the template's directory instead of the project", stderr.String())
		}
		killed := false
		for _, c := range r.calls {
			if len(c) >= 3 && c[0] == "kill-session" && c[2] == "$1" {
				killed = true
			}
		}
		if !killed {
			t.Errorf("no kill-session for $1; calls = %v", r.calls)
		}
	})

	t.Run("restart -all", func(t *testing.T) {
		setupWildcardProject(t, "widget")
		chdir(t, t.TempDir())

		r := &fakeRunner{listOutput: "$1|widget"}
		var stdout, stderr bytes.Buffer
		code := run([]string{"restart", "-all", "-y"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "created session templates") {
			t.Fatalf("restart -all built a session named after the template directory: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "killed session widget") {
			t.Errorf("stdout = %q, want the real session killed", stdout.String())
		}
		if !strings.Contains(stdout.String(), "created session widget") {
			t.Errorf("stdout = %q, want the real session rebuilt", stdout.String())
		}
	})
}

// -interval 0 reached time.NewTicker and panicked, so a typo was a stack
// trace. It is a bad flag value like any other: exit 2.
func TestStatusRejectsNonPositiveInterval(t *testing.T) {
	for _, arg := range []string{"0", "-1s"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"status", "-w", "-interval", arg}, &stdout, &stderr, &fakeRunner{},
			func() bool { return false }, nil)
		if code != 2 {
			t.Errorf("-interval %s: exit code = %d, want 2", arg, code)
		}
		if !strings.Contains(stderr.String(), "-interval must be positive") {
			t.Errorf("-interval %s: stderr = %q, want an explanation", arg, stderr.String())
		}
	}
}

// There was no spelling of `wyrm send` that could type text starting with "-":
// tmux read it as more send-keys flags.
func TestSendLeadingDashText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args, want []string
	}{
		{"bare", []string{"send", "sess", "-n hello"}, []string{"send-keys", "-t", "$1", "-l", "--", "-n hello"}},
		{"explicit terminator", []string{"send", "sess", "--", "-n", "hello"}, []string{"send-keys", "-t", "$1", "-l", "--", "-n hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{listOutput: "$1|sess"}
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr, r, func() bool { return false }, nil)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			found := false
			for _, c := range r.calls {
				if strings.Join(c, " ") == strings.Join(tc.want, " ") {
					found = true
				}
			}
			if !found {
				t.Errorf("want call %v, got %v", tc.want, r.calls)
			}
		})
	}
}
