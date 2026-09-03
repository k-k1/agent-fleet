package memoryx

// エージェントメモリの版管理（docs/log/39 ⑤ / ADR 0022 決定 5）— export。
//
// 既定は **git bundle（全履歴・全 ref）**。1 ファイルで履歴ごと運べ、受け側は
// `git bundle verify` で完全性を検証できる。「最新だけ軽く持ち出す」用に tar.gz
// （HEAD ツリーのみ）も併設する。
//
// 生成前に必ず secret スキャンを通す（★4・docs/log/39 決着 #2）。検出時は既定でブロックし、
// 本人が内容を確認したうえで ack=1 を付け直したときだけ通す。UI 側の確認だけに頼らず
// API 単体でも止まる形にしてあるのは、export が「個人情報を環境の外へ出す」唯一の
// 経路だから。

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	memoryFormatBundle = "bundle"
	memoryFormatTar    = "tar"
)

// memoryWorkDir は export の一時ファイルと import の受領物を置く場所。repo と同じ
// マウントに置く（EFS 越しのクロスデバイス移動を避ける・$TMPDIR の寿命に依存しない）。
func memoryWorkDir() string { return filepath.Join(claude.ConfigDir(), "af-memory.work") }

// memoryExportManifest は tar.gz の先頭に入れる自己記述。import 側は無くても動くが、
// 「どの環境のいつの状態か」を人が見て判断できるようにしておく。
type memoryExportManifest struct {
	Format      string   `json:"format"` // "af-memory-tar"
	Version     int      `json:"version"`
	GeneratedAt string   `json:"generatedAt"`
	Head        string   `json:"head"`
	Kinds       []string `json:"kinds"`
	Files       int      `json:"files"`
}

// memoryExportName は DL ファイル名（受け取り側で中身が分かる名前にする）。
func memoryExportName(format string, now time.Time) string {
	ts := now.UTC().Format("20060102T150405Z")
	if format == memoryFormatTar {
		return "af-memory-" + ts + ".tar.gz"
	}
	return "af-memory-" + ts + ".bundle"
}

// memoryExportScan は export しようとしている中身を走査する。bundle は全履歴を運ぶので
// 到達可能な全 blob、tar は HEAD ツリーだけ — 運ぶ範囲と走査範囲を一致させる。
func memoryExportScan(format string) ([]memorySecretFinding, error) {
	if format == memoryFormatTar {
		return memoryScanRevTree(memoryBranch)
	}
	return memoryScanAllReachable()
}

// memoryExportBundle は `git bundle create --all` で全履歴・全 ref を 1 ファイルにする。
// 生成先は memoryWorkDir 配下の一時ファイルで、呼び出し側が送出後に消す。
func memoryExportBundle() (string, error) {
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(memoryWorkDir(), "export-*.bundle")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()
	// git は出力先を自分で作る。空ファイルが残っていると「既にある」で困らないよう先に消す。
	_ = os.Remove(path)
	if _, err := memoryGitRun("bundle", "create", path, "--all"); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("git bundle create: %w", err)
	}
	return path, nil
}

// memoryExportTar は HEAD ツリーだけを tar.gz にする（履歴なし・最新のみ）。
func memoryExportTar(now time.Time) (string, error) {
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		return "", err
	}
	head, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		return "", err
	}
	listed, err := memoryGitRun("ls-tree", "-r", "--long", memoryBranch)
	if err != nil {
		return "", err
	}
	type entry struct {
		sha  string
		size int64
		path string
	}
	var entries []entry
	kinds, seenKind := []string{}, map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || f[1] != "blob" {
			continue
		}
		size, perr := strconv.ParseInt(f[3], 10, 64)
		if perr != nil {
			continue
		}
		entries = append(entries, entry{sha: f[2], size: size, path: p})
		if k, _, ok := strings.Cut(p, "/"); ok && !seenKind[k] {
			seenKind[k] = true
			kinds = append(kinds, k)
		}
	}

	out, err := os.CreateTemp(memoryWorkDir(), "export-*.tar.gz")
	if err != nil {
		return "", err
	}
	path := out.Name()
	fail := func(e error) (string, error) {
		_ = out.Close()
		_ = os.Remove(path)
		return "", e
	}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	manifest, _ := json.MarshalIndent(memoryExportManifest{
		Format: "af-memory-tar", Version: 1, GeneratedAt: now.UTC().Format(time.RFC3339),
		Head: head, Kinds: kinds, Files: len(entries),
	}, "", "  ")
	if err := memoryTarAdd(tw, "manifest.json", manifest, now); err != nil {
		return fail(err)
	}
	for _, e := range entries {
		blob, berr := memoryGit("cat-file", "blob", e.sha).Output()
		if berr != nil {
			return fail(fmt.Errorf("read %s: %w", e.path, berr))
		}
		if err := memoryTarAdd(tw, e.path, blob, now); err != nil {
			return fail(err)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// memoryTarAdd は 1 エントリを書く（cleanup_archive.go の tarAdd と同じ流儀）。
func memoryTarAdd(tw *tar.Writer, name string, b []byte, now time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: now, Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// HandleMemoryExport は bundle / tar.gz を生成して DL させる（?format=&ack=）。
//
// secret 検出時は 409 + findings を返し、Console はそれを見せてから ack=1 で叩き直す。
// findings に生の秘密は入らない（memory_secrets.go）。
func HandleMemoryExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := strings.TrimSpace(q.Get("format"))
	if format == "" {
		format = memoryFormatBundle
	}
	if format != memoryFormatBundle && format != memoryFormatTar {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "format must be \"bundle\" or \"tar\"")
		return
	}
	if err := memoryEnsureRepo(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	if !memoryHasCommits() {
		httpx.WriteErr(w, http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
		return
	}

	findings, err := memoryExportScan(format)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	ack := q.Get("ack") == "1" || q.Get("ack") == "true"
	if len(findings) > 0 && !ack {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{
				"code":    errCodeMemorySecretDetected,
				"message": fmt.Sprintf("%d possible secret(s) found in the memory being exported", len(findings)),
			},
			"secrets": findings,
		})
		return
	}

	now := time.Now()
	var path string
	if format == memoryFormatTar {
		path, err = memoryExportTar(now)
	} else {
		path, err = memoryExportBundle()
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	defer os.Remove(path) // 一時ファイルは送出後に必ず消す（平文の持ち出し物を置き残さない）

	f, err := os.Open(path)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	name := memoryExportName(format, now)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	// 検出を押し切って持ち出したことは、応答ヘッダにも残しておく（監査の傍証）。
	if len(findings) > 0 {
		w.Header().Set("X-AF-Memory-Secrets", strconv.Itoa(len(findings)))
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}
