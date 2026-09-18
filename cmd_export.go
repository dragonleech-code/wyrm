package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jskoll/wyrm/internal/sessions"
)

// exportDoc is the JSON document `wyrm export` writes: the running sessions
// plus enough context to tell two exports apart.
type exportDoc struct {
	TmuxVersion string             `json:"tmux_version"`
	ExportedAt  time.Time          `json:"exported_at"`
	Current     string             `json:"current,omitempty"`
	Sessions    []sessions.Session `json:"sessions"`
}

// export writes the running sessions as JSON, to stdout or to -o.
func (a *app) export(args []string) error {
	fs := a.newFlagSet("export")
	outPath := fs.String("o", "", "write to this file instead of stdout")
	limit := fs.Int("n", 0, "only the N most recently active sessions (0 = all)")
	current := fs.Bool("current", false, "record which session is attached")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErrf("export takes no arguments, got %q", fs.Arg(0))
	}
	if *limit < 0 {
		return usageErrf("-n must be 0 or more, got %d", *limit)
	}

	list, err := sessions.List(a.runner)
	if err != nil {
		return err
	}
	if *limit > 0 {
		var recent []sessions.Session
		for i := 0; i <= *limit && i < len(list); i++ {
			recent = append(recent, list[i])
		}
		list = recent
	}

	doc := exportDoc{
		TmuxVersion: tmuxVersion(),
		ExportedAt:  time.Now().UTC(),
		Sessions:    list,
	}
	if *current {
		doc.Current = attachedSession(list).Name
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding sessions: %w", err)
	}
	data = append(data, '\n')

	if *outPath == "" {
		_, err := a.stdout.Write(data)
		return err
	}
	return writeExport(*outPath, data)
}

func writeExport(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if strings.HasSuffix(path, ".gz") {
		return fmt.Errorf("%s: compressed export is not supported", path)
	}
	f.Write(data)
	return f.Close()
}

// attachedSession returns the first attached session in list.
func attachedSession(list []sessions.Session) *sessions.Session {
	for i := range list {
		if list[i].Attached {
			return &list[i]
		}
	}
	return nil
}

// tmuxVersion reports the tmux version, e.g. "3.4".
func tmuxVersion() string {
	out, _ := exec.Command("tmux", "-V").Output()
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "tmux ")
}
