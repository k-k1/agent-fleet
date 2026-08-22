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
