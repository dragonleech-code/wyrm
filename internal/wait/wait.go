// Package wait implements readiness checks: a TCP port accepting connections,
// a file existing, or an HTTP endpoint answering. It backs a split's wait_for
// key, which gates the pane's command on `wyrm wait`, and has no dependency on
// the rest of wyrm so internal/config can validate specs with it.
package wait

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultTimeout is how long `wyrm wait` polls when no timeout is given.
const DefaultTimeout = 2 * time.Minute

// pollInterval is the delay between readiness probes.
const pollInterval = 250 * time.Millisecond

// Kind names what a Target checks.
type Kind string

// The supported target kinds.
const (
	TCP  Kind = "tcp"
	File Kind = "file"
	HTTP Kind = "http"
)

// Target is one parsed readiness spec.
type Target struct {
	Kind Kind
	// Addr is host:port for TCP, a path for File, and the full URL for HTTP.
	Addr string
}

func (t Target) String() string {
	if t.Kind == HTTP {
		return t.Addr
	}
	return string(t.Kind) + ":" + t.Addr
}

// Parse reads a spec: "tcp:HOST:PORT", "file:PATH", or an http:// or https://
// URL.
//
// A single quote is rejected outright. The spec is embedded in a command line
// typed into the pane's shell, wrapped in single quotes, and POSIX shells and
// fish disagree about escaping a quote inside them — refusing it keeps the
// generated command identical in every shell.
func Parse(spec string) (Target, error) {
	if strings.ContainsAny(spec, "'\n\r") {
		return Target{}, fmt.Errorf("wait_for %q: must not contain quotes or newlines", spec)
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Target{}, errors.New("empty wait_for spec")
	}
	if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
		return Target{Kind: HTTP, Addr: spec}, nil
	}
	kind, rest, ok := strings.Cut(spec, ":")
	if !ok || rest == "" {
		return Target{}, fmt.Errorf("wait_for %q: want tcp:HOST:PORT, file:PATH, or an http(s) URL", spec)
	}
	switch Kind(kind) {
	case TCP:
		host, port, err := net.SplitHostPort(rest)
		if err != nil || port == "" {
			return Target{}, fmt.Errorf("wait_for %q: want tcp:HOST:PORT", spec)
		}
		if host == "" {
			rest = net.JoinHostPort("localhost", port)
		}
		return Target{Kind: TCP, Addr: rest}, nil
	case File:
		return Target{Kind: File, Addr: rest}, nil
	}
	return Target{}, fmt.Errorf("wait_for %q: unknown kind %q (want tcp, file, http, https)", spec, kind)
}

// Ready reports whether t is ready right now.
func (t Target) Ready(ctx context.Context) bool {
	switch t.Kind {
	case TCP:
		d := net.Dialer{Timeout: time.Second}
		conn, err := d.DialContext(ctx, "tcp", t.Addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	case File:
		_, err := os.Stat(t.Addr)
		return err == nil
	case HTTP:
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.Addr, nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode < 400
	}
	return false
}

// For blocks until every target is ready, or returns an error naming the ones
// that were not once ctx ends.
func For(ctx context.Context, targets []Target) error {
	pending := append([]Target(nil), targets...)
	for {
		remaining := pending[:0]
		for _, t := range pending {
			if !t.Ready(ctx) {
				remaining = append(remaining, t)
			}
		}
		pending = remaining
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			names := make([]string, len(pending))
			for i, t := range pending {
				names[i] = t.String()
			}
			return fmt.Errorf("timed out waiting for %s", strings.Join(names, ", "))
		case <-time.After(pollInterval):
		}
	}
}
