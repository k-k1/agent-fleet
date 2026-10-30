package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudruns"
)

// awsExecChildEnv makes TestMain run af-aws-exec with the newline-separated arguments it holds.
const awsExecChildEnv = "AF_TEST_AWS_EXEC_CHILD"

const fakeAWSForRows = `#!/bin/sh
case "$1 $2" in
"configure export-credentials")
  [ -f "$HOME/loggedIn" ] || { echo "Error loading SSO Token: Token for af-prod does not exist" >&2; exit 255; }
  echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"sekret","SessionToken":"tok","Expiration":"2030-01-01T00:00:00+00:00"}'
  exit 0 ;;
"sts get-caller-identity")
  echo "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me"; exit 0 ;;
esac
exit 1
`

// runWrapper runs af-aws-exec in a child process with a fake aws, a session named sess and
// args, and returns its exit code, its stderr and the rows of that session.
func runWrapper(t *testing.T, home, sess string, args ...string) (int, string, []cloudruns.Row) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fakeAWSForRows), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"AF_SESSIONS_DIR=" + filepath.Join(home, "sessions"),
		"AF_SESSION_NAME=" + sess,
		awsExecChildEnv + "=" + strings.Join(args, "\n"),
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	var rows []cloudruns.Row
	dir := filepath.Join(home, ".local", "state", "agent-fleet", "cloud-runs", sess)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var r cloudruns.Row
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return code, stderr.String(), rows
}

// homeWithProd is a HOME whose ~/.aws/config defines the SSO profile prod, and in which the
// session "rows" exists.
func homeWithProd(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := "[sso-session af-prod]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-northeast-1\n" +
		"sso_registration_scopes = sso:account:access\n\n[profile prod]\nsso_session = af-prod\nsso_account_id = 123456789012\n" +
		"sso_role_name = Dev\nregion = us-west-2\n"
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"rows","kind":"claude","dir":"` + home + `"}`
	if err := os.WriteFile(filepath.Join(home, "sessions", "rows.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// ADR 0126 decision 1: a run leaves a row with the identity it ran as, the command's exit
// status is still the wrapper's (it becomes the command), and the command's arguments reach no
// row and no file of the Agent's state.
func TestAWSExecWritesARunRow(t *testing.T) {
	home := homeWithProd(t)
	if err := os.WriteFile(filepath.Join(home, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const sentinel = "zq-sentinel-e2e-91c4"
	code, stderr, rows := runWrapper(t, home, "rows", "--profile", "prod", "--account", "123456789012", "-q", "--no-login",
		"--", "sh", "-c", "exit 7", sentinel)
	if code != 7 {
		t.Fatalf("exit %d, want the command's 7; stderr:\n%s", code, stderr)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Session != "rows" || r.Profile != "prod" || r.Account != "123456789012" || r.Role != "Dev" || r.Region != "us-west-2" ||
		r.Program != "sh" || r.Refused != "" || r.EndedAt != "" {
		t.Fatalf("row = %+v", r)
	}
	err := filepath.Walk(filepath.Join(home, ".local", "state"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(sentinel)) {
			t.Errorf("%s holds the command's argument", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A run refused before the command starts leaves a row that says why, and exits as before.
func TestAWSExecRecordsARefusedRun(t *testing.T) {
	home := homeWithProd(t)
	code, _, rows := runWrapper(t, home, "rows", "--profile", "prod", "--account", "123456789012", "-q", "--no-login",
		"--", "true")
	if code != 3 {
		t.Fatalf("exit %d, want 3 (login required)", code)
	}
	if len(rows) != 1 || rows[0].Refused != cloudruns.RefusedLogin || rows[0].EndedAt == "" || rows[0].Account != "" {
		t.Fatalf("rows = %+v", rows)
	}
}

// The wrapper becomes the command (exec), so the caller's signals reach the command as they
// would without af-aws-exec: one SIGINT sent to the process group arrives once, not once
// directly and once forwarded, and a SIGKILL of the wrapper's PID ends the command, which holds
// the credentials. A supervising wrapper broke both.
func TestAWSExecBecomesTheCommand(t *testing.T) {
	home := homeWithProd(t)
	if err := os.WriteFile(filepath.Join(home, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fakeAWSForRows), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	pidFile, hits, ready := filepath.Join(out, "pid"), filepath.Join(out, "hits"), filepath.Join(out, "ready")
	script := `echo $$ > "$1"; trap 'echo int >> "$2"' INT; touch "$3"; while :; do sleep 0.05; done`
	args := []string{"--profile", "prod", "--account", "123456789012", "-q", "--no-login", "--", "sh", "-c", script, "sh", pidFile, hits, ready}
	cmd := exec.Command(self)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "AF_SESSIONS_DIR=" + filepath.Join(home, "sessions"),
		"AF_SESSION_NAME=rows", awsExecChildEnv + "=" + strings.Join(args, "\n")}
	// Its own process group and no terminal: an agent's shell tool signalling the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL) }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if ok() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the command to start", func() bool { _, err := os.Stat(ready); return err == nil })
	b, _ := os.ReadFile(pidFile)
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("the command runs as PID %s, the wrapper as %d: af-aws-exec did not become the command", got, cmd.Process.Pid)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waitFor("the trap", func() bool { b, _ := os.ReadFile(hits); return len(b) > 0 })
	time.Sleep(300 * time.Millisecond)
	if b, _ := os.ReadFile(hits); strings.Count(string(b), "int") != 1 {
		t.Fatalf("one SIGINT to the group arrived %d times", strings.Count(string(b), "int"))
	}
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the command outlived a SIGKILL of the wrapper's PID")
	}
}
