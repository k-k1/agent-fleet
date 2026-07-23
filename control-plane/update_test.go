package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stageLink builds a fake install layout (<root>/opt/agent-fleet/<v>/{af,VERSION})
// with ~/.local/bin/af -> the versioned af, and points AF_SELF_LINK at the symlink.
func stageLink(t *testing.T, ver string) string {
	t.Helper()
	root := t.TempDir()
	pkg := filepath.Join(root, "opt", "agent-fleet", ver)
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	af := filepath.Join(pkg, "af")
	if err := os.WriteFile(af, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "VERSION"), []byte(ver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "af")
	if err := os.Symlink(af, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestStagedVersion(t *testing.T) {
	t.Setenv("AF_SELF_LINK", stageLink(t, "9.9.9"))
	if got := stagedVersion(); got != "9.9.9" {
		t.Fatalf("stagedVersion = %q, want 9.9.9", got)
	}
	t.Setenv("AF_SELF_LINK", "")
	if got := stagedVersion(); got != "" {
		t.Fatalf("stagedVersion (no link) = %q, want empty", got)
	}
}

func TestUpdateStatusRestartRequired(t *testing.T) {
	t.Setenv("AF_SELF_LINK", stageLink(t, "9.9.9")) // staged on disk
	old := buildVersion
	buildVersion = "1.0.0" // running an older build
	defer func() { buildVersion = old }()

	rr := httptest.NewRecorder()
	updateStatus(rr, httptest.NewRequest("GET", "/api/update/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var body struct {
		Current, Installed string
		RestartRequired    bool `json:"restartRequired"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Current != "1.0.0" || body.Installed != "9.9.9" || !body.RestartRequired {
		t.Fatalf("unexpected status: %+v", body)
	}
}

func TestUpdateStatusUpToDate(t *testing.T) {
	t.Setenv("AF_SELF_LINK", stageLink(t, "1.0.0"))
	old := buildVersion
	buildVersion = "1.0.0"
	defer func() { buildVersion = old }()

	rr := httptest.NewRecorder()
	updateStatus(rr, httptest.NewRequest("GET", "/api/update/status", nil))
	var body struct {
		RestartRequired bool `json:"restartRequired"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.RestartRequired {
		t.Fatalf("restartRequired should be false when versions match")
	}
}

// apply refuses when nothing newer is staged.
func TestUpdateApplyNoStaged(t *testing.T) {
	t.Setenv("AF_SELF_LINK", stageLink(t, "1.0.0"))
	old := buildVersion
	buildVersion = "1.0.0"
	defer func() { buildVersion = old }()

	rr := httptest.NewRecorder()
	updateApply(rr, httptest.NewRequest("POST", "/api/update/apply", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("apply with no staged update = %d, want 409", rr.Code)
	}
}
