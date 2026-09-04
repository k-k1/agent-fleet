// wiretest_dup_test.go — control-plane と workspace/agent の 2 つの写しが
// **漂流していない**ことを機械で保つ。
//
// なぜ写しが 2 つ必要か: モジュール横断の共有 Go パッケージは見送られている
// （ADR 0012 決定 3）。モジュール内では 1 コピーに畳めるが、**モジュール境界は畳めない。**
//
// 🔴 **手書きの複製は漂流する**（運用キット 0.5）。片方だけ直すと、もう片方は
// **両モジュールのテストが独立に緑のまま**古い実装を使い続ける。
//
// 検査は 2 本立てで、**2 本目が 1 本目の穴を構造的に塞ぐ**:
//
//	① 共有区間の byte 比較 — 番兵より下が 1 バイトも違わないこと
//	② ファイル名集合の一致 — 片方にファイルが 1 枚増えたら赤くなる
//
// ①だけだと**守っているのは「区間」であってパッケージではない**——
// 片方の番兵の外や新しいファイルに共有すべき道具を足すと、**緑のまま漂流する**。
// ②はその道を塞ぐ（#346 のレビュワー観察①が、同じファイル内に区間が同居していたため
// 閉じられなかった穴。**こちらは共有単位がパッケージ 1 つなので機械で閉じられる**）。
//
// 番兵より上（package 宣言と import）は**モジュールパスが違うので一致しない**。
// 正規化ではなく**番兵を import の下に置く**方式を採った——正規化は「何を無視してよいか」
// の判断がコードに埋まるが、番兵は境界がソースに見える。
//
// 🔴 **このファイル自身も 2 つの写しで byte 一致している**（相手の位置を定数で持たず、
// リポジトリ根から探すため）。片方にしか置かないと ② が自分自身で落ちる。
package wiretest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ===== 共有区間ここから（control-plane と workspace/agent で byte 一致）=====
// 🔴 この行より下は 2 つのモジュールで **1 バイトも違ってはいけない**
// （wiretest_dup_test.go が検査する）。番兵より上は package 宣言と import だけで、
// モジュールパスが違うため一致しない。

// wiretestSentinel — 共有区間の開始行。各ファイルに 1 度だけ現れる。
//
// 🔴 **連結して書くのは自己参照を切るため。**1 本のリテラルで書くと、
// **この行自身が 2 個目の番兵として数えられて検査が落ちる**（実際に踏んだ）。
// 「検査の中の文字列が検査対象に混ざる」形なので、ソース上で分けておく。
const wiretestSentinel = "// ===== 共有区間ここから" + "（control-plane と workspace/agent で byte 一致）====="

// wiretestCopies — 2 つの写しのリポジトリ根からの位置。
var wiretestCopies = []string{
	filepath.Join("control-plane", "internal", "wiretest"),
	filepath.Join("workspace", "agent", "internal", "wiretest"),
}

func TestWiretestCopiesDoNotDrift(t *testing.T) {
	self, peer := selfAndPeer(t)
	for _, name := range goFiles(t, self) {
		a := mustShared(t, filepath.Join(self, name))
		b := mustShared(t, filepath.Join(peer, name))
		if a != b {
			t.Errorf("%s の共有区間が一致しない（%d バイト 対 %d バイト）。\n"+
				"  片方だけ直すと、両モジュールのテストは独立に緑のまま漂流する。\n"+
				"  どちらかへ揃えること: %s / %s", name, len(a), len(b), self, peer)
		}
	}
}

// TestWiretestCopiesHaveSameFiles — ①の穴を塞ぐ。
// 🔴 byte 比較が守るのは**番兵で囲んだ区間**であって**パッケージ**ではない。
// 片方に新しいファイルを 1 枚足せば、①は全部緑のまま漂流する。
func TestWiretestCopiesHaveSameFiles(t *testing.T) {
	self, peer := selfAndPeer(t)
	got, want := goFiles(t, self), goFiles(t, peer)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("2 つの写しのファイル構成が違う。\n  %s: %v\n  %s: %v\n"+
			"  片方だけにファイルを足すと、byte 比較は全部緑のまま漂流する。", self, got, peer, want)
	}
	if len(got) == 0 {
		t.Fatal(".go が 1 枚も見つからない（走査が壊れている）")
	}
}

// selfAndPeer はリポジトリ根を上へ探し、2 つの写しの実パスを返す。
//
// 🔴 相手の位置を定数で持たない。持つと**このファイル自身が 2 つの写しで違う**ことになり、
// ②（ファイル名集合の一致）を満たすために置いた写しが、①（byte 一致）を壊す。
func selfAndPeer(t *testing.T) (string, string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		a, b := filepath.Join(dir, wiretestCopies[0]), filepath.Join(dir, wiretestCopies[1])
		if isDir(a) && isDir(b) {
			if sameDir(t, wd, a) {
				return a, b
			}
			return b, a
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// 🔴 Skip ではなく Fatal。見つからないまま飛ぶと、
	// 「漂流していない」ではなく「検査していない」が緑になる。
	t.Fatalf("リポジトリ根（%v の両方を持つ階層）が %s から見つからない。"+
		"移動したならこの表を直すこと——見つからないまま緑にはしない。", wiretestCopies, wd)
	return "", ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// mustShared は番兵より下を返す。番兵が無い／2 つ以上あるのは検査の失敗として扱う。
func mustShared(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(b)
	if n := strings.Count(s, wiretestSentinel); n != 1 {
		t.Fatalf("%s の番兵が %d 個（1 個であるべき）。"+
			"番兵が消えると比較範囲が変わり、**検査が黙って別のものを見る**。", path, n)
	}
	return s[strings.Index(s, wiretestSentinel)+len(wiretestSentinel):]
}
