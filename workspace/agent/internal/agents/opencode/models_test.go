package opencode

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestParseModels(t *testing.T) {
	out := `opencode/big-pickle
opencode/deepseek-v4-flash-free

anthropic/claude-sonnet-4-5
WARN some warning line
  opencode/hy3-free
not-a-model
`
	got := parseModels(out)
	want := []string{
		"opencode/big-pickle",
		"opencode/deepseek-v4-flash-free",
		"anthropic/claude-sonnet-4-5",
		"opencode/hy3-free",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModels = %v, want %v", got, want)
	}
}

func TestParseModelsEmpty(t *testing.T) {
	if got := parseModels(""); len(got) != 0 {
		t.Fatalf("parseModels(empty) = %v, want []", got)
	}
}

func TestMergeCommandEnv(t *testing.T) {
	got := mergeCommandEnv(
		[]string{"PATH=/usr/bin", "OPENCODE_API_KEY=old", "KEEP=yes"},
		[]string{"OPENCODE_API_KEY=stored", "ANTHROPIC_API_KEY=anthropic", "invalid"},
	)
	want := []string{
		"PATH=/usr/bin",
		"OPENCODE_API_KEY=stored",
		"KEEP=yes",
		"ANTHROPIC_API_KEY=anthropic",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeCommandEnv = %v, want %v", got, want)
	}
}

// The daemon's listing includes deprecated entries (measured: 31 of 110). They must be
// dropped to line up with the CLI's output (79), otherwise retired models show up in the
// launch list.
func TestFilterDaemonModels(t *testing.T) {
	no, yes := false, true
	got := filterDaemonModels([]daemonModel{
		{ID: "gpt-5.6-luna", ProviderID: "opencode-go", Status: "active"},
		{ID: "hy3-free", ProviderID: "opencode", Status: "deprecated"},
		{ID: "kimi-k3", ProviderID: "opencode-go", Status: "active", Enabled: &yes},
		{ID: "paid-locked", ProviderID: "opencode", Status: "active", Enabled: &no},
		{ID: "", ProviderID: "opencode", Status: "active"},
		{ID: "orphan", ProviderID: "", Status: "active"},
	})
	want := []string{"opencode-go/gpt-5.6-luna", "opencode-go/kimi-k3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterDaemonModels = %v, want %v", got, want)
	}
}

// A running daemon is authoritative: a one-shot `opencode models` does not see the Console
// account's login (measured without a key: 8 entries from the CLI, 86 from serve).
func TestModelsPrefersRunningDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/model" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"location":{},"data":[
			{"id":"kimi-k3","providerID":"opencode-go","status":"active"},
			{"id":"hy3-free","providerID":"opencode","status":"deprecated"}]}`))
	}))
	defer srv.Close()
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return srv.URL, true }
	defer func() { oauthProbe = orig }()

	modelsMu.Lock()
	modelsList, modelsAt = nil, time.Time{}
	modelsMu.Unlock()
	defer InvalidateModels()

	if got := Models(); !reflect.DeepEqual(got, []string{"opencode-go/kimi-k3"}) {
		t.Fatalf("Models = %v, want [opencode-go/kimi-k3]", got)
	}
}

// With no daemon present it falls back to the CLI as before; it never starts one to fetch.
func TestModelsFallsBackWithoutDaemon(t *testing.T) {
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	if got := modelsFromDaemon(); got != nil {
		t.Fatalf("no daemon present, yet got %v", got)
	}
}

// An id dropped from the catalog keeps its name on record: if the launch guard cannot tell
// a typo from a retired model, the user doubts a correct id and keeps re-entering it.
// Measured: opencode-go/ox-alpha-free was published on 2026-08-21, went deprecated within a
// week, and launches failed with "not available".
func TestRetiredRemembersDroppedIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"location":{},"data":[
			{"id":"kimi-k3","providerID":"opencode-go","status":"active"},
			{"id":"ox-alpha-free","providerID":"opencode-go","status":"deprecated","cost":[{"input":0}]},
			{"id":"paid-locked","providerID":"opencode","status":"active","enabled":false}]}`))
	}))
	defer srv.Close()
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return srv.URL, true }
	defer func() { oauthProbe = orig }()

	modelsMu.Lock()
	modelsList, modelsAt, retiredIDs = nil, time.Time{}, nil
	modelsMu.Unlock()
	defer InvalidateModels()

	if got := Models(); !reflect.DeepEqual(got, []string{"opencode-go/kimi-k3"}) {
		t.Fatalf("Models = %v, want [opencode-go/kimi-k3]", got)
	}
	if !Retired("opencode-go/ox-alpha-free") {
		t.Fatal("a deprecated id was not remembered as retired")
	}
	if !Retired("opencode/paid-locked") {
		t.Fatal("an enabled:false id was not remembered as retired")
	}
	// A live id, and an id that was never in the catalog at all, are not retired. Getting
	// this wrong labels a typo as "no longer available".
	if Retired("opencode-go/kimi-k3") || Retired("opencode-go/typo") {
		t.Fatal("a live / nonexistent id was judged retired")
	}
}

// The catalog command must not let the AWS credential chain resolve. opencode turns its
// built-in `amazon-bedrock` provider on whenever it does, and every ECS task carries
// AWS_CONTAINER_CREDENTIALS_RELATIVE_URI — measured on the production deployment, that made
// `opencode models` take 45 seconds against a 10-second budget, so it was killed every time
// and the launch picker showed "default only" (docs/log/64 §64.43.8).
func TestWithoutAWSCredentialChain(t *testing.T) {
	t.Setenv("AF_OPENCODE_SHOW_BEDROCK", "")
	got := withoutAWSCredentialChain([]string{
		"PATH=/usr/bin",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/v2/credentials/abc",
		"AWS_ACCESS_KEY_ID=AKIAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY=secret",
		"AWS_SESSION_TOKEN=token",
		"AWS_PROFILE=default",
		"AWS_REGION=ap-northeast-1",
		"OPENCODE_API_KEY=stored",
	})
	want := []string{
		"PATH=/usr/bin",
		// AWS_REGION stays: it names a region, it does not authenticate. Dropping it would
		// change behaviour for anything that legitimately reads it.
		"AWS_REGION=ap-northeast-1",
		"OPENCODE_API_KEY=stored",
		"AWS_EC2_METADATA_DISABLED=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutAWSCredentialChain = %v, want %v", got, want)
	}
}

// IMDS is not an environment variable, and on ecs-ec2 the slot's instance profile answers it.
// Dropping the container variables alone would therefore leave the chain resolvable.
func TestWithoutAWSCredentialChainClosesIMDS(t *testing.T) {
	t.Setenv("AF_OPENCODE_SHOW_BEDROCK", "")
	got := withoutAWSCredentialChain([]string{"PATH=/usr/bin"})
	if len(got) == 0 || got[len(got)-1] != "AWS_EC2_METADATA_DISABLED=true" {
		t.Fatalf("IMDS not disabled: %v", got)
	}
}

// The escape hatch has to be honoured here as well as in keepInMenu. Honouring it in only one
// of the two would mean the flag puts amazon-bedrock/* back in the menu while this function
// has already made the command unable to see it.
func TestWithoutAWSCredentialChainRespectsShowBedrock(t *testing.T) {
	t.Setenv("AF_OPENCODE_SHOW_BEDROCK", "1")
	in := []string{"PATH=/usr/bin", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/v2/credentials/abc"}
	got := withoutAWSCredentialChain(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("AF_OPENCODE_SHOW_BEDROCK=1 must leave the chain alone: %v", got)
	}
}

func TestFirstLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"   \n  ", ""},
		{"boom", ": boom"},
		{"first\nsecond", ": first"},
	} {
		if got := firstLine(tc.in); got != tc.want {
			t.Fatalf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
