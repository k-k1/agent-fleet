package awsx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
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
	// "dev" collides only with the member's [sso-session af-dev]: reported apart, since
	// there is no profile of theirs to "use".
	if strings.Join(res.Shadowed, ",") != "prod" || strings.Join(res.SessionShadowed, ",") != "dev" || strings.Join(res.Exported, ",") != "ok" {
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
	// The value is quoted with its newline escaped, so it cannot start a line of its own
	// in a log or on a terminal.
	if strings.Contains(string(b), "credential_process") || len(res.Invalid) != 1 ||
		!strings.HasPrefix(res.Invalid["evil"], "the role name ") || strings.Contains(res.Invalid["evil"], "\n") {
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
		_, _ = w.Write([]byte(`{"profiles":[{"name":"prod","label":"prod","startUrl":"https://example.awsapps.com/start","ssoRegion":"ap-northeast-1","accountId":"123456789012","roleName":"Dev"}],"conflicts":[{"name":"app","labels":["app","App"]}]}`))
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
	if res.Settings["prod"].AccountID != "123456789012" || len(res.Conflicts) != 1 || res.Conflicts[0].Name != "app" {
		t.Fatalf("sync result: %+v", res)
	}
	// The last list survives for when the CP cannot be asked.
	srv.Close()
	if _, err := Sync(); err == nil {
		t.Fatal("expected a fetch error with the CP gone")
	}
	m, c, ok := CachedSettings()
	if !ok || m["prod"].RoleName != "Dev" || len(c) != 1 {
		t.Fatalf("cached settings: %v %v %v", m, c, ok)
	}
}

// "default" is what every bare aws/SDK call uses; exporting it would move those calls off
// the workload role onto an SSO login (measured with aws-cli 2.36.46).
func TestApplyNeverExportsTheDefaultProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	res, err := Apply(path, []Profile{prof("default"), prof("prod")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "[profile default]") || strings.Join(res.Exported, ",") != "prod" {
		t.Fatalf("default exported: %+v\n%s", res, b)
	}
}

// A static-key profile in ~/.aws/credentials is the member's own; an SSO block under the
// same name would turn `aws --profile full` into an SSO profile.
func TestApplyLeavesCredentialsFileProfilesAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte("[full]\naws_access_key_id = AKIA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(filepath.Join(dir, "config"), []Profile{prof("full"), prof("prod")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Shadowed, ",") != "full" || strings.Join(res.Exported, ",") != "prod" {
		t.Fatalf("result = %+v", res)
	}
}

func TestExportedInReadsTheBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(path, []Profile{prof("prod"), prof("sandbox")}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ExportedIn(path), ","); got != "prod,sandbox" {
		t.Fatalf("ExportedIn = %q (the member's own [profile mine] must not be listed)", got)
	}
}

// botocore reads [profile "prod"] as profile prod; a quoted header of the member's own
// must shadow the Settings profile like an unquoted one (verified with aws-cli 2.36.46:
// exporting over it moved every plain `aws --profile prod` to the Settings account).
func TestApplyTreatsQuotedHeadersAsTheMembersOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	own := "[profile \"prod\"]\nsso_account_id = 999999999999\n\n[profile 'stg']\nregion = eu-west-1\n\n[sso-session \"af-dev\"]\nsso_region = us-east-1\n"
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(path, []Profile{prof("prod"), prof("stg"), prof("dev"), prof("ok")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Shadowed, ",") != "prod,stg" || strings.Join(res.SessionShadowed, ",") != "dev" || strings.Join(res.Exported, ",") != "ok" {
		t.Fatalf("result = %+v", res)
	}
}

// When the CP answered but the file could not be written, the answer is still fresh.
func TestSyncMarksAFetchThatCouldNotBeWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A directory where the config file should be makes the read fail.
	if err := os.MkdirAll(filepath.Join(home, ".aws", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[],"conflicts":[{"name":"app","labels":["app","App"]}]}`))
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_x.y")
	res, err := Sync()
	if err == nil || !res.Fetched || len(res.Conflicts) != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

// With the CP unreachable the block is re-applied from the last list the CP gave, so a
// [DEFAULT] line added since holds a profile back offline just as it would online.
func TestSyncReappliesTheCachedListWhenTheCPIsDown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"name":"prod","label":"prod","startUrl":"https://example.awsapps.com/start","ssoRegion":"ap-northeast-1","accountId":"123456789012","roleName":"Dev","region":"us-west-2"}]}`))
	}))
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_x.y")
	if res, err := Sync(); err != nil || strings.Join(res.Exported, ",") != "prod" {
		t.Fatalf("online: %+v %v", res, err)
	}
	srv.Close()
	path := filepath.Join(home, ".aws", "config")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append([]byte("[DEFAULT]\nregion = eu-west-1\n\n"), b...), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Sync()
	if err == nil || !res.FromCache || len(res.Exported) != 0 || res.DefaultClash["prod"] == "" {
		t.Fatalf("offline: %+v %v", res, err)
	}
	if got := ExportedIn(path); len(got) != 0 {
		t.Fatalf("the block still holds %v offline", got)
	}
}

// [DEFAULT] lends its keys to both the exported profile and its sso-session; a value
// one of them sets differently makes the CLI refuse the profile, so it is not exported
// and the reason is reported.
func TestApplyDoesNotExportAProfileADEFAULTKeyBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("[DEFAULT]\nregion = eu-west-1\noutput = json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	same := prof("same")
	same.Region = "eu-west-1"
	res, err := Apply(path, []Profile{prof("prod"), same})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.DefaultClash["prod"], `region = "eu-west-1" differs`) || strings.Join(res.Exported, ",") != "same" {
		t.Fatalf("result = %+v", res)
	}

	// A [DEFAULT] role_arn or web identity path takes every profile off SSO: the Settings
	// name would assume that role instead (measured: the CLI goes to AssumeRole). Next to
	// an empty web_identity_token_file role_arn leaves the CLI on SSO.
	for text, held := range map[string]bool{
		"[DEFAULT]\nrole_arn = arn:aws:iam::333333333333:role/Other\nsource_profile = src\n": true,
		"[DEFAULT]\nrole_arn =\n":                                                  true,
		"[DEFAULT]\nweb_identity_token_file = /tmp/t\n":                            true,
		"[DEFAULT]\nrole_arn = arn:aws:iam::1:role/x\nweb_identity_token_file =\n": false,
		"[DEFAULT]\ncredential_process = /bin/x\n":                                 false, // the CLI stays on SSO; af-aws-exec's policy refuses it at run time
		"[DEFAULT]\nregion =\n":                                                    true,
	} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		p := prof("prod")
		res, err := Apply(path, []Profile{p})
		if err != nil {
			t.Fatal(err)
		}
		if _, got := res.DefaultClash["prod"]; got != held {
			t.Errorf("%q: held back = %v, want %v (%+v)", text, got, held, res)
		}
	}
	// An empty value is quoted, not invisible.
	path = filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("[DEFAULT]\nregion =\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ := Apply(path, []Profile{prof("prod")}); !strings.HasPrefix(res.DefaultClash["prod"], `region = "" differs`) {
		t.Fatalf("empty value: %q", res.DefaultClash["prod"])
	}
}

// A Settings profile without both an account and a role is never exported: the CLI does
// not treat it as SSO, so `aws --profile <name>` would fall through to the workspace's
// own role, or to a [DEFAULT] account (both measured with aws-cli 2.36.46).
func TestApplyDoesNotExportAProfileWithoutAccountAndRole(t *testing.T) {
	for _, defaults := range []string{"", "[DEFAULT]\nsso_account_id = 999999999999\nsso_role_name = Admin\n"} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(defaults), 0o600); err != nil {
			t.Fatal(err)
		}
		noRole, blank := prof("norole"), prof("blank")
		noRole.RoleName = ""
		blank.AccountID, blank.RoleName = "", ""
		res, err := Apply(path, []Profile{noRole, blank, prof("ok")})
		if err != nil {
			t.Fatal(err)
		}
		// ("ok" itself clashes with the [DEFAULT] account in the second case.)
		if !strings.Contains(res.Incomplete["blank"], "fall back to the workspace's own role") ||
			res.Incomplete["norole"] != "Settings has an account but no role; set both" ||
			(defaults == "" && strings.Join(res.Exported, ",") != "ok") {
			t.Fatalf("defaults %q: result = %+v", defaults, res)
		}
	}
}

// Why exporting it would be a trap, against the real CLI: the block RenderSSMConfig writes
// for a profile without account and role resolves to whatever the container credentials
// endpoint hands out. Skipped where no aws CLI is installed.
func TestAProfileWithoutAccountAndRoleFallsToTheWorkloadRole(t *testing.T) {
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"AccessKeyId":"ASIAWORKLOAD","SecretAccessKey":"s","Token":"t","Expiration":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	path := filepath.Join(home, ".aws", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	blank := prof("noacct")
	blank.AccountID, blank.RoleName = "", ""
	ini, err := sessionx.RenderSSMConfig(session.SSMMeta{Profile: blank.Name, StartURL: blank.StartURL, SSORegion: blank.SSORegion, Region: blank.Region})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(aws, "configure", "export-credentials", "--profile", "noacct", "--format", "process")
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "AWS_CONTAINER_CREDENTIALS_FULL_URI=" + srv.URL, "AWS_EC2_METADATA_DISABLED=true"}
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "ASIAWORKLOAD") {
		t.Fatalf("expected the workload credentials (the hazard this test documents), got:\n%s", out)
	}
}

// A Settings value the AWS config cannot hold is reported even when a [DEFAULT] line
// would also hold the profile back: it needs fixing in Settings either way.
func TestApplyReportsAnUnwritableValueBeforeADEFAULTClash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("[DEFAULT]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	colon := prof("colon")
	colon.RoleName = "Dev:Ops"
	res, err := Apply(path, []Profile{colon})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Invalid["colon"], `the role name "Dev:Ops"`) || res.DefaultClash["colon"] != "" {
		t.Fatalf("result = %+v", res)
	}
}

// The cache belongs to the membership whose bridge token fetched it: another token (a
// restored or shared home) never gets it applied. And the CP's latest answer is cached
// even when the block could not be written, so a later offline run does not fall back
// to an older list.
func TestSettingsCacheIsBoundAndAlwaysLatest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	answer := `{"profiles":[{"name":"prod","label":"prod","startUrl":"https://example.awsapps.com/start","ssoRegion":"ap-northeast-1","accountId":"111111111111","roleName":"Dev"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(answer)) }))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member-a")
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	if m, _, ok := CachedSettings(); !ok || m["prod"].AccountID != "111111111111" {
		t.Fatalf("own cache: %v %v", m, ok)
	}
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member-b")
	if _, _, ok := CachedSettings(); ok {
		t.Fatal("another membership's cache was accepted")
	}

	// The CP now says account 222…, but the block cannot be written (a half block).
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member-a")
	answer = strings.Replace(answer, "111111111111", "222222222222", 1)
	path := filepath.Join(home, ".aws", "config")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(string(b)[:strings.Index(string(b), blockEnd)]), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(); err == nil {
		t.Fatal("expected the half block to fail the write")
	}
	if m, _, _ := CachedSettings(); m["prod"].AccountID != "222222222222" {
		t.Fatalf("the cache kept the older answer: %v", m)
	}
}

// An offline run chooses the cached list under the same lock a fresh run writes the block
// and the cache under: if the cache changes while it waits for the lock, it applies the
// newer list, never the one it would have read before waiting.
func TestOfflineSyncReadsTheCacheUnderTheLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:9") // nothing listens: offline
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member")
	mustMkdir(t, filepath.Join(home, ".aws"), 0o700)
	older := prof("prod")
	older.AccountID = "111111111111"
	if err := saveSettingsCache([]Profile{older}, nil); err != nil {
		t.Fatal(err)
	}
	unlock, _, err := lockConfig(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan SyncResult)
	go func() {
		res, _ := Sync()
		done <- res
	}()
	time.Sleep(200 * time.Millisecond) // let it reach the lock
	newer := prof("prod")
	newer.AccountID = "222222222222"
	if err := saveSettingsCache([]Profile{newer}, nil); err != nil {
		t.Fatal(err)
	}
	unlock()
	res := <-done
	if res.Settings["prod"].AccountID != "222222222222" {
		t.Fatalf("the offline run applied the list it saw before the lock: %+v", res.Settings)
	}
	b, _ := os.ReadFile(ConfigPath())
	if !strings.Contains(string(b), "sso_account_id = 222222222222") {
		t.Fatalf("block:\n%s", b)
	}
}

// A fresh answer that cannot be cached is reported; an offline re-apply that could not write the block is not
// reported as applied.
func TestCacheSaveFailureAndFailedReapply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustMkdir(t, filepath.Join(home, ".aws"), 0o700)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member")
	if err := saveSettingsCache([]Profile{prof("old")}, nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"name":"prod","label":"prod","startUrl":"https://example.awsapps.com/start","ssoRegion":"ap-northeast-1","accountId":"123456789012","roleName":"Dev"}]}`))
	}))
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	// Make only the cache write fail: a non-empty directory where the cache file goes
	// cannot be replaced by a rename (the config and its lock are unaffected).
	if err := os.Remove(settingsCachePath()); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(settingsCachePath(), "x"), 0o700)
	before, _ := os.ReadFile(ConfigPath())
	_, err := Sync()
	if err == nil || !strings.Contains(err.Error(), "Settings cache") {
		t.Fatalf("a failed cache save was not reported: %v", err)
	}
	// Fail closed: the block is not written past a cache that could not be saved.
	if after, _ := os.ReadFile(ConfigPath()); string(after) != string(before) {
		t.Fatalf("the block was written although the cache was not:\n%s", after)
	}
	if err := os.RemoveAll(settingsCachePath()); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	// Offline with a half block: the re-apply fails and FromCache stays false.
	if err := saveSettingsCache([]Profile{prof("prod")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte(blockBegin+"\n[profile x]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Sync()
	if err == nil || res.FromCache {
		t.Fatalf("failed re-apply reported as applied: %+v %v", res, err)
	}
}

// The fetch itself waits for the lock, so two online runs cannot commit out of order (one
// that fetched earlier writing its older list after a newer one).
func TestSyncFetchesUnderTheLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"profiles":[]}`))
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_AWS_PROFILES_TOKEN", "afp_member")
	unlock, _, err := lockConfig(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = Sync()
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		unlock()
		t.Fatalf("the CP was asked %d time(s) before the lock was free", n)
	}
	unlock()
	<-done
	if hits.Load() != 1 {
		t.Fatalf("hits = %d after the lock was released", hits.Load())
	}
}
