# 54. opencode Console アカウントの OAuth 接続

opencode に「APIキーを貼る」以外の認証経路として、**opencode.ai のアカウント
（Console org＝`wrk_…` の workspace）でのサインイン**を Console から行えるようにする。
APIキー方式（`workspace/agent/internal/agents/opencode/auth.go` の env 注入）は
そのまま残り、両者は独立して併存する。

実装: `workspace/agent/internal/agents/opencode/oauth.go`、Console 側は
`console/src/features/settings/AgentsTab.tsx` の opencode カード。

## 54.1 なぜ CLI の PTY ではなく serve の HTTP API か

cursor（docs/40）や kiro（docs/43）は CLI を PTY に載せて URL をスクレイプしている。
opencode はそれが不要で、共有 `opencode serve` daemon（docs/27 の RuntimeSupervisor）
が**構造化された device flow API** を持つ。OpenAPI（`GET /doc`）が契約の正本。

| 用途 | エンドポイント | 応答（`data` の中身） |
| --- | --- | --- |
| 開始 | `POST /api/integration/opencode/connect/oauth`<br>`{"methodID":"device","inputs":{}}` | `{attemptID, url, instructions, mode, time:{created,expires}}` |
| 進捗 | `GET /api/integration/attempt/{attemptID}` | `{status: pending｜complete｜failed｜expired, message?}` |
| 中断 | `DELETE /api/integration/attempt/{attemptID}` | — |
| 状態 | `GET /api/integration/opencode` | `{methods, connections:[{type:"credential",id,label} ｜ {type:"env",name}]}` |
| 切断 | `DELETE /auth/opencode` | — |

いずれも `{location, data}` の包みで返る。実測は 1.18.13。

- **`mode` は `"auto"`**。opencode 自身がトークンをポーリングするので、Console 側で
  コードを貼らせる必要はない（cursor と同じ「URL を出して poll」型）。`url` は
  `verification_uri_complete`（ユーザーコード入り）で、`instructions` に
  `Enter code: …` が入る。
- **状態の出どころは `connections[]`**。`type:"credential"` が Console アカウント接続、
  `type:"env"` は `OPENCODE_API_KEY` の存在を示すだけで別経路。`label` は接続先の
  org 名（opencode 側の label 解決が `metadata.orgName` を返す）。
- **切断だけ v2 に口がない**。connection 削除の HTTP ルートは存在せず、資格情報は
  `~/.local/share/opencode/auth.json` なので v1 の `DELETE /auth/{providerID}` を使う。
  daemon が居ないときは `opencode auth logout opencode` にフォールバックする。

### 対応範囲を Console アカウントに絞った理由

opencode の OAuth は提供 API によって顔ぶれが違う（実測 1.18.13）:

| | v2 `/api/integration` | v1 `/provider/auth` |
| --- | --- | --- |
| opencode Console アカウント（device） | ✅ | — |
| ChatGPT Pro/Plus（browser / headless） | ✅ | ✅ |
| GitHub Copilot / xAI / GitLab / Poe / DigitalOcean / Snowflake | — | ✅ |
| Anthropic Claude Pro/Max | — | — （TUI に文言だけ残り、メソッド未登録） |

v1 は `method` が配列インデックス指定で、`prompts`（select / text）に応じた動的
フォームが Console 側に要る。今回は Console アカウントのみを対象とし、v1 系は
必要になった時点で別トラックにする。

## 54.2 反映タイミング（daemon を再起動しない設計）

鍵の変更は daemon の再起動が必要（docs/27 §7）だが、**OAuth ログインは再起動不要**。
ログインは daemon 自身の API を通り、成立時に daemon 内で
`integration.connection.updated` が publish され、opencode プラグインがそれを購読して
「接続の再読込 → Console org の `/api/config` を Bearer＋`x-org-id` で取得 →
`catalog.reload()`」までプロセス内で完結するため（バイナリ実測）。

| 層 | いつ効くか |
| --- | --- |
| 共有 serve daemon（managed セッション） | 即時（上記のイベント購読） |
| 走行中のターン | 解決済みのプロバイダで走り切る → **次のターンから** |
| Terminal (CLI) の TUI セッション | 別プロセス＝別イベントバス（プロセス内 PubSub）→ **そのセッションの再起動後** |
| Agent のモデル一覧（起動モーダル / MCP `list_models`） | `opencode models` の 60 秒キャッシュ。キャッシュキーは注入 env のハッシュで、OAuth は env を変えない＝**自力では古いまま**。そこで poll が `complete` を見た時点で `InvalidateModels()` を呼び即時化する |
| Console の接続表示 | `/connections` のポーリング。成立/切断時に状態キャッシュを落とす |

## 54.3 状態表示の決め方

`GET /connections` の `opencode` は次を返す:

```
connected      … envs があるか、または Console アカウント接続あり（kind ゲートが見る）
envs           … 保存済み API キーの env 名（従来どおり）
oauth          … Console アカウント接続あり
oauth_label    … 接続先の org 名
oauth_known    … 一度でも daemon から読めたか。false = daemon 未起動で未確認
oauth_disabled … マネージド opencode が無効（AF_OPENCODE_SERVE_DISABLE=1）
```

- 状態確認のために daemon を**起こさない**（`/connections` は数秒おきに来る）。動いて
  いれば読み、居なければ最後に読めた値を返す（stale-if-error）。接続表示が点滅すると
  ユーザーを不要な再ログインへ誘導してしまうため。
- `oauth_disabled` のときはサインイン導線を出さない。この状態でも端末セッションから
  `opencode auth login` は従来どおり可能。

## 54.4 未確定（実アカウントでの確認待ち）

- Console ログインだけで（APIキー無しで）どのモデル集合が生えるか。有料モデルのゲートは
  `OPENCODE_API_KEY || Console接続あり || 明示apiKey` の OR なので**ゲート自体は開く**が、
  実際の顔ぶれは org の `/api/config` 次第。受け入れ確認は
  **ログイン前後の `opencode models` の差分**を見る。
- `DELETE /auth/opencode` が daemon 内のカタログ再読込まで誘発するか（`ConnectionUpdated`
  を publish するのは v2 側の経路）。切断後にモデル一覧が古いままなら、切断時のみ
  Supervisor 再起動へ格上げする。
