# 0027. オペレーター↔セッションのやり取りを SVG シーケンス図で可視化

[English](0027-operator-interaction-graph.md) | 日本語

- 状態: **採用・着工中**（Phase 0 契約凍結）。設計は [docs/44](../log/44-operator-interaction-graph.md)。
- 関連: [0015](0015-agent-managed-driver.ja.md)（managed driver）/ [0021](0021-scheduled-execution.ja.md)（定時実行）/
  docs/30（セッション完了報告→フリート・オペレーター）/ [history/19](../log/19-assistant-chat.md)（assistant-chat）

## 背景

フリート・オペレーター（af_write 会話）は複数セッションを `create_session` / `send_to_session` で
駆動し、各セッションは完了/異常終了を `role:"report"` メッセージで会話へ返す（docs/30）。この
**やり取りを時系列の往復として一望する図**が無く、「いま何を投げ、どれが稼働中で、いつ返ったか」を
チャットの行を遡って読むしかなかった。

## 決定

**1オペレーター会話ごとに、手書き SVG の UML シーケンス図で可視化する。**

1. **描画は手書き SVG（mermaid 不採用）**。この機能の価値はライブ状態の色付け・稼働中アニメ・
   ノードクリックでセッションを開く・`--kind-*` テーマ追従にあり、静的な mermaid では賄えない。
   SCM コミットグラフ（`CommitGraph`/`lib/gitgraph.ts`）の「純関数レイアウト＋インライン SVG」を
   構造テンプレートに流用する。
2. **指示エッジは新設のディスパッチ台帳で永続化する**。往復は保存が非対称で、報告
   （セッション→オペレーター）は会話内 `role:"report"` に永続だが、指示（オペレーター→セッション）は
   arm ストアにしか無く報告配送時に消える。`armSessionReport()` 併置点で追記型
   `~/.config/agent-fleet/operator-graph/<conv>.jsonl` に `{ts,session,sessionKind,kind,dir,excerpt}` を
   書く。読み出しは `GET /api/chat/conversations/{id}/dispatches`。
3. **範囲は 1 オペレーター会話**。データモデル（`report_to`＝会話 id）と素直に一致し、ChatView ヘッダの
   af_write 会話向けボタンから開く。
4. **契約先行の 3 並列**。P0 で REST DTO＋TS 型（`console/src/types/opgraph.ts`・import 専用）＋
   doc/ADR 骨を凍結し、P1 で S-BE(Go)／S-LOGIC(`lib/opgraph.ts`)／S-VIEW(view+pane 配線+i18n) を
   worktree 並列。共有グルー（pane union/Pane.tsx/paneTitle/ChatView/i18n）は S-VIEW 専有にして
   衝突をマージ 1 点へ閉じる。

### 捨てた選択肢

- **mermaid `sequenceDiagram`**（既存依存で最小コード）: 静的で、ライブ状態・稼働アニメ・クリック起動・
  テーマ注入に弱い。まず安く試すなら有力だが、本機能の主眼（ライブ俯瞰）に合わないため不採用。
- **ノードリンク（放射状ハブ）図**: トポロジは映えるが「やり取り＝時間的往復」を表現しづらい。
  力学レイアウトの実装も重い。
- **全フリート俯瞰を同じ図で兼ねる**: 複数会話の突合とレイアウトが重く、シーケンス図の主眼を薄める。
  コミットグラフ×セッション×エージェント相関の**別図・別タスク**に切り出す。
- **指示エッジを台帳無しで既存データから再現**: 報告側だけ線が引け往路が片側欠ける。約30行の台帳で
  完全化できるため、指示側も永続化する方を採る。

## 帰結

- 追加は Agent 側 `operator_graph.go`（台帳＋読み出し）＋ `session_handlers.go`/`session_io.go` の
  `recordDispatch` 併置＋ルート（agent／CP 許可リスト両方）／Console（`types/opgraph.ts`・
  `lib/opgraph.ts`・`features/opgraph/*`・pane 配線・ChatView 導線・i18n）。既存の報告・通知経路は無改造。
- **限界（意図）**: 台帳は導入以降のみ（過去指示は遡及不可・報告は既に履歴）。managed 報告は本文なし。
  opencode の af_write 会話は `report_to` 非注入で非リンク（docs/30 の制限を継承）。
