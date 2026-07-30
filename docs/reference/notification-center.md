# 通知センター

> ℹ️ **現行仕様**（アーカイブではない）。reference/ の他ファイルは docs/dev/ への移設スタブだが、
> 本書は通知センターの現行仕様として本文を保持している。

## 目的

Console のセッション通知と利用量リセット通知を、ブラウザごとの `localStorage`
ではなく membership（ユーザー × テナント）単位の履歴として扱う。PC とスマート
フォンを同時に開いても、履歴・未読は同じになり、通知から対象セッションへ戻れる。

## データ所有と同期

```text
Agent hook / Codex rollout
        │
        ▼
Workspace Agent durable outbox
        │  GET /notifications + POST /notifications/ack
        ▼
Control Plane notification table ── membership 単位の履歴・既読
        │  GET /api/notifications + POST /api/notifications/seen
        ▼
Console notification store ── OS 通知 / TTS / 通知センター
```

- 正本は Control Plane の `notification` テーブル。履歴は7日、一覧は新しい順に最大50件。
- `seen_at` は行単位で保持する。センターを開くと読み込んだ `maxSeq` までを既読にする。
  現在表示中のセッションに来た通知は、そのイベントだけを既読にする。
- セッション本文、回答、質問文は保存しない。種類、セッションID・kind、表示名、時刻と
  構造化メタデータだけを保存する。
- セッションイベントは Agent の `~/.config/agent-fleet/notification-outbox` に先に
  永続化する。両方のブラウザが閉じていても、次回同期時に Control Plane へ移る。
- Control Plane への insert は `event_id` で冪等。DB 保存後にだけ Agent を ack するため、
  通信切断時は再送しても重複しない。
- Codex の `request_user_input` は hook がないため rollout の末尾から検出し、call ID の
  marker で同じ未回答質問を再通知しない。

## イベント

| kind | 発生条件 | 対象 |
|---|---|---|
| `answer-ready` | `working → idle` | session |
| `question` | 質問待ちへ遷移 | session |
| `plan-approval` | プラン承認待ちへ遷移 | session |
| `permission-request` | ツール権限待ちへ遷移 | session |
| `usage-reset` | 90%以上に達した5時間/週間枠の `resetsAt` が前進 | usage |

利用量の armed 状態も Control Plane の `notification_usage_state` が正本である。Console は
Claude/Codex の利用量取得時に観測値を送るだけで、通知判定を `localStorage` に持たない。
端末の「利用制限リセット通知」設定が OFF でも履歴は作り、OS/TTS 配信だけを抑止する。

## Console の挙動

- TOPバーのベルから直近履歴を開く。未読数は `9+` を上限表示する。
- 通知の通常クリックは現在ペイン、Ctrl/Cmd+クリックと中クリックは新ペインで開く。
  専用の「新ペイン」ボタンは置かず、既存セッション一覧と同じ操作規則にする。
- 各通知の音声アイコンは内容を手動で再生する。手動再生をTOPバーで止めても通知設定は
  変更しない。
- 対象が一覧にない場合は一度セッション一覧を更新し、それでもなければ toast を出す。
- 初回ロードは履歴とバッジだけを復元し、過去通知を OS/TTS へ再配信しない。ページを
  開いている間に増えた未読イベントだけを配信する。
- 現在の active session のイベントはその端末では OS/TTS を抑止する。通知後に対象
  セッションをアクティブにした場合も、そのセッション宛ての未読だけを既読にし、
  TOPバーのバッジと他端末へ同期する。通常クリック、新ペイン、履歴移動を同じ扱いにする。
- ブラウザの通知権限はセンター内のボタンからだけ要求する。起動時には要求しない。
- Agent が新 API に 404 を返す場合だけ、従来のセッション状態差分通知へフォールバックする。
  Workspace 停止中は履歴を読めるためフォールバックしない。

## 端末ローカル設定と音声

履歴・既読は共有する一方、OS 通知権限と音声設定は端末ローカルのままにする。PC と
スマートフォンの両方で音声通知を ON にすれば両方で再生される。

TTS の再生目的は `reading` / `session-notification` / `usage-notification` / `manual`
として管理する。TOPバーで再生中の音声を止めた場合、読み上げなら `ttsEnabled`、
セッション通知なら `ttsSessionNotify`、利用量通知なら `usageResetNotify` を OFF にする。
手動再生は停止だけで設定を変えない。

## API

- `GET /api/notifications`: `{items,maxSeq,unseenCount,sourceState}`。`sourceState` は
  `ready | offline | unsupported`。
- `POST /api/notifications/seen`: `{throughSeq?,eventIds?}`。
- `POST /api/notifications/usage-observations`: `{source,windows[]}`。
- Agent internal: `GET /notifications`, `POST /notifications/ack`。

SQLite migration は `0019_notification.sql`、PostgreSQL migration は
`0003_notification.sql`。
