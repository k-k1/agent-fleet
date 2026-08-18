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
| 切断 | `DELETE /api/credential/{credentialID}` | — （id は `connections[]` が持つ・§54.5） |

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
- **切断は credential ID 指定**。v1 の `DELETE /auth/{providerID}` は auth.json 側の経路で、
  Console アカウントの資格情報には効かない（§54.5 に症状と根拠）。daemon が停止している
  ときは消せないので、その旨を返す。

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

## 54.5 使う枠の4択（オフ / 無料枠 / Go / Zen）

opencode.ai は 3 つの課金経路を持ち、実測でそれぞれ独立していた（§54.4）。どれを使うかは
運用の判断なので、設定 > エージェント > opencode の**「使う枠」**で選ばせる。
旧「モデル一覧」設定（`go-first` / `hide-zen` / `all`）の置き換えで、ui-prefs のキーは
`opencodeCatalog` のまま値だけ変わる（`hide-zen`→`go`、`go-first`/`all`→`zen`、
未設定/不明→`off`。Agent 側 `CatalogPref` と Console 側 `migrateOpencodeCatalog` が
同じ規則を持つ）。

| 枠 | 一覧に出すもの | 必要な認証 | 起動ゲート |
| --- | --- | --- | --- |
| オフ | 何も出さない（直結プロバイダも含め全部落とす） | — | **常に不可**（鍵・アカウントがあっても無視） |
| 無料枠 | コスト 0 のモデルだけ | 不要 | **認証ゼロでも起動可**（実測: 未接続で free モデルが応答） |
| Go | `opencode-go/…` だけ | API キー（アカウントは任意） | キーがあれば |
| Zen | `opencode/…`（Go 契約があれば Go も） | アカウント か API キー | どちらかがあれば |

- どの枠でも（オフを除き）**直結プロバイダ（`anthropic/…` 等）は落とさない**。利用者自身の
  課金であって、opencode.ai の枠の選択とは無関係だから。
- **無料枠では `OPENCODE_API_KEY` を注入しない**（`env()`）。「無料枠で使う」と決めた
  ワークスペースが、鍵が保存されたままというだけで課金経路に乗らないように。他プロバイダの
  鍵は触らない。枠の切替は注入内容を変えるので、鍵の変更と同じく serve を作り直す。
- 無料判定は opencode 自身の規則を借りる（プラグインは `cost.some(c => c.input > 0)` を
  「有料」として無認証時に無効化する）。`GET /api/model` の cost から拾うので、モデル id を
  ハードコードしない。CLI 由来で価格が無いときは素通し — 無料枠では鍵を注入しないので、
  その CLI が返す opencode.ai の一覧はもともと無料枠のものだけになる。
- 起動ゲート（`registry.ts` の `available`）は `supported !== false` かつ
  「キーあり / アカウントあり / 無料枠」（`usage==="off"` が先頭で全部を否定）。
  バイナリ不在（旧イメージ）は従来どおり隠す。
- **既定値はオフ**（`CatalogPref("")`/`UsagePref` の未配線デフォルトとも `UsageOff`）。
  新規ワークスペースは opencode.ai を一切使わない状態から始まり、利用者が「使う枠」で
  明示的に選ぶまで鍵やアカウントがあっても起動しない。以前は未設定を Zen 扱いしていた
  （鍵を1本保存しただけで無断に課金経路が動く可能性があった）ため、この既定を変更した。
  オフは `UsagePref()==UsageOff` を `env()`/`connected()`/`Catalog()` の**先頭で** override
  する明示的なロックで、鍵や OAuth が後から増えても動かない
  （`internal/agents/opencode/auth.go` の `connected()`、`catalog.go` の `keepForUsage`）。
- **アシスタント・チャットの取りこぼしを修正済み（2026-08-13）**: 対話セッションの起動ゲート
  （`registry.ts` の `available`、Go 側 `Status().connected`）は元から上表の式を守っていたが、
  アシスタント・チャット側のバックエンド選定（`chat_providers.go` の
  `headlessAgentAvailable`）は `opencode.Available()`（バイナリが PATH にあるかだけ）しか
  見ておらず、この式と食い違っていた。結果、claude/codex が未ログインな環境でアシスタントが
  自動フォールバックする際、`opencode/nemotron-3-ultra-free` が何の認証も無いまま実行され、
  無料枠が**無断で**動いていた。修正は `opencode.Connected()`（＝上表の式）を切り出して
  `headlessAgentAvailable` にも適用し、両者を同じ一枚の判定に揃えたこと。新規トグルではなく
  既存の「使う枠」を両方の入口が正しく参照するようにしただけ。

## 54.6 切断は credential ID を消す（`/auth/{providerID}` では消えない）

当初は v1 の `DELETE /auth/opencode` を切断に使っていたが、**あれは何も消さない**。
v1 は `~/.local/share/opencode/auth.json` を書き換える経路で、Console アカウントの
資格情報は **SQLite 側の credential テーブル**に載っている。症状はこうだった:

- `opencode auth list` は「0 credentials」と言うのに、`GET /api/integration/opencode` の
  `connections[]` には `{type:"credential", label:"Personal"}` が居る。
- `DELETE /auth/opencode` は 200 を返すが、**新しく起こしたプロセスからも接続が見え続ける**
  （＝ストアから消えていない）。

正しい口は `DELETE /api/credential/{credentialID}`（`v2.credential.remove`）で、
id は `connections[]` の `id`（`cred_…`）。実測で 204 が返り、**共有 daemon の
`connections[]` はその場で credential を落とした**（再起動不要）。別プロセスを新しく
起こしても接続は無く、モデルは zero-auth の 8 件に戻る。

環境変数由来の接続（`OPENCODE_API_KEY`）は別経路なので巻き添えにしない — 削除対象は
`type:"credential"` の1件だけで、無ければ何も消さずに冪等成功として返す。

### APIキー側は「保存しただけ」では daemon に効かない

鍵は起動時に env として注入される（docs/27 §7）。したがって Console でキーを保存/削除
しても、**動いている serve daemon は自分の環境に古い鍵を持ったまま**になる。実測: キーを
消した後も daemon は `connections[]` に `{type:"env"}` を出し続け、モデルも 79 件のまま
（＝消したはずの鍵で課金され得る）。Agent を再起動しても `Ensure` は生きている daemon を
adopt するので直らない。反映パスは generation++ ＋ drain ＝ `Supervisor.Restart` なので、
鍵の保存/削除で明示的に呼ぶ（drain は最大60秒なのでハンドラは待たない）。

`Models()` が daemon 由来になったことで、この穴は起動一覧にも出るようになった
（以前は CLI をその場の env で走らせていたので、鍵を消せば一覧も即縮んだ）。

カタログの縮小そのもの（61 → 8）は、APIキーがある環境では鍵が覆い隠すため単独では
観測できない。削除経路も `ConnectionUpdated` を publish するので、ログインと同じく
プラグインの購読で catalog.reload() まで走るはず、というのが現時点の根拠。

## 54.7 利用枠（Go）の導線 — 数値は取り込めない

利用枠の画面（`https://opencode.ai/workspace/{wrk}/go`。ローリング/週間/月間の利用率と
リセットまでの時間が出る）を Console から扱いたい、という要求に対する到達点。**実測の
結論は「ID は持てる／数値は取り込めない」**:

| 試したこと | 結果 |
| --- | --- |
| `GET https://opencode.ai/workspace/{wrk}/go` を素で取得 | **302 → `/auth/authorize`**（ブラウザセッション前提） |
| `https://opencode.ai/api/…`（workspace/usage 系を数種） | すべて **404**（この画面向けの JSON API は無い） |
| `GET https://console.opencode.ai/api/orgs` / `/api/user` | **401 `{"_tag":"Unauthorized"}`** — 経路は存在し Bearer を受ける（CLI がここで `orgID` を取る） |
| `GET https://console.opencode.ai/api/usage` | **404**（利用枠の API は無い） |

つまり `wrk_…` は「アクセストークンがあれば `/api/orgs` から引ける」が、そのトークンは
opencode 自身の資格情報ストア（SQLite）にあり読み出す口が無い（`/api/credential/{id}` は
PATCH と DELETE だけ）。数値に至っては API 自体が存在しない。

そこで実装したのは 2 つだけ:

1. **workspace ID を持つ** — 手入力（利用枠ページの URL から）と、失敗からの自動学習。
   保存先は Agent のデータディレクトリ（`opencode-workspace.json`）。ID は秘密ではなく
   URL のパス片なので、封印ストアには置かない。学習が手入力を上書きすることはない。
2. **上限に当たったときの枠情報を見せる** — 失敗の decode 地点（`errorEnvelope.pick`）で
   `scanFailure` が拾う。材料は opencode 本体が読むのと同じ場所:
   - 文面の billing/go URL（残高切れは `…/workspace/wrk_x/billing` を含む — 実測）
   - `data.metadata.workspace` / `limitName`
   - `data.responseBody` を JSON として読み直した `metadata`（本体と同じ読み方）
   - `data.responseHeaders["retry-after"]` → リセット時刻（秒数と HTTP-date の両方）
   どれも optional で、載っていない版・載っていない失敗では静かに空になる。

Console は ID があれば「Go の利用状況を開く」リンクと、観測できた直近の上限
（枠名＋リセット時刻）をカードに出す。**利用率の % は出さない** — 取得手段が無いものを
それらしく見せない。

（残る選択肢としては、Chromium アタッチで利用者自身のブラウザセッションを使って開く／
上流に API を要望する、の 2 つ。どちらも自動取得ではない。）
