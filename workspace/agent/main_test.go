package main

import (
	"context"
	"errors"
	"os"
	"testing"
)

// The workspace running these tests may have a real shared codex app-server up and advertised
// (AF_CODEX_APP_SERVER_ADDR), and a codex Terminal launch probes it (codex/release.go). No test
// may reach it: the ones that need a server start a fake and set the address themselves.
//
// The branch-name resolver starts an AI one-shot for a non-ASCII title, and the AI-assist
// toggle defaults to on: a test that resolves such a title would otherwise start a real CLI.
// The tests that exercise the English slug install their own fake.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("AF_CODEX_APP_SERVER_ADDR")
	englishSlugOneShot = func(context.Context, string) (string, error) {
		return "", errors.New("no real AI one-shot in tests")
	}
	os.Exit(m.Run())
}
