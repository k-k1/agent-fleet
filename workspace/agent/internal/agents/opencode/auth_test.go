package opencode

import (
	"testing"
	"time"
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
