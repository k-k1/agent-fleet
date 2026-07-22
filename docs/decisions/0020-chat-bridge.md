# 0020. チャットブリッジは「外向き常時接続」型（Slack Socket Mode / Discord Gateway）を採用し、Teams は送信専用枠に格下げする

- 状態: **採用・実装中**（2026-07-22）。P1／P1.5（Discord 片方向通知）＋P2a（受信＝スレッド
  返信→セッション注入）実装済み、P2b（AUQ／許可ボタン化）以降は未着手。実装計画は
  [docs/37](../37-chat-bridge.md)。
- 関連: [docs/30](../30-session-report.md)（完了報告 — 通知内容の供給元）、
  docs/25（PagerDuty/Grafana — Connections 追加の先例）、docs/07 §7.6（秘密は CP を素通り）。

## 背景

セッションの「入力待ち・完了・異常終了」を Console の外（スマホ）へ届け、
その場で返信・選択肢回答・承認までできる口が欲しい。現状の通知はブラウザ
`Notification`（フォアグラウンドのみ）で web-push は無く、外出中はフリートが止まる。

候補 3 者の受信経路は同型ではない: Slack（Socket Mode）と Discord（Gateway）は
**外向き WSS 1 本で受信・ボタン応答まで完結**し公開端点不要。Teams（Bot Framework）は
公開 HTTPS 端点＋Entra ID アプリ登録が必須で、native/WSL2/NAT 裏という本プロダクトの
デプロイ形態と根本的に非互換。

## 決定

1. **プロバイダ抽象は capability フラグ（canSend/canReceive/canInteract）付き**とし、
   v1 は Slack・Discord の常時接続実装（全 ○）のみ。Teams は将来の送信専用実装
   （canSend のみ、Workflows webhook）として同じ interface に載せる。双方向のための
   公開端点ホスティングは**やらない**（デプロイ形態を壊さないことを優先）。
2. **ブリッジ本体（WSS 接続・送受信）は workspace Agent 側**に置く。トークンは
   per-user `secrets.enc` にあり、CP に秘密を持たせない原則を維持する。
3. **トークンはユーザー自前登録**（自分の Slack App / Discord Bot）。中央共有 App は
   v1 では作らない — テナント管理・審査・スコープ管理の重さを回避し、
   PagerDuty/Grafana と同じ Connections カード追加パターンで済ませる。
4. **配送は通知 outbox の脇の fan-out**（`notice.Put` 直後）で、outbox 本体を
   ブロックしない。fire-and-forget＋有限リトライ。チャット側は通知センターの
   **写し**であり、seen 同期はしない。
5. **双方向は本人限定**: プロバイダ側 user ID ↔ AF ユーザーの明示紐付けを必須とし、
   紐付いた本人の DM／発言／ボタン押下のみをルーティング。チャンネル聴取の面を
   作らない（プロンプトインジェクションと越権承認の防壁はこの 1 点に集約）。
6. **質問・承認は構造化写像**: AUQ / permission-request の選択肢をボタンに写し、
   応答は既存の構造化経路（/respond・send_to_session）へ。tmux ペインへのキー送出は
   経由しない（AUQ キー駆動の壊れやすさの教訓）。

## 結果（見込みと受け入れる制約）

- Slack/Discord でスマホからの返信・AUQ 回答・承認まで成立し、Console の
  モバイル制約（ブラウザペイン非対応等）を迂回する実用経路ができる。
- 受け入れる制約: Teams ユーザーには通知のみ（双方向が要るなら Slack/Discord を併用）。
  ユーザー自前 App 登録の初期設定コスト — Connections カードのセットアップ
  ウィザードで緩和（トークン検証→招待 URL 自動生成→チャンネルピッカー→接続時
  テスト通知。数字 ID コピー不要。docs/37 P1 追補）。チャット側と通知センターの
  既読ずれ（写し原則として許容）。外部プラットフォーム仕様のドリフト
  （live 契約テスト `AF_SLACK_LIVE` / `AF_DISCORD_LIVE` を一次検知とする）。
- **受信（P2a）の制約**: Discord のスレッド返信を読むには **MESSAGE_CONTENT 特権 intent**
  が要る（Bot<100 サーバは開発者ポータルでチェック1つ・審査不要）。受信は opt-in
  （`Discord.Receive`）で、有効ユーザーにのみ Gateway WSS を 1 本張る（メモリ配慮）。
  受信のルーティングは契約5の本人限定（bound user のスレッド内発言のみ）を唯一の防壁とし、
  チャンネル聴取の面は作らない。DM モード受信は対応表が無いため対象外（チャンネル＋
  スレッド構成が前提）。
