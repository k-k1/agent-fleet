package main

// env_tool_versions.go — GET /env/tool-versions（read-only）。バンドルされた各ツール
// について「実効（PATH 解決の実体）/ 焼き込み（イメージの実体）/ ユーザー local
// （~/.local/bin の override）」の 3 つの版と、イメージビルド時のピン
// （/usr/local/share/agent-fleet/versions.json、Dockerfile が ARG から書き出す）を
// 返す。PATH は ~/.local/bin が /usr/local より先なので実効≠焼き込みが平気で起きる
// （gh の home shadow、docs/dev/08 §8.3 と同型）— その可視化が目的。
// claude --version などは ~1s かかるため結果は短時間キャッシュし、各ツールは並列に叩く。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const buildPinsPath = "/usr/local/share/agent-fleet/versions.json"

// toolSpec は 1 ツール分の観測点。Cmd は PATH 解決（実効）と ~/.local/bin/<Cmd>
// （ユーザー local）の両方に使う。Baked はイメージが焼く実体パス（gh はラッパーでは
// なく libexec の本体、go は tarball 展開先）。
type toolSpec struct {
	Name  string   // 表示名
	Cmd   string   // command -v する名前
	Baked string   // イメージ焼き込みの実体パス
	Args  []string // 版取得の引数（既定 --version）
	Pin   string   // versions.json のキー（無ピンのツールは空）
}

var toolSpecs = []toolSpec{
	{Name: "claude", Cmd: "claude", Baked: "/usr/local/bin/claude", Pin: "claude"},
	{Name: "opencode", Cmd: "opencode", Baked: "/usr/local/bin/opencode", Pin: "opencode"},
	{Name: "codex", Cmd: "codex", Baked: "/usr/local/bin/codex", Pin: "codex"},
	// agy は GitHub Releases（google-antigravity/antigravity-cli）の versioned アセット
	// からの真のピン（workspace/Dockerfile の AGY_VERSION + sha256 検証）。RDRAND 非提示
	// ホストでは --version 自体が SIGABRT するため probeVersion は "(取得失敗)" になる
	// （それ自体がガード対象ホストの兆候）。
	{Name: "agy", Cmd: "agy", Baked: "/usr/local/bin/agy", Pin: "agy"},
	{Name: "copilot", Cmd: "copilot", Baked: "/usr/local/bin/copilot", Pin: "copilot"},
	{Name: "rtk", Cmd: "rtk", Baked: "/usr/local/bin/rtk", Pin: "rtk"},
	{Name: "gh", Cmd: "gh", Baked: "/usr/local/libexec/gh", Pin: "gh"}, // /usr/local/bin/gh は透過認証ラッパー
	{Name: "go", Cmd: "go", Baked: "/usr/local/go/bin/go", Args: []string{"version"}, Pin: "go"},
	{Name: "node", Cmd: "node", Baked: "/usr/local/bin/node"},
	{Name: "python", Cmd: "python3", Baked: "/usr/bin/python3"},
}

type toolBin struct {
	Path    string `json:"path"`
	Version string `json:"version"` // 抜き出した番号（例 2.1.207）。抜けなければ raw と同じ
	Raw     string `json:"raw"`     // --version 出力の先頭行
}

type toolReport struct {
	Name      string   `json:"name"`
	Pin       string   `json:"pin,omitempty"` // イメージビルド時の ARG ピン
	Effective *toolBin `json:"effective"`     // PATH 解決の実体（無ければ null）
	Baked     *toolBin `json:"baked"`         // イメージの実体（無ければ null）
	UserLocal *toolBin `json:"userLocal"`     // ~/.local/bin の override（無ければ null）
	// Overridden: 実効が home 配下を指している（= ユーザー local が焼き込みを隠している）。
	// 実効と焼き込みの単純パス比較にしないのは、gh のようにラッパー経由が正常なツールが
	// あるため（home 配下でなければ「イメージ由来」とみなす）。
	Overridden bool `json:"overridden"`
}

var toolVerCache struct {
	sync.Mutex
	at  time.Time
	out []toolReport
}

const toolVerCacheTTL = 3 * time.Minute

// verNumRe は --version 出力から版番号を抜く（1.2 / 1.2.3 / 3.11.2 など）。
var verNumRe = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)

// probeVersion は path の版を取得して toolBin にする。バイナリが無ければ nil。
// 5s タイムアウト（壊れた実体でハンドラが吊るまないように）。
func probeVersion(path string, args []string) *toolBin {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil
	}
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if ctx.Err() != nil {
		return &toolBin{Path: path, Raw: "(timeout)"}
	}
	if err != nil && len(out) == 0 {
		return &toolBin{Path: path, Raw: "(取得失敗)"}
	}
	raw := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return &toolBin{Path: path, Version: extractVer(raw), Raw: raw}
}

func extractVer(raw string) string {
	if m := verNumRe.FindString(raw); m != "" {
		return m
	}
	return raw
}

func readBuildPins() map[string]string {
	pins := map[string]string{}
	if b, err := os.ReadFile(buildPinsPath); err == nil {
		_ = json.Unmarshal(b, &pins)
	}
	return pins
}

func collectToolVersions() []toolReport {
	pins := readBuildPins()
	home := homeDir()
	out := make([]toolReport, len(toolSpecs))
	var wg sync.WaitGroup
	for i, spec := range toolSpecs {
		wg.Add(1)
		go func(i int, spec toolSpec) {
			defer wg.Done()
			r := toolReport{Name: spec.Name, Pin: pins[spec.Pin]}
			if p, err := exec.LookPath(spec.Cmd); err == nil {
				if abs, err := filepath.EvalSymlinks(p); err == nil {
					// symlink（~/.local/bin/claude → share 配下など）は実体で判定する
					r.Overridden = strings.HasPrefix(abs, home+string(os.PathSeparator))
				} else {
					r.Overridden = strings.HasPrefix(p, home+string(os.PathSeparator))
				}
				r.Effective = probeVersion(p, spec.Args)
			}
			r.Baked = probeVersion(spec.Baked, spec.Args)
			r.UserLocal = probeVersion(filepath.Join(home, ".local", "bin", spec.Cmd), spec.Args)
			out[i] = r
		}(i, spec)
	}
	wg.Wait()
	return out
}

// handleToolVersions は GET /env/tool-versions。?refresh=1 でキャッシュを飛ばす。
func handleToolVersions(w http.ResponseWriter, r *http.Request) {
	toolVerCache.Lock()
	defer toolVerCache.Unlock()
	if r.URL.Query().Get("refresh") == "1" || toolVerCache.out == nil ||
		time.Since(toolVerCache.at) > toolVerCacheTTL {
		toolVerCache.out = collectToolVersions()
		toolVerCache.at = time.Now()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"tools":     toolVerCache.out,
		"checkedAt": toolVerCache.at.Format(time.RFC3339),
	})
}
