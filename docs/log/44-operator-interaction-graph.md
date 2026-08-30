# 44. オペレーター↔セッション やり取りの可視化（シーケンス図）

> 状態: **設計・着工中**。Phase 0（契約凍結）実装。以降 P1（3並列実装）→ P2（統合）。
> ADR: [0027](../decisions/0027-operator-interaction-graph.md)。前提: docs/30（セッション完了報告→
> フリート・オペレーター）、[history/19](19-assistant-chat.md)（assistant-chat）、docs/38（定時実行）。

## 0. 目的

フリート・オペレーター（af_write 会話）が `create_session` / `send_to_session` で駆動する
**複数セッションとのやり取りを、1オペレーター会話ごとに UML シーケンス図として可視化**する。
「いま誰に何を投げ、どれが稼働中で、いつ完了/異常終了が返ったか」を時系列の往復として一望する。

**別スコープ（本ドキュメント対象外・別タスク）**: 全オペレーター会話×全セッション×エージェントを
1枚で見る**フリート全体の俯瞰図（相関図）**は別の図・別タスク。

## 1. データ源 — 往復の非対称（設計の肝）

| 方向 | 永続性 | 源 |
|---|---|---|
| セッション→オペレーター（**報告**） | 永続 | 会話内 `role:"report"` メッセージ（`session`＋`ts`＋kind `answer-ready`/`exit`）。`chatGet()` で取得済み |
| オペレーター→セッション（**指示**） | 揮発 | arm ストア `session-report/<name>.json`＝`{conv,armed,at}`。**報告配送時に消える**。アシスタント発話は `steps[].tools` にツール名のみで宛先/文面は残らない |

→ 指示エッジを永続化するため**追記型ディスパッチ台帳**を新設する（§2）。報告エッジは既存のまま使う。

## 2. バックエンド（S-BE 実装）

### 2.1 ディスパッチ台帳
- ストア: 追記型 `~/.config/agent-fleet/operator-graph/<conv>.jsonl`。上限 N 件（古いものから捨てる）。
- 1件 = `DispatchEntry`（型は `console/src/types/opgraph.ts` が正）:
  `{ ts, session, sessionKind, kind:"launch"|"instruct", dir?, excerpt? }`。
  `excerpt` はプロンプト先頭 ≤140字・単一行に切り詰めた**表示専用**文字列（絶対に実行しない・
  レンダリング時サニタイズ。prompt-injection 方針は docs/30 を踏襲）。
- 書込点: `armSessionReport()` が呼ばれる箇所に `recordDispatch(conv, …)` を併置する。
  - `session_handlers.go`（`POST /sessions` create 成功）→ `kind:"launch"`（`dir`・initial_prompt 抜粋）
  - `session_io.go`（`POST /sessions/{name}/input`）→ `kind:"instruct"`（prompt 抜粋）
  - `session_io.go`（`POST /sessions/{name}/turn` managed）→ `kind:"instruct"`

### 2.2 読み出し API
- `GET /api/chat/conversations/{id}/dispatches` → `DispatchList`（`{ dispatches: DispatchEntry[] }`）。
- `workspace/agent/routes.go` と `control-plane/routes.go` の**両方**に登録（CP は明示許可リスト方式・
  memory `cp-rest-proxy-allowlist`）。既存の chat 会話サブルートと同じ登録パターンに合わせる。

## 3. フロント純ロジック（S-LOGIC 実装）

`console/src/lib/opgraph.ts` に `buildSeqModel: BuildSeqModel`（型は `types/opgraph.ts`）。
会話メッセージ（user/assistant/report）＋台帳＋ライブ `sessions`（name→Session）をマージし、
`SeqModel`（participants／arrows〔dispatch=実線→・report=破線⟵〕／activations〔稼働帯・未完は
`y1:null`〕／`laneX`／`height`）を算出。純ロジックは `.ts`（vitest が `.tsx` を拾わない件の回避）。
`opgraph.test.ts` で固定 fixture の幾何を検証。

## 4. フロント描画（S-VIEW 実装）

- 新ペイン種別 `kind:"opgraph"`（`conversationId` 保持）。`layout/types.ts` union／`Pane.tsx` 分岐／
  `paneTitle.ts` KIND_JA＋case／open-verb／`ChatView` の af_write 会話ヘッダに導線ボタン。
- `features/opgraph/OperatorGraphView.tsx`＋`opgraph.css`。手書き SVG（SCM `CommitGraph`/`lib/gitgraph.ts`
  を構造テンプレに流用）: ライフライン（縦破線・見出し `--kind-*` 色）・稼働帯（kind 色・ライブ稼働中は
  点滅）・矢印（実線=指示／破線=報告・`exit`/oom は赤）・左ガター時刻。ヘッダは `view-head fileinfo`
  規約（右余白 `--pane-ctl-w`）。`chatGet`＋dispatches を 4〜5s 軽ポール＋`onPush("sessions")` で状態色追従。
  ライフライン/稼働帯クリックでそのセッションを開く（`openSessionChat`/`openSessionTerminal`）。
- i18n は `ja`/`en` 両カタログ（`pane.kind.opgraph`＋`opgraph.*`）。色クラスは全 twin を grep 突合
  （memory `kind-color-css-checklist`）。

## 5. 制約（明記）

- 台帳は**導入以降のみ**（過去の指示は遡及不可。報告は既に履歴に残っている）。
- managed ドライバ（codex/opencode）の報告は**本文なし**＝完了の事実のみ。
- opencode の af_write 会話は `report_to` 自動注入が無く**非リンク**（docs/30 の制限を継承）。

## 6. 並列実行（P0/P1/P2）

- **P0 契約凍結**（本コミット）: REST DTO＋`types/opgraph.ts`（`DispatchEntry`/`SeqModel`/`BuildSeqModel`・
  import 専用）＋docs/44・ADR0027 骨。pane 種別 union/Pane.tsx/paneTitle は触らない（S-VIEW 専有）。
- **P1 3並列 worktree**（ホスト OOM 対策で同時ビルド禁止・並列 ≤3）: S-BE(Go 台帳+API)／
  S-LOGIC(`lib/opgraph.ts`+test)／S-VIEW(view+pane 配線+i18n・fixture で描画)。共有グルーは S-VIEW 専有。
- **P2 統合**: マージ順 BE→LOGIC→VIEW・fixture を本物 `buildSeqModel` に差替・全ゲート
  （`go test`/`tsc`/`vitest`/`vite build`/`i18n:lint`）＋ headless 描画検証・ビルドは1つずつ。
