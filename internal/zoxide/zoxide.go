// Package zoxide integrates with the optional zoxide binary for
// frecency-based directory discovery: jumping to a directory you've cd'd
// into often, whether or not it has a wyrm config of its own.
//
// This is wyrm's only dependency on a binary other than tmux, which is
// worth being deliberate about: every function here assumes the caller has
// already checked Available, and the feature that uses this package
// (internal/tui's Projects panel) is opt-in via settings and silently
// absent otherwise — never a hard requirement, never on by default just
// because zoxide happens to be installed.
package zoxide

import (
	"fmt"
	"github.com/jskoll/wyrm/internal/process"
	"os/exec"
	"strconv"
	"strings"
)

// Entry is one directory zoxide knows about, with its frecency score
// (higher means more frecently used).
type Entry struct {
	Path  string
	Score float64
}

// Available reports whether the zoxide binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("zoxide")
	return err == nil
}

// Query returns the directories zoxide knows about, most-frecent first,
// trimmed to at most limit entries (limit <= 0 means no trimming).
func Query(limit int) ([]Entry, error) {
	cmd, cancel := process.Command("zoxide", "query", "--list", "--score")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("zoxide query: %w", err)
	}
	entries := parseEntries(string(out))
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// parseEntries reads zoxide's "--score" output: one "<score> <path>" pair
// per line, the score right-padded to a fixed width. SplitN at the first
// space (after trimming the padding) is what keeps a path containing spaces
// intact — Fields would instead break it apart.
func parseEntries(out string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		score, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			continue
		}
		path := strings.TrimSpace(parts[1])
		if path == "" {
			continue
		}
		entries = append(entries, Entry{Path: path, Score: score})
	}
	return entries
}

// Add records path in zoxide's database, as if the user had cd'd into it —
// used for wyrm's optional "track" mode. Best-effort: a caller that can't
// afford to fail a session build over zoxide's own bookkeeping should
// ignore the error rather than propagate it.
func Add(path string) error {
	cmd, cancel := process.Command("zoxide", "add", path)
	defer cancel()
	return cmd.Run()
}
