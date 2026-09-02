// routes_golden_test.go — buildMux が登録する (method, path) を全件ゴールデン化する。
//
// なぜ要るか（ADR 0067 決定 6）。並列リファクタの移送は 1 PR で数千行動く。レビュワーは
// それを読み切れないので、**ワイヤ互換だけは機械に証明させる**。ルート表はワイヤの
// 半分（もう半分は DTO のキー集合 = wire_golden_test.go）で、ハンドラを internal/ へ
// 移す途中で register 呼び出しを 1 本落としても、それ以外のテストは全部緑のまま通る
// ——ここが赤くなる状態を先に作っておく。
//
// ★ 静的解析（ソースの HandleFunc を grep する）ではなく、**組み上がった mux から**
// 取る。register 関数が internal/ へ移っても、登録された表そのものを見ているので
// 偽の赤にならない。代償は net/http の内部表現に触れること（下の muxRoutes 参照）。
//
// 更新の仕方（ルートを意図して増減させたとき）:
//
//	cd control-plane && go test -run TestRouteTableGolden -update-routes-golden ./...
//
// 生成された差分は PR に載せること。**差分が意図と違うなら、それが検知したかった事故**。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateRoutesGolden = flag.Bool("update-routes-golden", false,
	"testdata/routes.golden を実際のルート表で書き換える（ルートを意図して増減させたときだけ）")

const routesGoldenPath = "testdata/routes.golden"

// TestRouteTableGolden: buildMux の全ルートが testdata/routes.golden と一致すること。
//
// AF_MCP_ENABLED は **CP で唯一の env 条件付きルート**（registerMCPRoutes）なので、
// ゴールデンは「全部入り」を撮るために true で撮る。off のときの差が /mcp の 1 本だけで
// あることは TestRouteTableMCPIsTheOnlyOptIn が別に固定する。
func TestRouteTableGolden(t *testing.T) {
	t.Setenv("AF_MCP_ENABLED", "true")
	_, mux := smokeEnv(t)
	got := muxRoutes(t, mux)

	if *updateRoutesGolden {
		writeRoutesGolden(t, routesGoldenPath, got)
		t.Logf("wrote %s (%d routes)", routesGoldenPath, len(got))
		return
	}
	assertGoldenLines(t, routesGoldenPath, got)
}

// TestRouteTableMCPIsTheOnlyOptIn: env で表が変わるのは /mcp だけ。
// 条件付き登録が増えると「ゴールデンは緑なのに本番の表が違う」が起きるので、
// **条件付きが増えたこと自体**をここで赤くする。
func TestRouteTableMCPIsTheOnlyOptIn(t *testing.T) {
	t.Setenv("AF_MCP_ENABLED", "")
	_, mux := smokeEnv(t)
	off := muxRoutes(t, mux)

	want := []string{}
	for _, r := range readGoldenLines(t, routesGoldenPath) {
		if r != "ANY /mcp" {
			want = append(want, r)
		}
	}
	if diff := lineDiff(want, off); diff != "" {
		t.Errorf("AF_MCP_ENABLED 無効時のルート表がゴールデン−/mcp と一致しない"+
			"（env で分岐するルートが増えた？）:\n%s", diff)
	}
}

// muxRoutes は組み上がった *http.ServeMux から登録済みの (method, path) を取り出す。
//
// ⚠️ net/http の**内部表現**に reflect で触る。公開 API に列挙は無く（Handler() は
// 1 リクエストぶんの照合しか返さない）、ここだけが「登録された全件」を知る方法である。
// 内部表現は実際に変わる: Go 1.25 まで在った ServeMux.patterns スライスは 1.26 で
// 消えており、今は tree（routingNode）を歩くしかない。壊れたときは黙って空を返さず、
// **何が変わったか分かるメッセージで落とす**こと——0 件のゴールデンほど危険なものはない。
func muxRoutes(t *testing.T, mux *http.ServeMux) []string {
	t.Helper()
	root := reflect.ValueOf(mux).Elem().FieldByName("tree")
	if !root.IsValid() {
		t.Fatalf("http.ServeMux に tree フィールドが無い: net/http の内部表現が変わった。" +
			"go/src/net/http/routing_tree.go を読んで muxRoutes を直すこと")
	}
	var raw []string
	if err := walkRoutingNode(root, &raw); err != nil {
		t.Fatalf("routingNode の走査に失敗: %v（net/http の内部表現が変わった）", err)
	}
	if len(raw) == 0 {
		t.Fatal("ルートが 1 本も取れなかった: 走査が壊れている（buildMux は必ず登録する）")
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		line := routeLine(p)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sortRouteLines(out)
	return out
}

// routeLine は登録文字列（"GET /api/x" / "/api/x/"）を "METHOD PATH" に正規化する。
// メソッド無し（全メソッド受け）は ANY と書く——空欄だと golden の行頭が揃わず、
// 「メソッドが消えた」のか「元から無い」のかが読めなくなる。
func routeLine(pattern string) string {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || strings.HasPrefix(pattern, "/") {
		return "ANY " + pattern
	}
	return method + " " + strings.TrimSpace(path)
}

// sortRouteLines: パス順 → メソッド順。同じパスのメソッド違いが隣り合うので、
// 「DELETE だけ落ちた」が diff で 1 行に見える。
func sortRouteLines(lines []string) {
	key := func(s string) (string, string) {
		m, p, _ := strings.Cut(s, " ")
		return p, m
	}
	sort.Slice(lines, func(i, j int) bool {
		pi, mi := key(lines[i])
		pj, mj := key(lines[j])
		if pi != pj {
			return pi < pj
		}
		return mi < mj
	})
}

// walkRoutingNode は net/http の routingNode 木を降りて、葉が持つ pattern.str を集める。
// 子は children（mapping: 8 件までは slice s、超えると map m に切り替わる — **両方見る**）
// と multiChild / emptyChild。
func walkRoutingNode(n reflect.Value, out *[]string) error {
	if !n.IsValid() {
		return nil
	}
	if n.Kind() == reflect.Pointer {
		if n.IsNil() {
			return nil
		}
		n = n.Elem()
	}
	if n.Kind() != reflect.Struct {
		return fmt.Errorf("routingNode が struct でない: %s", n.Kind())
	}
	pat := n.FieldByName("pattern")
	if !pat.IsValid() {
		return fmt.Errorf("routingNode に pattern フィールドが無い")
	}
	if !pat.IsNil() {
		str := pat.Elem().FieldByName("str")
		if !str.IsValid() {
			return fmt.Errorf("pattern に str フィールドが無い")
		}
		*out = append(*out, str.String())
	}
	if ch := n.FieldByName("children"); ch.IsValid() {
		s := ch.FieldByName("s")
		if !s.IsValid() {
			return fmt.Errorf("mapping に s フィールドが無い")
		}
		for i := 0; i < s.Len(); i++ {
			if err := walkRoutingNode(s.Index(i).FieldByName("value"), out); err != nil {
				return err
			}
		}
		m := ch.FieldByName("m")
		if !m.IsValid() {
			return fmt.Errorf("mapping に m フィールドが無い")
		}
		if !m.IsNil() {
			for _, k := range m.MapKeys() {
				if err := walkRoutingNode(m.MapIndex(k), out); err != nil {
					return err
				}
			}
		}
	}
	if err := walkRoutingNode(n.FieldByName("multiChild"), out); err != nil {
		return err
	}
	return walkRoutingNode(n.FieldByName("emptyChild"), out)
}

// --- golden ファイルの読み書き（このパッケージの他のゴールデンからも使う） ---

// readGoldenLines は `#` 始まりのコメントと空行を捨てて行を返す。
func readGoldenLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v（初回は -update-routes-golden で生成する）", path, err)
	}
	var out []string
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func writeRoutesGolden(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var b strings.Builder
	b.WriteString("# buildMux() が登録する (method, path) の全件。生成物 —— 手で編集しない。\n")
	b.WriteString("# 更新: cd control-plane && go test -run TestRouteTableGolden -update-routes-golden ./...\n")
	b.WriteString("# ANY = メソッド指定なしの登録。AF_MCP_ENABLED=true で撮っている。\n")
	fmt.Fprintf(&b, "# count: %d\n", len(lines))
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertGoldenLines(t *testing.T, path string, got []string) {
	t.Helper()
	if diff := lineDiff(readGoldenLines(t, path), got); diff != "" {
		t.Errorf("%s と一致しない:\n%s\n"+
			"意図した増減なら -update-routes-golden で撮り直す。"+
			"身に覚えが無いなら、移送でルートを落としている。", path, diff)
	}
}

// lineDiff は want / got の集合差を "- 消えた行 / + 増えた行" で返す（一致なら空）。
func lineDiff(want, got []string) string {
	inWant := map[string]bool{}
	for _, s := range want {
		inWant[s] = true
	}
	inGot := map[string]bool{}
	for _, s := range got {
		inGot[s] = true
	}
	var lost, added []string
	for _, s := range want {
		if !inGot[s] {
			lost = append(lost, "- "+s)
		}
	}
	for _, s := range got {
		if !inWant[s] {
			added = append(added, "+ "+s)
		}
	}
	if len(lost) == 0 && len(added) == 0 {
		return ""
	}
	return strings.Join(append(lost, added...), "\n")
}
