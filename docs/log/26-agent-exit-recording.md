# 26. エージェントプロセスの終了理由記録（OOM / signal / crash） — 設計

**Status: 🚧 Phase 1 + Phase 2 実装済み（残=実フリート再ビルド反映＋実機目視のみ）** — 2026-07-12 起草・同日実装

> 実装サマリ（2026-07-12）:
> - **Phase 2（agent）**: pane ラッパー方式で per-session の終了理由を記録。`record_exit.go`（record-exit サブコマンド＋`exitReason` 解釈＋自 cgroup `memory.events` の `oom_kill` をセッション開始時ベースラインと比較して OOM 確定）、`status.ExitInfo` ストア、`startSessionTmux` のラッパー付与＋ベースライン記録、`wireSession` の終了理由付与、Stop/Archive/Recreate のクリーンアップ。Console は `exitReason/exitCode/exitSignal` を左ペインに反映（異常終了は warn チップ＋ツールチップ）。単体テスト＋実バイナリでのケース検証済み。**「意図的停止フラグ」は実証の結果不要と判明**（§4.2 追記）。
> - **Phase 1（control-plane）**: `metrics.go` に `memory.events` の `oom_kill` 追跡（`oomTracker`、ポーリング跨ぎで `oom_recent`）と停止コンテナの docker `.State.OOMKilled/.ExitCode` を追加。`/api/workspace/stats` に `oom_kill_total`/`oom_recent`/`oom_killed`/`exit_code` を露出。単体テスト済み。
> - **UI/契約/ADR 完了**: WsBar に OOM 表示（メモリタイル crit＋状態チップ warn、`258af54`）、`docs/dev/05-api-contracts.md` に stats 契約追記、[decisions/0014](../decisions/0014-agent-exit-recording.md) 起票。
> - **残**: 実フリート再ビルド反映と実機目視のみ（agent はイメージ焼き込みのため要再ビルド）。

コンテナ内で動くエージェントプロセス（claude / codex / opencode の各セッション）が **なぜ終了したか**（正常終了 / OOM kill / その他 signal / クラッシュ / 意図的停止）を捕捉して記録し、Console に事実ベースで提示するための設計。現状は「tmux セッションが消えた＝停止」しか見ておらず、終了理由の情報はゼロ。

> 本書は設計ドキュメント。Phase 1（control-plane のみ・再ビルド不要）と Phase 2（agent 改修・要再ビルド）に分けて段階導入する。§7 の決定事項を確定してから個別実装に入る。

---

## 0. 要旨

- **現状、終了理由はまったく捕捉していない。** エージェントは `os/exec` の子ではなく `tmux new-session -d` で起動される（`workspace/agent/session_tmux.go:22`）ため、親は tmux サーバであり `cmd.Wait()` による `WaitStatus`/exit code/signal 取得の導線が存在しない。終了検知は `tmuxx.LiveSessionNames()` のポーリングで「セッションが消えたか」を見るだけ（`workspace/agent/session_handlers.go:33-62`）で、消えたら `Meta.StoppedAt` に時刻を打つのみ。**なぜ死んだかは残らない。**
- cgroup は CP が直読みしているが（`control-plane/metrics.go:176-196` の `memory.current`/`memory.max`）、**`memory.events` は未読**。`dmesg`/`journald`/docker `.State.OOMKilled`/`.State.ExitCode` も一切参照していない。Console の「OOM の可能性」表示（`console/src/app/WsBar.tsx:683`）は docker state が `stopped` になったことからの**単なる推測**で根拠データがない。
- 捕捉には粒度・コストの異なる **3 つのメカニズム**があり（§2）、**pane 単位の exit code 捕捉（手段 A）が最も頑健**で、cgroup / docker 由来のシグナル（手段 B/C）は原因ラベル付けの裏取りに使う、という役割分担にする。
- **Phase 1（CP のみ・再ビルド不要）**で「コンテナ OOM の確定検知」を先に入れ、**Phase 2（agent 再ビルド）**で「どのセッションが何で死んだか」の attribution を足す。docs/25（運用監視）はこのテーマを扱っていないので新規設計項目。

---

## 1. なぜ今は取れていないか（構造的な理由）

| 事実 | 根拠 |
|---|---|
| エージェントは tmux セッションとして起動（親は tmux サーバ、Go 側に `*exec.Cmd` を保持しない） | `session_tmux.go:22` `startSessionTmux` → `tmux new-session -d -s <name> -c <cwd> <program>` |
| 終了は「tmux セッションの存在ポーリング」で検知 | `internal/tmuxx/tmuxx.go:63` `LiveSessionNames()`、突き合わせは `session_handlers.go:33-62` |
| 終了時に残すのは `StoppedAt`（時刻）だけ。exit code / signal / WaitStatus は未取得 | `internal/session/meta.go` `WriteMeta` → `~/.config/agent-fleet/sessions/<name>.json`。リポジトリ全体に `ProcessState`/`ExitCode`/`WaitStatus` のエージェント用途の利用なし |
| cgroup は読むが `memory.events` は未読、OOM は未参照 | `control-plane/metrics.go:176-196`（`memory.current`/`memory.max`/`cpu.stat` のみ） |
| OOM/137/OOMKilled のプログラム的検知なし。`dmesg`/`journald` 参照なし | ソース全体 grep 0 件。`137` は `workspace/workspace-notes.md:76` の説明文のみ |
| Console の OOM 表示は推測 | `console/src/app/WsBar.tsx:683`（state からの tooltip 推測） |

PTY（`internal/agents/flow.go`）で `*exec.Cmd` を持つのは**認証ログインフロー専用**であり、作業セッションではない。よって現行の作業セッションには exit を受け取る口が一切ない。

---

## 2. 捕捉に使える 3 メカニズム（コスト / 精度の違い）

| 手段 | 何が分かる | 粒度 | 要イメージ再ビルド | OOM 確定度 |
|---|---|---|---|---|
| **A. tmux pane の exit code 捕捉** | exit code（signal kill は shell 規約で `128+N`、SIGKILL→137） | **セッション単位** | 要（agent 改修） | 高（B と併用で確定） |
| **B. cgroup `memory.events` の `oom_kill` 差分** | 「このコンテナ内で OOM kill が起きた回数」 | コンテナ単位 | 不要（CP のみ） | 中（誰が、は不明） |
| **C. docker inspect `.State.OOMKilled`/`.ExitCode`** | コンテナ自身が OOM で落ちたか | コンテナ単位 | 不要（CP のみ） | 高（コンテナ丸ごと死んだ時だけ） |

### 2.1 重要な落とし穴 — 3 種類の「OOM」を区別する

1. **セッション個別 OOM** — コンテナは生きたまま claude プロセスだけが cgroup 上限で kill される。docker `.State.OOMKilled` は **false のまま**（init が生存）。→ B（`oom_kill` が +1）＋ A（当該セッションが 137 で消えた）でしか捕まらない。
2. **コンテナ丸ごと OOM** — cgroup 上限超過で init ごと死ぬ。→ C（`.State.OOMKilled=true`）で確定。
3. **ホスト全体 OOM**（既知リスク `host-oom-fleet-risk`：重ビルドでホストが枯渇しフリート横断で victim 選定）— この場合 **cgroup の `oom_kill` は増えないことがある**（cgroup 上限起因ではないため）。B/C は取りこぼす。→ **A の 137 だけが唯一残る証跡**になる。

このため **A（pane 単位の raw exit code）を主柱**、B/C を裏取りに使う。

### 2.2 各手段の signal 取得の質

- 手段 A：shell の `$?` は signal kill を `128+N` にエンコードする（SIGKILL=9 → 137、SIGTERM=15 → 143、SIGSEGV=11 → 139）。exit code の観測はできるが「どの signal か」は `N=$?-128` の逆算で分かる。ただし `$?` が 128 以上でも純粋な exit code として 128+ を返すプログラムと区別はつかない（実務上ほぼ問題にならない）。
- 手段 B：`memory.events` の `oom_kill` は「回数」であり、どのプロセスかは含まない。差分検知には前回値の保持が要る。
- 手段 C：`.State.OOMKilled` は bool、`.ExitCode` は int。コンテナ init のもの。

---

## 3. データモデル

### 3.1 セッション終了レコード（Phase 2 の中心）

`internal/session/session.go` の `Meta`（`~/.config/agent-fleet/sessions/<name>.json` に永続化）へ終了情報フィールドを追加する。`StoppedAt`（既存, RFC3339）はそのまま活かす。

```go
// Meta に追加（案）
ExitCode   int    `json:"exit_code,omitempty"`   // pane wrapper が記録した raw exit code。128+N は signal kill
ExitSignal int    `json:"exit_signal,omitempty"` // ExitCode>=128 のとき N=ExitCode-128 を格納（0=signal でない）
ExitReason string `json:"exit_reason,omitempty"` // 解釈済みラベル。§3.3 の enum
ExitAt     string `json:"exit_at,omitempty"`     // wrapper が終了を観測した時刻（RFC3339）。StoppedAt=CP/Agent が気づいた時刻とは別物
```

`StoppedAt`（外側が“消滅に気づいた”時刻）と `ExitAt`（プロセス自身が“抜けた”時刻）を分けておくと、ポーリング遅延と実終了時刻がずれても正しく扱える。

### 3.2 コンテナ OOM イベント（Phase 1 の中心）

CP 側に per-workspace の OOM カウンタ状態を持つ。`memory.events.oom_kill` の直近値と、増分を観測した時刻・回数を保持し、`GET /api/workspace/stats`（`metrics.go:200`）と admin ビューに載せる。永続化するかは §7-3 の決定事項（当面は in-memory + イベント時にログ / メモキュー投函でも足りる）。

### 3.3 ExitReason の enum（解釈層で確定）

| 値 | 条件 |
|---|---|
| `exited` | ExitCode == 0（正常終了） |
| `stopped` | 意図的停止（Stop/Halt/Archive ハンドラが kill を要求した）→ signal でも crash 扱いにしない（§4.2） |
| `oom` | ExitCode == 137（SIGKILL）**かつ** 直近に cgroup `oom_kill` 差分あり **かつ** 意図的停止でない |
| `killed` | signal kill（ExitCode>=128）だが oom 根拠なし・意図的停止でもない（ホスト OOM 含む “原因未確定の kill”） |
| `crashed` | 非 0 で signal でない exit（アプリ内部エラー等） |

---

## 4. 記録フロー

### 4.1 Phase 1 — control-plane のみ（再ビルド不要・低リスク・即効）

`control-plane/metrics.go` の `containerStats()`（`metrics.go:176`）に足す:

1. **`memory.events` を読む。** 既存 `readCgroupUint`（`metrics.go:131`）は単一値用なので、`memory.events` は key-value 複数行（`low/high/max/oom/oom_kill/oom_group_kill`）をパースする小ヘルパを追加し `oom_kill` を得る。
2. **差分検知。** per-workspace で前回の `oom_kill` を保持（`cpuTracker`（`metrics.go:74`）と同様の in-memory tracker）。増えていたら「直近 OOM kill 発生」を確定として記録・stats に載せる。
3. **docker inspect にフィールド追加。** `dockerContainerID()`（`metrics.go:113`）は既に `docker inspect -f` を叩いているので、`-f` テンプレに `.State.OOMKilled`/`.State.ExitCode` を足してコンテナ丸ごと OOM を拾う。
4. **`high`/`max` カウンタと `memory.current/memory.max` 比で“事前”警告**（副産物）。チップは既に比率表示済み（`WsBar.tsx:525`）なので、閾値超過の予兆を同経路で出せる。

Phase 1 だけで「このワークスペースで OOM kill が起きた／コンテナが OOM 落ちした」を**推測でなく確定**で持てる。ただし**どのセッションかは分からない**。

### 4.2 Phase 2 — pane 単位の exit 記録（要 agent 再ビルド・attribution 付き）

`session_tmux.go:22` の launch で pane プログラムを薄くラップし、抜けた瞬間の exit code を記録する。

現状:
```go
program := toolchainShellPrefix() + plan.Program
args := []string{"new-session", "-d", "-s", session.TmuxName(m.Name), "-c", plan.Cwd, program}
```

案（ラッパー方式）: pane コマンドを `sh -c '<program> ; ec=$? ; workspace-agent record-exit <name> "$ec"'` 相当に変える。

- `<program>` が抜けると `$?` に exit code（signal kill は `128+N`）が入り、`record-exit` サブコマンドが `Meta` に `ExitCode`/`ExitSignal`/`ExitAt` を書く。
- **既存の「セッション消滅＝停止」検知はそのまま**動く（ラッパーのシェルが抜ければセッションは消える）。追加記録が乗るだけで挙動非破壊。
- **意図的停止フラグは実証の結果「不要」と判明（実装では省略）。** 当初は Stop/Halt/Archive が kill する前にフラグを立てる設計を想定したが、tmux 3.3a 実機検証で **`tmux kill-session` はラッパーのシェルごと SIGHUP で落とすため `record-exit` が走らない**ことを確認（内側プロセスの SIGKILL では 137 が記録される）。よって意図的停止は原理的に記録されず、フラグは不要。加えて graceful signal（SIGINT/TERM/HUP=130/143/129）は `exitReason` で `stopped` に倒すので二重に安全。
- **OOM 確定はクロスチェック。** `ExitCode==137` かつ Phase 1 の `oom_kill` 差分（同一ウィンドウ）あり → `ExitReason=oom`。137 だが oom_kill 差分なし → `killed`（ホスト OOM の可能性込み）。

**代替案（tmux フック方式）:** `set remain-on-exit on` ＋ `pane-died` フックで `#{pane_dead_status}`／`#{pane_dead_signal}`（tmux ≥3.4）を読む。program 文字列を汚さず signal を直接取れる利点があるが、セッション自動破棄が止まるので後始末（dead pane/session の明示 kill）を自前で行い、現行の消滅検知ウィンドウと噛み合わせる必要があり複雑。**まずはラッパー方式を推奨**（簡単・移植性高・tmux バージョン非依存）。実装リスクは §6。

### 4.3 記録の突き合わせ（誰が結線するか）

- Phase 2 の `record-exit` は raw な事実（ExitCode/ExitAt）だけを書く。
- **OOM ラベル付けの結線点**は 2 案:
  - (a) **Agent 側**で結線 — record-exit 時に Agent が自身の cgroup `memory.events.oom_kill` を読んで差分判定（Agent もコンテナ内なので自 cgroup は読める）。CP に依存せず完結する利点。
  - (b) **CP 側**で結線 — Phase 1 の CP tracker が持つ oom_kill 差分と、Agent 由来の ExitAt を突き合わせる。CP が既にコンテナ横断で見ているので集約に自然。
  - → §7-2 で決定。**(a) を第一候補**（セッション終了と同じコンテキストで完結し、CP への往復不要）。

---

## 5. Console への提示

- **セッションカード / tooltip**: 現状の推測表示（`WsBar.tsx:683`「停止（コンテナが自走終了 — クラッシュ / OOM の可能性）」）を、記録済み `ExitReason` による事実表示に置換（`oom`→「メモリ不足で強制終了(OOM)」、`crashed`→「異常終了(code N)」、`killed`→「強制終了(signal N)」、`stopped`→通常停止表示）。
- **ワークスペースバー**: 「直近 OOM 発生」を確定で表示（Phase 1 データ）＋ `memory.current/memory.max` の予兆警告。
- **admin ビュー**（`admin_stats.go:47` `memberStats`）: member 別に直近 OOM 回数・OOM で落ちたセッション一覧を出せると運用上有用。
- API 契約（`GET /api/workspace/stats` / セッション一覧の wire 型 `session.Session`）に終了理由フィールドを追加 → `docs/dev/05-api-contracts.md` を更新。

---

## 6. リスク・注意点

- **ラッパーの quoting**: `plan.Program`（例: `claude --session-id <sid> --dangerously-skip-permissions`, `internal/agents/claude/program.go:28`）と `toolchainShellPrefix()` を `sh -c '...'` に安全に埋める必要がある。既存も tmux に文字列を渡しているので破綻はしないが、`record-exit` 追記時のエスケープはテスト必須（3 kind 分：claude/codex/opencode）。
- **ラッパー自身が OOM victim になる稀ケース**: 子（agent）ではなくラッパーの `sh` が OOM で殺されると `$?` を記録できない。実務上、victim は oom_score が高い巨大プロセス（node/claude 本体）になるため agent 側が選ばれるのが通常。取りこぼしは Phase 1 の `oom_kill` 差分＋セッション消滅で「原因未確定の消滅」として補足できる（A と B の相補性）。
- **意図的停止の取りこぼし**: 意図的停止フラグの set/clear に漏れがあると通常停止を `killed`/`crashed` と誤表示する。ハンドラ（Stop/Halt/Archive/Recreate、`session_handlers.go`）とシャットダウン（`shutdown.go` の `gracefulShutdown`→`C-c`→`kill-server`）の全経路を洗う。
- **`memory.events` パス**: CP は systemd cgroup ドライバ前提の `/sys/fs/cgroup/system.slice/docker-<id>.scope`（`metrics.go:125`）を読む。Agent 側で自 cgroup を読む場合（4.3 案 a）はコンテナ内から見た自分の cgroup パス（`/sys/fs/cgroup/…` 直下 or `/proc/self/cgroup` 起点）を使う点に注意。ECS（cgroup 構成が異なる）での差異も §7-4 で確認。
- **ホスト OOM は cgroup に出ない**: §2.1-3 の通り、`oom_kill` はホスト全体 OOM を取りこぼしうる。この場合は A の 137 のみ残るので `killed` ラベルに落ちる（誤って `oom` 断定しない設計）。

---

## 7. 決定事項（実装前に確定する）

1. **粒度スコープ**: 初期は「コンテナ OOM（Phase 1）＋ セッション exit code（Phase 2）」で十分か。per-session sub-cgroup（各 pane を独自 cgroup に入れて per-session `memory.events`／メモリ上限）は cgroup delegation 権限が要り重い → **当面オーバーキルとして見送り**でよいか。
2. **OOM ラベルの結線点**: §4.3 の (a) Agent 側完結 か (b) CP 側集約か。→ (a) 第一候補。
3. **永続化範囲**: セッション終了理由は `Meta`（ディスク永続、resume/TTL を跨ぐ）で確定。コンテナ OOM イベントは in-memory tracker で足りるか、履歴として残す（メモキュー投函 / ログ / DB ミラー）か。
4. **ECS 差異**: `memory.events` パスと docker `.State` 相当（ECS は `docker inspect` 不可）の取得経路を ECS でも確認。ECS は `runtime_ecs.go` 経由なので stopped reason / container exit code は ECS API 側から取れる可能性 → 別途確認。
5. **ラッパー方式 vs tmux フック方式**: §4.2。→ ラッパー第一候補。
6. **意図的停止フラグの実装**: 既存の `status.Remove` / Meta 更新の流れ（`session_handlers.go`）にフラグを相乗りさせるか、別ストアにするか。

意思決定は ADR 化済み: [decisions/0014-agent-exit-recording.md](../decisions/0014-agent-exit-recording.md)（実装で確定した §7 の各決定を記録）。

---

## 8. 段階導入まとめ

| Phase | 変更範囲 | 再ビルド | 得られるもの |
|---|---|---|---|
| **1** | `control-plane/metrics.go`（+ `routes.go`/`WsBar.tsx` 表示） | 不要 | コンテナ OOM の**確定検知**（`memory.events.oom_kill` 差分 ＋ docker `.State.OOMKilled/ExitCode`）＋ 事前警告。どのセッションかは不明 |
| **2** | `workspace/agent/session_tmux.go`＋`record-exit` サブコマンド＋`Meta` 拡張＋意図停止フラグ＋Console 表示 | 要（agent イメージ） | **セッション単位**の exit code / signal / 解釈済み `ExitReason`。Phase 1 と突き合わせて OOM 確定 |
| 3（見送り候補） | per-session sub-cgroup | 要 | per-session の `memory.events`／メモリ上限。§7-1 で当面オーバーキル判断 |

**Phase 1 を先行**（低リスク・即価値）、**Phase 2 で attribution**、が費用対効果的な順序。
