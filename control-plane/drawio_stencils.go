// drawio_stencils.go — `.drawio` ビューアのステンシルを CP がプロキシしてディスクに
// キャッシュする（docs/65 §65.5.3 / ADR 0046 決定 5）。
//
// なぜ同梱しないのか: ステンシル全体は 203 ファイル / 40.8 MB（`aws4.xml` だけで
// 6.2 MB）。1 枚の図が使うのはそのうち 1〜2 セットで、実行時は元からオンデマンド
// （`mxStencilRegistry` は図に現れたセットだけを 1 回取る）なので、問題は配布サイズ
// だけである。だからバイト列は持たず、**20 KB の台帳だけ**を同梱する。
//
// 台帳（assets/drawio-stencils.json）は 2 つの役割を兼ねる:
//  1. **SSRF の防壁。** セット名は信用できない `.drawio` の中身（`shape=mxgraph.<set>.*`）
//     から来る。台帳に無い名前を取りに行く実装は「図を開かせるだけで CP に任意 URL を
//     叩かせる」道具になる。だから allowlist は「台帳にあるか」だけで判定し、
//     upstream の URL は台帳の base とセット名から CP が組み立てる（要求は URL を運ばない）。
//  2. **完全性の担保。** 取得したバイト列を sha256 で突き合わせてから保存する。
//
// **この経路は authGate の内側に置く（除外しない）。** 取りに来るのは Console の
// 親ウィンドウであってサンドボックス iframe ではないため、セッション cookie が付く。
// フレームに直接取らせる案は実測で否決した —— オリジンを持たないフレームからの要求は
// cross-site 扱いで SameSite=Lax の cookie が付かず、authGate に 401 で弾かれる
// （docs/65 §65.11-7 と同じ穴）。詳細は docs/65 §65.5.4。
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed assets/drawio-stencils.json
var drawioStencilManifestJSON []byte

type drawioStencilEntry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type drawioStencilManifest struct {
	Version string                        `json:"version"`
	Base    string                        `json:"base"`
	Sets    map[string]drawioStencilEntry `json:"sets"`
}

var (
	drawioManifestOnce sync.Once
	drawioManifest     drawioStencilManifest
	drawioManifestErr  error
)

func loadDrawioManifest() (drawioStencilManifest, error) {
	drawioManifestOnce.Do(func() {
		if err := json.Unmarshal(drawioStencilManifestJSON, &drawioManifest); err != nil {
			drawioManifestErr = err
			return
		}
		if len(drawioManifest.Sets) == 0 || drawioManifest.Base == "" {
			drawioManifestErr = errors.New("drawio stencil manifest is empty")
		}
	})
	return drawioManifest, drawioManifestErr
}

// drawioStencilHTTP は upstream（raw.githubusercontent）からの取得用。1 ファイル最大
// 6.2 MB なので、細い回線でも切れない程度に余裕を持たせる。
var drawioStencilHTTP = &http.Client{Timeout: 60 * time.Second}

type drawioStencils struct {
	cacheDir string
	// 同じセットへの同時要求で upstream を何度も叩かないための鍵付きロック。
	mu      sync.Mutex
	loading map[string]*sync.Mutex
}

func newDrawioStencils(cfg config) *drawioStencils {
	root := "/tmp/af-data"
	if cfg.mgr != nil && cfg.mgr.dataRoot != "" {
		root = cfg.mgr.dataRoot
	}
	return &drawioStencils{
		cacheDir: filepath.Join(root, "drawio-stencils"),
		loading:  map[string]*sync.Mutex{},
	}
}

func registerDrawioStencilRoutes(mux *http.ServeMux, cfg config) {
	d := newDrawioStencils(cfg)
	mux.HandleFunc("GET /api/drawio/stencils/{name...}", d.serve)
	// 版と件数だけを返す（ハーネスと運用の確認用）。バイト列は含まない。
	mux.HandleFunc("GET /api/drawio/stencils", d.index)
}

func (d *drawioStencils) index(w http.ResponseWriter, r *http.Request) {
	m, err := loadDrawioManifest()
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	cached := 0
	for _, e := range m.Sets {
		if st, err := os.Stat(d.pathFor(e)); err == nil && st.Size() == e.Size {
			cached++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": m.Version,
		"sets":    len(m.Sets),
		"cached":  cached,
	})
}

// pathFor はキャッシュ上の置き場所。**セット名をそのままパスに使わない** ——
// 台帳で照合済みとはいえ、`/` を含む名前（`rack/f5.xml`）をディレクトリに広げると
// 台帳の更新でパスの意味が変わりうる。sha256 を名前にすれば、内容が変われば別の
// ファイルになり、古いバイト列が生き残ることも無い。
//
// **引数は名前ではなく台帳のエントリ。** 名前から埋め込み台帳を引き直す実装にすると、
// 呼び出し側が渡した台帳（テストや事前投入）と食い違い、名前の違う複数のセットが
// 同じファイルへ落ちる。実際そう書いてテストに捕まえられた。
func (d *drawioStencils) pathFor(entry drawioStencilEntry) string {
	return filepath.Join(d.cacheDir, entry.SHA256+".xml")
}

func (d *drawioStencils) lockFor(name string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	mu, ok := d.loading[name]
	if !ok {
		mu = &sync.Mutex{}
		d.loading[name] = mu
	}
	return mu
}

func (d *drawioStencils) serve(w http.ResponseWriter, r *http.Request) {
	m, err := loadDrawioManifest()
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	// 台帳に無い名前は取りに行かない。これがこの経路の唯一の allowlist であり、
	// ここを緩めると任意 URL 取得（SSRF）になる。`..` や絶対パスも、台帳の鍵と
	// 一致しない時点でここで落ちる。
	entry, ok := m.Sets[name]
	if !ok {
		http.Error(w, "unknown stencil set", http.StatusNotFound)
		return
	}

	body, err := d.fetch(r.Context(), m, name, entry)
	if err != nil {
		// 閉域では取れない。**これは異常ではなく想定された劣化**なので、Console 側は
		// 静かに「枠と色だけ」の絵に落とす（docs/65 §65.5.3）。
		log.Printf("drawio stencil %s unavailable: %v", name, err)
		http.Error(w, "stencil unavailable", http.StatusBadGateway)
		return
	}
	// 内容はバージョンで固定され、sha256 で検証済み。名前は台帳の鍵なので、
	// 版が変われば台帳ごと変わる = 長期キャッシュしてよい。
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (d *drawioStencils) fetch(ctx context.Context, m drawioStencilManifest, name string, entry drawioStencilEntry) ([]byte, error) {
	path := d.pathFor(entry)
	if b, err := os.ReadFile(path); err == nil && verifyDrawioStencil(b, entry) == nil {
		return b, nil
	}

	mu := d.lockFor(name)
	mu.Lock()
	defer mu.Unlock()
	// ロックを取り直す間に別の要求が入れたかもしれない。
	if b, err := os.ReadFile(path); err == nil && verifyDrawioStencil(b, entry) == nil {
		return b, nil
	}

	url := m.Base + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agent-fleet")
	res, err := drawioStencilHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d", res.StatusCode)
	}
	// 台帳がサイズを持っているので、読む量にも上限を掛けられる（+1 で超過を検出）。
	b, err := io.ReadAll(io.LimitReader(res.Body, entry.Size+1))
	if err != nil {
		return nil, err
	}
	if err := verifyDrawioStencil(b, entry); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(d.cacheDir, 0o755); err != nil {
		// キャッシュに置けなくても、この要求には答えられる。
		log.Printf("drawio stencil cache dir: %v", err)
		return b, nil
	}
	// 名前は内容の sha256 なので、書きかけが正規名で見えると壊れたものを配る。
	// 一時名に書いてから rename する（同一ディレクトリなので atomic）。
	tmp, err := os.CreateTemp(d.cacheDir, ".tmp-*")
	if err != nil {
		log.Printf("drawio stencil cache write: %v", err)
		return b, nil
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		log.Printf("drawio stencil cache write: %v", err)
		return b, nil
	}
	tmp.Close()
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		log.Printf("drawio stencil cache rename: %v", err)
	}
	return b, nil
}

func verifyDrawioStencil(b []byte, entry drawioStencilEntry) error {
	if int64(len(b)) != entry.Size {
		return fmt.Errorf("size %d, want %d", len(b), entry.Size)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, entry.SHA256) {
		return fmt.Errorf("sha256 %s, want %s", got, entry.SHA256)
	}
	return nil
}
