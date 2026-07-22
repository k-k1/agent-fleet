# 37. チャットブリッジ（Slack / Discord 連携）— 通知・双方向操縦・承認ゲート

- 状態: **P1 実装済み（Discord）**（2026-07-22。P2 以降は未着手）。採用判断は
  [decisions/0020](decisions/0020-chat-bridge.md)。実装メモは [§P1 実装記録](#p1-実装記録2026-07-22)。
- **着手順は Discord 優先**（2026-07-22 決定）: トークン準備が最軽量（私設ギルド＋Bot、
  管理者承認・課金なし）のため、P1→P2 を Discord で縦に貫通させてから Slack を
  同じプロバイダ抽象に足す。抽象の設計は最初から 2 プロバイダ前提で行う
  （Discord 専用の形に倒さない）。
- ゴール: Slack / Discord を **agent-fleet のモバイルフロントエンド**にする。
  セッションの完了・質問・異常終了がスマホに届き、その場で返信・選択肢回答・承認まで
  できる状態。Console を開けない外出先でもフリートが止まらない。
- 非ゴール（v1）:
  - MS Teams の双方向（Bot Framework は公開 HTTPS 端点＋Entra ID 登録が必須で、
    native/WSL2/NAT 裏デプロイと非互換 — 将来の送信専用枠として P4 に留める）。
  - Console 通知センターとの既読（seen）同期。チャット側は**写し**であり正本は通知センター。
  - チャンネル公開運用（複数人が 1 セッションを操縦する形）。v1 は本人 1:1 のみ。

## 前提（2026-07-22 時点の外部仕様）

| | Slack | Discord | Teams |
|---|---|---|---|
| 外向き常時接続での受信 | ○ Socket Mode（WSS、公開端点不要） | ○ Gateway（WSS、公開端点不要） | ✗（Bot Framework は公開端点必須） |
| ボタン/選択 UI | ○ Block Kit | ○ Message Components（ボタン 5×5・セレクト） | Adaptive Card（受信不可のため実質 ✗） |
| スレッド | ○ | ○ | ○ |
| 送信のみ | ○ chat.postMessage | ○ REST | ○ Workflows webhook / Graph 委任投稿 |
| Go ライブラリ | slack-go/slack (socketmode) | bwmarrin/discordgo | —（着手時に現行仕様を再確認） |
| 資格情報 | bot token `xoxb-` ＋ app-level token `xapp-` | bot token | webhook URL 等 |

Slack と Discord は「外向き WSS 1 本で送受信・ボタン応答まで完結」という同型であり、
1 つのプロバイダ抽象に載る。これが本設計の核。

## 先に固定する契約

1. **プロバイダ抽象は capability フラグ付き**: `canSend` / `canReceive` / `canInteract`。
   Slack・Discord は全 ○ の常時接続実装、Teams（将来）は `canSend` のみの送信専用実装。
   Console はフラグで「このプロバイダは通知のみ」を表示し分ける。
2. **ブリッジは workspace Agent 側に置く**（CP ではなく）。理由: トークンは per-user の
   `secrets.enc`（`internal/secrets/secrets.go` `Data`）にあり、「秘密は CP を素通り」
   原則（docs/07 §7.6・dev/08）を維持するため。WSS 接続もユーザー毎に Agent 内
   goroutine で 1 本（プロバイダ接続中のみ）。
3. **トークンはユーザー自前登録**（v1）: 中央ホストの共有 App は作らない。
   ユーザーが自分の Slack App（Socket Mode 有効）/ Discord Bot（私設ギルド招待済み）を
   作り、トークンを Settings > Connections に登録する。PagerDuty/Grafana と同じ
   カード＋`handlePut…Conn` 型（`connections.go` / `routes.go` / CP `routes.go` の三点セット）。
4. **配送 seam は通知 outbox の脇**: `notice.Put`（`internal/notice/notice.go`）で
   outbox に書いた直後、ブリッジ配送キューにも積む。**outbox 本体は絶対にブロック
   しない**（egress 制限環境でチャット先が不達でも Console 通知は無傷）。
   配送は fire-and-forget＋有限リトライ（上限超過は破棄しログのみ）。
5. **双方向の同一性検証は必須**: Slack user ID / Discord user ID ↔ AF ユーザーを
   Connections 登録時に明示紐付けし、**紐付いた本人の DM／本人発言のみ**をセッションに
   ルーティングする。ボタン押下（AUQ 回答・承認）も押下者 ID を検証。
   チャンネル残留者の発言・他人の押下は無視（プロンプトインジェクション対策）。
6. **セッション↔スレッド対応表は Agent 側の小さな永続 store**
   （`~/.config/agent-fleet/` 配下。sessionName → channel/thread ID）。
   コンテナ recreate 後も home 永続で生き残る。
7. **質問・承認は構造化写像**: AskUserQuestion の選択肢 → Block Kit / Components の
   ボタンにそのまま写す。tmux ペインへのキー送出（AUQ キー駆動の既知の壊れやすさ）を
   経由せず、既存の構造化応答経路（/respond・send_to_session）に落とす。

## ユースケースとフェーズ分割

### P1 — Connections ＋ 片方向通知（canSend）

- `secrets.Data` に `Slack` / `Discord` 追加、PUT/DELETE ルート三点セット、
  Console カード 2 枚（i18n ja/en・i18n:lint）。
- 送るイベント（トグルで選択可）: `answer-ready` / `question` / `permission-request` /
  異常終了（oom/crashed/killed — `record_exit.go`）/ セッション完了報告
  （docs/30 `buildReportContent` の報告文を再利用）。
- 通知文にはセッション表示名・kind・要約を含める。リンクは Console の該当
  セッション URL（deployment のベース URL が取れる場合のみ）。

#### P1 実装記録（2026-07-22）

- **プロバイダ抽象**: `internal/bridge`（workspace Agent）。`Provider` interface
  （`Name/Caps/Wants/Send`）＋ `Caps{CanSend,CanReceive,CanInteract}`（契約 1）。
  Discord は P1 では Send のみ配線（REST だけで送れる — Gateway は P2 の受信で張る）。
- **配送キューはファイル**（`~/.config/agent-fleet/bridge-queue/`）: `notice.Put` と
  `record-exit` は**hook 子プロセス**（`workspace-agent session-status` 等）で走るため、
  積む側はファイル 1 枚の書き込みのみ（非ブロック・エラーは飲む — 契約 4）。送信は
  デーモン側 `bridge.StartSender()`（3 秒 tick・単一 goroutine・逐次送信）。
  有限リトライ = attempts をエントリに永続化し 5 回で破棄ログ、キュー上限 200
  （古い方から破棄）。未設定時はキューを掃除するだけ。
- **異常終了イベント**: exit（oom/crashed/killed）は notice outbox を通らない
  （セッション一覧が ExitInfo を直接出す）ため、`record_exit.go` から独自に enqueue。
- **イベントトグル**: `bridge.EventKeys` = answer-ready / question（plan-approval を含む）/
  permission-request / exit / session-report。空 = 全部オン。chat-*（コンテキスト系）は
  ブリッジ対象外。
- **Connections**: `secrets.Data.Discord`（token＋channelId/userId 排他＋DM チャンネル
  キャッシュ＋events）。三点セット `PUT|DELETE /connections/discord`（agent）＋
  `/api/connections/discord`（CP proxy）＋ OpsTab の DiscordCard（カテゴリ
  「チャット通知（ブリッジ）」・i18n ja/en）。PUT 時に `GET /users/@me` でトークン検証
  （401/403 は拒否、ネットワーク不達は保存を許す — egress 制限環境向け）。
- **通知文**: 表示名・kind・見出しのみ（秘密・生ログなし）。`AF_CP_BASE_URL` がある時だけ
  Console リンクを付ける（セッション deep link は残課題のまま）。
- **検証**: ユニット（キュー境界・リトライ/破棄・トグルフィルタ・Discord REST 契約 =
  httptest・notice fan-out）＋ live 契約テスト `AF_DISCORD_LIVE=1 AF_DISCORD_TOKEN=…
  AF_DISCORD_CHANNEL=…`（`internal/bridge/discord_live_test.go`）。実 Discord 通し・
  スマホ実機目視は未（トークンはユーザー準備）。

### P2 — 双方向: スレッド＝セッション ＋ AUQ ボタン（canReceive / canInteract）

- セッション初回通知でスレッドを起こし、以後の報告・質問を同スレッドへ。
- スレッド返信 → 対応セッションへ `send_to_session` 相当（`/input` 経路）で注入。
- AUQ・permission-request をボタン化。押下 → 構造化回答。タイムアウト・
  セッション側で先に回答済みのケースはボタン無効化更新で表面化。
- Slack: Socket Mode（`xapp-` token）。Discord: Gateway intents は
  DM＋ギルドメッセージ＋interactions の最小構成。

### P3 — 承認ゲート ＋ オペレーター bot

- オペレーターの shell 実行確認（方針C）・破壊的操作を「承認/却下」ボタン付き
  メッセージに写像。押下者検証は契約 5 のとおり。
- オペレーターセッション（`operatorPersona`）へのチャット窓口:
  `/af run <repo> <task>`（スラッシュコマンド/テキストコマンド）→ create_session、
  「フリート何やってる？」→ list/usage 系 MCP ツールの回答を整形返信。

### P4 — 周辺 ＋ Teams 送信専用

- Slack message shortcut / Discord メッセージコマンド → メモキュー取り込み
  （PWA Share Target のチャット版）。
- 日次ダイジェスト（稼働セッション・コミット・usage 消費）。
- Teams: Workflows webhook への Adaptive Card 送信（canSend のみ）。
  着手時に Microsoft 側の現行仕様を再確認（変更が激しい領域）。

## セキュリティ / 落とし穴（先に潰す論点）

- **インジェクション面**: 受信ルーティングは契約 5 の本人限定が唯一の防壁。
  チャンネル聴取のような「面」を最初から作らない。受信文はそのまま user 入力として
  注入し、システム側指示と混ぜない。
- **秘密の露出**: 通知文にトークン・鍵・ログ断片を含めない（既存の
  「秘密はコピーしない」運用と同じ）。報告文生成は docs/30 の要約経路を使い、
  生ログは送らない。
- **不達と滞留**: outbound 制限環境では接続自体が張れない。接続失敗は Connections
  カードに状態表示（`connected/error`）し、配送キューは有限（溢れたら古い方から破棄）。
- **メモリ**: WSS 常駐は goroutine ＋数 MB 級に抑える（memory-constrained host）。
  接続はプロバイダ登録済みユーザーのみ・切断時バックオフ再接続。
- **通知二重化**: Console を見ている最中もチャットに飛ぶ。v1 は許容（写し原則）。
  煩ければ P2 以降で「Console フォアグラウンド時は抑制」を検討。

## 検証方針

- 送受信はプロバイダ実物での live 契約テスト（copilot の AF_COPILOT_LIVE 方式に倣い
  `AF_SLACK_LIVE=1` / `AF_DISCORD_LIVE=1`、トークンは env 供給・CI では skip）。
- ボタン写像・identity 検証・不達リトライはユニットで固定。
- 実機目視: スマホ実機（Slack/Discord アプリ）で P1 通知 → P2 返信・ボタン回答の通し。

## 残課題（起案時点の未決）

- Console セッション URL の組み立て（deployment ベース URL の取得手段）。
- Discord の私設ギルド前提を README/ガイド（member/）にどう書くか。
- オペレーター bot（P3）の応答をどのセッション実体に持たせるか
  （常駐オペレーター 1 本を bot 専属にする案が有力 — P3 着手時に確定）。
