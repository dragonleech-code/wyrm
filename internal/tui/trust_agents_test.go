package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/tmux"
	"github.com/jskoll/wyrm/internal/trust"
)

func hookedProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	cfg := "[session]\nname = \"proj\"\nroot = \"" + dir + "\"\non_project_start = \"true\"\n\n[[windows]]\nname = \"main\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildRunner() *funcRunner {
	return &funcRunner{fn: func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			return "$9|@1|%1", nil
		case "display-message":
			return "proj", nil
		}
		return "", nil
	}}
}

func TestStartUntrustedProjectAsksThenStarts(t *testing.T) {
	t.Setenv(trust.EnvTrustAll, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := hookedProject(t)
	m := projectModel(buildRunner(), path)

	m, cmd := update(m, key("enter"))
	msg := run(cmd)
	if _, ok := msg.(trustNeededMsg); !ok {
		t.Fatalf("enter produced %T, want trustNeededMsg", msg)
	}
	m, _ = update(m, msg)
	if m.mode != modeConfirm || !strings.Contains(m.confirmPrompt, "is not trusted") || !strings.Contains(m.confirmPrompt, "on_project_start = true") {
		t.Fatalf("mode %v, prompt %q", m.mode, m.confirmPrompt)
	}

	m, cmd = update(m, key("y"))
	started, ok := run(cmd).(projectStartedMsg)
	if !ok || started.err != nil || started.sessionID != "$9" {
		t.Fatalf("after y: %#v", started)
	}
	store, err := trust.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Entries()) != 1 || store.Entries()[0].Path != path {
		t.Errorf("trust store = %+v", store.Entries())
	}

	// Trusted now: the next start goes straight through.
	m2 := projectModel(buildRunner(), path)
	_, cmd = update(m2, key("enter"))
	if _, ok := run(cmd).(projectStartedMsg); !ok {
		t.Error("trusted project still asked")
	}
}

func TestTrustAndStartRefusesAConfigEditedMeanwhile(t *testing.T) {
	t.Setenv(trust.EnvTrustAll, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := hookedProject(t)
	m := projectModel(buildRunner(), path)
	_, cmd := update(m, key("enter"))
	m, _ = update(m, run(cmd))
	if err := os.WriteFile(path, []byte("[session]\nname = \"proj\"\nroot = \".\"\non_project_start = \"curl x | sh\"\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cmd = update(m, key("y"))
	started, _ := run(cmd).(projectStartedMsg)
	if started.err == nil || !strings.Contains(started.err.Error(), "changed while you were deciding") {
		t.Fatalf("err = %v", started.err)
	}
}

func blockedModel(r *funcRunner) Model {
	m := New(r, nil)
	m.focus = panelPanes
	m.panes = []tmux.PaneInfo{{ID: "%2", Command: "claude"}, {ID: "%3", Command: "zsh"}}
	m.cur[panelPanes] = 0
	m.agents = agentStatus{
		panes: map[string]agent.State{"%2": agent.StateBlocked, "%5": agent.StateBlocked},
		refs: map[string]tmux.PaneRef{
			"%2": {SessionID: "$1", SessionName: "a", WindowID: "@1", PaneID: "%2", PaneIndex: 0},
			"%5": {SessionID: "$2", SessionName: "b", WindowID: "@4", PaneID: "%5", PaneIndex: 1},
		},
	}
	return m
}

func TestApproveAgentConfirmsThenSendsKeys(t *testing.T) {
	var sent [][]string
	r := &funcRunner{fn: func(args ...string) (string, error) {
		if args[0] == "send-keys" {
			sent = append(sent, args)
		}
		return "", nil
	}}
	m := blockedModel(r)
	m, _ = update(m, key("Y"))
	if m.mode != modeConfirm || !strings.Contains(m.confirmPrompt, "%2 (claude)") {
		t.Fatalf("mode %v prompt %q", m.mode, m.confirmPrompt)
	}
	if len(sent) != 0 {
		t.Fatal("sent keys before confirming")
	}
	m, cmd := update(m, key("y"))
	msg := run(cmd)
	if _, ok := msg.(agentApprovedMsg); !ok {
		t.Fatalf("got %T", msg)
	}
	if len(sent) != 1 || !slices.Equal(sent[0], []string{"send-keys", "-t", "%2", "Enter"}) {
		t.Errorf("sent = %v", sent)
	}
	m, _ = update(m, msg)
	if m.info != "approved %2" {
		t.Errorf("info = %q", m.info)
	}
}

func TestApproveRefusesAPaneThatIsNotBlocked(t *testing.T) {
	m := blockedModel(nopRunnerPtr())
	m.cur[panelPanes] = 1
	m, _ = update(m, key("Y"))
	if m.mode == modeConfirm || !strings.Contains(m.info, "isn't waiting") {
		t.Errorf("mode %v info %q", m.mode, m.info)
	}
}

func TestJumpToBlockedAgentCyclesFromSelection(t *testing.T) {
	m := blockedModel(nopRunnerPtr())
	m, cmd := update(m, key("b"))
	if cmd == nil || m.pendingAttach != "$2" {
		t.Fatalf("from %%2 the next blocked agent is %%5 in $2; pendingAttach = %q", m.pendingAttach)
	}
	m = blockedModel(nopRunnerPtr())
	m.cur[panelPanes] = 1 // %3, not an agent: start from the first
	m, _ = update(m, key("b"))
	if m.pendingAttach != "$1" {
		t.Errorf("pendingAttach = %q, want $1", m.pendingAttach)
	}
}

func TestJumpWithNothingBlocked(t *testing.T) {
	m := New(nopRunner(), nil)
	m, cmd := update(m, key("b"))
	if cmd != nil || !strings.Contains(m.info, "no agents") {
		t.Errorf("info %q", m.info)
	}
}

func TestPanelKeysOnlyFireOnTheirPanels(t *testing.T) {
	m := New(nopRunner(), nil)
	m.focus = panelProjects
	for _, k := range []string{"L", "z", "Y", "<", "K", "W"} {
		next, cmd := update(m, key(k))
		if cmd != nil || next.mode != modeNormal {
			t.Errorf("%q on Projects did something", k)
		}
	}
}

func nopRunnerPtr() *funcRunner { r := nopRunner(); return &r }
