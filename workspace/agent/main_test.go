package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// The workspace running these tests may have a real shared codex app-server up and advertised
// (AF_CODEX_APP_SERVER_ADDR), and a codex Terminal launch probes it (codex/release.go). No test
// may reach it: the ones that need a server start a fake and set the address themselves.
//
// The branch-name resolver starts an AI one-shot for a non-ASCII title, and the AI-assist
// toggle defaults to on: a test that resolves such a title would otherwise start a real CLI.
// The tests that exercise the English slug install their own fake.
func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() {
		_ = os.Unsetenv("AF_CODEX_APP_SERVER_ADDR")
		// A workspace that runs these tests may itself carry AF_CP_INTERNAL_URL, which would send
		// the tests' requests past their fake CP (cpurl.Request).
		_ = os.Unsetenv("AF_CP_INTERNAL_URL")
		englishSlugOneShot = func(context.Context, string) (string, error) {
			return "", errors.New("no real AI one-shot in tests")
		}
	}))
}
