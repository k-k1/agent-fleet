package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func testStencils(t *testing.T) *drawioStencils {
	t.Helper()
	return &drawioStencils{cacheDir: t.TempDir(), loading: map[string]*sync.Mutex{}}
}

// 台帳が壊れていたら他の全部が意味を失うので、まずそこを見る。
func TestDrawioManifestLoads(t *testing.T) {
	m, err := loadDrawioManifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Version == "" || !strings.HasPrefix(m.Base, "https://") {
		t.Fatalf("version/base が空: %+v", m.Version)
	}
	if len(m.Sets) < 150 {
		t.Fatalf("セット数 %d —— 台帳を絞ってはいけない（載っていない名前は 404 ＝ 図が黙って劣化する）", len(m.Sets))
	}
	// ビューアが要求するのは `<basename>.xml` だけ。それ以外を載せると防壁が緩む。
	for name, e := range m.Sets {
		if !strings.HasSuffix(name, ".xml") {
			t.Fatalf("%q は .xml ではない", name)
		}
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			t.Fatalf("%q は台帳の鍵として不正", name)
		}
		if len(e.SHA256) != 64 || e.Size <= 0 {
			t.Fatalf("%q のエントリが不正: %+v", name, e)
		}
	}
	// 実測で見つけた既知の穴: libraries は sap.xml を参照するが upstream に無い。
	// 「台帳の漏れ」と誤読して手で足さないこと（足すと存在しない URL を叩きに行く）。
	if _, ok := m.Sets["sap.xml"]; ok {
		t.Fatalf("sap.xml は upstream v31.1.8 に存在しない —— 台帳に足してはいけない")
	}
	if _, ok := m.Sets["aws4.xml"]; !ok {
		t.Fatalf("aws4.xml が台帳に無い")
	}
}

// 台帳に無い名前は **upstream を一切叩かずに** 404。ここが SSRF の防壁そのもの。
func TestDrawioStencilRejectsUnknownName(t *testing.T) {
	d := testStencils(t)
	for _, name := range []string{
		"evil.xml",
		"../../etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"aws4.xml.bak",
		"AWS4.XML", // 大小文字を寄せない（台帳の鍵はそのまま）
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/drawio/stencils/"+name, nil)
		req.SetPathValue("name", name)
		d.serve(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%q: %d（404 でなければ任意 URL 取得の道具になる）", name, rec.Code)
		}
	}
}

// upstream が台帳と違うバイト列を返したら、保存も配布もしない。
func TestDrawioStencilRejectsTamperedBytes(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"/>")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<shapes name=\"mxgraph.evil\"/>!!")) // 別物
	}))
	defer srv.Close()

	d := testStencils(t)
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{
		"test.xml": {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))},
	}}
	if _, err := d.fetch(context.Background(), m, "test.xml", m.Sets["test.xml"]); err == nil {
		t.Fatal("改竄されたバイト列を受け入れた")
	}
	ents, _ := os.ReadDir(d.cacheDir)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("検証に落ちたのにキャッシュへ残っている: %s", e.Name())
		}
	}
}

// 2 回目はキャッシュから返り、upstream を叩かない。
func TestDrawioStencilCaches(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	d := testStencils(t)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}

	for i := 0; i < 3; i++ {
		got, err := d.fetch(context.Background(), m, "test.xml", entry)
		if err != nil {
			t.Fatalf("%d 回目: %v", i, err)
		}
		if string(got) != string(body) {
			t.Fatalf("%d 回目の中身が違う", i)
		}
	}
	if hits != 1 {
		t.Fatalf("upstream を %d 回叩いた（1 回であるべき）", hits)
	}
	// 置き場所は sha256 の名前（台帳が変われば別ファイルになる）。
	if _, err := os.Stat(filepath.Join(d.cacheDir, entry.SHA256+".xml")); err != nil {
		t.Fatalf("キャッシュファイルが無い: %v", err)
	}
}

// 取れなくても 502 で済ませ、CP は落ちない（閉域での想定された劣化）。
func TestDrawioStencilUpstreamDownIs502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // 到達不能にする

	d := testStencils(t)
	entry := drawioStencilEntry{SHA256: strings.Repeat("0", 64), Size: 10}
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
	if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
		t.Fatal("到達不能な upstream でエラーにならなかった")
	}
}

// 既定束の名前が 1 つでも台帳に無ければ、その行は**黙って何もしない**。
// 綴りを間違えても動いてしまうので、ここで突き合わせる。
func TestDrawioPreseedDefaultBundle(t *testing.T) {
	m, err := loadDrawioManifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	for _, n := range drawioPreseedExact {
		if _, ok := m.Sets[n]; !ok {
			t.Fatalf("既定束の %q が台帳に無い（綴り違いは黙って無視される）", n)
		}
	}
	for _, p := range drawioPreseedPrefixes {
		hit := false
		for name := range m.Sets {
			if strings.HasPrefix(name, p) {
				hit = true
				break
			}
		}
		if !hit {
			t.Fatalf("既定束の接頭辞 %q に当たるセットが 1 つも無い", p)
		}
	}

	names := drawioPreseedNames(m, false)
	var total int64
	for _, n := range names {
		total += m.Sets[n].Size
	}
	// 既定束は「閉域の管理者がとりあえず入れる分」。全件（40.8 MB）に近づいたら
	// 既定である意味が無いし、逆に極端に小さければ束の体をなしていない。
	if total > 25<<20 {
		t.Fatalf("既定束が %.1f MB —— 大きすぎる（--all との差が無い）", float64(total)/(1<<20))
	}
	if len(names) < 20 {
		t.Fatalf("既定束が %d 件しかない", len(names))
	}
	// 巨大で用途の狭いものは既定に入れない。
	for _, n := range names {
		if strings.HasPrefix(n, "rack/hpe_aruba/") {
			t.Fatalf("%q（3.67 MB）は既定束に入れない", n)
		}
	}
	// --all は台帳と一致する。
	if got := len(drawioPreseedNames(m, true)); got != len(m.Sets) {
		t.Fatalf("--all が %d 件（台帳は %d 件）", got, len(m.Sets))
	}
	t.Logf("既定束 %d 件 / %.1f MB", len(names), float64(total)/(1<<20))
}

// 事前投入の眼目は「**外に出られなくても配れる**」こと。upstream を到達不能にしたまま
// 投入済みキャッシュから返せることを見る（これが緑でなければ P1b は何の役にも立たない）。
func TestDrawioStencilPreseededServesOffline(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	// 台帳の base は解決すらできないホストにしておく。1 本でも外に出たら落ちる。
	m := drawioStencilManifest{Base: "http://127.0.0.1:1/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}

	d := testStencils(t)
	if err := d.store(d.pathFor(entry), body); err != nil {
		t.Fatalf("事前投入: %v", err)
	}

	got, err := d.fetch(context.Background(), m, "test.xml", entry)
	if err != nil {
		t.Fatalf("投入済みなのに閉域で返せない: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("中身が違う")
	}

	// 投入されたものが台帳と食い違っていたら（版ずれ・壊れたコピー）、使わない。
	if err := os.WriteFile(d.pathFor(entry), []byte("<shapes/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
		t.Fatal("台帳と食い違うキャッシュをそのまま配った")
	}
}

// store は必ず一時名 → rename。書きかけが正規名で見えると、検証済みの顔をした
// 壊れたバイト列が配られる（事前投入は稼働中の CP と同じディレクトリを触る）。
func TestDrawioStencilStoreIsAtomic(t *testing.T) {
	d := testStencils(t)
	body := []byte("<shapes/>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if err := d.store(d.pathFor(entry), body); err != nil {
		t.Fatalf("store: %v", err)
	}
	ents, err := os.ReadDir(d.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	// 一時ファイルが残っていない ＝ rename されている。
	if len(ents) != 1 || strings.HasPrefix(ents[0].Name(), ".tmp-") {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("キャッシュの中身が %v", names)
	}
	if ents[0].Name() != entry.SHA256+".xml" {
		t.Fatalf("置き場所が %q（内容アドレスであるべき）", ents[0].Name())
	}
}

// upstream の瞬断は再試行する。**1 回の reset で 502 を返すと、Console 側は
// そのセットを「頼んだ済み」にしたまま二度と要求せず、アイコンだけが欠けたままになる。**
// 実測でも raw.githubusercontent の connection reset を踏んだ。
func TestDrawioStencilRetriesTransient(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}

	var tries int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&tries, 1) < 3 {
			// 接続ごと切る（ネットワーク層の失敗＝再試行すべきもの）。
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("hijack できない")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	d := testStencils(t)
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
	got, err := d.fetch(context.Background(), m, "test.xml", entry)
	if err != nil {
		t.Fatalf("2 回切られただけで諦めた: %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("中身が違う")
	}
	if n := atomic.LoadInt32(&tries); n != 3 {
		t.Fatalf("試行 %d 回（3 回であるべき）", n)
	}
}

// 逆に、何度やっても同じ失敗は繰り返さない（404 と、完全に取れたうえでの不一致）。
func TestDrawioStencilDoesNotRetryPermanent(t *testing.T) {
	body := []byte("<shapes/>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}

	for _, tc := range []struct {
		name  string
		hit   func(w http.ResponseWriter)
		tries int32
	}{
		{"404", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }, 1},
		// 長さは合っているのに中身が違う ＝ 途中で切れたのではなく別物。
		{"改竄", func(w http.ResponseWriter) { _, _ = w.Write([]byte("<shapeX/>")) }, 1},
		// 5xx は一時的なことがあるので再試行する。
		{"503", func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }, drawioFetchTries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tries int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&tries, 1)
				tc.hit(w)
			}))
			defer srv.Close()
			d := testStencils(t)
			m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
			if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
				t.Fatal("失敗するべきところで成功した")
			}
			if n := atomic.LoadInt32(&tries); n != tc.tries {
				t.Fatalf("試行 %d 回（期待 %d）", n, tc.tries)
			}
		})
	}
}
