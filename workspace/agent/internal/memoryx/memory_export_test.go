package memoryx

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryProjectMemPath は live 側のメモリファイルの絶対パス（テスト用の短縮）。
func memoryProjectMemPath(cfg, slug string, parts ...string) string {
	return filepath.Join(append([]string{cfg, "projects", slug, "memory"}, parts...)...)
}

// memoryTestAPI は隔離 HOME を組み、トークンゲート込みの実ルート表を返す。
func memoryTestAPI(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	_, cfg, slug := memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	return httpx.RequireToken(buildMux()), cfg, slug
}

// export の secret ゲート（★4・docs/log/39 決着 #2）。検出時は既定でブロックし、
// 明示の ack でだけ通す。UI の確認ダイアログではなく **API 単体**で止まること。
func TestMemoryExportBlocksSecretsUntilAcked(t *testing.T) {
	h, cfg, slug := memoryTestAPI(t)
	const fake = "AKIAQWERTYUIOPASDFGH"
	memoryWrite(t, memoryProjectMemPath(cfg, slug, "keys.md"), "deploy key "+fake+"\n")
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}

	w := smokeDo(t, h, "GET", "/agents/memory/export?format=bundle", "smoke-token", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("export with a secret should be blocked: %d", w.Code)
	}
	var blocked struct {
		Error   struct{ Code string }
		Secrets []memorySecretFinding
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if blocked.Error.Code != errCodeMemorySecretDetected || len(blocked.Secrets) == 0 {
		t.Fatalf("blocked payload = %+v", blocked)
	}
	// 応答に生値を混ぜない（これが漏れると防御が配布経路に化ける）。
	if strings.Contains(w.Body.String(), fake) {
		t.Fatal("the blocking response leaked the raw secret")
	}

	// ack すると本人の判断として通る。中身は本物の git bundle。
	w = smokeDo(t, h, "GET", "/agents/memory/export?format=bundle&ack=1", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("acked export: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".bundle") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if w.Header().Get("X-AF-Memory-Secrets") == "" {
		t.Error("acked export should still record that secrets were present")
	}
	if head := w.Body.String(); !strings.HasPrefix(head, "# v2 git bundle") && !strings.HasPrefix(head, "# v3 git bundle") {
		t.Fatalf("body is not a git bundle: %q", head[:min(32, len(head))])
	}

	// 一時ファイルを置き残さない（平文の持ち出し物をマウントに残さない）。
	if ents, err := os.ReadDir(memoryWorkDir()); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "export-") {
				t.Errorf("export temp file left behind: %s", e.Name())
			}
		}
	}
}

// tar.gz export（最新のみ）: manifest 付きで、repo のパスがそのまま入る。
// 秘密が無ければ ack 無しで通ること（ゲートが常時ブロックにならないこと）も見る。
func TestMemoryExportTar(t *testing.T) {
	h, _, slug := memoryTestAPI(t)
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}
	w := smokeDo(t, h, "GET", "/agents/memory/export?format=tar", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tar export: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".tar.gz") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	names, blobs := memoryReadTarGz(t, w.Body.Bytes())
	for _, want := range []string{
		"manifest.json",
		"claude/projects/" + slug + "/memory/MEMORY.md",
		"codex/MEMORY.md",
	} {
		if _, ok := blobs[want]; !ok {
			t.Errorf("%q missing from the archive: %v", want, names)
		}
	}
	// ★1 の裏返し: 取り出す側にも巻き込みが無いこと。
	for _, n := range names {
		if strings.Contains(n, ".jsonl") || strings.Contains(n, "credentials") || strings.Contains(n, ".git/") {
			t.Errorf("forbidden entry in the archive: %s", n)
		}
	}
	var m memoryExportManifest
	if err := json.Unmarshal(blobs["manifest.json"], &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Format != "af-memory-tar" || m.Head == "" || m.Files != 5 {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := time.Parse(time.RFC3339, m.GeneratedAt); err != nil {
		t.Errorf("manifest.generatedAt = %q", m.GeneratedAt)
	}
}

// 入力検証と、履歴ゼロのときの応答。
func TestMemoryExportBadInput(t *testing.T) {
	h, _, _ := memoryTestAPI(t)
	if w := smokeDo(t, h, "GET", "/agents/memory/export?format=zip", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown format: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/export", "smoke-token", ""); w.Code != http.StatusNotFound {
		t.Errorf("export before any snapshot: %d %s", w.Code, w.Body.String())
	}
}

// memoryReadTarGz は tar.gz を名前一覧と中身に開く。
func memoryReadTarGz(t *testing.T, b []byte) ([]string, map[string][]byte) {
	t.Helper()
	gr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	names := []string{}
	blobs := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, h.Name)
		body, _ := io.ReadAll(tr)
		blobs[h.Name] = body
	}
	return names, blobs
}
