package wait

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		spec string
		want Target
	}{
		{"tcp:localhost:5432", Target{TCP, "localhost:5432"}},
		{"tcp::8080", Target{TCP, "localhost:8080"}},
		{"file:/tmp/ready", Target{File, "/tmp/ready"}},
		{"http://localhost:3000/health", Target{HTTP, "http://localhost:3000/health"}},
		{"https://example.test", Target{HTTP, "https://example.test"}},
	}
	for _, c := range cases {
		got, err := Parse(c.spec)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.spec, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.spec, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{"", "tcp:nope", "udp:x:1", "file:", "file:it's", "tcp:a:1\n"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", spec)
		}
	}
}

func TestReadyTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	target := Target{TCP, addr}
	if !target.Ready(context.Background()) {
		t.Error("listening port not ready")
	}
	_ = ln.Close()
	if target.Ready(context.Background()) {
		t.Error("closed port reported ready")
	}
}

func TestReadyHTTP(t *testing.T) {
	status := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	target := Target{HTTP, srv.URL}
	if target.Ready(context.Background()) {
		t.Error("503 reported ready")
	}
	status = http.StatusOK
	if !target.Ready(context.Background()) {
		t.Error("200 not ready")
	}
}

func TestForFileBecomesReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(path, nil, 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := For(ctx, []Target{{File, path}}); err != nil {
		t.Fatal(err)
	}
}

func TestForTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := For(ctx, []Target{{File, filepath.Join(t.TempDir(), "never")}})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for file:") {
		t.Fatalf("err = %v", err)
	}
}
