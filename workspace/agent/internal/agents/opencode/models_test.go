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

// daemon の一覧は「非推奨」も含む（実測 110 件中 31 件）。CLI の出力（79 件）と
// 揃うよう落とすこと — 揃えないと起動一覧に廃止済みモデルが並ぶ。
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

// 稼働中の daemon があるならそちらが正: 一発起動の `opencode models` は Console
// アカウントのログインを見ないため（鍵なしで CLI 8 件 / serve 86 件を実測）。
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

// daemon が居なければ従来どおり CLI に落ちる（起動して取りに行ったりはしない）。
func TestModelsFallsBackWithoutDaemon(t *testing.T) {
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return "", false }
	defer func() { oauthProbe = orig }()
	if got := modelsFromDaemon(); got != nil {
		t.Fatalf("daemon 不在なのに %v", got)
	}
}

// カタログから落とした id は名前だけ覚えておく: 起動ガードが「打ち間違い」と「提供終了」を
// 区別できないと、利用者は正しい id を疑って再指定を繰り返す（実例: 2026-08-21 公開の
// opencode-go/ox-alpha-free が 1 週間で deprecated になり、起動が「利用できません」で落ちた）。
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
		t.Fatal("deprecated な id を退役として覚えていない")
	}
	if !Retired("opencode/paid-locked") {
		t.Fatal("enabled:false の id を退役として覚えていない")
	}
	// 生きている id と、そもそもカタログに無い id は退役ではない — ここを取り違えると
	// 打ち間違いに「提供終了」と表示してしまう。
	if Retired("opencode-go/kimi-k3") || Retired("opencode-go/typo") {
		t.Fatal("生きている / 存在しない id を退役と判定した")
	}
}
