package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gcpx"
)

func TestParseGCloudExecArgs(t *testing.T) {
	o, list := parseGCloudExecArgs([]string{"--profile=prod", "--project", "prod-project", "-q", "--no-login",
		"--", "terraform", "plan", "--project", "other"})
	if list || o.Profile != "prod" || o.Project != "prod-project" || !o.Quiet || o.Login != "never" ||
		strings.Join(o.Argv, " ") != "terraform plan --project other" {
		t.Fatalf("parsed %+v list=%v", o, list)
	}
	if o, list := parseGCloudExecArgs([]string{"--list"}); !list || o.Login != "auto" {
		t.Fatalf("--list: %+v %v", o, list)
	}
}

// TestGCloudExecHelper is the wrapper run as its own process by TestGCloudExecProcess.
func TestGCloudExecHelper(t *testing.T) {
	if os.Getenv("AF_TEST_GCLOUD_EXEC_HELPER") != "1" {
		t.Skip("helper process only")
	}
	buildPinsPath = os.Getenv("AF_TEST_PINS")
	var args []string
	_ = json.Unmarshal([]byte(os.Getenv("AF_TEST_ARGS")), &args)
	runGCloudExec(args)
}

const fakeGcloudMain = `#!/bin/sh
case "$1 $2" in
"config config-helper")
  if [ -f __DIR__/fail ]; then cat __DIR__/fail >&2; exit 1; fi
  printf '{"credential":{"access_token":"%s","token_expiry":"%s"}}\n' "$(cat __DIR__/token)" "$(cat __DIR__/expiry)";;
"auth login") echo "Re-using locally stored credentials." >&2;;
*) echo "fake gcloud: unexpected $*" >&2; exit 9;;
esac
`

// The wrapper end to end: its exit codes, and that neither its stdout nor its stderr ever
// carries the token. Every process it starts is waited for before the test returns.
func TestGCloudExecProcess(t *testing.T) {
	home, fake := t.TempDir(), t.TempDir()
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := "ya" + "29." + hex.EncodeToString(b)
	_ = os.WriteFile(filepath.Join(fake, "token"), []byte(token), 0o600)
	_ = os.WriteFile(filepath.Join(fake, "expiry"), []byte(time.Now().Add(50*time.Minute).UTC().Format(time.RFC3339)), 0o600)
	// A pinned SDK already in place, so first use installs nothing.
	sdk := filepath.Join(home, ".local", "share", "agent-fleet", "google-cloud-sdk")
	_ = os.MkdirAll(filepath.Join(sdk, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(sdk, "bin", "gcloud"), []byte(strings.ReplaceAll(fakeGcloudMain, "__DIR__", fake)), 0o755)
	_ = os.WriteFile(filepath.Join(sdk, "bin", "gke-gcloud-auth-plugin"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(sdk, "VERSION"), []byte("587.0.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sdk, gcloudInstallMarker), []byte(gcloudInstallIdentity("587.0.0", "x")), 0o644)
	pins := filepath.Join(fake, "versions.json")
	_ = os.WriteFile(pins, []byte(`{"gcloud":"587.0.0","gcloud_sha256":"x"}`), 0o644)

	bridgeTok := "afg_" + hex.EncodeToString(b[:6])
	profiles := []gcpx.Profile{
		{ID: "1", Name: "prod", Label: "Prod", LoginMethod: "google", Project: "prod-project", Account: "dev@example.com"},
		{ID: "2", Name: "fresh", Label: "Fresh", LoginMethod: "google", Project: "fresh-project"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bridgeTok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": profiles})
	}))
	t.Cleanup(srv.Close)

	command := func(args ...string) *exec.Cmd {
		a, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^TestGCloudExecHelper$")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "AF_TEST_GCLOUD_EXEC_HELPER=1",
			"AF_TEST_PINS=" + pins, "AF_TEST_ARGS=" + string(a),
			"AF_CP_BASE_URL=" + srv.URL, "AF_GCP_PROFILES_TOKEN=" + bridgeTok,
			"CLOUDSDK_AUTH_ACCESS_TOKEN_FILE=/nonexistent"}
		return cmd
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := command(args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String()+errb.String(), token) {
			t.Fatalf("%v: the token reached the output", args)
		}
		return code, out.String(), errb.String()
	}

	if code, _, _ := run("--profile", "prod", "--", "true"); code != 2 {
		t.Errorf("no --project: exit %d, want 2", code)
	}
	if code, _, msg := run("--profile", "prod", "--project", "other-project", "--", "true"); code != 1 || !strings.Contains(msg, "prod-project") {
		t.Errorf("project mismatch: exit %d %q", code, msg)
	}
	if code, _, msg := run("--profile", "fresh", "--project", "fresh-project", "--", "true"); code != 3 || !strings.Contains(msg, "--login") {
		t.Errorf("no account: exit %d %q", code, msg)
	}
	if code, out, _ := run("--list"); code != 0 || !strings.Contains(out, "prod\tprod-project\tdev@example.com") ||
		!strings.Contains(out, "fresh\tfresh-project\t(chosen at the first login)") {
		t.Errorf("--list: exit %d %q", code, out)
	}
	// A logged-in profile: the wrapper execs the command, whose output it does not add to.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, ".local", "state", "agent-fleet", "gcloud", "credentials.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE credentials (account_id TEXT PRIMARY KEY, value BLOB)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO credentials VALUES ('dev@example.com', '{"type":"authorized_user"}')`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, out, msg := run("--profile", "prod", "--project", "prod-project", "--", "sh", "-c", `test -s "$CLOUDSDK_AUTH_ACCESS_TOKEN_FILE" && echo ran`)
	if code != 0 || out != "ran\n" || !strings.Contains(msg, "runs as dev@example.com") {
		t.Errorf("run: exit %d out %q err %q", code, out, msg)
	}
	// A login that ran and still left no usable credential is exit 1, not 3.
	_ = os.WriteFile(filepath.Join(fake, "fail"), []byte("ERROR: (gcloud.config.config-helper) There was a problem refreshing "+
		"your current auth tokens: ('invalid_grant: Bad Request', {'error': 'invalid_grant'})\n"), 0o600)
	if code, _, msg := run("--profile", "prod", "--project", "prod-project", "--login", "--", "true"); code != 1 ||
		!strings.Contains(msg, "the login finished but still gave no usable credential") {
		t.Errorf("after a login: exit %d %q", code, msg)
	}
	if code, _, _ := run("--profile", "prod", "--project", "prod-project", "--no-login", "--", "true"); code != 3 {
		t.Errorf("rejected credential without a login: exit %d, want 3", code)
	}
	_ = os.Remove(filepath.Join(fake, "fail"))

	// Another process holds the root (a terminal login): the run says it waits, from its
	// very first sync, and finishes once the lock is released.
	lock, err := os.OpenFile(filepath.Join(home, ".local", "state", "agent-fleet", "gcloud", ".agent-fleet.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	cmd := command("--list")
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var listOut bytes.Buffer
	cmd.Stdout = &listOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stderrR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
		done <- cmd.Wait()
	}()
	// Whatever happens below, the child is gone before the test returns.
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()
	select {
	case l := <-lines:
		if l != gcpx.WaitingMessage {
			t.Fatalf("first stderr line %q, want the waiting notice", l)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no waiting notice while the root was locked")
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	for range lines {
	}
	if err := <-done; err != nil || !strings.Contains(listOut.String(), "prod\tprod-project") {
		t.Fatalf("--list after the release: %v %q", err, listOut.String())
	}
	done <- nil

	if _, err := os.Stat(filepath.Join(home, ".config", "gcloud")); err == nil {
		t.Error("the wrapper created ~/.config/gcloud")
	}
}

// ensureGCloud reuses a tree only when it is exactly the pin for this architecture; any
// other tree is reinstalled, and an install that still leaves something else is refused.
func TestEnsureGCloudChecksTheWholePin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pins := filepath.Join(t.TempDir(), "versions.json")
	_ = os.WriteFile(pins, []byte(`{"gcloud":"587.0.0","gcloud_sha256":"abc"}`), 0o644)
	oldPins, oldRun := buildPinsPath, gcloudInstallRun
	t.Cleanup(func() { buildPinsPath, gcloudInstallRun = oldPins, oldRun })
	buildPinsPath = pins
	sdk := gcloudSDKRoot()
	tree := func(marker string) {
		_ = os.RemoveAll(sdk)
		_ = os.MkdirAll(filepath.Join(sdk, "bin"), 0o755)
		for _, b := range gcloudLinkedBins {
			_ = os.WriteFile(filepath.Join(sdk, "bin", b), []byte("#!/bin/sh\n"), 0o755)
		}
		_ = os.WriteFile(filepath.Join(sdk, "VERSION"), []byte("587.0.0\n"), 0o644)
		if marker != "" {
			_ = os.WriteFile(filepath.Join(sdk, gcloudInstallMarker), []byte(marker), 0o644)
		}
	}
	other := "arm64"
	if gcloudGOARCH == "arm64" {
		other = "amd64"
	}
	for name, marker := range map[string]string{
		"wrong arch": "version=587.0.0 arch=" + other + " sha256=abc\n",
		"other sha":  gcloudInstallIdentity("587.0.0", "def"),
		"unmarked":   "",
	} {
		t.Run(name, func(t *testing.T) {
			tree(marker)
			ran := 0
			gcloudInstallRun = func() error { ran++; tree(gcloudInstallIdentity("587.0.0", "abc")); return nil }
			if _, err := ensureGCloud(); err != nil || ran != 1 {
				t.Fatalf("err %v, installs %d (want 1)", err, ran)
			}
			// An install that leaves the wrong tree is refused.
			tree(marker)
			gcloudInstallRun = func() error { return nil }
			if _, err := ensureGCloud(); err == nil {
				t.Fatal("a tree that is still not the pin was used")
			}
		})
	}
	tree(gcloudInstallIdentity("587.0.0", "abc"))
	gcloudInstallRun = func() error { t.Fatal("installed over the pinned tree"); return nil }
	if bin, err := ensureGCloud(); err != nil || bin != filepath.Join(sdk, "bin", "gcloud") {
		t.Fatalf("pinned tree: %s %v", bin, err)
	}
}
