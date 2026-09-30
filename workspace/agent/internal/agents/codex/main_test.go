package codex

import (
	"os"
	"testing"
)

// The workspace running these tests may have a real shared app-server up and advertised
// (AF_CODEX_APP_SERVER_ADDR); a Terminal launch probes it (release.go). No test may reach it:
// the ones that need a server start a fake and set the address themselves.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(appServerAddrEnv)
	os.Exit(m.Run())
}
