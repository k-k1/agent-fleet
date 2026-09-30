package main

import (
	"os"
	"testing"
)

// The workspace running these tests may have a real shared codex app-server up and advertised
// (AF_CODEX_APP_SERVER_ADDR), and a codex Terminal launch probes it (codex/release.go). No test
// may reach it: the ones that need a server start a fake and set the address themselves.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("AF_CODEX_APP_SERVER_ADDR")
	os.Exit(m.Run())
}
