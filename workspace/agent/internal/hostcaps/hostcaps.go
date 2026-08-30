// Package hostcaps はホスト CPU / 実行環境の能力検知。エージェント種別のうち
// ホスト条件で動かせないものを Console のセレクタから隠す（capability ガード）
// ための判定を一箇所に集める（docs/log/32 Track B）。
//
// 現在の対象は agy（Antigravity CLI, kind="agy"）のみ: agy は Go BoringCrypto
// (FIPS) ビルドで、x86 では FIPS 乱数モジュールが RDRAND 命令を必須とする。
// RDRAND を提示しないホスト（カーネルマスク / BIOS 無効。実例 = AMD Ryzen
// Embedded R2514）では起動直後に CRNGT 自己テストが SIGABRT し、ユーザー空間
// からは回避不可（docs/decisions/0008 PoC）。起動してから死なせるのではなく、
// 起動前にここで検知して kind ごと非露出にする。
package hostcaps

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

const cpuinfoPath = "/proc/cpuinfo"

// RDRAND reports whether the host CPU exposes the RDRAND instruction
// (the "rdrand" flag in /proc/cpuinfo). Result is cached for the process
// lifetime — CPU flags cannot change under a running container.
// Non-x86 hosts (no "flags" lines) report false; callers that only need
// RDRAND as an x86 FIPS requirement must gate on GOARCH themselves (see
// AgyStatus).
var RDRAND = sync.OnceValue(func() bool {
	b, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		// cpuinfo が読めない環境で偽陰性にして kind を隠すより、露出して実挙動に
		// 任せる（読めないのは Linux 外のテスト環境くらいで、フリートでは起きない）。
		return true
	}
	return rdrandInCPUInfo(string(b))
})

// rdrandInCPUInfo は /proc/cpuinfo テキストの flags 行に rdrand フラグ（完全一致の
// 語）があるかを返す。部分一致にしない — フラグ語彙は空白区切りの厳密な集合。
func rdrandInCPUInfo(cpuinfo string) bool {
	for line := range strings.Lines(cpuinfo) {
		key, vals, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		for _, f := range strings.Fields(vals) {
			if f == "rdrand" {
				return true
			}
		}
	}
	return false
}

// AgyStatus reports whether the agy kind is runnable on this host, with a
// machine-readable reason when it is not:
//
//	supported=false reason="not_installed" — agy binary absent (image without the
//	                                         bake; PATH は ~/.local/bin 優先も含む)
//	supported=false reason="no_rdrand"     — x86 host without RDRAND (agy would
//	                                         SIGABRT at launch)
//	supported=true  reason=""
//
// agy.Status()（GET /connections の "agy" フィールド）はこれをそのまま
// supported / reason として載せ、Console は supported=false の kind を
// セレクタに出さない。セッション作成側も同じ判定で拒否する（docs/log/32）。
func AgyStatus() (supported bool, reason string) {
	if _, err := exec.LookPath("agy"); err != nil {
		return false, "not_installed"
	}
	// RDRAND 要件は x86 の FIPS 乱数モジュール固有（0008）。arm64 等では課さない。
	//
	// ✅ 実測で確認済み（2026-08-22・docs/log/70 §70.13）: Graviton 3 世代（m8g=Graviton4 /
	// m7g=Graviton3 / m6g=Neoverse-N1）の Debian 12 コンテナで `agy --version` と
	// `agy --help` が終了コード 0。**決め手は m6g** で、`/proc/cpuinfo` に `rng`
	// （ARMv8.5-RNG＝RNDR。x86 の rdrand に相当）が **無いのに動いた**——つまり arm64 の
	// BoringCrypto FIPS 乱数は命令ではなくカーネルの getrandom(2) から取る。
	// この分岐は仮定ではなく測った結果である。
	if runtime.GOARCH == "amd64" && !RDRAND() {
		return false, "no_rdrand"
	}
	return true, ""
}
