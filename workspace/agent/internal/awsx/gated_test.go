package awsx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ADR 0126 decision 6: a profile that asks for confirmation before use passes every rule an
// exported one does but is not written to ~/.aws/config; it is usable, from a config of the
// Agent's own. A chain whose source is gated is withheld too. The member's own definitions
// still win over a gated profile exactly as over an exported one.
func TestGatedProfilesAreUsableButNotWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	own := "[profile mine-gated]\nregion = eu-west-1\n"
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	gated := prof("prod-admin")
	gated.ID, gated.ConfirmBeforeUse = "id-admin", true
	src := prof("src")
	src.ConfirmBeforeUse = true
	chain := chainProf() // assumes from "src"
	shadowed := prof("mine-gated")
	shadowed.ConfirmBeforeUse = true
	res, err := Apply(path, []Profile{gated, prof("sandbox"), src, chain, shadowed})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	cfg := string(b)
	for _, n := range []string{"prod-admin", "src", "deploy"} {
		if strings.Contains(cfg, "[profile "+n+"]") {
			t.Errorf("gated profile %s was written to ~/.aws/config:\n%s", n, cfg)
		}
	}
	if !strings.Contains(cfg, "[profile sandbox]") {
		t.Fatalf("the ungated profile is missing:\n%s", cfg)
	}
	slices.Sort(res.Withheld)
	if strings.Join(res.Withheld, ",") != "deploy,prod-admin,src" || strings.Join(res.Exported, ",") != "sandbox" {
		t.Fatalf("exported %v, withheld %v", res.Exported, res.Withheld)
	}
	if !slices.Contains(res.Shadowed, "mine-gated") || slices.Contains(res.Usable(), "mine-gated") {
		t.Fatalf("the member's own definition no longer wins over a gated profile: %+v", res)
	}

	// Each withheld profile has its own config; a chain's holds its source.
	g, err := os.ReadFile(GatedConfigPath("prod-admin"))
	if err != nil || !strings.Contains(string(g), "[profile prod-admin]") || !strings.Contains(string(g), "[sso-session af-prod-admin]") {
		t.Fatalf("gated config = %q, %v", g, err)
	}
	c, _ := os.ReadFile(GatedConfigPath("deploy"))
	if !strings.Contains(string(c), "[profile deploy]") || !strings.Contains(string(c), "[profile src]") {
		t.Fatalf("chain config lacks its source:\n%s", c)
	}
	usable := UsableNow()
	slices.Sort(usable)
	if strings.Join(usable, ",") != "deploy,prod-admin,sandbox,src" {
		t.Fatalf("UsableNow = %v", usable)
	}

	// Ungating moves the profile into the block and removes its own config.
	gated.ConfirmBeforeUse = false
	if _, err := Apply(path, []Profile{gated, prof("sandbox")}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "[profile prod-admin]") {
		t.Fatalf("an ungated profile is not written:\n%s", b)
	}
	if _, err := os.Stat(GatedConfigPath("prod-admin")); !os.IsNotExist(err) {
		t.Fatalf("a stale gated config stayed: %v", err)
	}
}

// PlanExec runs a withheld profile from its own config, which ~/.aws/config does not have.
func TestPlanExecRunsAWithheldProfileFromItsOwnConfig(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The profile exists only in the gated config: replace ~/.aws/config with one without it.
	if err := os.WriteFile(ConfigPath(), []byte("[default]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(ConfigPath()), "credentials"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gated := prodSettings["prod"]
	gated.ConfirmBeforeUse = true
	sp := map[string]Profile{"prod": gated}
	if _, err := Apply(ConfigPath(), []Profile{gated}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: sp, Login: "never",
		Argv: []string{"true"}, Quiet: true}); err == nil {
		t.Fatal("PlanExec found a withheld profile in ~/.aws/config")
	}
	if _, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: sp, Login: "never",
		Argv: []string{"true"}, Quiet: true, GatedConfig: GatedConfigPath("prod")}); err != nil {
		t.Fatalf("PlanExec of a withheld profile: %v", err)
	}
}

// A run of a withheld profile leaves none of its role credentials in the CLI's cache.
func TestForgetRoleCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("HOME"), ".aws", "cli", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sso := prof("src")
	ssoFile := ssoRoleCachePath(sso.AccountID, sso.RoleName, "af-src")
	assumed := filepath.Join(dir, "abc.json")
	other := filepath.Join(dir, "other.json")
	for p, body := range map[string]string{
		ssoFile: `{"ProviderType":"sso"}`,
		assumed: `{"AssumedRoleUser":{"Arn":"arn:aws:sts::210987654321:assumed-role/deploy/botocore-session-1"}}`,
		other:   `{"AssumedRoleUser":{"Arn":"arn:aws:sts::210987654321:assumed-role/reader/x"}}`,
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chain := chainProf()
	ForgetRoleCredentials(map[string]Profile{"src": sso, "deploy": chain}, "deploy")
	for _, p := range []string{ssoFile, assumed} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s stayed", p)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another role's cache went: %v", err)
	}
}

// UsableNow follows the files, not only the last sync: a withheld profile the member then
// defines in their own ~/.aws is theirs, and the Agent's routes refuse it, as they refuse an
// exported one that is no longer in the block.
func TestUsableNowFollowsTheMembersFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	gated := prof("prod-admin")
	gated.ConfirmBeforeUse = true
	if _, err := Apply(path, []Profile{gated, prof("sandbox")}); err != nil {
		t.Fatal(err)
	}
	if u := UsableNow(); !slices.Contains(u, "prod-admin") || !slices.Contains(u, "sandbox") {
		t.Fatalf("UsableNow = %v", u)
	}
	for name, edit := range map[string]func(){
		"own profile": func() {
			b, _ := os.ReadFile(path)
			os.WriteFile(path, append([]byte("[profile prod-admin]\nregion = us-east-1\n\n"), b...), 0o600)
		},
		"own sso-session": func() {
			b, _ := os.ReadFile(path)
			os.WriteFile(path, append([]byte("[sso-session af-prod-admin]\nsso_region = us-east-1\n\n"), b...), 0o600)
		},
		"credentials": func() {
			os.WriteFile(filepath.Join(filepath.Dir(path), "credentials"), []byte("[prod-admin]\naws_access_key_id = x\n"), 0o600)
		},
	} {
		if _, err := Apply(path, []Profile{gated, prof("sandbox")}); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(filepath.Dir(path), "credentials"))
		b, _ := os.ReadFile(path)
		user, _ := stripBlock(string(b))
		os.WriteFile(path, []byte(user), 0o600)
		if _, err := Apply(path, []Profile{gated, prof("sandbox")}); err != nil {
			t.Fatal(err)
		}
		edit()
		if slices.Contains(UsableNow(), "prod-admin") {
			t.Errorf("%s: a withheld profile the member now defines is still usable", name)
		}
		os.WriteFile(path, nil, 0o600)
		os.Remove(filepath.Join(filepath.Dir(path), "credentials"))
	}
}
