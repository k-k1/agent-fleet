package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudruns"
)

const fakeAWSCounting = `#!/bin/sh
echo "$1 $2" >> "$HOME/aws-calls"
case "$1 $2" in
"configure export-credentials")
  echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"sekret","SessionToken":"tok","Expiration":"2030-01-01T00:00:00+00:00"}'
  exit 0 ;;
"sts get-caller-identity")
  echo "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_0123456789abcdef/me"; exit 0 ;;
esac
exit 1
`

// gatedHome is a HOME with an empty ~/.aws, a shell session "gate" (the unmeasured, short
// wait) and a fake CP that serves one profile, prod-admin, asking for confirmation before use.
// It returns the HOME and the environment for af-aws-exec.
func gatedHome(t *testing.T) (string, []string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"gate","kind":"shell","dir":"` + home + `","createdAt":"2026-10-31T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(home, "sessions", "gate.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-AF-AWS-Profiles", "2")
		w.Write([]byte(`{"profiles":[{"id":"id-admin","name":"prod-admin","label":"prod admin","startUrl":"https://example.awsapps.com/start",` +
			`"ssoRegion":"ap-northeast-1","accountId":"123456789012","roleName":"Admin","region":"us-west-2","confirmBeforeUse":true}]}`))
	}))
	t.Cleanup(cp.Close)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fakeAWSCounting), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "AF_SESSIONS_DIR=" + filepath.Join(home, "sessions"),
		"AF_SESSION_NAME=gate", "AF_CP_BASE_URL=" + cp.URL, "AF_AWS_PROFILES_TOKEN=tok-gate"}
	// This process answers as the Agent does, from the same state.
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("AF_CP_BASE_URL", cp.URL)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "tok-gate")
	return home, env
}

// startWrapper starts af-aws-exec with env and args; wait returns its exit code and stderr.
func startWrapper(t *testing.T, env []string, args ...string) func() (int, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(append([]string{}, env...), awsExecChildEnv+"="+strings.Join(args, "\n"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() (int, string) {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), stderr.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, stderr.String()
	}
}

// pendingID waits for the Agent's list to show one request and returns its id.
func pendingID(t *testing.T) string {
	t.Helper()
	for i := 0; i < 300; i++ {
		rec := httptest.NewRecorder()
		awsx.HandleConfirmList(rec, httptest.NewRequest("GET", "/aws-confirm", nil))
		var got struct {
			Requests []struct {
				ID string `json:"id"`
			} `json:"requests"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &got) == nil && len(got.Requests) == 1 {
			return got.Requests[0].ID
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no confirmation request appeared")
	return ""
}

func answerAs(t *testing.T, id, verb string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/aws-confirm/"+id+"/"+verb, nil)
	r.SetPathValue("id", id)
	r.Header.Set("X-AF-Relay", "cp")
	rec := httptest.NewRecorder()
	if verb == "approve" {
		awsx.HandleConfirmApprove(rec, r)
	} else {
		awsx.HandleConfirmDeny(rec, r)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", verb, rec.Code, rec.Body.String())
	}
}

func rowsOf(t *testing.T, home, sess string) []cloudruns.Row {
	t.Helper()
	dir := filepath.Join(home, ".local", "state", "agent-fleet", "cloud-runs", sess)
	ents, _ := os.ReadDir(dir)
	var out []cloudruns.Row
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var r cloudruns.Row
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// ADR 0126 decisions 5 and 6 through the real wrapper: a gated profile is not in
// ~/.aws/config; a denied run exits 4 before the CLI was started at all, so no credential was
// fetched; an approved run runs the command, and its role credentials are not left in the
// CLI's cache.
func TestAWSExecConfirmBeforeUse(t *testing.T) {
	home, env := gatedHome(t)
	args := []string{"--profile", "prod-admin", "-q", "--no-login", "--", "sh", "-c", "exit 7"}

	wait := startWrapper(t, env, args...)
	answerAs(t, pendingID(t), "deny")
	code, stderr := wait()
	if code != 4 || !strings.Contains(stderr, "denied") {
		t.Fatalf("denied run: exit %d\n%s", code, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(home, "aws-calls")); err == nil {
		t.Fatalf("the CLI ran before the confirmation: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".aws", "config")); strings.Contains(string(b), "prod-admin") {
		t.Fatalf("the gated profile is in ~/.aws/config:\n%s", b)
	}
	if rows := rowsOf(t, home, "gate"); len(rows) != 1 || rows[0].Refused != cloudruns.RefusedConfirm {
		t.Fatalf("rows after the denial = %+v", rows)
	}

	// After the one-minute hold a new run asks again; allowed, the command runs.
	grants := filepath.Join(home, ".local", "state", "agent-fleet", "aws-confirm", "requests")
	ents, _ := os.ReadDir(grants)
	for _, e := range ents {
		os.Remove(filepath.Join(grants, e.Name())) // end the hold without waiting a minute
	}
	wait = startWrapper(t, env, args...)
	answerAs(t, pendingID(t), "approve")
	code, stderr = wait()
	if code != 7 {
		t.Fatalf("approved run: exit %d\n%s", code, stderr)
	}
	rows := rowsOf(t, home, "gate")
	last := rows[len(rows)-1]
	if last.Refused != "" || last.Account != "123456789012" || last.Role != "Admin" || last.ApprovedOutsideConsole {
		t.Fatalf("approved run's row = %+v", last)
	}
	sum := filepath.Join(home, ".aws", "cli", "cache")
	if ents, _ := os.ReadDir(sum); len(ents) != 0 {
		t.Fatalf("role credentials left in the CLI cache: %v", ents)
	}

	// The same session does not ask again within the grant.
	code, stderr = startWrapper(t, env, args...)()
	if code != 7 {
		t.Fatalf("second run asked again or failed: exit %d\n%s", code, stderr)
	}
}
