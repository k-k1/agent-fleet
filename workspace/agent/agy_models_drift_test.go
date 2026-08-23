//go:build drift

// agy のモデルカタログのドリフト検知（Tier 1）— agy_pane_drift_test.go の兄弟。
// build tag `drift` で通常の `go test ./...` から除外される（実 agy バイナリと
// 実サインインが要る。`agy models` は認証付き API を叩く）。
//
// ここが埋めるのは **`agy models` の出力形式への依存**。1.1.19 でこれが
// 「表示名だけ」から「id<TAB>表示名」へ変わり、行を丸ごと `--model` に渡して
// いた製品コードは CLI に既定へフォールバックされていた（docs/70 §70.14.8）。
//
// ⚠️ 気づけなかった理由がそのままこのテストの存在理由である: **セッションは
// 起動し、動き、黙って別のモデルだった**。エラーは TUI の警告 1 行だけで、
// Console にも API にも何も出ない。cli-release-watch が新版を見つけて
// agy-contract.yml を回す仕組みは既にあったが、検査していたのは TUI ペインの
// フッタだけで、カタログは誰も見ていなかった。
package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
)

// TestDriftAgyModelsCatalog asserts that what agy.Models() hands the launch picker
// still matches the shape the real CLI prints — and, crucially, that the ids it
// produces are ids rather than whole lines.
func TestDriftAgyModelsCatalog(t *testing.T) {
	needBin(t, "agy")
	if !agy.SignedIn() {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatal("agy is not signed in (E2E_REQUIRE=1 requires the real credential; `agy models` is an authenticated call)")
		}
		t.Skip("agy is not signed in — `agy models` needs a real token")
	}

	raw, err := exec.Command("agy", "models").Output()
	if err != nil {
		t.Fatalf("agy models failed: %v", err)
	}
	list := agy.Models()
	if len(list) == 0 {
		t.Fatal("the catalog is empty — the picker would offer 既定 only")
	}

	twoColumn := bytes.Contains(raw, []byte("\t"))
	t.Logf("agy models: %d entries, two-column=%v", len(list), twoColumn)

	for _, m := range list {
		if m.ID == "" || m.Label == "" {
			t.Fatalf("empty id or label: %+v", m)
		}
		// バナーや注意書きが stdout に現れ始めたら拾ってしまう。いまは
		// "Fetching available models..." は stderr なので .Output() には来ない。
		if strings.HasSuffix(m.ID, "...") || strings.Contains(m.ID, "  ") {
			t.Fatalf("this does not look like a model, it looks like prose: %q", m.ID)
		}
		if !twoColumn {
			// 旧形式（表示名がそのまま --model に通る）。id == label が契約。
			if m.ID != m.Label {
				t.Fatalf("single-column output but id != label: %+v", m)
			}
			continue
		}
		// ⚠️ 本命。2 カラム形式で id に空白が混じっているなら、それは行を
		// 丸ごと渡しているということで、CLI は黙って既定へ落とす。
		if strings.ContainsAny(m.ID, " \t") {
			t.Fatalf("id contains whitespace — the whole line is being passed as --model: %q", m.ID)
		}
		if m.ID == m.Label {
			t.Fatalf("two-column output but id == label; the split did not happen: %+v", m)
		}
	}

	// 列が 3 つ目を生やしたら、いまの Cut は 2 列目に残りを全部詰め込む。
	// 落とすほどではないが、黙って通してよいものでもない。
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if n := strings.Count(ln, "\t"); n > 1 {
			t.Errorf("a line has %d tabs — the catalog grew a column: %q", n+1, ln)
		}
	}
}
