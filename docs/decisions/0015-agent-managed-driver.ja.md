# 0015. エージェント制御の Managed Driver 化 — 共有 runtime＋構造化 RPC を既定・read 層温存・CLI ルート常設

[English](0015-agent-managed-driver.md) | 日本語

- 状態: 確定・P1〜P3 実装済み（2026-07-15）——P1（Codex 観測拡張）・P1.5（Console 受け皿＋Driver 層 IF）・
  P2（OpenCode managed 化 — Driver/RuntimeSupervisor/turn 状態機械/Interaction/reconciliation の初出、
  managed 作成解禁・opencode 新規既定＝managed。実測記録は docs/27 §12.2）、P3（Codex managed 化 —
  第2 Driver・daemon drain・新規既定・双方向排他切替。実測記録は docs/27 §12.3）まで完了
- 関連: [27-agent-managed-driver.md](../log/27-agent-managed-driver.md)（設計本体）/
  [0012-go-internal-refactor.md](0012-go-internal-refactor.ja.md)（`internal/agents` の Agent IF——本決定が増築する read 層）/
  [0014-agent-exit-recording.md](0014-agent-exit-recording.ja.md)（pane ラッパー——managed 化で supervisor へ移設）

## 背景

エージェント制御は 3 種とも「tmux 内 TUI＋send-keys 入力＋capture-pane スクレイプ＋注入 hooks＋
ネイティブストア解析」で成立している。Codex TUI のモデル勝手切替バグ（利用率 93〜99% で
`ThreadSettings` 連投→軽量モデルへ意図せず切替→圧縮。暫定対処 `hide_rate_limit_model_nudge` トグルは
`9414525` で main 済み）が露呈させたのは、**TUI 固有の対話にキー入力で応答する構成では、AF が構造化
イベントとして検知・制御できない**という限界。一方 Codex CLI 0.144.3 の `app-server`（双方向 JSON-RPC）は
既に Workspace ごとに 1 プロセス常駐しており（`codex_appserver.go`、圧縮検知の read-only オブザーバ、
`fa7e47d`）、OpenCode には TUI 併用が公式サポートの `opencode serve`（HTTP＋SSE）がある。
Claude だけは 1 プロセス集約の口がない（Agent SDK / stream-json は session 毎子プロセス、
Remote Control は公開ローカル API なし）。

設計は並行 2 セッション（sol=A / fable=B）で独立に起こし、比較の上でユーザーが裁定した。

## 決定

1. **ドライバ方針（エージェント別）**: **Codex / OpenCode は managed（共有 runtime＋構造化 RPC）を既定の
   第 1 ドライバ**とし、**ユーザー選択の CLI（TUI）ルートを常設**する。CLI ルート＝TUI が writer・AF は
   共有 runtime 経由の read-only 観測・チャット⇔ターミナル維持。切替は Codex が双方向排他
   （stop→drain→resume 経由）、OpenCode は排他不要（serve 直列化・TUIAttach 可）。
   **Claude は現状のターミナル CLI を維持**（compact 応答等の CLI 操作が運用上必要。Session Manager／
   idle eviction 案は凍結し docs/27 付録 A に温存）。TUI 経路の撤去はしない（保守対象として残る）。
2. **骨格はボトムアップ増築**: 既存 read 正規化層（`internal/agents` の `Agent`／`TranscriptData`／
   `WireLive`）を無傷で温存し、Driver 層（thread 単位の Send/Steer/Interrupt/UpdateSettings/Respond/
   Events/Snapshot）と RuntimeSupervisor 層（daemon 起動・再起動・generation・drain）を増築する。
   プロセス管理責務は Driver から分離する（A 案の部品）。
3. **記録は三層分担・二重永続化なし**: read＝ネイティブストア正本（rollout JSONL / SQLite / `<sid>.jsonl`）、
   live＝runtime イベント、write＝構造化 API。AF が永続化するのは会話内容を含まない運用メタデータ
   （turn 状態遷移・ClientMessageID 台帳・Interaction 監査・generation 履歴）のみ。履歴互換は正本を
   動かさないことで自動成立。
4. **turn 状態機械＋ClientMessageID**: `queued/starting/running/waiting_interaction/interrupting/
   completed/failed/cancelled/unknown` を明示し、AF 採番の ClientMessageID で再送を冪等化。切断時は
   unknown に落とし、**reconciliation 共通手順**（generation 更新→snapshot→ネイティブ履歴照合→Console
   snapshot→live 再購読）で回復する。イベント再生ではなく snapshot 照合。
5. **Interaction 一般化**: 承認/質問/plan 確認を `Interaction`（Decision: allow/deny/cancel/answer＋
   Scope: once/turn/thread）に構造化。初期実装は question 系のみ（3 者とも承認は bypass 運転のため）。
6. **認証・設定の反映は generation＋drain に一本化**: 再ログイン・config 変更・daemon 更新は
   「新世代を起こし旧世代を drain」のプロセス再生成パスで反映（Codex=daemon 再起動→全 thread 再 resume、
   OpenCode=env 注入ゆえ再起動必須で同一パス）。ホットリロードに賭けない。
7. **ワイヤは既存の汎用 `/sessions` 面のまま**: per-agent REST（`POST /claude/sessions/...` 等）は作らない。
   Console はエージェント固有知識を持たず Capabilities（Steer/Fork/DynamicModel/DynamicEffort/DynamicMode/
   Permissions/Questions/EventReplay/EphemeralThread/TUIAttach）から描画を決める。
8. **アシスタントチャットは統合しない**: 3 種 one-shot（`codex exec`／`claude -p`／`opencode run`、
   別ホーム隔離）を維持。thread 単位 config で隔離ホーム相当（ユーザ MCP 不起動・履歴不汚染）を再現できると
   確認できるまで見送り（将来の受け皿は `EphemeralThread`）。
9. **着手順**: P1 Codex 観測拡張（read-only、発端バグの TUI 層 vs サーバ側 reroute 切り分け）→
   P1.5 Console managed セッション UI → P2 OpenCode managed（Driver 型の初出・排他不要で最も安全）→
   P3 Codex managed 既定化＋ドライバ選択 UI＋排他切替。

## 捨てた選択肢

- **ハイブリッド書き込み（TUI と AF の二重 writer）**: rollout 書込・モデル設定・ターン状態が競合。
  threadごと単一 writer を不変条件とし、併存は「writer=TUI＋観測=AF」（CLI ルート）に限定。
- **Event journal / read model 新設による read 層の置き換え**（A 案）: 正本の複製で欠落・不一致の管理が
  増え、過去セッションの履歴互換も壊れる。read 層温存＋運用メタデータ補完に縮退して採用。
- **per-agent REST 面**（suqhrov の Claude 案）: エージェント別 API が 3 組に増殖し統一 Driver と矛盾。
- **共通 ControllerLease 機構**（A 案）: OpenCode は lease 不要（併用公式サポート）、Codex は排他切替のみで、
  共通機構は過剰。thread 毎 controller フィールド＋エージェント別許容遷移に縮退。
- **Codex/OpenCode の TUI 全廃**: 一度確定しかけたが、ユーザー選択の CLI ルートを残す形へ転換
  （操作の保険・ユーザーの明示的なメモリトレードオフとして）。
- **Claude Session Manager（stream-json 子プロセス＋idle eviction）の即時採用**: メモリ効果
  （待機 child の evict）はあるが、CLI 直接操作が必要な運用と排他になるため凍結。設計は付録で温存。
- **kind の分割（codex-app 等の新 kind）**: transcript/settings/auth/models を共有するため、
  driver は `session.Meta` のフィールドとし kind は分けない。

## 帰結

- 発端バグ（モデル勝手切替）は managed セッションで構造的に消える（AF が唯一の writer）。CLI ルートは
  `hide_rate_limit_model_nudge` トグルで抑止する二段構え（トグルは恒久残置）。
- メモリ: Codex 実測で TUI 1 セッション約 280 MiB → 共有 app-server 約 129 MiB＋thread 分。既定 managed で
  大勢を取り、CLI 選択者のみ約 +230 MiB を払う（選択 UI にコスト表示を検討）。Claude は改善しない。
- WireLive のヒューリスティクス（hooks 状態・rollout heal・pending probe・usage 解析・capture-pane）は
  managed でイベント駆動に置換。CLI ルート分と Claude 分は保守対象として残る。
- Console paneless 対応（ミラー主 UI・Interaction 応答・API 添付・exit recording の supervisor 移設）が
  P2 前のクリティカルパス。
- 実装前の筆頭検証だった server 経由作成 thread の TUI resume は成立し、managed→TUI→managed の
  双方向切替と旧TUI rollout互換を実証した。質問requestの再配送、resume後のpolicy再表明、daemon kill後の
  interrupted確定を含む実測とE2Eは docs/27 §12.3 に記録。
