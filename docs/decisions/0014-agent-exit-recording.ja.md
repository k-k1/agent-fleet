# 0014. エージェントプロセスの終了理由記録 — pane ラッパー方式・自 cgroup で OOM 帰属・per-container は CP 直読み

[English](0014-agent-exit-recording.md) | 日本語

- 状態: 確定（2026-07-12）・Phase 1（CP）＋ Phase 2（Agent）実装済み（同日）
- 関連: [26-agent-exit-recording.md](../log/26-agent-exit-recording.md)（設計本体）/
  [0012-go-internal-refactor.md](0012-go-internal-refactor.ja.md)（internal/status 抽出）

## 背景

エージェント（claude/codex/opencode）は `tmux new-session -d <program>` で起動する
（親は tmux サーバ）。このため Go 側にプロセスの `cmd.Wait()`／`WaitStatus` を受け取る導線が
無く、終了検知は「tmux セッションが消えたか」のポーリングのみで、**なぜ死んだか（正常終了・
クラッシュ・OOM kill）は一切残らなかった**。共有ホストの OOM は既知リスク
（[host-oom-fleet-risk] メモ）で、「どのセッションが OOM で落ちたか」を運用者・利用者が
知る手段が欲しい。cgroup v2 は CP がホストから直読み済み（`metrics.go`）だが `memory.events` は
未参照、docker の `.State.OOMKilled` も未使用だった。

論点は「pane の exit status をどう捕まえるか」「OOM をどの粒度で帰属するか」「意図的停止を
クラッシュと誤検知しない方法」の 3 つ。

## 決定

1. **pane ラッパー方式で per-session の exit code を捕捉**（tmux フック方式・sub-cgroup 方式を却下）。
   pane プログラム末尾に `; __af_ec=$?; workspace-agent record-exit '<name>' "$__af_ec"` を付与し、
   エージェント CLI 終了後にシェルが拾う `$?`（signal kill は `128+N`）を記録する。tmux バージョン
   非依存で program 文字列に閉じる。
2. **意図的停止フラグは設けない**（実証により不要と判明）。tmux 3.3a 実機検証で
   **`tmux kill-session` はラッパーのシェルごと SIGHUP で落とし record-exit を走らせない**ことを確認。
   よって Stop/Halt/Archive/Recreate の意図的停止は原理的に記録されず、通常停止をクラッシュと
   誤検知しない。加えて graceful signal（SIGINT/TERM/HUP=130/143/129）は解釈層で `stopped` に倒し二重に安全。
3. **OOM 帰属は Agent 側で完結**（CP 集約を却下）。record-exit が**自コンテナの** cgroup
   `/sys/fs/cgroup/memory.events` の `oom_kill` を、**セッション開始時に記録したベースライン**と比較し、
   `137`（SIGKILL）かつ増分ありのときだけ `oom` と確定（増分なしは `killed`）。CP 往復不要で、
   セッション終了と同じコンテキストで判定できる。
4. **終了情報は Meta と別ファイルに永続化**（`internal/status` の `ExitInfo`、セッション名キー）。
   record-exit と API ハンドラが同時に状態を書くため、単一 JSON の Meta では write 競合で潰し合う。
   既存の per-sid status ストアと同じ理由で専用ファイルに分離。開始時のベースライン書き込みが
   前回の死亡記録クリアも兼ね、再開セッションはクリーンに始まる。
5. **per-container の OOM は CP が cgroup 直読みで検知**（再ビルド不要）。`metrics.go` に
   `memory.events.oom_kill`（コンテナ内子プロセスの OOM。コンテナは生存＝docker では拾えない唯一の信号）と
   停止コンテナの docker `.State.OOMKilled/.ExitCode`（コンテナ丸ごとの OOM 落ち）を追加し、
   `/api/workspace/stats` に露出。稼働中コンテナにも即効。
6. **sub-cgroup（per-session cgroup）は見送り**。精密な per-session `memory.events`／メモリ上限が
   得られるが cgroup delegation 権限が要り重い。attribution は 1〜3 で十分。

## 帰結

- Phase 2（Agent・要イメージ再ビルド）: `record_exit.go`＋`status.ExitInfo`＋`startSessionTmux` の
  ラッパー付与／ベースライン記録＋`wireSession` の終了理由付与＋Stop/Archive/Recreate クリーンアップ。
  Console は左ペインで異常終了（oom/killed/crashed）を warn チップ＋ツールチップ表示。
- Phase 1（CP・再ビルド不要）: `oomTracker`（ポーリング跨ぎで `oom_recent` を 5 分フラグ、初回サンプルは
  ベースラインで誤検知回避）＋停止コンテナ inspect。WsBar はメモリタイル crit ＋状態チップ warn で提示。
- `exitReason` の解釈: `0`→exited / `137`+OOM→oom / `137`非OOM→killed / SIGINT/TERM/HUP→stopped /
  他 signal・非0非signal→crashed。単体テスト＋実バイナリでケース検証済み。
- 捨てた案: tmux `remain-on-exit`＋`pane-died` フック（セッション自動破棄が止まり後始末が要る）／
  意図的停止フラグ（kill-session が記録しないため不要）／CP 側での OOM 帰属集約（Agent 自 cgroup で完結できる）／
  sub-cgroup（権限・複雑さに見合わない）。
- 残: 実フリート再ビルド反映と実機目視。
