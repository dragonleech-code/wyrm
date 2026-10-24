package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dispatchVerbs parses main.go's run() switch statement and returns the set
// of canonical (non-flag) subcommand names it dispatches on — "version" and
// "help" included, their "-v"/"-h"-style aliases excluded. This is the single
// source of truth the other surfaces (the usage string, and the three shell
// completion scripts) are checked against below, so adding or renaming a
// subcommand in one place without the others fails a test instead of just
// going stale.
func dispatchVerbs(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var sw *ast.SwitchStmt
	ast.Inspect(file, func(n ast.Node) bool {
		// runWith holds the dispatch switch; run is the thin wrapper that
		// supplies default options.
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "runWith" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if s, ok := n.(*ast.SwitchStmt); ok && sw == nil {
					sw = s
					return false
				}
				return true
			})
			return false
		}
		return true
	})
	if sw == nil {
		t.Fatal("could not find switch statement in runWith()")
	}

	verbs := map[string]bool{}
	for _, clause := range sw.Body.List {
		cc, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range cc.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val := strings.Trim(lit.Value, `"`)
			if val == "" || strings.HasPrefix(val, "-") {
				continue
			}
			verbs[val] = true
		}
	}
	return verbs
}

func setOf(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		if it == "" {
			continue
		}
		s[it] = true
	}
	return s
}

func diffSets(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	for v := range want {
		if !got[v] {
			t.Errorf("%s: missing subcommand %q", label, v)
		}
	}
	for v := range got {
		if !want[v] {
			t.Errorf("%s: has unexpected subcommand %q", label, v)
		}
	}
}

// TestSubcommandsListedInUsage checks that every verb run() dispatches on is
// documented in the top-level usage string (Bad #6: the subcommand set is
// duplicated by hand across main.go, usage, and the completions).
func TestSubcommandsListedInUsage(t *testing.T) {
	verbs := dispatchVerbs(t)
	for v := range verbs {
		re := regexp.MustCompile(`(?m)^  wyrm ` + regexp.QuoteMeta(v) + `\b`)
		if !re.MatchString(usage) {
			t.Errorf("usage string does not document subcommand %q", v)
		}
	}
}

// TestSubcommandsMatchBashCompletion checks completions/wyrm.bash's static
// subcommand list against runWith()'s actual dispatch set.
func TestSubcommandsMatchBashCompletion(t *testing.T) {
	data, err := os.ReadFile("completions/wyrm.bash")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`local subcommands="([^"]+)"`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatal("could not find `local subcommands=\"...\"` in completions/wyrm.bash")
	}
	diffSets(t, "completions/wyrm.bash", setOf(strings.Fields(string(m[1]))), dispatchVerbs(t))
}

// TestSubcommandsMatchFishCompletion checks completions/wyrm.fish's static
// subcommand list against runWith()'s actual dispatch set.
func TestSubcommandsMatchFishCompletion(t *testing.T) {
	data, err := os.ReadFile("completions/wyrm.fish")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^set -l subcommands (.+)$`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatal("could not find `set -l subcommands ...` in completions/wyrm.fish")
	}
	diffSets(t, "completions/wyrm.fish", setOf(strings.Fields(string(m[1]))), dispatchVerbs(t))
}

// TestSubcommandsMatchZshCompletion checks completions/_wyrm's subcommands
// array (one 'name:description' entry per line) against run()'s actual
// dispatch set.
func TestSubcommandsMatchZshCompletion(t *testing.T) {
	data, err := os.ReadFile("completions/_wyrm")
	if err != nil {
		t.Fatal(err)
	}
	blockRe := regexp.MustCompile(`(?s)subcommands=\((.*?)\n\s*\)`)
	block := blockRe.FindSubmatch(data)
	if block == nil {
		t.Fatal("could not find `subcommands=( ... )` array in completions/_wyrm")
	}
	entryRe := regexp.MustCompile(`'([a-z-]+):`)
	entries := entryRe.FindAllSubmatch(block[1], -1)
	if entries == nil {
		t.Fatal("could not find any 'name:description' entries in completions/_wyrm's subcommands array")
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = string(e[1])
	}
	diffSets(t, "completions/_wyrm", setOf(names), dispatchVerbs(t))
}

// TestKnownSubcommandsMatchDispatch checks knownSubcommands (used only to
// power runAttachByName's "did you mean" hint) against run()'s actual
// dispatch set, so the hint list can't quietly drift from reality.
func TestKnownSubcommandsMatchDispatch(t *testing.T) {
	diffSets(t, "knownSubcommands", setOf(knownSubcommands), dispatchVerbs(t))
}

// TestDispatchVerbsSanityCheck guards the parser above itself: if it ever
// silently found zero verbs (e.g. run()'s switch got restructured in a way
// dispatchVerbs can't follow), every other test in this file would vacuously
// pass instead of catching a real drift.
func TestDispatchVerbsSanityCheck(t *testing.T) {
	verbs := dispatchVerbs(t)
	if len(verbs) < 10 {
		t.Fatalf("dispatchVerbs found only %d verbs (%v), want at least 10 — the switch parser may be broken", len(verbs), sortedKeys(verbs))
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestSubcommandsDocumentedInManPage checks that man/wyrm.1 has an entry for
// every verb run() dispatches on. Packages install it as the offline
// reference, so a verb missing there is missing for anyone without the docs
// site.
func TestSubcommandsDocumentedInManPage(t *testing.T) {
	data, err := os.ReadFile("man/wyrm.1")
	if err != nil {
		t.Fatal(err)
	}
	// An entry is a .B/.BI line naming the verb; troff escapes the hyphen in
	// "setup\-tmux", so undo that before matching.
	text := strings.ReplaceAll(string(data), `\-`, "-")
	for v := range dispatchVerbs(t) {
		re := regexp.MustCompile(`(?m)^\.BI? "?wyrm ` + regexp.QuoteMeta(v) + `\b`)
		if !re.MatchString(text) {
			t.Errorf("man/wyrm.1 has no entry for subcommand %q", v)
		}
	}
}
