package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// 鍵は起動時に env 注入されるので、保存/削除しただけでは動いている daemon に効かない。
// 実測: Console からキーを消しても daemon は自分の環境に持ったままで、connections[] に
// env 接続を出し続け、そのキーで課金され得るモデルも一覧に残る（Agent を再起動しても
// Ensure が生きた daemon を adopt するので直らない）。反映パスは Supervisor.Restart。
func TestApplyKeyChangeRestartsServeAndDropsCatalog(t *testing.T) {
	modelsMu.Lock()
	modelsList, modelsAt = []string{"opencode/stale"}, time.Now()
	modelsMu.Unlock()

	got := make(chan string, 1)
	orig := restartServe
	restartServe = func(reason string) { got <- reason }
	defer func() { restartServe = orig }()

	applyKeyChange("provider key removed: OPENCODE_API_KEY")

	select {
	case reason := <-got:
		if reason == "" {
			t.Error("再起動の理由が空")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("鍵の変更で serve を再起動していない — 消したキーが daemon に残り続ける")
	}
	modelsMu.Lock()
	stale := !modelsAt.IsZero()
	modelsMu.Unlock()
	if stale {
		t.Error("鍵の変更後もモデルキャッシュが有効なまま")
	}
}

// 無料枠では opencode.ai の鍵を注入しない — 「無料枠で使う」と決めたワークスペースが、
// 鍵が保存されたままというだけで課金経路に乗ってしまわないように。他プロバイダの鍵は
// 利用者自身の課金なので、どの枠でも触らない。
func TestEnvDropsOpencodeKeyOnlyForFreeUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Opencode = map[string]string{"OPENCODE_API_KEY": "sk-oc", "ANTHROPIC_API_KEY": "sk-an"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	orig := UsagePref
	defer func() { UsagePref = orig }()

	UsagePref = func() string { return UsageZen }
	if got := env(); len(got) != 2 {
		t.Errorf("zen で鍵が落ちた: %v", redactEnv(got))
	}
	UsagePref = func() string { return UsageGo }
	if got := env(); len(got) != 2 {
		t.Errorf("go で鍵が落ちた: %v", redactEnv(got))
	}
	UsagePref = func() string { return UsageFree }
	got := env()
	if len(got) != 1 || !strings.HasPrefix(got[0], "ANTHROPIC_API_KEY=") {
		t.Errorf("free = %v, want ANTHROPIC_API_KEY だけ", redactEnv(got))
	}
}

// redactEnv keeps assertions readable without printing key material.
func redactEnv(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name, _, _ := strings.Cut(e, "=")
		out = append(out, name+"=…")
	}
	return out
}
