//go:build contract

package opencode

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestContractFirstStartWaitsForV1Migration pins the premise of migrate.go on the real binary:
// after EnsureMigrated, a conversation that existed in the 1.x database answers 200 at once.
// Without the gate, opencode 2.0.29 answers 404 for it for as long as the import runs (~25 s for
// 50 conversations x 1000 messages) while a create succeeds.
//
// It needs a 1.x database large enough for the import to outlast the server's start, which is too
// big to keep in the repository: AF_TEST_V1_SEED_DB names one (made with opencode 1.x, any
// number of long conversations). Without it the test skips; it is not in the CI selection.
func TestContractFirstStartWaitsForV1Migration(t *testing.T) {
	requireOpencode(t)
	seed := os.Getenv("AF_TEST_V1_SEED_DB")
	if seed == "" {
		t.Skip("AF_TEST_V1_SEED_DB not set")
	}
	if !strings.HasPrefix(strings.TrimPrefix(opencodeVersion(t), "opencode v"), "2.") {
		t.Skip("needs opencode 2 on PATH")
	}
	home := t.TempDir()
	for k, v := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME": filepath.Join(home, ".local", "share"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache")} {
		t.Setenv(k, v)
	}
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	dir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(seed)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	in.Close()
	out.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`select id from session order by time_created desc limit 1`).Scan(&id); err != nil {
		t.Fatalf("the seed has no 1.x session table: %v", err)
	}
	db.Close()

	startMigrationServe = realStartMigrationServe // TestMain stubs it for the unit tests
	begin := time.Now()
	if err := EnsureMigrated(CurrentLaunch()); err != nil {
		t.Fatal(err)
	}
	t.Logf("EnsureMigrated took %v", time.Since(begin).Round(100*time.Millisecond))

	check := func(label string) {
		port := freePort(t)
		cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", port)
		cmd.Env = append(os.Environ(), "OPENCODE_SERVER_USERNAME=opencode", "OPENCODE_SERVER_PASSWORD=pw")
		cmd.Dir = home
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
		deadline := time.Now().Add(30 * time.Second)
		for {
			req, _ := http.NewRequest("GET", "http://127.0.0.1:"+port+"/api/session/"+id, nil)
			req.SetBasicAuth("opencode", "pw")
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == 503 && time.Now().Before(deadline) { // still booting, not an answer about the session
					time.Sleep(200 * time.Millisecond)
					continue
				}
				if res.StatusCode != 200 {
					t.Fatalf("%s: GET /api/session/%s = %d right after the gate, want 200 (the import was still running)", label, id, res.StatusCode)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the server never answered: %v", label, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	check("first start")

	// The documented rollback restores the pre-upgrade database over the imported one. The next
	// start must import it again: the gate follows the file's content, not its path.
	if needsMigration(filepath.Join(dir, "opencode.db")) {
		t.Fatal("the imported database still reads as needing migration")
	}
	in2, err := os.Open(seed)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := os.Create(filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out2, in2); err != nil {
		t.Fatal(err)
	}
	in2.Close()
	out2.Close()
	_ = os.Remove(filepath.Join(dir, "opencode.db-wal"))
	_ = os.Remove(filepath.Join(dir, "opencode.db-shm"))
	if !needsMigration(filepath.Join(dir, "opencode.db")) {
		t.Fatal("a restored 1.x database does not read as needing migration")
	}
	if err := EnsureMigrated(CurrentLaunch()); err != nil {
		t.Fatal(err)
	}
	check("after restoring the 1.x copy")
}
