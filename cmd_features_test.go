package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jskoll/wyrm/internal/config"
)

// recordingStatusRunner is statusFakeRunner plus a record of what was sent,
// for the agent verbs, which act on what status finds.
type recordingStatusRunner struct {
	statusFakeRunner
	calls [][]string
}

func (r *recordingStatusRunner) Run(args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return r.statusFakeRunner.Run(args...)
}

func (r *recordingStatusRunner) sent(verb string) [][]string {
	var out [][]string
	for _, c := range r.calls {
		if c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

const twoBlockedPanes = "$1\x01api\x01@1\x011\x01code\x01%1\x011\x01claude\n" +
	"$2\x01web\x01@2\x011\x01ui\x01%2\x011\x01claude"

func blockedAgents() *recordingStatusRunner {
	return &recordingStatusRunner{statusFakeRunner: statusFakeRunner{
		panes:   twoBlockedPanes,
		capture: map[string]string{"%1": "x\nenter to confirm", "%2": "x\nenter to confirm"},
	}}
}

func TestAgentApproveRequiresATargetWhenSeveralAreBlocked(t *testing.T) {
	r := blockedAgents()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"agent", "approve"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "2 agents are blocked; name one") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if len(r.sent("send-keys")) != 0 {
		t.Error("sent keys without a target")
	}

	stderr.Reset()
	if code := run([]string{"agent", "approve", "web"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if got := r.sent("send-keys"); len(got) != 1 || !slices.Equal(got[0], []string{"send-keys", "-t", "%2", "Enter"}) {
		t.Errorf("send-keys = %v", got)
	}
}

func TestAgentNextCyclesFromCurrentPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "%1")
	r := blockedAgents()
	var stdout, stderr bytes.Buffer
	code := run([]string{"agent", "next"}, &stdout, &stderr, r, func() bool { return true }, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if got := r.sent("select-pane"); len(got) != 1 || got[0][2] != "%2" {
		t.Errorf("select-pane = %v", got)
	}
	if got := r.sent("switch-client"); len(got) != 1 || got[0][2] != "$2" {
		t.Errorf("switch-client = %v", got)
	}
}

func TestAgentRejectsUnknownAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"agent", "dance"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 2 {
		t.Errorf("exit %d", code)
	}
}

func TestStatusJSONLIsOneLine(t *testing.T) {
	r := blockedAgents()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "-format", "jsonl"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := strings.TrimSuffix(stdout.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("jsonl spans lines: %q", out)
	}
	var report agentStatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Summary.Blocked != 2 {
		t.Errorf("report = %+v, err %v", report, err)
	}
}

func TestWaitSucceedsAndTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "-timeout", "5s", "tcp:" + ln.Addr().String()}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 0 {
		t.Fatalf("ready port: exit %d, stderr %q", code, stderr.String())
	}
	missing := filepath.Join(t.TempDir(), "never")
	stderr.Reset()
	if code := run([]string{"wait", "-timeout", "100ms", "file:" + missing}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 1 {
		t.Fatalf("timeout: exit %d", code)
	}
	if !strings.Contains(stderr.String(), "timed out waiting for file:") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if code := run([]string{"wait", "udp:x:1"}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 2 {
		t.Errorf("bad spec: exit %d", code)
	}
}

const diffConfig = `
[session]
name = "proj"
root = "/tmp/proj"
[[windows]]
name = "code"
[[windows]]
name = "logs"
`

func TestDiffReportsDriftAndExitsOne(t *testing.T) {
	path := writeConfig(t, diffConfig)
	r := &fakeRunner{
		listOutput:        "$1|proj",
		listWindowsOutput: "1|@1|1|layout|code",
		listPanesOutput:   map[string]string{"@1": "%1|0|1|zsh|/tmp/proj"},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"diff", "-config", path}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `+ window "logs" is in the config but not running`) {
		t.Errorf("stdout = %q", stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"diff", "-config", path, "-format", "json"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 1 {
		t.Fatalf("json: exit %d", code)
	}
	var out struct {
		Session string
		Drift   []map[string]any
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || out.Session != "proj" || len(out.Drift) != 1 {
		t.Errorf("json = %s (err %v)", stdout.String(), err)
	}
}

func TestDiffCleanExitsZero(t *testing.T) {
	path := writeConfig(t, "[session]\nname = \"proj\"\nroot = \"/tmp/proj\"\n[[windows]]\nname = \"code\"\n")
	r := &fakeRunner{
		listOutput:        "$1|proj",
		listWindowsOutput: "1|@1|1|layout|code",
		listPanesOutput:   map[string]string{"@1": "%1|0|1|zsh|/tmp/proj"},
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"diff", "-config", path}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d, stdout %q", code, stdout.String())
	}
}

func TestUpReconcileAddsMissingWindow(t *testing.T) {
	path := writeConfig(t, diffConfig)
	r := &fakeRunner{listOutput: "$1|proj", listWindowsOutput: "1|@1|1|layout|code"}
	var stdout, stderr bytes.Buffer
	code := run([]string{"up", "-reconcile", "-d", "-config", path}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "added window logs to session proj") {
		t.Errorf("stdout = %q", stdout.String())
	}
	for _, c := range r.calls {
		if c[0] == "new-session" {
			t.Error("reconcile rebuilt the session")
		}
	}
}

func TestUpReconcileRejectsDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"up", "-reconcile", "-n"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 2 {
		t.Errorf("exit %d", code)
	}
}

func writeSettings(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "wyrm", "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wyrm", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeShared(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wyrm", "settings", name+".wyrm.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStartsEveryProjectAndAttachesFirst(t *testing.T) {
	writeSettings(t, "[[workspace]]\nname = \"backend\"\nprojects = [\"api\", \"worker\"]\n")
	writeShared(t, "api", "[session]\nname = \"api\"\nroot = \"/tmp\"\n[[windows]]\nname = \"w\"\n")
	writeShared(t, "worker", "[session]\nname = \"worker\"\nroot = \"/tmp\"\n[[windows]]\nname = \"w\"\n")
	chdir(t, t.TempDir())

	r := &fakeRunner{}
	var attached string
	var stdout, stderr bytes.Buffer
	code := run([]string{"@backend"}, &stdout, &stderr, r, func() bool { return false }, func(id string) error { attached = id; return nil })
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if newSessions(r) != 2 || attached != "$1" {
		t.Errorf("sessions built %d, attached %q; stdout %q", newSessions(r), attached, stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"list-configs", "-names"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 || !strings.Contains(stdout.String(), "@backend") {
		t.Errorf("list-configs -names = %q", stdout.String())
	}
}

func TestWorkspaceWithUnknownMemberStartsNothing(t *testing.T) {
	writeSettings(t, "[[workspace]]\nname = \"backend\"\nprojects = [\"api\", \"nope\"]\n")
	writeShared(t, "api", "[session]\nname = \"api\"\nroot = \"/tmp\"\n[[windows]]\nname = \"w\"\n")
	chdir(t, t.TempDir())
	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"@backend"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), `no known project named "nope"`) || newSessions(r) != 0 {
		t.Errorf("stderr %q, sessions %d", stderr.String(), newSessions(r))
	}
}

func TestKillWorkspace(t *testing.T) {
	writeSettings(t, "[[workspace]]\nname = \"backend\"\nprojects = [\"api\", \"worker\"]\n")
	writeShared(t, "api", "[session]\nname = \"api\"\nroot = \"/tmp\"\n[[windows]]\nname = \"w\"\n")
	writeShared(t, "worker", "[session]\nname = \"worker\"\nroot = \"/tmp\"\n[[windows]]\nname = \"w\"\n")
	chdir(t, t.TempDir())
	r := &fakeRunner{listOutput: "$1|api"}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"kill", "@backend"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "killed session api\n" {
		t.Errorf("stdout = %q (worker isn't running and should be skipped quietly)", stdout.String())
	}
}

func TestSchemaMatchesCommittedFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"schema"}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	committed, err := os.ReadFile("schema/wyrm.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(committed) {
		t.Error("schema/wyrm.schema.json is stale; regenerate it with `go run . schema > schema/wyrm.schema.json`")
	}
	var doc map[string]any
	if err := json.Unmarshal(committed, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	for _, key := range []string{"extends", "session", "windows"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema has no %q", key)
		}
	}
}

func TestValidateExplain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".wyrm.toml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate", "-explain"}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	var chosen []string
	for _, l := range lines {
		if strings.HasPrefix(l, "->") {
			chosen = append(chosen, l)
		}
	}
	if len(chosen) != 1 || !strings.Contains(chosen[0], "local config") {
		t.Errorf("chosen = %q\n%s", chosen, stdout.String())
	}
}

// fakeSSH is a remoteRunner that records instead of reaching a host.
type fakeSSH struct {
	fakeRunner
	home     string
	hooks    []string
	attached string
}

func (f *fakeSSH) Home() (string, error) { return f.home, nil }
func (f *fakeSSH) Attach(target string) error {
	f.attached = target
	return nil
}

func (f *fakeSSH) RunShell(command, dir string, _ []string, _ time.Duration, _ io.Writer) error {
	f.hooks = append(f.hooks, dir+": "+command)
	return nil
}

func TestUpHostBuildsRemotelyWithRebasedRoot(t *testing.T) {
	fake := &fakeSSH{home: "/home/remote"}
	orig := newSSHRunner
	newSSHRunner = func(string, *config.Settings) remoteRunner { return fake }
	t.Cleanup(func() { newSSHRunner = orig })

	path := writeConfig(t, "[session]\nname = \"api\"\nroot = \"~/src/api\"\non_project_start = \"make deps\"\n[[windows]]\nname = \"w\"\n")
	local := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"up", "-host", "devbox", "-config", path}, &stdout, &stderr, local, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if len(local.calls) != 0 {
		t.Errorf("local tmux was used: %v", local.calls)
	}
	joined := ""
	for _, c := range fake.calls {
		joined += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(joined, "new-session -d -P -F #{session_id}|#{window_id}|#{pane_id} -s api -n w -c /home/remote/src/api") {
		t.Errorf("remote calls:\n%s", joined)
	}
	if !slices.Equal(fake.hooks, []string{"/home/remote/src/api: make deps"}) {
		t.Errorf("hooks = %v", fake.hooks)
	}
	if fake.attached != "$1" {
		t.Errorf("attached = %q", fake.attached)
	}
}

func TestUpHostRejectsRelativeRoot(t *testing.T) {
	fake := &fakeSSH{home: "/home/remote"}
	orig := newSSHRunner
	newSSHRunner = func(string, *config.Settings) remoteRunner { return fake }
	t.Cleanup(func() { newSSHRunner = orig })
	path := writeConfig(t, "[session]\nname = \"api\"\nroot = \".\"\n[[windows]]\nname = \"w\"\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"up", "-host", "devbox", "-config", path}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "a remote build needs an absolute or ~/ root") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDeprecatedPlaceholderWarns(t *testing.T) {
	path := writeConfig(t, "[session]\nname = \"proj\"\nroot = \"/tmp\"\non_project_start = \"echo ${who}\"\n[[windows]]\nname = \"w\"\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"up", "-n", "-config", path, "-var", "who=me"}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "placeholder ${name} is deprecated") || !strings.Contains(stdout.String(), "echo me") {
		t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}
