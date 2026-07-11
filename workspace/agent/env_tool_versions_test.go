package main

import "testing"

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
