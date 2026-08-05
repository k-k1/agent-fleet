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
- **承認ページはコードが入力済み**（実測）。`?user_code=` を持ったまま開くので、
  ユーザーは「表示されたコードが手元と一致するか」を確認して Authorize を押すだけで、
  どこにも貼り付けない（AWS SSO の device flow と同じ）。Console の手順表示は
  `DeviceSteps` の confirm 形（①リンクを開く ②コードの一致を確認して承認 ③承認を待つ）。
  既定形（①コードをコピー ②リンクを開いて貼り付け）のままだと、存在しない入力欄を
  探させることになる。
- **状態の出どころは `connections[]`**。`type:"credential"` が Console アカウント接続、
  `type:"env"` は `OPENCODE_API_KEY` の存在を示すだけで別経路。`label` は接続先の
  org 名（opencode 側の label 解決が `metadata.orgName` を返す）。
- **切断だけ v2 に口がない**。connection 削除の HTTP ルートは存在せず、資格情報は
  `~/.local/share/opencode/auth.json` なので v1 の `DELETE /auth/{providerID}` を使う。
  daemon が居ないときは `opencode auth logout opencode` にフォールバックする。

### 起動レース: health ではなく「メソッド」を待つ

`Supervisor.Ensure()` は `/global/health` が 200 になった時点で成功を返すが、**device
メソッドを登録するのは opencode のプラグインで、その load は health より後に終わる**。
この窓で `connect/oauth` を叩くと daemon は 500 を返す:

```
level=ERROR ref=err_91d98832 error="OAuth method not found: opencode/device"
```

実機の再現（Console のボタン初回クリック）は、その daemon 世代の最初のログ行から
**85ms 後**の失敗だった。したがって start は health ではなく
`GET /api/integration/opencode` の `methods[]` に `{type:"oauth", id:"device"}` が
現れるまで待つ（`waitOAuthMethod`、最大 20 秒）。現れないまま切れた場合は
`serve_not_ready` として理由を返す（古い CLI ならメソッド自体が無い）。

同じ窓では integration 自体が未登録で `GET /api/integration/opencode` が `data:null` を
返す。状態表示側（`oauthStatus`）はこれを「未接続」と確定させず、直前の値を保つ —
ログイン済みのユーザーに一瞬「未接続」と見せないため。

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

## 54.4 実アカウントで測った「何が使えるようになるか」

実測（1.18.13、Personal org でサインイン済み。いずれも `status:"deprecated"` を除いた
active 数）:

| 認証 | active モデル | 内訳 |
| --- | --- | --- |
| 無し（zero-auth） | 8 | `opencode/*-free` ＋ big-pickle |
| **Console ログインのみ**（`OPENCODE_API_KEY` 無し） | **61** | すべて `opencode/`（Zen） |
| ログイン＋APIキー | 79 | Zen 61 ＋ `opencode-go/` 18 |

- **Console ログインだけで Zen の有料モデルは開く**（8 → 61）。ゲート
  （`OPENCODE_API_KEY || Console接続 || 明示apiKey` の OR）の実挙動どおり。
- **Go サブスクのモデル（`opencode-go/`）はログインでは生えない**。あれは
  `OPENCODE_API_KEY` に紐づくので、Go を使うなら従来どおりキーが要る。両方あれば両方出る。
- 接続ラベルは org 名（この環境では `Personal`）。`/experimental/console/orgs` は
  ログイン後も空のままで、こちらは別系統（`wrk_` はここからは取れない）。

### `opencode models`（CLI）はログインを見ない — カタログは daemon から読む

同じ資格情報でも、**一発起動の `opencode models` は 8 件**しか出さないのに、同じストアを
読む serve は 86 件（active 61）を返す。CLI 側はプラグインが Console org の
`/api/config` を取り終える前に出力しているとみられる。`Models()` が CLI に依存したままだと
**OAuth だけのユーザーの起動一覧が free 8 件に見える**（managed セッションは実際には 61 件
使えるのに）。そこで `Models()` は**稼働中の daemon があれば `GET /api/model`** を正とし、
`deprecated` と `enabled:false` を落として CLI と同じ形の id 列にする。daemon が無いときだけ
従来の CLI にフォールバックする（一覧の更新のために daemon を起こしはしない）。

## 54.5 未確定

- `DELETE /auth/opencode` が daemon 内のカタログ再読込まで誘発するか（`ConnectionUpdated`
  を publish するのは v2 側の経路）。切断後にモデル一覧が古いままなら、切断時のみ
  Supervisor 再起動へ格上げする。
