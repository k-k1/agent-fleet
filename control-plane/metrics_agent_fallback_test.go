// metrics_agent_fallback_test.go — ホストの cgroup が読めない構成（ECS 全般）で
// リソースの実測値が Agent から来ることの契約（docs/63 §63.9）。
//
// これが崩れると症状は「稼働中なのにメモリ / CPU / ディスクが 3 つとも –」で、
// 例外もログも出ない——タイルが空欄なだけなので、壊れたことに誰も気付けない。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// agentStatsRuntime は「API 越しに State は分かるが docker からは見えない」という
// クラウド系アダプタの形に、Agent の口（Endpoint）を足したもの。
type agentStatsRuntime struct {
	stubRuntime
	state string
}

func (r agentStatsRuntime) State(context.Context) string { return r.state }

type agentStatsFactory struct {
	endpoint string
	state    string
}

func (f agentStatsFactory) New(Workspace, string, []string) Runtime {
	return agentStatsRuntime{stubRuntime: stubRuntime{endpoint: f.endpoint}, state: f.state}
}

// statsAgent は GET /workspace/stats だけ答える Agent。body をそのまま返す。
func statsAgent(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace/stats" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func memberStatsJSON(t *testing.T, mgr *manager) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
	r.SetPathValue("slug", "sales")
	r.SetPathValue("key", "leaver-acme-co-jp")
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberStats(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 本題。docker で見えない Workspace でも、Agent が自分の cgroup から読んだ値が
// メンバー詳細に載ること。
func TestMemberStatsFallsBackToTheAgentWhenTheHostCannotSeeTheCgroup(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":1073741824,"mem_max":4294967296,"cpu_pct":12.5,"oom_kill_total":0,"disk_used":21474836480,"disk_total":42949672960}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if out["running"] != true {
		t.Fatalf("running = %v, want true", out["running"])
	}
	if out["mem_used"] != float64(1073741824) {
		t.Errorf("mem_used = %v, want 1073741824", out["mem_used"])
	}
	if out["mem_max"] != float64(4294967296) {
		t.Errorf("mem_max = %v, want 4294967296", out["mem_max"])
	}
	if out["cpu_pct"] != 12.5 {
		t.Errorf("cpu_pct = %v, want 12.5", out["cpu_pct"])
	}
	// du はこの構成では対象パスが CP に無いので、ディスクは Agent の statfs が出どころ。
	// 容量まで返るのが `ecs-ec2` の要点（home = 永続 EBS）で、画面はこれを分母に使う。
	if out["disk_used"] != float64(21474836480) || out["disk_total"] != float64(42949672960) {
		t.Errorf("disk = %v/%v, want 21474836480/42949672960", out["disk_used"], out["disk_total"])
	}
}

// cpu_pct 0 と「CPU が測れない」は**別**。ゼロ値の省略でこれを潰すと、画面は
// 測れないものを 0% として描いてしまう。
func TestZeroGaugesSurviveTheWire(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":100,"cpu_pct":0,"oom_kill_total":0}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if v, ok := out["cpu_pct"]; !ok || v != float64(0) {
		t.Errorf("cpu_pct = %v (present=%v), want 0 present", v, ok)
	}
	if v, ok := out["oom_kill_total"]; !ok || v != float64(0) {
		t.Errorf("oom_kill_total = %v (present=%v), want 0 present", v, ok)
	}
}

// 測れなかった軸はキーごと落ちる。CP が代わりに 0 を置くと、区別が消える。
func TestUnmeasuredAxesStayAbsent(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":100}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	for _, key := range []string{"cpu_pct", "mem_max", "disk_total"} {
		if v, ok := out[key]; ok {
			t.Errorf("%s = %v, want the key absent", key, v)
		}
	}
}

// CP がイメージより新しいときは Agent にこのルートが無い（404）。エラー body の
// フィールドを混ぜず、黙って「測れない」に倒すこと。
func TestOlderAgentWithoutTheRouteDegrades(t *testing.T) {
	srv := statsAgent(t, http.StatusNotFound, `{"error":"not_found"}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if out["running"] != true {
		t.Errorf("running = %v, want true — 版ずれでも稼働の事実は変わらない", out["running"])
	}
	if _, ok := out["mem_used"]; ok {
		t.Errorf("mem_used present (%v) from a 404 body", out["mem_used"])
	}
}

// 止まっている Workspace には問い合わせない。届かない相手を毎 tick 叩けば、
// タイムアウトぶんだけ SSE の tick が遅れる（4 秒周期に 5 秒の待ちが入る）。
func TestStoppedWorkspaceIsNotProbed(t *testing.T) {
	probed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probed = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mem_used":1}`))
	}))
	defer srv.Close()
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "stopped"})

	out := memberStatsJSON(t, mgr)
	if probed {
		t.Error("stopped workspace was probed for stats")
	}
	if out["running"] != false {
		t.Errorf("running = %v, want false", out["running"])
	}
}
