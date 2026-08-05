package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 実ツールの --version 出力（実測）から版番号が抜けることを固定する。
func TestExtractVer(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"2.1.207 (Claude Code)", "2.1.207"},
		{"1.17.18", "1.17.18"},
		{"codex-cli 0.144.1", "0.144.1"},
		{"rtk 0.43.0", "0.43.0"},
		{"gh version 2.96.0 (2026-07-02)", "2.96.0"},
		{"go version go1.26.4 linux/amd64", "1.26.4"},
		{"v22.17.0", "22.17.0"},
		{"Python 3.11.2", "3.11.2"},
		{"2026.07.20-8cc9c0b", "2026.07.20"}, // cursor: 日付版数（sha 接尾辞は落ちる）

		{"(timeout)", "(timeout)"}, // 番号なし → raw をそのまま返す
	}
	for _, c := range cases {
		if got := extractVer(c.raw); got != c.want {
			t.Errorf("extractVer(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// probeVersion: 存在しないパスは nil（UI は「—」表示）。
func TestProbeVersionMissing(t *testing.T) {
	if got := probeVersion("/no/such/binary", nil); got != nil {
		t.Errorf("probeVersion(missing) = %+v, want nil", got)
	}
}

// uvToolVersion: `uv tool install` した Python MCP サーバーの版を **exec せずに**
// dist-info 名から読む（cloudwatch MCP は --version でサーバーが起動してしまい、
// AWS MCP プロキシは --version 自体を持たない — toolSpec.PyDist のコメント参照）。
// 焼き込み（/usr/local 側）とユーザー導入（home 側）で venv の root が違うので、
// home 配下かどうかで root を選び分けているところまで見る。
func TestUVToolVersion(t *testing.T) {
	home := t.TempDir()
	// ユーザー導入の uv tool を模す: <home>/.local/share/uv/tools/<tool>/…
	tool := filepath.Join(home, ".local", "share", "uv", "tools", "mcp-proxy-for-aws")
	sp := filepath.Join(tool, "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(filepath.Join(sp, "mcp_proxy_for_aws-1.6.4.dist-info"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, ".local", "bin", "mcp-proxy-for-aws")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := uvToolVersion(exe, "mcp-proxy-for-aws", home)
	if got == nil || got.Version != "1.6.4" {
		t.Fatalf("uvToolVersion = %+v, want 1.6.4", got)
	}
	// 実体が無ければ nil（UI は「—」＝未導入）。
	if got := uvToolVersion(filepath.Join(home, ".local", "bin", "nope"), "mcp-proxy-for-aws", home); got != nil {
		t.Errorf("uvToolVersion(missing) = %+v, want nil", got)
	}
	// 実体はあるのに venv が別 root（= 焼き込み側を見に行く）→「未導入」に化けさせず
	// 版不明として実体を見せる。home="" で home 判定を外すと /usr/local 側を引く。
	if got := uvToolVersion(exe, "mcp-proxy-for-aws", ""); got == nil || got.Version != "" {
		t.Errorf("uvToolVersion(root 不一致) = %+v, want 版不明", got)
	}
}

// collectToolVersions は環境にツールが無くても panic せず全ツール分の行を返す
// （CI ランナーには claude 等が無い — bins は nil で埋まるだけ）。Workspace
// コンテナ内で走らせれば実プローブの生値が -v ログで見える。
func TestCollectToolVersions(t *testing.T) {
	out := collectToolVersions()
	if len(out) != len(toolSpecs) {
		t.Fatalf("got %d rows, want %d", len(out), len(toolSpecs))
	}
	for i, r := range out {
		if r.Name != toolSpecs[i].Name {
			t.Errorf("row %d name = %q, want %q", i, r.Name, toolSpecs[i].Name)
		}
		t.Logf("%-8s pin=%-8s eff=%v baked=%v local=%v overridden=%v",
			r.Name, r.Pin, binStr(r.Effective), binStr(r.Baked), binStr(r.UserLocal), r.Overridden)
	}
}

func binStr(b *toolBin) string {
	if b == nil {
		return "-"
	}
	return b.Version
}
