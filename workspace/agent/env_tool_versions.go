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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// buildPinsPath は Dockerfile が ARG から書き出すピン一覧。var なのはテストで
// 差し替えるため（install_kiro のピン比較テスト）— 実行時は書き換えない。
var buildPinsPath = "/usr/local/share/agent-fleet/versions.json"

// bakedUVToolRoot はイメージが `uv tool install` を置く root（Dockerfile の
// UV_TOOL_DIR）。buildPinsPath と同じくテスト差し替えのための var — 実行時は
// 書き換えない。ハードコードのままだと「焼き込み側には無いはず」を確かめる
// テストが *実コンテナの焼き込み* を拾ってしまい、焼き込みの無い CI では通るのに
// 実環境の Workspace では落ちる、という逆向きの脆さになる。
var bakedUVToolRoot = "/usr/local/share/uv/tools"

// toolSpec は 1 ツール分の観測点。Cmd は PATH 解決（実効）と ~/.local/bin/<Cmd>
// （ユーザー local）の両方に使う。Baked はイメージが焼く実体パス（gh はラッパーでは
// なく libexec の本体、go は tarball 展開先）。
type toolSpec struct {
	Name  string   // 表示名
	Cmd   string   // command -v する名前
	Baked string   // イメージ焼き込みの実体パス
	Args  []string // 版取得の引数（既定 --version）
	Pin   string   // versions.json のキー（無ピンのツールは空）
	// PyDist は「`uv tool install` で入れた Python 製 MCP サーバー」の PyPI 配布名。
	// これらは **実行して版を訊けない**（実測 2026-08-06）:
	//   - cloudwatch MCP: `--version` が版を出さず、代わりに**サーバーが起動する**
	//     （メトリクスメタデータ 1179 件をロードする）。版取得のたびに起動させるのは論外。
	//   - AWS MCP プロキシ: `--version` は argparse に弾かれて exit 2・stdout 空。
	//     版は `--help` の 13 行目にしか出ず、先頭 1 行しか見ない probeVersion では拾えない。
	// なので exec せず、uv の venv にある dist-info ディレクトリ名から読む（uvToolVersion）。
	PyDist string
}

var toolSpecs = []toolSpec{
	{Name: "claude", Cmd: "claude", Baked: "/usr/local/bin/claude", Pin: "claude"},
	{Name: "opencode", Cmd: "opencode", Baked: "/usr/local/bin/opencode", Pin: "opencode"},
	{Name: "codex", Cmd: "codex", Baked: "/usr/local/bin/codex", Pin: "codex"},
	// agy は公式installer manifestが示す不変GCS objectからの真のピン
	// （workspace/Dockerfile の AGY_VERSION + AGY_RELEASE_BUILD + sha256 検証）。RDRAND 非提示
	// ホストでは --version 自体が SIGABRT するため probeVersion は "(取得失敗)" になる
	// （それ自体がガード対象ホストの兆候）。
	{Name: "agy", Cmd: "agy", Baked: "/usr/local/bin/agy", Pin: "agy"},
	{Name: "copilot", Cmd: "copilot", Baked: "/usr/local/bin/copilot", Pin: "copilot"},
	// cursor（kind="cursor"、docs/log/40）は npm でなく版付き tarball の Node.js バンドル。
	// 焼き込みは /usr/local/share/cursor-agent/versions/<ver>/ で、/usr/local/bin/cursor-agent
	// はその wrapper への symlink（realpath で版ディレクトリを解決）。版は日付形式
	// （2026.07.20-8cc9c0b）で semver でないが、`cursor-agent --version` はその文字列を返す。
	{Name: "cursor", Cmd: "cursor-agent", Baked: "/usr/local/bin/cursor-agent", Pin: "cursor"},
	// kiro（kind="kiro"、docs/log/43）は焼き込み（/usr/local・BAKE=1）でも、既定では
	// オンデマンドで ~/.local へ入る（~855MB のため全ユーザー boot-install しない）。
	// 実効/焼き込み/ユーザー local の 3 版表示は他 CLI と同じ（未導入なら effective/baked
	// とも null＝「未導入」がそのまま可視化される）。`kiro-cli --version` は「kiro-cli 2.14.1」。
	{Name: "kiro", Cmd: "kiro-cli", Baked: "/usr/local/bin/kiro-cli", Pin: "kiro"},
	{Name: "rtk", Cmd: "rtk", Baked: "/usr/local/bin/rtk", Pin: "rtk"},
	{Name: "gh", Cmd: "gh", Baked: "/usr/local/libexec/gh", Pin: "gh"}, // /usr/local/bin/gh は透過認証ラッパー
	{Name: "go", Cmd: "go", Baked: "/usr/local/go/bin/go", Args: []string{"version"}, Pin: "go"},
	{Name: "node", Cmd: "node", Baked: "/usr/local/bin/node"},
	{Name: "python", Cmd: "python3", Baked: "/usr/bin/python3"},
	// AWS / ops MCP 系（docs/log/25）。CLI ほど目立たないが、ピンずれと home shadow が起きる
	// 条件は同じ（`install-awscli` は ~/.local/bin へ、grafana の fallback も ~/.local/bin を
	// 見る）うえ、lean variant では焼き込みが無く versions.json のピンだけが手掛かりになる。
	// ここに出ていないと「MCP サーバーが古い/入っていない」を Console から確かめる術が無い。
	{Name: "awscli", Cmd: "aws", Baked: "/usr/local/bin/aws", Pin: "awscli"},
	{Name: "mcp-grafana", Cmd: "mcp-grafana", Baked: "/usr/local/bin/mcp-grafana", Pin: "mcp_grafana"},
	{Name: "cloudwatch-mcp", Cmd: "awslabs.cloudwatch-mcp-server", Baked: "/usr/local/bin/awslabs.cloudwatch-mcp-server",
		Pin: "cloudwatch_mcp", PyDist: "awslabs-cloudwatch-mcp-server"},
	{Name: "aws-mcp", Cmd: "mcp-proxy-for-aws", Baked: "/usr/local/bin/mcp-proxy-for-aws",
		Pin: "aws_mcp_proxy", PyDist: "mcp-proxy-for-aws"},
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
	// Overridden: 焼き込み実体が在り、かつ実効がそれとは別の home 配下実体を指している
	// （= ユーザー local が焼き込みを隠している）。焼き込みが無い lean variant では
	// ~/.local がピン本体そのものなので false（従来は全行に override が点いて無意味だった）。
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

// uvToolRoot は path の uv tool ルート。`uv tool install` は
// <root>/<tool>/bin/<exe> に実体を置き、<root>/<tool>/lib/pythonX.Y/site-packages に
// venv を作る。root は「イメージ焼き込み（/usr/local/share/uv/tools — Dockerfile の
// UV_TOOL_DIR）」と「ユーザー導入（~/.local/share/uv/tools — uv の既定）」の 2 つで、
// どちらかは exe が home 配下にあるかで決まる。3 列（実効／焼き込み／~/.local）の
// 意味とちょうど一致するので、パスを遡らずこの判定で選ぶ（shim が symlink でなく
// コピーの uv 版でも壊れない）。
func uvToolRoot(exePath, home string) string {
	if home != "" && strings.HasPrefix(exePath, home+string(os.PathSeparator)) {
		return filepath.Join(home, ".local", "share", "uv", "tools")
	}
	return bakedUVToolRoot
}

// uvToolVersion は uv tool の venv にある dist-info ディレクトリ名から版を読む。
// **実体を exec しない**のが要点（理由は toolSpec.PyDist のコメント）。
// dist-info の名前は PEP 427 の正規化を受けるので、配布名の "-" は "_" になる
// （awslabs-cloudwatch-mcp-server → awslabs_cloudwatch_mcp_server-0.1.4.dist-info）。
func uvToolVersion(exePath, dist, home string) *toolBin {
	if fi, err := os.Stat(exePath); err != nil || fi.IsDir() {
		return nil
	}
	norm := strings.ReplaceAll(dist, "-", "_")
	pattern := filepath.Join(uvToolRoot(exePath, home), "*", "lib", "python*", "site-packages", norm+"-*.dist-info")
	for _, m := range globSorted(pattern) {
		base := strings.TrimSuffix(filepath.Base(m), ".dist-info")
		if v := strings.TrimPrefix(base, norm+"-"); v != base && v != "" {
			return &toolBin{Path: exePath, Version: extractVer(v), Raw: dist + " " + v}
		}
	}
	// 実体はあるのに venv が見つからない（uvx 実行だけで PATH に置いた等）。版が
	// 分からないことを「未導入（—）」に化けさせない — 出所不明の実体こそ見せたい。
	return &toolBin{Path: exePath, Raw: "(版不明)"}
}

func globSorted(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)
	return m
}

// probeTool は 1 実体分の版取得。uv tool の Python サーバーだけ exec を避ける。
func probeTool(spec toolSpec, path, home string) *toolBin {
	if spec.PyDist != "" {
		return uvToolVersion(path, spec.PyDist, home)
	}
	return probeVersion(path, spec.Args)
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
			effPath := ""
			if p, err := exec.LookPath(spec.Cmd); err == nil {
				// symlink（~/.local/bin/claude → share 配下など）は実体で判定する
				effPath = p
				if abs, err := filepath.EvalSymlinks(p); err == nil {
					effPath = abs
				}
				r.Effective = probeTool(spec, p, home)
			}
			r.Baked = probeTool(spec, spec.Baked, home)
			// go: a lean rootfs bakes no /usr/local/go — surface the on-demand
			// toolchain (install-go, docs/log/35 §35.7.2-5) in the image column instead.
			if r.Baked == nil && spec.Name == "go" {
				if vers := installedGoVersions(); len(vers) > 0 {
					r.Baked = probeVersion(filepath.Join(goHomeRoot(), vers[len(vers)-1], "bin", "go"), spec.Args)
				}
			}
			// Overridden は「隠される焼き込み実体がある」時だけ（struct コメント参照）。
			// go の on-demand toolchain（home 配下を Baked に立てる）は実効＝焼き込みの
			// 同一実体なので実体パス比較で除外される。
			if effPath != "" && r.Baked != nil {
				bakedPath := r.Baked.Path
				if abs, err := filepath.EvalSymlinks(bakedPath); err == nil {
					bakedPath = abs
				}
				r.Overridden = strings.HasPrefix(effPath, home+string(os.PathSeparator)) && effPath != bakedPath
			}
			r.UserLocal = probeTool(spec, filepath.Join(home, ".local", "bin", spec.Cmd), home)
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
