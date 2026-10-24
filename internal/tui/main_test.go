package tui

import (
	"os"
	"testing"

	"github.com/jskoll/wyrm/internal/trust"
)

// TestMain keeps every test away from the developer's own ~/.config/wyrm —
// starting a project writes lifecycle history and, with trust on, the trust
// store — and turns the trust prompt off except where a test asks for it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wyrm-tui-test-config")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv(trust.EnvTrustAll, "1")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
