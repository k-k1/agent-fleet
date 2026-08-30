package main

// ホーム配下のパス規約（docs/log/23 P1-W5）。実体は internal/paths に移設（docs/log/23
// 残① Wave A）— internal/session・internal/status と main が同じ規約を共有する。
// main 内の多数の呼び出し箇所のため薄い委譲を残す。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/paths"

func homeDir() string { return paths.HomeDir() }
