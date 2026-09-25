package sessions

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jskoll/wyrm/internal/tmux"
)

// stubRunner returns canned output/err for whatever it's given and records the
// calls it received.
type stubRunner struct {
	out   string
	err   error
	calls [][]string
}

func (s *stubRunner) Run(args ...string) (string, error) {
	s.calls = append(s.calls, args)
	return s.out, s.err
}

func TestListParses(t *testing.T) {
	// Fields are id|windows|attached|activity|name; beta is the most recently
	// active, so it must sort first. "weird|name" exercises a name containing
	// the delimiter (it's the last field, so SplitN keeps it whole).
	r := &stubRunner{out: strings.Join([]string{
		"$1|3|1|1000|alpha",
		"$2|1|0|2000|beta",
		"$3|1|0|500|weird|name",
	}, "\n")}

	got, err := List(r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	// Ordered by activity descending: beta(2000), alpha(1000), weird|name(500).
	if got[0].Name != "beta" || got[1].Name != "alpha" || got[2].Name != "weird|name" {
		t.Fatalf("wrong order: %q, %q, %q", got[0].Name, got[1].Name, got[2].Name)
	}
	if got[1].Windows != 3 || !got[1].Attached {
		t.Errorf("alpha parsed wrong: %+v", got[1])
	}
	if got[0].Attached {
		t.Errorf("beta should be unattached: %+v", got[0])
	}
	if got[0].ID != "$2" || got[1].ID != "$1" || got[2].ID != "$3" {
		t.Errorf("wrong IDs: beta=%q alpha=%q weird|name=%q", got[0].ID, got[1].ID, got[2].ID)
	}
	if len(r.calls) != 1 || r.calls[0][0] != "list-sessions" {
		t.Fatalf("unexpected tmux call: %v", r.calls)
	}
}

// Malformed records must fail rather than silently omitting live sessions.
func TestListRejectsMalformedRows(t *testing.T) {
	r := &stubRunner{out: strings.Join([]string{
		"$1|1|0|100|good",
		"notanid|1|0|200|bogus",
		"|1|0|300|empty",
		"$2|1|0|400|alsogood",
	}, "\n")}
	if _, err := List(r); err == nil {
		t.Fatal("List accepted a malformed session ID")
	}
}

func TestListNoServer(t *testing.T) {
	r := &stubRunner{out: "no server running on /tmp/tmux-1000/default", err: errors.New("exit status 1")}
	got, err := List(r)
	if err != nil {
		t.Fatalf("no-server should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func TestListNoServerViaSentinel(t *testing.T) {
	r := &stubRunner{out: "", err: tmux.ErrNoServer}
	got, err := List(r)
	if err != nil {
		t.Fatalf("no-server should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func TestListRealError(t *testing.T) {
	r := &stubRunner{out: "something else broke", err: errors.New("exit status 1")}
	if _, err := List(r); err == nil {
		t.Fatal("expected error for non-'no server' failure")
	}
}

func TestKillByID(t *testing.T) {
	r := &stubRunner{}
	if err := Kill(r, "$3"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	want := []string{"kill-session", "-t", "$3"}
	if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0], want) {
		t.Fatalf("got %v, want %v", r.calls, want)
	}
}

func TestKillReportsFailure(t *testing.T) {
	r := &stubRunner{out: "can't find session", err: errors.New("exit status 1")}
	if err := Kill(r, "$3"); err == nil {
		t.Fatal("expected an error when tmux refuses the kill")
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		query, target string
		want          bool
	}{
		{"", "anything", true},
		{"dev", "dev-api", true},
		{"dev", "d-e-v", true},
		{"api", "dev-api", true},
		{"xyz", "dev-api", false},
		{"DEV", "dev-api", true}, // case-insensitive
		{"deva", "dev", false},   // query longer than any subsequence
	}
	for _, tt := range tests {
		_, ok := FuzzyMatch(tt.query, tt.target)
		if ok != tt.want {
			t.Errorf("FuzzyMatch(%q, %q) ok=%v, want %v", tt.query, tt.target, ok, tt.want)
		}
	}
}

func TestFuzzyMatchRanksContiguousAndBoundary(t *testing.T) {
	contig, _ := FuzzyMatch("dev", "dev-api")
	scattered, _ := FuzzyMatch("dev", "d-e-v")
	if contig <= scattered {
		t.Errorf("contiguous %d should outscore scattered %d", contig, scattered)
	}
	boundary, _ := FuzzyMatch("api", "dev-api")  // matches at word start
	midword, _ := FuzzyMatch("api", "devapisrv") // matches mid-word
	if boundary <= midword {
		t.Errorf("boundary %d should outscore mid-word %d", boundary, midword)
	}
}

// FormatRow pads by display width, not rune count: a CJK or emoji name is twice
// as wide on screen as it is long in runes, which used to push the window count
// out of alignment on every row.
func TestFormatRowAlignsByDisplayWidth(t *testing.T) {
	wide := FormatRow(Session{Name: "日本語セッション", Windows: 2})
	ascii := FormatRow(Session{Name: "plain-session", Windows: 2})
	if wideCol, asciiCol := countColumn(t, wide), countColumn(t, ascii); wideCol != asciiCol {
		t.Errorf("window count starts at column %d for a wide name and %d for an ascii one", wideCol, asciiCol)
	}
}

// countColumn returns the display column the window count starts at. Byte
// offset would not do: the whole point is that a CJK name occupies more bytes
// and more columns than its rune count suggests.
func countColumn(t *testing.T, row string) int {
	t.Helper()
	i := strings.Index(row, "2 windows")
	if i < 0 {
		t.Fatalf("row has no window count: %q", row)
	}
	return ansi.StringWidth(row[:i])
}

// A name too long for the column is truncated with an ellipsis rather than
// running over the count and the attached marker.
func TestFormatRowTruncatesLongNames(t *testing.T) {
	row := FormatRow(Session{Name: strings.Repeat("verylongname", 5), Windows: 1})
	if !strings.Contains(row, "…") {
		t.Errorf("row = %q, want an ellipsis marking the truncation", row)
	}
	if !strings.Contains(row, "1 window") {
		t.Errorf("row = %q, want the window count still present", row)
	}
}

func TestFormatRowSingularAndAttached(t *testing.T) {
	one := FormatRow(Session{Name: "s", Windows: 1})
	if !strings.Contains(one, "1 window") || strings.Contains(one, "1 windows") {
		t.Errorf("row = %q, want the singular unit", one)
	}
	if strings.Contains(one, "attached") {
		t.Errorf("row = %q, want no attached marker", one)
	}
	att := FormatRow(Session{Name: "s", Windows: 2, Attached: true})
	if !strings.Contains(att, "2 windows") || !strings.Contains(att, "(attached)") {
		t.Errorf("row = %q, want the plural unit and the attached marker", att)
	}
}

// names renders a session list for a failure message.
func names(s []Session) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = v.Name
	}
	return out
}
