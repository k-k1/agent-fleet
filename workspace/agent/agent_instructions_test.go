package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

const fleetFixture = "# Workspace Guide (operating policy)\n\ndo not delete other sessions' work\n"

// instrEnv isolates HOME and every env-pinned CLI config dir, and points the fleet
// guide at a fixture instead of the image copy.
func instrEnv(t *testing.T) {
	t.Helper()
	isolateAgentConfigDirs(t)
	notes := filepath.Join(t.TempDir(), "workspace-notes.md")
	if err := os.WriteFile(notes, []byte(fleetFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_WORKSPACE_NOTES", notes)
	instrErrs = map[string]string{}
}

// stubRTKOnPath puts a dummy `rtk` at the front of PATH.
//
// reconcileAgentInstructions() re-applies the rtk block from the durable setting
// AND claude.RTKAvailable() (= exec.LookPath("rtk")), so on a host without rtk it
// strips the block again right after the test wrote it. The tests below then
// asserted on whether the *developer's machine* happened to have rtk installed —
// green on a workspace image, red on every CI runner. Stub the binary so the
// reconcile logic is what is under test.
func stubRTKOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rtk"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// doJSON drives the handlers through a mux registered exactly like routes.go, so a
// pattern typo here fails the test rather than silently 404ing in production.
func doJSON(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user-notes", handleUserNotesGet)
	mux.HandleFunc("PUT /user-notes", handleUserNotesPut)
	mux.HandleFunc("GET /user-notes/preview", handleUserNotesPreview)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

func TestReconcileDeliversToEverySupportedKind(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("Always report in Japanese.\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()

	for _, tc := range []struct{ name, path string }{
		{"claude", claude.UserInstructionsPath()},
		{"codex", codex.AgentsPath()},
		{"opencode", instrOpencodeFile()},
		{"copilot", copilot.UserInstructionsPath()},
		{"agy", agy.AgentsPath()},
		{"kiro", kiro.UserInstructionsPath()},
	} {
		if got := read(t, tc.path); !strings.Contains(got, "Always report in Japanese.") {
			t.Fatalf("%s: user text missing from %s:\n%s", tc.name, tc.path, got)
		}
	}
	// opencode は AGENTS.md を触らず、設定の instructions から参照する。
	if strings.Contains(read(t, opencode.AgentsPath()), "Always report in Japanese.") {
		t.Fatal("opencode: user text must not be composed into AGENTS.md")
	}
	if !opencodeRefers(instrOpencodeFile()) {
		t.Fatalf("opencode: config does not reference the AF file:\n%s", read(t, opencode.ConfigPath()))
	}
	// claude の managed policy 側は別レイヤ。AF は user memory にだけ書く。
	if !strings.Contains(read(t, claude.UserInstructionsPath()), "workspace guide wins") {
		t.Fatal("claude: precedence sentence missing")
	}
}

func TestReconcileComposesFleetGuideForCodexAndOpencode(t *testing.T) {
	instrEnv(t)
	reconcileAgentInstructions()
	for _, path := range []string{codex.AgentsPath(), opencode.AgentsPath(), agy.AgentsPath()} {
		got := read(t, path)
		if !strings.Contains(got, "do not delete other sessions' work") {
			t.Fatalf("%s: fleet guide missing:\n%s", path, got)
		}
		if !strings.Contains(got, "<!-- agent-fleet:fleet -->") {
			t.Fatalf("%s: fleet content must be marked so the user's own text survives", path)
		}
	}
}

// ★ 実害②の回帰: agy / copilot / kiro はワークスペースの運用方針を一切読んでいなかった。
// フリート方針はオペレーター所有なので、ユーザー指示が空でも・適用先を全部外しても配る。
func TestFleetGuideReachesTheKindsThatHadNone(t *testing.T) {
	instrEnv(t)
	off := false
	if err := userinstr.SavePrefs(userinstr.Prefs{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()

	for _, tc := range []struct{ name, path string }{
		{"copilot", copilot.FleetNotesPath()},
		{"kiro", kiro.FleetNotesPath()},
	} {
		if got := read(t, tc.path); !strings.Contains(got, "do not delete other sessions' work") {
			t.Fatalf("%s: fleet guide missing from %s:\n%s", tc.name, tc.path, got)
		}
	}
	if !strings.Contains(read(t, agy.AgentsPath()), "do not delete other sessions' work") {
		t.Fatal("agy: fleet guide missing")
	}
	// ユーザー指示は切ってあるので、そちらの artifact は無いままであること。
	if read(t, copilot.UserInstructionsPath()) != "" || read(t, kiro.UserInstructionsPath()) != "" {
		t.Fatal("user artifacts must not appear while the master switch is off")
	}
}

// agy の 1 ファイルに fleet / user / rtk が同居しても、順序と個数が壊れないこと。
func TestAgyFileHoldsFleetUserAndRTKInOrder(t *testing.T) {
	instrEnv(t)
	stubRTKOnPath(t)
	if err := userinstr.SaveText("AGYORDER\n"); err != nil {
		t.Fatal(err)
	}
	agy.ApplyRTK(true)
	reconcileAgentInstructions()
	reconcileAgentInstructions() // 冪等
	got := read(t, agy.AgentsPath())
	fleet := strings.Index(got, "<!-- agent-fleet:fleet -->")
	user := strings.Index(got, "<!-- agent-fleet:user-notes -->")
	rtk := strings.Index(got, "<!-- agent-fleet:rtk -->")
	if fleet < 0 || user < 0 || rtk < 0 || !(fleet < user && user < rtk) {
		t.Fatalf("order must be fleet<user<rtk (%d,%d,%d):\n%s", fleet, user, rtk, got)
	}
	if n := strings.Count(got, "<!-- agent-fleet:fleet -->"); n != 1 {
		t.Fatalf("fleet block written %d times:\n%s", n, got)
	}
}

// ★ 実害①の回帰: 利用者が AGENTS.md へ書き足した文章を、起動のたびに消さない。
func TestReconcilePreservesUserWrittenTextInAgentsFile(t *testing.T) {
	instrEnv(t)
	path := codex.AgentsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# my own codex notes\n\nprefer table output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	reconcileAgentInstructions() // 2 回目も同じ（冪等）
	got := read(t, path)
	if !strings.Contains(got, "prefer table output") {
		t.Fatalf("hand-written text was destroyed:\n%s", got)
	}
	if n := strings.Count(got, "<!-- agent-fleet:fleet -->"); n != 1 {
		t.Fatalf("fleet block written %d times:\n%s", n, got)
	}
}

// ★ 移行の回帰: cp -f 時代の生のフリート方針は 1 度だけ剥がし、二重に積まない。
func TestReconcileMigratesLegacyCopiedGuide(t *testing.T) {
	instrEnv(t)
	path := codex.AgentsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "# Workspace Guide (operating policy)\n\nOLD IMAGE TEXT\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	got := read(t, path)
	if strings.Contains(got, "OLD IMAGE TEXT") {
		t.Fatalf("stale guide from the previous image survived:\n%s", got)
	}
	if n := strings.Count(got, "# Workspace Guide (operating policy)"); n != 1 {
		t.Fatalf("guide appears %d times (duplication):\n%s", n, got)
	}
}

func TestCodexFileOrderIsFleetThenUserThenRTK(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("USERTEXT\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	got := read(t, codex.AgentsPath())
	fleet := strings.Index(got, "<!-- agent-fleet:fleet -->")
	user := strings.Index(got, "<!-- agent-fleet:user-notes -->")
	if fleet < 0 || user < 0 || fleet > user {
		t.Fatalf("fleet block must precede the user block (%d,%d):\n%s", fleet, user, got)
	}
	if rtk := strings.Index(got, "<!-- agent-fleet:rtk -->"); rtk >= 0 && rtk < user {
		t.Fatal("rtk block must stay last")
	}
}

// 適用先を外したら残骸を残さない（「外したのにまだ効いている」を作らない）。
func TestUntickingATargetRemovesItsArtifact(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("hello\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if read(t, copilot.UserInstructionsPath()) == "" {
		t.Fatal("precondition: copilot artifact should exist")
	}
	off := false
	if err := userinstr.SavePrefs(userinstr.Prefs{Targets: map[string]*bool{"copilot": &off}}); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if got := read(t, copilot.UserInstructionsPath()); got != "" {
		t.Fatalf("copilot artifact survived being unticked:\n%s", got)
	}
	if read(t, claude.UserInstructionsPath()) == "" {
		t.Fatal("unticking one kind must not affect the others")
	}
	// opencode 側は設定の参照も外れること。
	if err := userinstr.SavePrefs(userinstr.Prefs{Targets: map[string]*bool{"opencode": &off}}); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if opencodeRefers(instrOpencodeFile()) {
		t.Fatalf("stale reference left in opencode config:\n%s", read(t, opencode.ConfigPath()))
	}
}

// 読めない設定は触らない（整形し直して利用者の記述を壊さない）。効かないことは申告する。
func TestUnreadableOpencodeConfigIsLeftAloneAndReported(t *testing.T) {
	instrEnv(t)
	cfg := opencode.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "{ // a comment opencode allows but encoding/json does not\n  \"permission\": {} }\n"
	if err := os.WriteFile(cfg, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := userinstr.SaveText("hello\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if read(t, cfg) != raw {
		t.Fatalf("af rewrote a config it could not parse:\n%s", read(t, cfg))
	}
	if instrErrs["opencode"] != "config_unreadable" {
		t.Fatalf("failure not reported: %v", instrErrs)
	}
	st := instrState()
	for _, tgt := range st.Targets {
		if tgt.Kind == "opencode" && (tgt.Applied || tgt.Error == "") {
			t.Fatalf("opencode row must show it is not in effect: %+v", tgt)
		}
	}
}

// agy は rtk と同じ ~/.gemini/AGENTS.md を共有する。両方が並び、互いを消さないこと。
func TestAgyUserBlockCoexistsWithRTKBlock(t *testing.T) {
	instrEnv(t)
	stubRTKOnPath(t)
	if err := userinstr.SaveText("AGYTEXT\n"); err != nil {
		t.Fatal(err)
	}
	agy.ApplyRTK(true)
	reconcileAgentInstructions()
	got := read(t, agy.AgentsPath())
	if !strings.Contains(got, "AGYTEXT") {
		t.Fatalf("user text missing:\n%s", got)
	}
	if !strings.Contains(got, "rtk (token saver)") {
		t.Fatalf("rtk block was destroyed by the user block:\n%s", got)
	}
	if u, r := strings.Index(got, "<!-- agent-fleet:user-notes -->"), strings.Index(got, "<!-- agent-fleet:rtk -->"); u > r {
		t.Fatalf("rtk must stay last (%d,%d):\n%s", u, r, got)
	}
}

// kiro は global steering ディレクトリに AF 専用の 1 本を置く。他人の steering は触らない。
func TestKiroLeavesOtherSteeringFilesAlone(t *testing.T) {
	instrEnv(t)
	mine := filepath.Join(filepath.Dir(kiro.UserInstructionsPath()), "team-conventions.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("# team\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := userinstr.SaveText("KIROTEXT\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if !strings.Contains(read(t, kiro.UserInstructionsPath()), "KIROTEXT") {
		t.Fatal("kiro artifact not written")
	}
	if err := userinstr.SaveText(""); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	if read(t, kiro.UserInstructionsPath()) != "" {
		t.Fatal("clearing the body must remove af's steering file")
	}
	if read(t, mine) != "# team\n" {
		t.Fatal("af removed a steering file that is not its own")
	}
}

func TestStateListsUnsupportedKindsWithReasons(t *testing.T) {
	instrEnv(t)
	targets := instrState().Targets
	// cursor だけは実装待ちではなく**構造的に配れない**（ローカルに user 層が無い）。
	want := map[string]string{"cursor": "no_user_scope"}
	for _, tgt := range targets {
		if reason, ok := want[tgt.Kind]; ok {
			if tgt.Supported || tgt.Reason != reason {
				t.Fatalf("%s: want unsupported/%s, got %+v", tgt.Kind, reason, tgt)
			}
			delete(want, tgt.Kind)
		}
	}
	if len(want) > 0 {
		t.Fatalf("unsupported kinds missing from the list (they must not vanish): %v", want)
	}
}

func TestPreviewShowsWhatTheKindActuallyReads(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("PREVIEWME\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	var body struct {
		Path    string `json:"path"`
		Exists  bool   `json:"exists"`
		Content string `json:"content"`
	}
	w := doJSON(t, "GET", "/user-notes/preview?kind=codex", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Exists || !strings.Contains(body.Content, "PREVIEWME") || body.Path != codex.AgentsPath() {
		t.Fatalf("preview did not show the composed file: %+v", body)
	}
	if w := doJSON(t, "GET", "/user-notes/preview?kind=nope", ""); w.Code != 400 {
		t.Fatalf("unknown kind should be 400, got %d", w.Code)
	}
}

func TestPutRejectsOversizeWithItsOwnCode(t *testing.T) {
	instrEnv(t)
	big, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", userinstr.MaxBytes+1)})
	w := doJSON(t, "PUT", "/user-notes", string(big))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "too_large") {
		t.Fatalf("want 400/too_large, got %d %s", w.Code, w.Body.String())
	}
	if userinstr.Load().Text != "" {
		t.Fatal("rejected body was written anyway")
	}
}

func TestPutSavesAndAppliesInOneCall(t *testing.T) {
	instrEnv(t)
	w := doJSON(t, "PUT", "/user-notes", `{"text":"ROUNDTRIP\n"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(read(t, codex.AgentsPath()), "ROUNDTRIP") {
		t.Fatal("PUT did not apply to the artifacts")
	}
	if !strings.Contains(w.Body.String(), "ROUNDTRIP") {
		t.Fatal("PUT response should carry the new snapshot")
	}
}
