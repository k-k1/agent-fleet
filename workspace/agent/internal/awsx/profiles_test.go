package awsx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prof(name string) Profile {
	return Profile{Name: name, StartURL: "https://example.awsapps.com/start", SSORegion: "ap-northeast-1",
		AccountID: "123456789012", RoleName: "Dev", Region: "us-west-2"}
}

const userConfig = "[default]\nregion = eu-west-1\n\n[profile mine]\nsso_session = mine\n"

func TestApplyKeepsUserContentAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(userConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(path, []Profile{prof("prod"), prof("sandbox")})
	if err != nil || !res.Changed {
		t.Fatalf("apply: %+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	if !strings.HasPrefix(got, userConfig) {
		t.Fatalf("user content was not preserved at the top:\n%s", got)
	}
	for _, want := range []string{"[profile prod]", "[sso-session af-prod]", "sso_account_id = 123456789012", "[profile sandbox]", blockBegin, blockEnd} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}

	res, err = Apply(path, []Profile{prof("prod"), prof("sandbox")})
	if err != nil || res.Changed {
		t.Fatalf("second apply rewrote an unchanged file: %+v %v", res, err)
	}

	// Removing every profile removes the block and leaves the user's file as it was.
	if _, err := Apply(path, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != userConfig {
		t.Fatalf("after removing all profiles:\n%q\nwant\n%q", b, userConfig)
	}
}

// Lines the member appended after the block survive a rewrite of the block.
func TestApplyKeepsContentAfterTheBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if _, err := Apply(path, []Profile{prof("prod")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	tail := "\n[profile later]\nregion = us-east-1\n"
	if err := os.WriteFile(path, append(b, tail...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(path, []Profile{prof("other")}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	got := string(b)
	if !strings.Contains(got, "[profile later]") || !strings.Contains(got, "[profile other]") || strings.Contains(got, "[profile prod]") {
		t.Fatalf("unexpected result:\n%s", got)
	}
}

func TestApplyLeavesTheMembersOwnDefinitionAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	own := "[profile prod]\nregion = eu-west-1\n\n[sso-session af-dev]\nsso_start_url = https://x.example/start\n"
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(path, []Profile{prof("prod"), prof("dev"), prof("ok")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Shadowed, ",") != "prod,dev" || strings.Join(res.Exported, ",") != "ok" {
		t.Fatalf("result = %+v", res)
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "[profile prod]") != 1 {
		t.Fatalf("a duplicate section was written:\n%s", b)
	}
}

// A value that could inject INI keys (credential_process runs a command) is refused by
// the same allowlist the SSM session config uses.
func TestApplyRefusesInjectedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	bad := prof("evil")
	bad.RoleName = "Dev\ncredential_process = /bin/sh -c id"
	res, err := Apply(path, []Profile{bad, prof("fine")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "credential_process") || strings.Join(res.Invalid, ",") != "evil" {
		t.Fatalf("injection not refused: %+v\n%s", res, b)
	}
}

func TestApplyWithNothingToExportCreatesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aws", "config")
	if _, err := Apply(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an empty config was created: %v", err)
	}
}

// A begin marker without its end marker means a hand edit; guessing where the block ends
// could delete the member's lines, so nothing is written.
func TestApplyRefusesAHalfBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	half := "[default]\n" + blockBegin + "\n[profile x]\n"
	if err := os.WriteFile(path, []byte(half), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(path, []Profile{prof("prod")}); err == nil {
		t.Fatal("expected an error for a begin marker without an end marker")
	}
	if b, _ := os.ReadFile(path); string(b) != half {
		t.Fatalf("file was modified:\n%s", b)
	}
}

// ~/.aws/config may be a symlink onto durable storage; the rename must replace the
// target, not turn the link into a real file.
func TestApplyWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "durable", "config")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(link, []Profile{prof("prod")}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "[profile prod]") {
		t.Fatalf("target not updated:\n%s", b)
	}
}

func TestSyncPullsFromTheCPAndIsOffWithoutTheBridge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_AWS_PROFILES_TOKEN", "")
	if _, err := Sync(); err != ErrBridgeOff {
		t.Fatalf("err = %v, want ErrBridgeOff", err)
	}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/aws-profiles" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"profiles":[{"name":"prod","label":"prod","startUrl":"https://example.awsapps.com/start","ssoRegion":"ap-northeast-1","accountId":"123456789012","roleName":"Dev"}]}`))
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL+"/")
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_x.y")
	res, err := Sync()
	if err != nil || !res.Changed || gotAuth != "Bearer afp_x.y" {
		t.Fatalf("sync: %+v %v auth=%q", res, err, gotAuth)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".aws", "config"))
	if !strings.Contains(string(b), "[profile prod]") {
		t.Fatalf("config not written:\n%s", b)
	}
}
