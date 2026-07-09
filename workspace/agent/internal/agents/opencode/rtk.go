package opencode

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — opencode 側の適用artifact。on/off の実体は
// ~/.config/opencode/plugin/rtk.ts の有無（bash/shell ツール呼び出しを
// `rtk rewrite` に横取りするプラグイン）。durable な設定と起動時 reconcile、
// codex 側（AGENTS.md のマーカーブロック）は package main の agent_rtk.go 側に
// 残る（Wave E で codex 縦割りへ）。

const rtkPluginSrc = "/usr/local/share/agent-fleet/opencode-plugin/rtk.ts"

func rtkPluginDst() string {
	return filepath.Join(paths.HomeDir(), ".config", "opencode", "plugin", "rtk.ts")
}

// ApplyRTK seeds (on) or removes (off) the vendored rtk plugin. Writes only
// when the content differs, so the startup reconcile is a no-op on an unchanged home.
func ApplyRTK(on bool) {
	dst := rtkPluginDst()
	if !on {
		_ = os.Remove(dst)
		return
	}
	src, err := os.ReadFile(rtkPluginSrc)
	if err != nil {
		return // no vendored plugin in this image — nothing to seed
	}
	if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(src) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	tmp := dst + ".af-tmp"
	if os.WriteFile(tmp, src, 0o644) == nil {
		_ = os.Rename(tmp, dst)
	}
}
