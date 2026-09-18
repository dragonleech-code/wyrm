package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/tmux"
)

func reviewServer(t *testing.T) tmux.Exec {
	t.Helper()
	if testing.Short() {
		t.Skip("requires tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	r := tmux.Exec{SocketName: fmt.Sprintf("wyrm-review-%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "-"))}
	cmd := exec.Command("tmux", "-L", r.SocketName, "-f", "/dev/null", "new-session", "-d", "-s", "keeper", "-x", "160", "-y", "60", "/bin/sh")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated server: %v %s", err, out)
	}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })
	if _, err := r.Run("set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	return r
}

func reviewCreate(t *testing.T, r tmux.Exec, cfg *config.Config) string {
	t.Helper()
	var warnings bytes.Buffer
	_, id, _, err := session.Create(r, cfg, io.Discard, &warnings)
	if err != nil {
		t.Fatalf("Create: %v; warnings=%s", err, warnings.String())
	}
	if warnings.Len() > 0 {
		t.Log(warnings.String())
	}
	return id
}

func reviewWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReviewPaneTitlesEveryWindow(t *testing.T) {
	r := reviewServer(t)
	yes := true
	id := reviewCreate(t, r, &config.Config{Session: config.Session{Name: "titles", Root: t.TempDir(), EnablePaneTitles: &yes}, Windows: []config.Window{{Name: "one"}, {Name: "two"}}})
	wins, err := tmux.ListWindows(r, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wins {
		out, err := r.Run("show-options", "-wv", "-t", w.ID, "pane-border-status")
		if err != nil || out != "top" {
			t.Errorf("window %s status=%q err=%v", w.Name, out, err)
		}
	}
}

func TestReviewNestedRootAndRun(t *testing.T) {
	r := reviewServer(t)
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	yes := true
	cfg := &config.Config{Session: config.Session{Name: "nested", Root: root}, Windows: []config.Window{{Name: "one", Splits: []config.Split{{Children: []config.Split{{Root: "child", Env: map[string]string{"NESTED": "leaf"}, Run: `printf '%s\n%s\n' "$NESTED" "$PWD" > "$PWD/result"`, RemainOnExit: &yes}, {Type: "v"}}}, {Type: "h"}}}}}
	id := reviewCreate(t, r, cfg)
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := r.Run("list-panes", "-t", id, "-F", "#{pane_id}|#{pane_dead}")
		if err != nil {
			t.Fatal(err)
		}
		first := strings.Split(out, "\n")[0]
		if strings.HasSuffix(first, "|1") {
			captured, err := os.ReadFile(filepath.Join(child, "result"))
			if err != nil || string(captured) != "leaf\n"+child+"\n" {
				t.Fatalf("nested root/env/run: %q, %v", captured, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nested run never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReviewRemainOnExitFastProcess(t *testing.T) {
	r := reviewServer(t)
	yes := true
	id := reviewCreate(t, r, &config.Config{Session: config.Session{Name: "fast", Root: t.TempDir()}, Windows: []config.Window{{Name: "one", RemainOnExit: &yes, Splits: []config.Split{{Run: "true", RemainOnExit: &yes}}}}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := r.Run("list-panes", "-t", id, "-F", "#{pane_dead}")
		if err != nil {
			t.Fatal(err)
		}
		if out == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process did not exit: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReviewSynchronizedStartup(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(fmt.Sprint(inherited), func(t *testing.T) {
			r := reviewServer(t)
			yes := true
			var sync *bool
			if inherited {
				if _, err := r.Run("set-window-option", "-g", "synchronize-panes", "on"); err != nil {
					t.Fatal(err)
				}
			} else {
				sync = &yes
			}
			id := reviewCreate(t, r, &config.Config{Session: config.Session{Name: "sync", Root: t.TempDir()}, Windows: []config.Window{{Name: "one", Synchronize: sync, Splits: []config.Split{{Command: "printf 'FIRST\\n'"}, {Type: "h", Command: "printf 'SECOND\\n'"}}}}})
			wins, err := tmux.ListWindows(r, id)
			if err != nil {
				t.Fatal(err)
			}
			panes, err := tmux.ListPanes(r, wins[0].ID)
			if err != nil || len(panes) != 2 {
				t.Fatalf("panes=%v err=%v", panes, err)
			}
			for i, p := range panes {
				want, unwanted := "FIRST", "SECOND"
				if i == 1 {
					want, unwanted = unwanted, want
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					out, err := tmux.CapturePane(r, p.ID)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(out, unwanted) {
						t.Fatalf("pane %s got another pane's startup: %q", p.ID, out)
					}
					if strings.Contains(out, want) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("missing startup %s: %q", want, out)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if out, err := r.Run("show-options", "-w", "-A", "-v", "-t", wins[0].ID, "synchronize-panes"); err != nil || out != "on" {
				t.Fatalf("sync not restored: %q %v", out, err)
			}
			if inherited {
				// Restored by unsetting, so the window still follows the global
				// setting instead of carrying its own copy of it.
				if out, err := r.Run("show-options", "-w", "-v", "-t", wins[0].ID, "synchronize-panes"); err != nil || out != "" {
					t.Fatalf("inherited sync pinned onto the window: %q %v", out, err)
				}
				if _, err := r.Run("set-window-option", "-g", "synchronize-panes", "off"); err != nil {
					t.Fatal(err)
				}
				if out, err := r.Run("show-options", "-w", "-A", "-v", "-t", wins[0].ID, "synchronize-panes"); err != nil || out != "off" {
					t.Fatalf("global change did not reach the window: %q %v", out, err)
				}
			}
		})
	}
}

func TestReviewRestartInvalidRootPreservesSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), ".wyrm.toml")
	t.Setenv("WYRM_REVIEW_MISSING_ROOT", "")
	reviewWrite(t, path, "[session]\nname='review'\nroot='.'\n[[windows]]\nname='one'\nroot='${WYRM_REVIEW_UNDEFINED_PATH}'\n")
	r := &fakeRunner{listOutput: "$1|review"}
	var out, errs bytes.Buffer
	code := run([]string{"restart", "-d", "-config", path}, &out, &errs, r, func() bool { return false }, func(string) error { return nil })
	if code == 0 {
		t.Fatal("invalid replacement accepted")
	}
	for _, call := range r.calls {
		if call[0] == "kill-session" {
			t.Fatal("destroyed existing session before root validation")
		}
	}
}

func TestReviewMigrationPreservesOwnerAndRoot(t *testing.T) {
	for _, rootValue := range []string{".", "backend"} {
		t.Run(rootValue, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			child := filepath.Join(root, "child")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "backend"), 0700); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(root, ".wyrm.toml")
			reviewWrite(t, src, "['session'] # project\nroot='"+rootValue+"'\n[[windows]]\nname='one'\n")
			settingsPath, _ := config.SettingsPath()
			reviewWrite(t, settingsPath, "storage='shared'\n")
			t.Chdir(child)
			var out, errs bytes.Buffer
			a := &app{stdout: &out, stderr: &errs}
			if err := a.migrateConfig(nil); err != nil {
				t.Fatal(err)
			}
			settings, _ := config.LoadSettings()
			dst, _ := settings.SharedConfigPath(root)
			cfg, err := config.Load(dst)
			if err != nil {
				t.Fatal(err)
			}
			_, actual, err := cfg.Session.Resolve(cfg.Dir())
			if err != nil || actual != filepath.Join(root, rootValue) {
				t.Fatalf("root=%q err=%v", actual, err)
			}
			if owner, known := config.SharedConfigOwner(dst); !known || owner != root {
				t.Fatalf("owner=%q known=%v", owner, known)
			}
			if _, err := os.Stat(src); !os.IsNotExist(err) {
				t.Fatalf("migration source remains: %v", err)
			}
			t.Chdir(root)
			resolved, _, err := config.ResolveEffective(settings, "")
			if err != nil || resolved.Session.Root != actual {
				t.Fatalf("migrated config undiscoverable: %v %v", resolved, err)
			}
		})
	}
}

func TestReviewNumericWindowName(t *testing.T) {
	r := &fakeRunner{listOutput: "$1|review", listWindowsOutput: "0|@0|1|layout|first\n1|@1|0|layout|0"}
	id, err := resolveSendTarget(r, "review:0")
	if err != nil || id != "@1" {
		t.Fatalf("got %q err=%v, want exact name @1", id, err)
	}
}

func TestReviewBulkKillFailureExit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	r := &fakeRunner{listOutput: "$1|review", fail: map[string]bool{"kill-session": true}}
	var out, errs bytes.Buffer
	code := run([]string{"kill", "-all", "-y"}, &out, &errs, r, func() bool { return false }, func(string) error { return nil })
	if code == 0 {
		t.Fatalf("exit 0 despite failing kill: %s", errs.String())
	}
}

func TestReviewDryRunAttachHook(t *testing.T) {
	path := writeConfig(t, "[session]\nname='review'\non_project_attach='echo attachment'\n[[windows]]\nname='one'\n")
	for _, verb := range []string{"up", "restart"} {
		for _, detached := range []bool{false, true} {
			args := []string{verb, "-n", "-config", path}
			if detached {
				args = append(args, "-d")
			}
			var out, errs bytes.Buffer
			code := run(args, &out, &errs, &fakeRunner{}, func() bool { return false }, func(string) error { return nil })
			if code != 0 {
				t.Fatalf("%v: %s", args, errs.String())
			}
			if got := strings.Contains(out.String(), "echo attachment"); got == detached {
				t.Fatalf("attach dry run detached=%v: %s", detached, out.String())
			}
		}
	}
}

func TestReviewLiveSessionDoesNotRunAliasHook(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	t.Chdir(root)
	reviewWrite(t, filepath.Join(root, ".wyrm.toml"), "[session]\nname='owner'\naliases=['live']\non_project_attach='touch wrong-hook'\n[[windows]]\nname='one'\n")
	r := &fakeRunner{listOutput: "$1|live", lastSessionName: "live"}
	var out, errs bytes.Buffer
	if code := run([]string{"live"}, &out, &errs, r, func() bool { return false }, func(string) error { return nil }); code != 0 {
		t.Fatal(errs.String())
	}
	if _, err := os.Stat(filepath.Join(root, "wrong-hook")); !os.IsNotExist(err) {
		t.Fatalf("unrelated hook ran: %v", err)
	}
}

func TestReviewBulkRestartFailureAndNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	t.Chdir(root)
	reviewWrite(t, filepath.Join(root, ".wyrm.toml"), "[session]\nname='review'\naliases=['alias']\n[[windows]]\nname='one'\n")
	var out, errs bytes.Buffer
	r := &fakeRunner{listOutput: "$1|review", fail: map[string]bool{"kill-session": true}}
	if code := run([]string{"restart", "-all", "-y"}, &out, &errs, r, func() bool { return false }, func(string) error { return nil }); code == 0 {
		t.Fatal("bulk restart suppressed failure")
	}
	out.Reset()
	if code := run([]string{"list-configs", "-names"}, &out, &errs, &fakeRunner{}, func() bool { return false }, func(string) error { return nil }); code != 0 {
		t.Fatal(errs.String())
	}
	if out.String() != "review\nalias\n" {
		t.Fatalf("names=%q", out.String())
	}
}

func TestReviewDiagnosticsAllowBrokenSettings(t *testing.T) {
	for _, cmd := range []string{"doctor", "help", "version", "--help", "--version"} {
		if !settingsOptional([]string{cmd}) {
			t.Errorf("%s requires settings", cmd)
		}
	}
	if settingsOptional([]string{"up"}) {
		t.Fatal("up must not ignore invalid settings")
	}
}
