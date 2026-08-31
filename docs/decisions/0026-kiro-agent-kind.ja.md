# 0026. `kind=kiro`（Kiro）を第8のエージェント種別として追加する

[English](0026-kiro-agent-kind.md) | 日本語

- 状態: **採用（Track A＋A2＋B＋C＋D 実装済み）**（2026-07-24。Track 0 プローブ合格 →
  read 層＋TUI＋managed driver＋配備＋CP/Console＋ライブ使用量配線を実装。`go build`/`go vet`/
  `gofmt`／`go test`＋`KIRO_LIVE=1` 実 `kiro-cli` 契約テスト／Console typecheck・i18n:lint・
  vitest・vite build 緑。Track A/B/C/A2 は全 P1 レビュー修正込みで develop へマージ済み
  〔merge de2fb25b〕、Track D は temp/kiro-track-d）。残＝実フリート再ビルド後の実機目視。
- 関連: [0023](0023-cursor-agent-kind.ja.md)（cursor — 直近の種別追加・本件のテンプレ）、
  [0019](0019-copilot-agent-kind.ja.md)（copilot — 紫色の前所有者）、
  [0008](0008-antigravity-cli-agent-kind.ja.md)（agy — Terminal 専用 MVP・ContextReporter の先例）、
  [0015](0015-agent-managed-driver.ja.md)（managed driver 抽象）。
  実装計画・Track 0 実測・各 Track の実装メモ（read 契約・ACP 契約・配備・色・ライブ使用量）は
  [docs/43](../log/43-kiro-agent-kind.md)。
  ※ 0022 はエージェントメモリ版管理（未マージブランチ temp/s7in3bh）が、0025 は native 自動更新が
  使用中のため 0026 を採番。

## 背景

Kiro（`kiro-cli`・旧 Amazon Q Developer CLI、2025-11-17 改名。AWS Kiro IDE のターミナル版）
は `kiro-cli acp`（ACP = JSON-RPC over stdio）・`chat`（TUI）・`--list-models -f json`・
`--resume-id`・v2 JSONL セッションストア（`~/.kiro/sessions/cli/<sid>.jsonl`・TUI と ACP が共用）を
備える。実バイナリ 2.14.1 を本 Workspace（Debian 12/x86_64/glibc 2.36）へ導入し Builder ID（free）
device-flow ログイン込みで全プローブを実施した（docs/43 §2）。既存 7 種の中では cursor/copilot
（per-session-child ACP driver）に最も近い統合面を持つ。

## 決定

1. **色 = 紫（Kiro が copilot から紫を継承）。3 種同時変更**（ユーザー決定 2026-07-24）。実描画
   （両テーマ headless chromium スウォッチ）で確定した最終値: **kiro = dark #a371f7 / light #8250df**
   （旧 copilot 値）、**copilot = 中立チャコール dark #7d8590 / light #30363d**、**opencode = 薄い
   スレートグレー dark #aab4be / light #6e7781**。方針の候補値（copilot dark #6b7075／light #24292f、
   opencode light #9aa4ae）は暗背景チャコール／白背景淡グレーで低コントラストだったため、階層
   （copilot=濃いめ・opencode=薄め）を保ったまま可読値へ寄せた。色クラス twin は `kind-color-css-checklist`
   の全ファイル（tokens.css dark/light＋app/terminal/sessions/settings/ui.css）を 3 種総ざらい。
   **アイコン=`compass`（codicon）・表示順=copilot の後**（ユーザー確認済み）。

2. **配備 = 既定オンデマンド導入・利用ユーザー限定、`BAKE_AGENT_CLIS=1` では焼いてよい**
   （ユーザー決定 2026-07-24）。展開 ~855MB（`kiro-cli-chat` 663M 含む）が他 kind と桁違いに巨大なため、
   lean 一律 boot-install ループには**入れない**。kiro を使うユーザーの**初回起動時（or 接続カードの
   インストールボタン）に、その `~/.local` へ manifest sha256 ピン付きで導入**する新パターン
   （`workspace-agent install-kiro`）。arm64/Debian12 は glibc 2.39 要求を避け **musl 変種**必須。
   auto-update は `app.disableAutoupdates` を entrypoint 起動毎に再固定。855MB が home ボリュームに
   載る旨は UI で明示。

3. **headlessChat = 不要（v1 スコープ外で確定）**。`ASSISTANT_AGENT_KINDS`/`defaultHeadlessOrder` に
   kiro を加えない。headless `--no-interactive` は JSON を出さず（issue #5423/#9066）ACP 経由が筋だが、
   アシスタントチャットの需要は既存バックエンドで足りる。**タイトル AI 提案は現行機構のまま動く**
   （`session_title.go` の oneShotHeadless が generic read 層＝Track A の転写実装を読む）。Track D で
   再検討したが変更なし。

4. **ToS = 注意事項として記載**（Builder ID free の業務利用可否・組織ポリシー整合は採用組織側の
   確認事項）。**開発・検証は Free（Builder ID）で進める**。

5. **セッション ID は CLI 採番。`session/new` へ降格してよいのは「そのストア（`<sid>.json`）が実際に
   消滅したとき」だけ**（レビューで確定・A2-1）。kiro はセッション ID を CLI 側で採番し、自己採番の
   `--resume-id` を渡しても採用されない（実測）。resume の `session/load` は `.lock`（pid 入り）による
   クロスプロセス排他で「active in another process」を返し得るが、**この lock-busy を「会話が消えた」と
   誤認して `session/new` で新規化すると、生きた会話を無言で切り離す**。したがって新規化の可否は
   **オンディスクのストア存在**という決定的事実から判断し、lock エラー文言の有無やドリフトでは判断しない
   （`isLockBusy` は -32603 AND メッセージで厳格化し、RETRY 判断のみに使う）。壊れたストアは resume を
   永久エラーにする方が会話保全上正しい（意図的 fail-safe）。

6. **sid 発見は「枠の作成時刻」でフェンスする**（レビューで確定・A-1）。kiro は起動後に
   `~/.kiro/sessions/cli/<sid>.json`（cwd 記録付き）を生成するので、AF は cwd＋更新時刻でそれを発見して
   sidstore にキャッシュする。しかし **recreate は同一 dir に新しいスラグを切る**ため、フレッシュ起動の窓で
   同一 cwd に居残る**前身セッションを誤って掴む**危険がある。よって discover は**その枠の CreatedAt
   （Meta.CreatedAt・作成時に確定し resume 跨ぎで安定）以降に作られたストアだけ**を採用する。managed 経路
   でも `threadHandle.createdAt` を spawn の discover へ引き回して同じフェンスを効かせる。CreatedAt が
   解釈不能なときはフェンス無し（発見不能で固まるより退行が軽い）。

7. **ライブ使用量は `_kiro.dev/metadata` を %→token 変換で既存 UI に載せる（Track D）**。cursor は
   ライブ経路に usage が一切乗らず不採用だった（ADR0023 決定7）が、**kiro は managed（ACP）の
   `_kiro.dev/metadata` 通知に `contextUsagePercentage`（0–100・最新値）＋`meteringUsage`（credit・累積）が
   毎ターン乗る**（実測）。転写にトークンが無いため、**% をモデルの実 context window（`--list-models` の
   `context_window_tokens`）に対するトークン数へ変換**し、window を明示で渡すことで既存のトークンベース
   ContextBar／`get_session_usage` にそのまま載せる（% は厳密往復）。ミラーへは agy と同じ
   `agents.ContextReporter`（`ContextFill`）seam で配線し**フロント無改修**（managed=paneless はミラーが
   唯一のビュー）。credits は `get_session_usage.cumulative.credits` で返す。**プラン残量チップ（/usage
   PTY スクレイプ → get_agent_usage）は本 Track では見送り**（機械可読手段なし＝issue #7752・非公式 API/
   スクレイプの脆さは usage-chip 429 事件と同種のため、必要時に別途起票）。**API キー認証も見送り・
   login-only 継続**（TUI への env 注入が `ps` 露出＝ADR0023 決定5 と同理由）。

## リスク（受け入れ）

- 週次リリースの CLI ドリフト。managed は ACP 公式契約（`session/update` 判別子・`session/load` リプレイ・
  `.lock` 解放・`stopReason`）依存＋`KIRO_LIVE` 契約テストで一次検知。TUI は明示テキスト契約
  （「Kiro is working」/「ask a question or describe a task」/「requires approval」）で、2.14.1 に Stop hook が
  無い（実測）ためスピナー regex は使わない。metadata の field 名/スケールが変われば Track D の契約テストで
  落ちる。
- 展開 855MB のオンデマンド導入。中断（初回起動待ちきれずペイン kill）に対し staging→原子 rename＋
  presence marker（kiro-cli を最後に設置）＋`--version` サニティ＋flock 排他で自己修復可能に。実 855MB DL の
  通し目視は実フリート再ビルド後。
- linux arm64: musl 変種の資産健全性は検証済だが実 arm64 ハードでの起動は未検証（本コンテナ x64）。
- v2 JSONL ストアの世代差（v1 SQLite／v3 JSONL）。`--agent-engine v2` を明示ピンして read/状態契約が
  将来の既定 v3 化で崩れないよう保険。
- ライブ使用量は**稼働中 managed のみ**（in-memory・停止/TUI/未受信は非表示）。token 数は % からの概算
  （% 自体は正確）。

## 結果

- Track A/A2/B/C 実装済み（2026-07-24）＝read 層＋TUI 状態＋v2 JSONL 転写／managed ACP driver／
  オンデマンド配備＋焼き込みノブ／CP・Console 配線＋色3種同時変更。全 P1 レビュー（9 件）修正込みで
  develop へマージ（de2fb25b）。
- Track D 実装済み（2026-07-24・temp/kiro-track-d）＝`_kiro.dev/metadata` のライブ context%／credits を
  %→token 変換で ContextBar（ミラー・ContextReporter 経由）と `get_session_usage`（context＋
  cumulative.credits）へ配線。headlessChat／API キー／プラン残量チップは決定7 のとおり見送り。
- 残: 実フリート再ビルド後の実機目視（色描画・オンデマンド 855MB 導入・device-flow ログイン・
  ミラー ContextBar の pct 推移）と arm64 実機起動。詳細・トラック分割・プローブ一覧は
  [docs/43](../log/43-kiro-agent-kind.md)。
