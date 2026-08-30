# 37. チャットブリッジ（Slack / Discord 連携）— 通知・双方向操縦・承認ゲート

- 状態: **P1／P1.5＋P2a（受信＝スレッド返信→注入）＋全文ブリッジ（応答本文投稿）＋
  P2b（AUQ／許可／プラン承認のボタン化・claude/TUI＋managed codex/opencode/copilot）＋
  通知/全文の整理（全文は本文のみ・メンション時間ゲート・受信 ack）＋
  P3先取り（@メンション→フリート・オペレーター会話・専用スレッド）＋
  P3 承認ゲート（破壊的操作＝削除系＋shell を Discord のボタンで承認）実装済み（Discord）＋
  重複・可読性の修正（429/配送冪等化・session-report 二重掲出解消・Console 入力ミラー・長文分割・
  テーブル→コードブロック・区切り線）実装済み＋
  **Slack 追随（Socket Mode で全機能パリティ＝送信・スレッド=セッション・メンション・deep link・
  全文・双方向受信・AUQ/許可/プランのボタン化・オペレーター会話・P3 承認ゲート）実装済み**
  （2026-07-23。Discord/Slack 同時接続対応＝store は provider スコープ化）。採用判断は
  [decisions/0020](../decisions/0020-chat-bridge.ja.md)。実装メモは
  [§P1 実装記録](#p1-実装記録2026-07-22)／[§P2a 実装記録](#p2a-実装記録-スレッド返信--セッション注入2026-07-22)／
  [§全文ブリッジ 実装記録](#全文ブリッジ-実装記録-応答本文をチャットへ2026-07-22)／
  [§P2b 実装記録](#p2b-実装記録-askuserquestion許可プラン承認のボタン化2026-07-22)／
  [§P3 承認ゲート 実装記録](#p3-承認ゲート-実装記録-破壊的操作をボタンで承認2026-07-23)。
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
   原則（build/07-security.ja.md §7.6・dev/08）を維持するため。WSS 接続もユーザー毎に Agent 内
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
  AF_DISCORD_CHANNEL=…`（`internal/bridge/discord_live_test.go`）。
  **実 Discord DM 通しは 2026-07-22 に完了**（実フリート反映後、本番経路の実
  answer-ready が着弾・Console リンク付与も確認）。

#### P1 追補: カードのセットアップウィザード化（2026-07-22）

初期設定コスト（契約 3 のトレードオフ）を、貼られた bot トークンから残りを全部
導出することで圧縮した。Discord REST は bot トークンだけで application 情報・
所属ギルド・チャンネル一覧が取れ（特権 intent 不要）、ユーザーの手作業は
「Developer Portal でトークン取得」と「招待リンクを開いて追加」だけになる。
数字 ID のコピー（開発者モード）と OAuth2 URL Generator は不要。

- **手順**: ①トークン貼付→検証（`POST /connections/discord/inspect` — bot 名＋
  `GET /oauth2/applications/@me` の application id から招待 URL を生成。権限は
  VIEW_CHANNEL+SEND_MESSAGES=3072）→ ②カードが `POST /connections/discord/guilds`
  （`GET /users/@me/guilds`＋`GET /guilds/{id}/channels`、text=type 0 のみ）を
  3 秒ポーリングし、Bot がギルドに入った瞬間チャンネルピッカー表示 → ③接続
  （PUT）と同時に**同期テスト通知**（kind `bridge-test`・キュー非経由）を 1 通
  送り、失敗は `testError` としてカードに表面化（接続自体は保存する — 権限不足等は
  設定ミスではなく後から直せるため）。
- **DM モードは上級者向けに格下げ**: ユーザー ID の自動検出だけは特権 intent
  （GUILD_MEMBERS）が要るため自動化せず、手入力のまま残す。私設サーバーの
  チャンネル宛てが実質 DM と同等の既定経路。
- 中央共有 App によるワンクリック招待は引き続き不採用（ADR0020 決定 3 のまま。
  self-host デプロイで成立しない）。

#### P1.5: スレッド＝セッション ＋ メンション（2026-07-22）

送信専用のまま（Gateway 不要・REST のみ）、通知をセッション毎のスレッドに束ねる。
チャンネル＝セッション案は不採用（MANAGE_CHANNELS という強権限が要る・ギルド 500
チャンネル上限・自動アーカイブ無しで増殖 — スレッドが Discord 側の専用プリミティブ）。

- **起票**: セッション初回の通知はチャンネルへ通常投稿し、そのメッセージから公開
  スレッドを起こす（名前=セッション表示名・auto_archive 24h）。以後の通知は
  対応表（**契約 6 の store を前倒し実装** — `~/.config/agent-fleet/bridge-threads.json`、
  sessionName → {channel, thread}）を引いてスレッド内へ。
- **自壊対応**: アーカイブ済み（code 50083）→ unarchive して再送。手動削除
  （404/10003）→ 対応表を破棄して次イベントで再起票。スレッド起票自体の失敗は
  通知を落とさない（フラット投稿は済んでいる — 束ねは次イベントで再試行）。
  チャンネル変更は対応表の channel 不一致で自然に無効化。切断時は store ごと削除。
- **メンション＝push の生命線**: Discord のチャンネル/スレッド既定通知は
  「@メンションのみ」で、素の投稿はバッジ止まり（スマホに push されない）。
  そこで MentionUserID 設定時は**全通知の先頭に `<@id>` を付ける**（スレッド内の
  メンションは自動でスレッドメンバー化もする）。DM モードは不要（DM は既定で push）。
- **ユーザー ID は開発者モード不要で自動解決**: `GET /guilds/{id}` の **owner_id** ＝
  推奨フロー（自分で私設サーバーを作る）ではユーザー本人。guilds API が ownerId/
  ownerName（`GET /users/{id}` で表示名解決）を返し、カードがメンション先へ自動入力
  （編集可 — オーナーでない共有サーバーに入れた場合だけ書き換える）。特権 intent 不要。
  ※ GUILD_MEMBERS でのメンバー列挙は特権 intent が要るため不採用。
- **権限**: 招待 URL を 3072 → **292057779200**（+CREATE_PUBLIC_THREADS+
  SEND_MESSAGES_IN_THREADS）。既存招待も私設サーバーの @everyone 既定で通常は動く。
- **P2 への布石**: この対応表の逆引き（thread → session）がそのまま P2 の
  「スレッド返信 → セッションへ注入」のルーティングになる。捨てコード無し。
- 検証: ステートフル fake での契約テスト（起票→スレッド投稿→アーカイブ復帰→
  削除再起票→セッション分離→セッション無しイベントはフラット）＋ live テスト拡張
  （`AF_DISCORD_CHANNEL` 指定時にスレッド 2 通・`AF_DISCORD_MENTION` でメンション）。

#### P1.5 追補: 通知文の簡潔化・言語・ペイン deep link（2026-07-22）

- **簡潔化**: `【agent-fleet】` プレフィクスと「セッション:」ラベルを廃止（アプリ名は
  Bot 名で分かる）。形は「見出し ＋ 「表示名」（kind）＋ リンク」の最大 3 行。
  kind は Console の agent registry に揃えた製品名（claude → Claude Code 等 —
  `bridge/format.go kindLabel`、registry 変更時は同期）。
- **言語**: 接続時の Console ロケールを `DiscordCreds.Lang` に保存し、bridge が
  ja/en を出し分け。`"en"` 以外（旧接続の空含む）は日本語。切替はカード再接続で。
- **ペイン deep link スキーマ確定**（残課題だった件）: 通知リンクは
  `<base>/?session=<slug>`。Console が boot 時に `?session=` を消費
  （`lib/sessionDeepLink.ts` — param 即除去→sessions store に現れるまで最長 90 秒
  待って rail クリックと同じ流儀で開く: chat-capable はミラー、他はターミナル）。
  Discord 側は `<…>` 包みで埋め込みプレビューを抑止。テナントは URL に載せない
  （マルチテナントでの越境オープンは対象外 — 必要になったら ?tenant= を検討）。

### P2 — 双方向: スレッド＝セッション ＋ AUQ ボタン（canReceive / canInteract）

- セッション初回通知でスレッドを起こし、以後の報告・質問を同スレッドへ
  （**起票・対応表・アーカイブ復帰は P1.5 で実装済み** — P2 は受信側のみ）。
- スレッド返信 → 対応セッションへ `send_to_session` 相当（`/input` 経路）で注入。
  → **P2a で実装済み**（下記 §P2a 実装記録）。
- AUQ・permission-request をボタン化。押下 → 構造化回答。タイムアウト・
  セッション側で先に回答済みのケースはボタン無効化更新で表面化。→ **P2b で実装済み**
  （下記 §P2b 実装記録・claude/TUI 対象）。
- Slack: Socket Mode（`xapp-` token）。Discord: Gateway intents は
  DM＋ギルドメッセージ＋interactions の最小構成。

#### P2b 実装記録: AskUserQuestion／許可／プラン承認のボタン化（2026-07-22）

チャットが Console 無しで完結する遠隔クライアントになる最後のピース（P2a の返信＋全文
ブリッジと組む）。**セッションの pending な質問・許可・プラン承認を Discord の Message
Components（ボタン）で回答する**。回答はキー送出でなく構造化写像（契約7）で、押下者は
本人限定（契約5）。

- **相互作用は同じ Gateway に届く**: Discord は Interactions Endpoint URL 未設定なら
  ボタン押下を `INTERACTION_CREATE` として Gateway に流す（＝ローカル専用・外部端点なしの
  本命構成そのもの）。P2a の受信 Gateway に相乗りし、公開端点は不要。押下は 3 秒以内に
  callback を返す必要があるため、受信は**即 deferred-ACK（type 6・ローディング非表示）→
  適用→メッセージ編集（ボタン除去＋結果表示）**の順（`receiver.go routeInteraction`）。
- **送信（ボタン描画・`internal/bridge/interact.go`）**: 受信が有効（`Receive`＋channel
  モード）なとき question/plan-approval/permission-request にボタンを添える。permission→
  「許可/拒否」、plan→「承認/却下」、question→**質問ごとに 1 メッセージ**（単一/複数問で
  一様）＝オプション 1 個 = ボタン 1 個（5/行・最大 25）。`custom_id` にセッション・質問/
  選択肢インデックス・**questions のフィンガープリント**（陳腐化検出）を格納。**multi-select
  や予算超過の質問はボタン化せず素のテキストのまま**（Console で回答）。
- **回答適用（`bridge_answer.go`／package main・`ReceiverDeps.Answer` で DI）**: 押下を
  デコードし `meta.DriverKind()` で分岐。**v1 は claude/TUI 対象**（フックが pending
  ペイロードを記録する状態＝ここが唯一ボタンの出る面）。
  - **AUQ**: `status.ReadPendingQuestion` を再読しフィンガープリント照合（不一致＝陳腐化で
    拒否）。単一選択を**質問ごとに 1 押下**で受け、複数問は per-session の蓄積ストア
    （`bridge-answers/`）に貯め、**全問揃ったら** claude モーダルへ `Down×index＋Enter`／
    末尾 `Enter`（`buildClaudeSingleSelectKeys` ＝ console `questionKeys.ts buildClaudeSeq`
    の単一選択パスの Go 再現）を送出。
  - **permission / plan**: MirrorView が実際に送る検証済みキーを厳密に再現（許可=Enter・
    拒否=Down Down Enter／承認=Enter・却下=Down Down Down Enter）。**現在の状態が
    permission/plan でなければ送らない**（陳腐化した押下が composer に迷子キーを打ち込むのを防ぐ）。
  - **managed（codex/opencode/copilot）＝実装済み（2026-07-22）**: 当初は ID の識別子
    不一致（通知経路の rollout call_id ↔ ドライバのライブ `h.inter.ID`）を理由に Console
    誘導だったが、**custom_id にライブ Interaction id を載せず、回答時に再取得する**設計で
    解決した。送信側＝`notifications.go` の codex-question 通知に、ライブハンドルを resume
    せず覗く `codex.PendingInteraction(name)`（`json.Marshal(inter.Questions)`）で questions
    を添付（無ければ従来どおりボタン無しの Console フォールバック）。回答側＝`bridge_answer.go`
    の `answerManagedQuestion`＝`driverOf→Resume→Snapshot` で**現在の**Interaction を読み、
    `Kind=="question"`＋**フィンガープリント照合**（`json.Marshal(snap.Interaction.Questions)`
    は送信側と同一バイト＝同一 fp）で陳腐化を弾き、`bridge-answers/` に per-question 蓄積して
    全問揃ったら `h.Respond(InteractionReply{ID: snap.Interaction.ID, Decision: answer,
    Answers})`。**識別子不一致を構造的に回避**（送信時に id を知る必要がない）＋二重ガード
    （fp 不一致／`Respond` 自身の `inter.ID != reply.ID`）で late-click 誤答を防ぐ。permission/
    plan は managed には描画されない（notice が出ない）ので `p`/`pl` 押下が来ても Console 誘導。
    3 ドライバとも `Respond`＋`Snapshot().Interaction` は同型なので opencode/copilot も同経路で
    効く（question 通知に payload が載れば自動でボタン化・載らなければ Console フォールバック）。
    検証: ユニット（`buildInteractionAnswers`／fake ThreadHandle で蓄積→全問で `Respond`・
    id/Answers 検証／fp 陳腐化・interaction 消失は非 Respond／`PendingInteraction` の
    fingerprint 契約＝peek バイト == `json.Marshal(inter.Questions)`）。go 524 緑。**ライブ
    codex（実クリック→`Respond`）検証は再ビルド後・実 codex セッションが要る＝未実施**。
- **UX**: 受信トグルの説明に「質問・許可・プラン承認はボタンで回答」を追記（i18n ja/en）。
- 検証: ユニット（custom_id 往復・フィンガープリント安定/差異・単一/複数問のボタン描画・
  multi-select/予算超過のテキスト退避・permission/plan 行・Send のスレッド内ボタン投函・
  Receive 無効時はボタンなし・Gateway INTERACTION_CREATE 配送＝component 型のみ・キー列の
  Console 一致）＋ live 拡張（`AF_DISCORD_BUTTONS=1` で質問＋許可を投函、受信テストが
  `INTERACTION_CREATE` を記録）。go 緑。実機（実クリック→回答適用）は再ビルド後に残。

#### P2a 実装記録: スレッド返信 → セッション注入（2026-07-22）

送信専用だった受信側の最初の縦貫。**本人がセッションのスレッドに返信すると、その本文が
セッションへ user 入力として注入され、応答は既存の answer-ready 通知で同じスレッドへ戻る**。
Console を開けない外出先からセッションを操縦できる（AUQ／許可のボタン化は P2b に分離）。

- **Discord Gateway（`internal/bridge/gateway.go`）**: gorilla/websocket の長命クライアント。
  `GET /gateway/bot` で WSS URL 取得 → HELLO の heartbeat_interval で心拍（op1／ACK op11・
  zombie 検出で再接続）→ IDENTIFY（intents = **GUILD_MESSAGES | MESSAGE_CONTENT = 33280**）→
  READY で resume_gateway_url/session_id 保持 → MESSAGE_CREATE を配送。RECONNECT(op7)／
  INVALID_SESSION(op9) は RESUME(op6) で復帰、非 resumable は IDENTIFY からやり直し。
  close **4014（Disallowed intents）＝MESSAGE_CONTENT 未有効**は致命として再試行を止める。
  送信は従来どおり REST のみ（このファイルは READ 専用）。
- **受信スーパーバイザ（`internal/bridge/receiver.go`）**: `StartReceiver(deps)` が secrets を
  interval poll（変更通知が無いため sender と同流儀）し、`Discord.Receive` on かつ token／
  bound user あり→Gateway 起動、off／identity 変化→停止・再ダイヤル。バックオフ再接続
  （健全接続後はリセット）。**受信は opt-in**＝WSS 常駐を有効ユーザーに限定（メモリ配慮）。
- **本人限定ルーティング（ADR0020 契約5・唯一の防壁）**: MESSAGE_CREATE を
  ①bot は無視（自分の通知エコー含む）②`author.id == bound user`（channel モードは
  auto-fill 済み `mentionUserId`＝オーナー＝本人、DM は `userId`）以外は無視 ③channel_id を
  **thread→session 逆引き**（`ThreadToSession`・`bridge-threads.json` の P1.5 対応表をそのまま
  逆引き＝捨てコード無し）で解決、未知スレッドは無視。**チャンネル聴取の面は作らない**。
  先頭のメンションを剥がして注入。受信文はそのまま user 入力として注入（システム指示と混ぜない）。
- **注入コア（`bridge_inbound.go`／package main）**: import cycle 回避のため注入能力は
  main→bridge へコールバック注入（既存 `cacheDiscordDM` と同 DI）。`injectSessionPrompt` は
  `handleSessionInput` の `{prompt}` 分岐を非 HTTP 化（tui=`typeLineAndSubmit`／managed=
  driver `Send`・同じ guard = 質問未応答は拒否＝AUQ 誤答防止）。**応答が同スレッドへ戻るのは
  answer-ready 通知が既存 P1 経路で thread に push されるため（追加配線なし）**。
- **発信元バッジ（追加要件）**: 注入された user turn をミラーで**オペレーター注入と同様に
  バッジ表示**して自分の入力と見分ける。既存の `recordOperatorInjection`＋`tagOperatorTurns`
  ＋Console `from-operator` を**発信元付きに汎用化**（`recordInjection(name,text,source)`＋
  `tagInjectedTurns`＋Console `from-chat` バッジ・`source="discord"`／将来 `"slack"`）。
- **Connections/UX**: `DiscordCreds.Receive`（channel モード限定）＋ DiscordCard に
  「返信で操縦（双方向）」トグル＋**MESSAGE_CONTENT 有効化の案内**＋接続時「受信」ピル。
- **要件（受け入れる制約）**: 受信には Discord の **MESSAGE_CONTENT 特権 intent** が要る
  （Bot<100 サーバは開発者ポータルでチェック1つ・審査不要）。DM モード受信は thread→session
  対応表が無いため P2a 対象外（推奨のチャンネル＋スレッド構成が前提）。
- 検証: ユニット（fake Gateway で HELLO→IDENTIFY(intents)→READY→MESSAGE_CREATE 配送・
  close4014→致命・本人検証ゲート＝他人／bot／未知スレッド／空文を全 drop・逆引き）。
  実機通し（再ビルド後）は残。

### P3 — 承認ゲート ＋ オペレーター bot

- オペレーターの shell 実行確認（方針C）・破壊的操作を「承認/却下」ボタン付き
  メッセージに写像。押下者検証は契約 5 のとおり。
  → **実装済み**（2026-07-23。下記 §P3 承認ゲート 実装記録）。
- オペレーターセッション（`operatorPersona`）へのチャット窓口:
  「フリート何やってる？」→ list/usage 系 MCP ツールの回答を整形返信。
  → **@メンション→オペレーター会話は P3先取りとして実装済み**（下記）。`/af run` の独自
  スラッシュは不採用（専用スレッド返信で「本物のオペレーター会話」がそのまま回るため）。

#### P3先取り 実装記録: @メンション→フリート・オペレーター会話（2026-07-22）

Console を開けない外出先から、走らせている個々のセッションだけでなく **フリートの司令塔
（フリート・オペレーター）そのものとチャットで会話**できるようにする。「何やってる？」→
稼働状況、「◯◯を直して」→ セッション起動/指示、までスマホで完結する。

- **オペレーターは built-in アシスタント会話**（`assistants.go` ID `"operator"`・
  `af_write` MCP）。セッションではないが、外部イベント駆動でターンを回す機構は既存
  （`chat_report.go deliverSessionReport→runReportAutoTurn→prov.send` が同期で応答テキストを
  返す）。**ターン機構は再発明せず** `runOperatorTurn`（`bridge_operator.go`）＝
  `handleChatSend` の非HTTP版（同じ lock／`maybeAutoCompact`／`injectPendingReports`／
  overflow 自己修復／`AutoTurns` リセット）で回し、応答テキストを返す。肥大化は docs/33 の
  予防的自動圧縮で頭打ち＝Console のオペレーターチャットと同じ振る舞い（**これは Console
  からも deep link で開ける同一会話**）。
- **受信面＝専用オペレータースレッド**（「面を作らない」を維持）: 接続時（`handlePutDiscordConn`・
  受信＋channel モード）に常設スレッド「🛰 フリート・オペレーター」を 起票 し、
  `bridge-operator.json`（`internal/bridge/operator.go`）に `{channel, thread, conv}` を永続化。
  `conv` は正準オペレーター会話の UUID（1本の連続会話・create-or-reuse で再接続をまたいで維持）。
  bridge にとって `conv` は不透明で、`bridge-threads.json`（セッション用）とは別ファイル＝
  `ThreadToSession` は決してマッチせず `routeInbound` が **`OperatorThreadMatch` で別分岐**する。
- **ルーティング**（`receiver.go routeInbound`）: 本人限定ゲート（契約5）通過後、
  `ThreadToSession` が当たらないとき operator スレッドか判定→一致なら `routeOperatorInbound`。
  即 👀 リアクション＋typing パルス（オペレーターターンは LLM+MCP で遅いので **Gateway reader
  goroutine を塞がず** `go` で回し、`typingPulseInterval`〔8秒〕でパルスを継続）→ 応答を
  **同スレッドへ投函**（セッションと違い answer-ready 通知に乗らないので受信側が明示 post。
  `postOperatorChunks`＝`ScrubSecrets`＋`chunkMessage` で秘密伏字化＋2000字分割・アーカイブは
  unarchive 再送）。`ReceiverDeps.Operator` コールバック（`bridge_inbound.go` で `runOperatorTurn`
  を配線・Inject/Answer と同じ import-cycle 回避）。
- **自律応答も可視化**: オペレーターが指示したセッションからの報告（`report_to: mcpConvID` を
  `create_session`/`send_to_session` が自動付与）はこの会話へ戻り `runReportAutoTurn` が後続処理する。
  その自律応答も operator スレッドへミラーする（`chat_report.go maybePushOperatorReply`＝conv 一致
  かつ thread ありのとき best-effort）。生の報告自体は従来どおりセッション自身のスレッドに出る。
  **＝Discord からオペレーターを駆動→セッション起動→報告→オペレーターが要約、の外出先ループが閉じる**。
- **切断時**（`handleDeleteDiscordConn`）: `ResetOperatorThread()`＝thread/channel 座標は破棄するが
  **`conv` は残す**（連続会話・Console から履歴閲覧可）。次回接続で thread 再 起票・conv 再利用。
- **UX**: 受信トグル説明に専用スレッドの案内を追記＋接続時「オペレーター」ピル（`discordStatus`
  の `operator`・i18n ja/en）。招待権限は据え置き（既存のスレッド作成/送信/リアクション権限で足りる）。
- 検証: ユニット（operator store の save/match/reset＝conv 保持／`routeInbound` の operator 分岐＝
  本人限定ゲート流用・他人/bot drop・mention 剥がし・👀＋return-leg／`postOperatorChunks` の
  scrub＋分割／`runOperatorTurn`＝fake provider で user→assistant 追記・AutoTurns リセット・欠落 conv の
  局所化理由／`createOperatorConversation`＝operator スナップショットで af_write 付与）＋
  live 拡張（`AF_DISCORD_OPERATOR=1` でスレッド 起票＋return-leg 目視）。go 533／Console
  typecheck・i18n:lint・vitest 355 緑。**残＝実 Discord での実機目視（@メンション→会話→応答が
  同スレッドへ・自律応答ミラー）は再ビルド後**。破壊的操作の確認（承認ゲート）は下記 §P3 承認ゲートで実装。

#### P3 承認ゲート 実装記録: 破壊的操作をボタンで承認（2026-07-23）

Discord から駆動されるオペレーター会話（Console を開けない外出先・無人）が **破壊的操作** を
実行する直前に、専用オペレータースレッドへ「承認/却下」ボタンを出し、本人が押すまで実行を止める。
押下者検証は契約5（本人限定）を流用。P2b のボタン往復（`interact.go`／`routeInteraction`／
`ParsedInteraction`）にそのまま載せる。

- **ゲート対象＝削除系＋shell**（ユーザー確認 2026-07-23）: `delete_session`／`delete_worktree`／
  `delete_branch`／`purge_cleanup_archive`（データ損失・回収系）＋ `create_session(kind=shell)` と
  **shell セッションへの** `send_to_session`（任意コマンド実行＝生シェルはガードレール無し・方針Cの穴）。
  `stop_session`／`archive_session` は可逆なので対象外。
- **プロセス構成の制約**: 書込 MCP ツールは `workspace-agent mcp-stdio --write --conv <id>` の
  **別サブプロセス**で走り、各破壊的ツールはデーモン REST（`/halt`・`DELETE …`）へ中継する。
  ボタン押下は **デーモン側 Gateway** に届く（別プロセス）ため、両者はファイルで協調する（bridge-queue／
  bridge-answers 等と同じ fstore 方式）。
- **Discord 由来か Console 由来かの区別**（信号が無い＝同一 conv・同一 spawn 引数）: `runOperatorTurn`
  （Discord 駆動のみ）が `prov.send` の前後で **オリジン・マーカー** `bridge-operator-turn.json`
  `{conv, expiresAt}` を arm/disarm する（`bridge_approval.go`）。Console の `handleChatSend` は
  書かない＝**Discord 駆動時だけゲート作動**（Console は人が見ているので従来どおり）。conv ロックで
  直列化済み＝マーカーは一意。TTL 自己失効でプロセス死時も残らない。
- **ゲート本体**（`mcp_stdio.go` の各破壊的ハンドラが実行直前に `bridgeApprovalGate(op, summary)` を呼ぶ）:
  マーカーが自分の conv で無効なら即 `nil`（＝従来どおり実行）。有効なら承認レコード
  `bridge-approvals/<id>.json` を書き、`bridge.PostOperatorApproval` で専用スレッドへボタン投稿
  （`ScrubSecrets` 済み＝summary にコマンド/プロンプトが載るため）→ decision をポーリング。
  承認→実行／却下→`errApprovalRejected`／時間切れ→`errApprovalTimeout`。error 時は `mcpToolErr` で
  LLM に文面を返し、オペレーターが結果を報告。**フェイルセーフ**＝マーカー有効なのに投稿不可
  （thread/secrets 欠落）→ **実行せず中止**（fail-closed）。
- **承認ウィンドウ**: `runOperatorTurn` の turn timeout を Console 用 `chatTimeout`（240秒）より
  長い `operatorTurnTimeout`（6分）に引き上げ、外出先でも押せる猶予（`bridgeApprovalTimeout` 既定4分）を
  確保。待機中は既存の typing パルスがスレッドを「作業中」に保つ。ブロッキング方式なのでツール結果は
  会話文脈にそのまま戻り、承認後の連鎖もできる（実行はサブプロセスのまま＝REST 中継ロジックの重複無し）。
- **押下適用**（デーモン側）: `ParseCustomID` に `af|op|<approve|reject>|<id>`（Kind `"op"`・`Approval` id）を追加。
  `answerInteraction` が `op` を `bridgeApprovalDecision` へ分岐＝レコードに decision を書き、フィードバック
  （「✓ 承認しました」等）を返す→`routeInteraction` が既存どおりメッセージをボタン除去＋結果に編集。
  二重押下/期限切れは decision 済み判定で弾く。孤児レコードは書込時 TTL 掃引（`sweepStaleApprovals`）。
- **面は作らない／新 REST 追加なし**: 投稿先は既存の専用オペレータースレッド、押下適用は既存
  `ReceiverDeps.Answer`（`answerInteraction`）に相乗り。
- 検証: go **557 緑**（`bridge_approval_test.go`＝マーカー arm/disarm/失効・非武装 no-op・fail-closed・
  wait の承認/却下/時間切れ・decision の初回/二重/欠落・往復・`sessionIsShell`・stale 掃引／
  `internal/bridge/approval_test.go`＝custom_id 往復・不正 op 拒否・no-target・投稿＋scrub）＋
  live 拡張（`AF_DISCORD_APPROVAL=1` でスレッドへボタン投稿→クリックは `TestDiscordLiveReceive` の
  `INTERACTION_CREATE` ログで parsed `op` を確認）。**残＝実 Discord 実機目視（削除/ shell を Discord から
  頼む→ボタン→承認で実行・却下で不実行）は再ビルド後**。

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

- ~~Console セッション URL の組み立て~~ → 解決済み: ベース URL は `AF_CP_BASE_URL`、
  セッション単位の deep link は `?session=<slug>`（P1.5 追補参照）。
- ~~Discord の私設ギルド前提を README/ガイド（member/）にどう書くか~~ →
  カード内ウィザード（P1 追補）にほぼ吸収。member ガイドの独立ページは P2 の
  双方向設定が増えた時点で検討。
- オペレーター bot（P3）の応答をどのセッション実体に持たせるか
  （常駐オペレーター 1 本を bot 専属にする案が有力 — P3 着手時に確定）。

#### 全文ブリッジ 実装記録: 応答本文をチャットへ（2026-07-22）

**現状の deep link は「Console が外から開ける」前提**（検証環境は Tailscale Funnel 公開＋
Google OAuth なので URL が活きる）。だが**シングルユーザーの実際の主戦場はローカル PC の
native/docker・外部到達なし**で、そこでは Console URL がスマホから開けず **deep link が死ぬ**。
その環境では「入力待ちです」という**通知＋リンクだけでは価値が薄い**。そこで、応答本文そのものが
チャットで読めて返信できる＝**チャットが単独で成立する遠隔インターフェース**へ格上げする（P2a の
返信→注入と組んで、Console 無しでもセッションが回る）。論点1（どこまで載せる・整合・分割・発火）を
ユーザー確認の上で下記に決着（明示トグルのみ／answer-ready のみ／既知トークン形＋高エントロピー）。

- **本文の出所（新規捕捉なし）**: answer-ready の最終 turn 散文は既に捕捉済み。`MessageDisplay`
  フックが本文チャンクを蓄積し（`status.AppendPendingText`）、`recordSessionNotification` の
  answer-ready 分岐で `turnText` として持っている（session-report の「直近の出力（抜粋）」で
  既に再利用）。これを `notice.Event.Payload["body"]`（`tailRunes` で `reportExcerptCap`＝2000
  runes に丸め）へ載せ、`notice.Put` → `bridge.Enqueue` が `Message.Body` へ転写する。
  **answer-ready のみ**（interim の question/plan/permission は完了 turn の本文を持たない）。
  tool ログ・思考は載せない（そもそも `turnText` に含まれない）。ストリーミングはせず turn 確定時に投稿。
- **既定オフ＋明示トグルのみ（自動判定なし）**: `secrets.DiscordCreds.FullText`（既定 false）。
  `AF_CP_BASE_URL` 追従の自動全文化は**不採用**（tailscale URL 等は設定済みでも到達可＝到達性の
  能動判定は誤診する）。チャット＝写しの既定姿勢を保ち、両端を所有する本人だけが opt-in する。
  link と本文は排他でなく併用（base URL があれば compact な `<...>` link も併記）。全文モードは
  channel/DM いずれの宛先でも効く（本文はスレッド/チャンネル/DM のいずれにも載る）。
- **秘密スクラブ**（`internal/bridge/fulltext.go ScrubSecrets`・多層防御）: ①既知トークン形
  （`xox[baprs]-`／`gh[pousr]_`／`AKIA|ASIA`／`sk-`／`Bearer …`／JWT／PEM 秘密鍵ブロック）を
  無条件伏字化 ②**大文字**の秘密語 env 代入（`AWS_SECRET_ACCESS_KEY=…`／`PASSWORD=hunter2`）を
  値ごと伏字化（大文字限定で「the api key: …」等の散文を除外）③高エントロピー独立トークン
  （20字以上・英数混在・Shannon エントロピー ≥3.5）を伏字化。best-effort＝稀な散文誤伏字より漏洩を
  重く見る方針（ユーザー確認済み）。一次防壁はあくまで「両端所有＋opt-in」。
- **2000 字分割**（`chunkMessage`）: Discord の 2000 字上限に対し conservative に 1990 runes で
  分割（メンション接頭辞ぶんは先頭チャンクの予算から差し引き＝ping は先頭 1 回だけ）。改行→空白の
  境界優先、無ければハードカット。チャンク数は `maxBodyChunks`=5 で頭打ち（暴走 turn のフラッド防止）、
  超過は末尾に「…」。スレッドモードでは先頭チャンクがスレッド起票の種になり残りはスレッド内へ。
- **UX**: DiscordCard に「全文モード（応答本文をチャットに載せる）」トグル＋注意書き（ローカル向け・
  既定オフ・自動伏字化・2000字分割・両端所有前提）＋接続時「全文」ピル。i18n ja/en。
- 検証: ユニット（scrub の各形＋散文非誤爆・チャンク境界/上限/メンション予算・全文 on/off レンダリング・
  body の queue 転写）＋ live 契約テスト拡張（`AF_DISCORD_FULLTEXT=1` で分割＋スクラブを目視）。
  go 503／Console typecheck・i18n:lint・vitest 355 緑。実機目視（実 Discord へ全文投稿）は再ビルド後に残。

#### 通知／全文の整理・受信 ack（2026-07-22）

managed ボタン化に着手する前に、実運用のノイズと外出先の体感を上げる 3 点をユーザー確認の上で整理した。

- **全文モードは本文のみ**（`discord.go Send`）: 全文モードで `m.Body` があるとき（＝
  answer-ready のみ本文を持つ）、見出し・「表示名」・`<deep link>` の前置きを落とし
  **スクラブ済み本文だけ**を投稿する。スレッド名に表示名は出ており、リンクは全文モードが
  狙うローカル専用環境では大抵死んでいる。P1 の「link と本文は併用」はこの決定で
  「全文時は本文のみ」に改訂。非全文・本文なしのイベントは従来どおり見出しを保つ。
- **メンションの時間ゲート**（`discord.go shouldMention`＋`threads.go` の `threadRef.LastPostAt`）:
  Discord のスレッド既定 push は「@メンションのみ」なので push の生命線は残しつつ、**連続した
  返信→回答のやり取り中に毎ターン ping しない**ようにした。判定は
  ①**question / plan-approval / permission-request / exit は常にメンション**（要対応・異常＝
  取りこぼし厳禁）②**answer-ready 等の読むだけイベントは、そのセッションのスレッドへ bot が
  最後に投函してから `mentionQuietWindow`（既定 10 分）静かなときだけ**メンション。スレッド未
  起票（初回）・フラット/DM は従来どおり常時メンション。`LastPostAt` はスレッド投函成功のたびに
  `touchThreadPost` で更新（RFC3339・秒粒度で 10 分窓には十分）。「時間が経った」の基準は bot の
  最終投函時刻＝連続回答は抑制、席を外して間が空いたら次の回答で再び push。
- **受信 ack（返信を受け付けた合図）**（`receiver.go routeInbound`＋`discord.go`＋
  `bridge_inbound.go`）: P2a の返信→注入で、成功/失敗とも無反応だったのを解消。**成功時**＝
  ユーザーのメッセージに 👀 リアクション（永続の受領印・`DiscordAddReaction`）＋対象スレッドに
  typing パルス（実行中らしさ・`DiscordTriggerTyping`、~10 秒で回答が置き換える）。**失敗時**＝
  スレッドに**局所化した理由**を短く返信（`injectFailureReason`：質問ペンディング→「ボタンで
  回答して」、停止中→「開始してから返信して」、未知エラーは汎用文で包み dev 文言を漏らさない・
  ja/en）。`ReceiverDeps.Inject` は `(reason string, err error)` を返す形に変更（`Answer` の
  `(feedback,err)` と同型）。ADD_REACTIONS を招待権限に追加（292057779200→**292057779264**）＝
  新規セットアップで既定付与、既存招待は私設ギルド @everyone 既定で通常動く・拒否は best-effort で
  静かにログのみ。
- 検証: ユニット（mention 時間ゲートの kind 別・窓境界／全文本文のみ描画／成功 ack＝
  reaction+typing・失敗 ack＝理由投函と ack 抑止／gate 落ちは Discord に触れない）。go 520 緑。
  実 Discord 目視（全文本文のみ・時間ゲート・👀/typing・失敗理由）は再ビルド後に残。

#### 重複・可読性の修正 実装記録（2026-07-23）

実 Discord 運用のフィードバックで表面化した 5 件を、ユーザー確認の上まとめて修正した（Console から
駆動する既存経路の不具合＝Slack 追随の前段）。

- **① 同一メッセージが大量に届く（重複）**＝**配送の非冪等＋429 握り潰し**が真因。1 通知は複数 POST
  （メンションチャンク＋本文チャンク＋P2b ボタン＋スレッド起票）に展開されるが、`discordDo` が
  Discord の **429（レート制限）を一般エラー扱い**（`Retry-After` 無視）→ 途中失敗で `Send` が
  エラーを返し **キューエントリ丸ごと再送**（既配送チャンクを再 POST・最大 `maxAttempts`＝5 回）。
  長文・全文モードで POST が連続すると 429 が出やすく多重着弾。対策 3 段:
  - **1a 429 インライン処理**（`discord.go discordDo`）: 429 を `retry_after`（JSON／`Retry-After`
    ヘッダ）に従い**同一 POST を上限付きで再試行**（`discordRateRetries`＝3・`discordRetryCap`＝5s で
    クランプ）。主因（バーストの途中失敗→丸ごと再送）を除去。
  - **1b 配送の冪等化**（`ResumableSender` 抽象・`bridge.go`／`queue.go`／`sender.go`／`discord.go`）:
    キューエントリに**プロバイダ別の配送カーソル** `Delivered map[string]int` を持たせ、`Provider` が
    任意で実装する `SendFrom(m, from) (delivered, err)` で**未達サブメッセージだけ**送る。中断再送でも
    重複しない。スレッド起票済み状態も保持（`sendThreaded`/`postRangeToThread` が `from` を尊重）。
    **前進した tick は attempts をリセット**（長文が cap に達して落ちない）。非対応プロバイダは従来の
    丸ごと `Send` にフォールバック。
  - **1c session-report の二重掲出解消**（`discord.go SendFrom`）: オペレーター起動セッションは 1 回の
    完了で `answer-ready` と `session-report` が**同じスレッドへ 2 通**入る。スレッドモードでは
    `session-report` を**そのセッションのスレッドへは投稿しない**（完了は answer-ready が届けており、
    オペレーター可視化はオペレータースレッドの写しが担う）。フラット/DM では従来どおり掲出。
- **② Console 入力が Discord に出ない**＝Console のコンポーザーで送った user 入力を**同スレッドへ写す
  経路が無かった**。`bridge.MirrorUserInput(session, text)` を新設し、`handleSessionInput` の {prompt}
  成功時（TUI／managed 両方・**report_to == "" の生人間入力に限定**＝オペレーター/MCP 注入は除外）に
  `go` で best-effort 投函（🧑 マーカー付き・スレッド既存時のみ＝echo はスレッドを新規作成しない）。
  既定 ON の opt-in トグル（`DiscordCreds.MirrorInputOff`＝**逆持ち**で既存接続も既定 ON・OpsTab トグル
  ＋「入力写し」ピル・i18n ja/en）。
- **③ 長文回答の一部が欠ける**＝本文が `tailRunes(turnText, reportExcerptCap=2000)` で**末尾 2000
  rune だけ**（頭が落ちる）。ブリッジ本文専用に **`bridgeBodyCap`＝12000 rune の head-first
  （`headRunes`）**へ decouple（`reportExcerptCap` はオペレーター抜粋用に据え置き）＋`maxBodyChunks`
  を 5→**12** に引き上げ、**全文を頭から複数メッセージへ「うまく分割」**（無言の切り捨てを解消）。
- **④ Markdown テーブルが表示されない**＝Discord は**テーブル記法未対応**。`fulltext.go
  tablesToCodeBlocks` で GFM テーブルを**fenced コードブロック**へ包み等幅で桁を保つ
  （`renderBodyForDiscord`＝scrub→table 変換。ヘッダ行＋区切り行の検出・bare hr は非テーブル扱い）。
- **⑤ 連続投稿・入力の文脈が繋がって読みづらい**＝全文回答と**ミラーした入力の末尾に区切り線**
  （`bridgeDivider`＝U+2500 の連続。Discord は Markdown「---」を hr にしないため）を付け、
  連続する投稿が 1 塊に見えないようにした。
- 検証: go **562 緑**（新規＝`TestDiscord429RetriesInline`／`TestDrainResumesWithoutDuplicate`＝カーソル
  冪等・重複無し／`TestSessionReportThreadSuppressed`／`TestMirrorUserInput`＝写し・opt-out・スレッド無し／
  `TestTablesToCodeBlocks`／全文本文のみ＋divider）＋Console typecheck・i18n:lint・vitest 365 緑。
  **新 REST は無し**（ミラーは既存 PUT /connections/discord に相乗り＝CP allowlist 影響なし）。
  **残＝実 Discord 実機目視（重複解消・入力ミラー・長文分割・テーブル・区切り線）は再ビルド後**。

#### Slack 追随 実装記録（Socket Mode で全機能パリティ・2026-07-23）

Discord で縦貫した機能一式を、同じ `bridge.Provider` 抽象に **Slack Socket Mode** で載せた
（ADR0020 の「抽象は最初から 2 プロバイダ前提」を回収）。**中核は無改修で再利用**＝`Provider`／
`ResumableSender`／ファイルキュー（`Delivered` は既に `Name()` キー）／`ScrubSecrets`／`custom_id`
スキーム＋`ParseCustomID`／`ReceiverDeps`（Inject/Answer/Operator は中立型）／`Message.headline`
＋`kindLabel`。前セッションで先行整地済みの `turnSourceSlack`・Console ミラーの Slack バッジも回収。

- **共通整地（provider 非依存化）**: `chunkMessage`→`chunkTo(content,prefix,limit)` に一般化
  （Discord=1990／Slack=3900）。thread/operator ストアを **provider スコープ化**（`threadStore`／
  `operatorStore` 型・`bridge-threads-slack.json`／`bridge-operator-slack.json`・Discord は無印で
  後方互換）＝Discord/Slack **同時接続で衝突しない**。operator の返信/承認投稿は **conv→provider**
  を走査（各 provider が独立 conv を持つので一意）＝`PostOperatorReply(conv,text)`／
  `PostOperatorApproval(conv,content,id)`。`bridgeAnswerEN` は両 provider の Lang を見る。
- **Slack と Discord の設計差（移植を簡素化）**: ①受信は **Socket Mode**
  （`apps.connections.open`→WSS→`hello`/`events_api`/`interactive` エンベロープを **3 秒以内に
  envelope_id ACK**・`disconnect` で再接続）＝opcode/heartbeat/IDENTIFY 不要で `gateway.go` より薄い。
  ②スレッドは **`thread_ts`（ルート ts）だけ**＝別オブジェクト無し・archive 無しで自己修復は再作成 1 本。
  ③Web API は常に HTTP 200＋`{ok,error}` 包み＝成功は `ok` フラグ（429 だけ HTTP・Retry-After で
  インライン再試行）。④typing 表示が無い＝受信 ack は 👀 リアクションのみ（オペレーターターンも 👀＋最終返信）。
  ⑤記法は **mrkdwn**（`renderBodyForSlack`＝scrub→`tablesToCodeBlocks`→`mrkdwnFromGFM`〔`## 見出し`→
  `*bold*`／`**x**`→`*x*`〕・best-effort）。⑥deep link は `<url|label>`。⑦トークンは **2 本**
  （bot `xoxb-`＝Web API＋app-level `xapp-`＝Socket）。⑧宛先の bound user は 1 フィールド（`UserID`＝
  DM 先＋メンション＋本人検証、Discord の owner とは違い guild owner 概念が無いので一本化）。
- **実装ファイル**: `internal/bridge/slack.go`（Web API＋送信 Provider＝`SendFrom`/threading/mention
  時間ゲート再利用/`destChannel`/`mirrorSlackInput`）／`slack_interact.go`（Block Kit ボタン・同 custom_id）
  ／`slack_socket.go`（Socket Mode クライアント＋受信スーパーバイザ＋本人限定ルーティング〔契約5〕）。
  `StartReceiver` が Discord Gateway と Slack Socket の 2 スーパーバイザを起動。
- **接続 UX**: `secrets.SlackCreds`＋`Data.Slack`。三点セット＝`connections_slack.go`
  （`slackStatus`／`handlePut/DeleteSlackConn`／`handleSlackInspect`〔auth.test＋apps.connections.open〕／
  `handleSlackChannels`〔users.conversations＋**email→users.lookupByEmail で bound user 自動解決**〕）
  ＋`routes.go`＋**CP allowlist `/api/connections/slack*`**（[[cp-rest-proxy-allowlist]] 遵守）。Console
  `SlackCard`（2 トークン貼付→チャンネルピッカー→接続＋テスト通知・i18n ja/en・`BADGE_SHORT.slack="sl"`）。
- **セットアップの差**: Slack は App マニフェスト（Socket Mode＋scope＋message 購読＋connections:write）を
  作り `/invite` で招待する分だけ Discord より手数が多い（カードの hint に scope 列挙）。bound user は
  email 自動解決＋手入力フォールバック（Copy-Member-ID 不要）。
- 検証: go **58 テスト緑**（= bridge パッケージ内の累計・当時点。うち Slack 11＝送信/スレッド起票＋resume 冪等/session-report 抑制/ボタン描画/
  入力ミラー opt-out/mention 時間ゲート/mrkdwn＋table/本人限定ルーティング〔bot/subtype/他人/非スレッド/
  他人押下を全 drop〕/エンベロープ parse）＋live 拡張 `slack_live_test.go`（`AF_SLACK_LIVE=1`＋
  `AF_SLACK_BOT_TOKEN`/`AF_SLACK_APP_TOKEN`/`AF_SLACK_CHANNEL`/`AF_SLACK_USER`＋
  `AF_SLACK_BUTTONS`/`AF_SLACK_FULLTEXT`/`AF_SLACK_SOCKET`）。Console typecheck/i18n:lint/vitest365 緑。
  **残＝実 Slack 実機目視（App 作成・2 トークン・招待・スマホ通し）はユーザー環境＋再ビルド後**。

## 将来の方向（次セッション検討）

- **論点1（全文ブリッジ）＋ P2b（ボタン化・claude/TUI ＋ managed）とも実装済み** →
  **Console 無しでフリートが回る**（P2a 返信＋全文表示＋P2b ボタン）。managed のボタン化も
  完了（§P2b 実装記録 managed 節）＝残るは**ライブ codex 実クリック検証**（再ビルド後）。
- **P3 先取り＝@メンション→フリート・オペレーター会話＝実装済み**（上記 §P3先取り 実装記録）。
- **P3 承認ゲート＝破壊的操作（削除系＋shell）を Discord のボタンで承認＝実装済み**（上記 §P3 承認ゲート
  実装記録）。Discord 駆動（無人）時のみ作動＝Console は従来どおり。残るは実機目視（再ビルド後）。
- session-report 本文（オペレーター向け報告文）の全文投稿は別トグル候補として保留（用途・言語が
  answer-ready 本文と異なるため今回スコープ外）。
- **Slack 追随（Socket Mode）＝実装済み**（上記 §Slack 追随 実装記録）。`ScrubSecrets`／`chunkTo`／
  `custom_id`／`ReceiverDeps` を再利用し、分割上限は Slack の ~4000 字（`slackContentLimit`=3900）に調整。
  Discord/Slack 同時接続対応（store は provider スコープ化）。残るは実 Slack 実機目視（再ビルド後）。
