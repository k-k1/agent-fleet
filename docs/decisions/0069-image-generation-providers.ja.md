# 0069. 画像生成をフリートの道具にする——provider 抽象は 1 つ、層は「鍵の持ち主」で切る

[English](0069-image-generation-providers.md) | 日本語

- 状態: **採用（P0 実装済み）**（2026-09-06）。以下の数値はすべて同日に Workspace
  コンテナで実測したもの、または同日に各社の公式資料から取得したもの（出典は末尾）。
  同日にレビュー済み: 未解決だった 2 点は実測で決着し（末尾「未解決の 2 点——決着」）、
  決定 3・8・9 は名指ししているコードの現物に合わせて訂正した。P0——Codex 経路・opt-in
  ゲート・MCP ツール・使用量の行・掃除——は実装済み。P0 が意図して落としたもの、および
  実装が上記から踏み出した箇所は末尾「実装メモ」にある。第 2・第 3 層（決定 3）は未着手。
- 関連: [0013-tts-zundamon.ja.md](0013-tts-zundamon.ja.md)（写した前例。`ttsProvider` と
  `chooseTTSProvider`、そして「前処理は provider の外」）/
  [0031-mcp-registry.ja.md](0031-mcp-registry.ja.md)（レジストリは 1 本のリスト。`af`
  ビルトインが自前サーバを全 kind へ届ける経路）/
  [0038-chromium-attach-view.ja.md](0038-chromium-attach-view.ja.md)（セッション側へ狭く
  加算する道具の形）/
  [0041-cross-session-messaging.ja.md](0041-cross-session-messaging.ja.md)（既定 OFF の
  opt-in ゲート: ui-prefs → 起動引数 → 再 materialize）/
  [0029-usage-accounting.ja.md](0029-usage-accounting.ja.md)（拡張することになる列挙）/
  [0047-tenant-network-restriction.ja.md](0047-tenant-network-restriction.ja.md)（agent 直
  呼びに allowlist が要り、CP 経由には要らない理由）

## 背景

いま画像を生成できるのは codex セッションだけで、他の kind は何もできない。組み込みの
`image_gen` は Codex CLI に同梱され、`gpt-image-2` を後ろに持ち、`OPENAI_API_KEY` を必要と
せず利用者の ChatGPT ログインで動き、そのプランの利用枠をテキストのターンより 3〜5 倍速く
消費する。Agent Fleet 側にはこれを提供する仕組みが無く、claude にも opencode にも相当機能は
無い——Anthropic は画像生成 API を出していないので、**claude セッションにとってこの能力は
外から持ってくる以外にない**。

要件は、他の kind にも同じ能力を与えること、そして後から Gemini や他社の画像 API を、作り
直さずに足せるようにすることである。

重要な発見は、**これがシェルからは既に動く**ことだ。コンテナ内のどのセッションでも
`codex exec` を叩けば PNG が得られる。したがって論点は可否ではない。機能として作ることで
増えるのは、発見可能性、毎回 30 秒の試行錯誤ではない安定した契約、opt-in のゲート、Console
が見せられる場所に落ちること、使用量が記録されること、ディスクが掃除されること——この 6 つ
である。

### 実測（2026-09-06・Workspace コンテナ内）

- Codex CLI 0.153.4、`auth_mode=chatgpt`（API キーは無い）、feature `image_generation` は
  stable/true。
- `codex -a never -s read-only exec --json --skip-git-repo-check --ephemeral --color never
  -C <空dir> -m gpt-5.4-mini "<prompt>"` で **27.6 秒**で PNG 生成成功。
  **read-only サンドボックスで足りる**——`image_gen` はネイティブツールでシェル経由では
  ないため、サンドボックスの外に書く。
- 出力先は `~/.codex/generated_images/<thread_id>/call_*.png`。`<thread_id>` は
  `thread.started` イベントで取れる。**0.144 では `exec-*.png` だった**——命名は既に一度
  動いている。
- `--ephemeral` でもファイルは残る（スレッドは残らない）。
- 1 枚あたりの driver コスト: input 約 60k トークン（うちキャッシュ約 24k）、output 数百。
- **2 回とも、要求サイズ（256 / 1024）に関わらず 1254×1254** が返り、2 回とも
  `` `num_last_images_to_include` must be between 1 and 5 `` が 1 度出てからモデルが自力で
  リトライして成功した——1 枚につき 1 往復の無駄。
- このコンテナの `~/.codex/generated_images` は既に **80 MB**（1 枚 2〜3 MB）。掃除する主体は
  存在しない。
- コンテナからの到達性: `generativelanguage.googleapis.com` 404 /
  `api.openai.com` 421 / `bedrock-runtime.us-east-1.amazonaws.com` 404 /
  `api.stability.ai` 307 / `api.replicate.com` 200 / `api.bfl.ai` 302 ——全部到達する（この
  配備は egress を強制していない）。**`.amazonaws.com` を除き、これらは
  `defaultEgressAllowlist` に入っていない。**
- Bedrock は**このコンテナに既にある AWS プロファイルのまま**列挙できた（新しい秘密ゼロ）:
  `us-east-1` は `amazon.nova-canvas-v1:0` ＋ Stability 一式（`stable-image-inpaint` /
  `outpaint` / `remove-background` / `style-transfer` / `search-replace` /
  `control-sketch` / アップスケーラ群）、`ap-northeast-1` は
  **`amazon.nova-canvas-v1:0` のみ**。

### 各社資料から（2026-09-06 取得）

- Codex の `imagegen` スキルは、組み込みツールの保存先が `$CODEX_HOME/generated_images/`
  であること、プロジェクトの資産をそこに置いたままにしないこと、そして明示的に
  **「組み込みツールで任意のパスへ書けると約束するな」**と書いている。さらに
  **`gpt-image-2` は背景透過に対応しない**（`gpt-image-1.5` は対応・利用者の同意がある時
  だけ使う）、参照画像は最大 5 枚——これが上の `num_last_images_to_include` 1〜5 のエラーの
  正体である。
- 画像生成は **ChatGPT Free プランでは使えない**。API キーで codex を使うとプラン枠ではなく
  API 課金になる。
- **この橋渡しには先行事例が 2 つある**。`~/.codex/auth.json` を読んで Codex の**非公開**
  バックエンドへ POST する単体 CLI と、`codex exec --full-auto` を叩いて標準出力の
  `SAVED: <path>` を拾う Claude Code プラグインである。前者の README 自身が「Codex の内部は
  変わりうる。更新後に動かなくなることがある」と書いており、同時に**バックエンドは
  `--size`（`auto` / `1024x1024` / `1536x1024` / `1024x1536`）・`--quality`・`--background`
  を受け付ける**ことを示している。つまりパラメータは後ろに存在する。実測が示したのは
  **エージェント的な `codex exec` のターンを経由すると、それが driver モデルに媒介されて
  効かなかった**という事実である。
- OpenAI の Images API（鍵経路）は現在 gpt-image-2 / gpt-image-1.5 / gpt-image-1（2026-10-23
  に削除予定）/ gpt-image-1-mini。サイズは 1024×1024・1024×1536・1536×1024 を基本に、
  両辺が 16 の倍数・アスペクト比 1:3〜3:1 という一般則。透過は gpt-image-2 では**生成で
  preview・編集では非対応**。**高品質・高解像度で 1 枚 235 秒**という報告がある。
- Google の現行画像モデルは `gemini-3-pro-image`（Nano Banana Pro）。1K/2K で約 $0.134、
  4K で約 $0.24。より安い Nano Banana 2 系もある。API に無料枠は無い。
- Bedrock Nova Canvas は `InvokeModel`、base64 入出力、辺は 16 の倍数・320〜4096、生成は
  2048×2048 まで、`quality` は standard / premium、タスクは text-to-image・インペイント
  （マスク**または**テキストで領域指定）・アウトペイント・バリエーション・色指定生成。

**各社の数値は「その日の値」として扱うこと。** この ADR を書く数か月の間にモデル ID も価格も
2 度動いている。実装時に取り直すこと、そして**特定のモデル ID が有効であり続ける前提に設計を
依存させないこと**。

## 決定

**1. 抽象は 1 つ、置き場は Agent、形は TTS（ADR 0013）から写す。**
`workspace/agent/internal/imagegen/` に `Provider`（`ID` / `Caps` / `Ready` / `Generate`）と、
`chooseTTSProvider` と同型の `chooseImageProvider(pref, req, ready…)` を置く。CP ではなく
Agent に置くのは、Codex CLI と利用者の ChatGPT ログインがコンテナの中にしか無く、生成物は
Console の file API が読めるディスクへ落ちる必要があるためである。

**2. provider は画素を作るだけ。** 保存・命名・保持期限・後処理・使用量記録・MCP の面は
コアが持つ。ADR 0013 が `ttsProvider` からテキスト前処理を外に出したのと同じ切り方である。
好きな場所に好きな名前で書く provider は、差し替えられない provider である。

**3. 層は「鍵の持ち主」で切る。ベンダーでは切らない。** 実装コストを決めるのは API の差では
なく、誰が鍵を持つかである。

| 層 | サービス | 資格情報 | フリート側の追加 | egress |
|---|---|---|---|---|
| **既存接続を流用** | Codex（`image_gen`）、**Bedrock**（Nova Canvas・Stability 一式） | 利用者の ChatGPT ログイン／AWS の資格情報チェーンを `CloudWatchConn` / `AWSConn` と同じ流儀で参照する（`AWSProfileRef`: プロファイル名＋任意のリージョン、秘密は保存しない） | provider ファイルのみ | Codex の既存経路／`.amazonaws.com` は**既に allowlist にある** |
| **会員の鍵** | Gemini・OpenAI Images・Stability・FLUX・Ideogram・Recraft・Replicate | 会員が鍵を貼る（`secrets.Opencode` と同じ流儀） | ＋ Connections のカード 1 枚 | **allowlist 追加が必要** |
| **テナントの鍵** | Vertex AI・Azure OpenAI | 管理者が一度設定 | ＋ CP 側 provider。`CPBridge` と同型の bridge をもう 1 本足して経由する（現存する唯一の実体は git 資格情報ヘルパー用の `GitOAuthBridge`） | **不要**——CP の通信は制限の外（ADR 0047・`tts.go`） |

したがって **2 つ目の provider は Bedrock にする**。新しい秘密が要らず、ホストは既に
allowlist にあり、決定 5 を早期に実物で検証させる編集系の操作が一気に入る。

レビューでの訂正が 2 つ。どちらも初稿が名指しした型の話である。`AWSConn` は Agent Toolkit for
AWS MCP の接続であり、ready 判定は `s.AWS.Profile != ""` だ。これを Bedrock の資格情報として
流用すると「画像を作れる」が「AWS MCP を繋いである」に依存する。よって Bedrock provider は
自前の `AWSProfileRef` を持ち（AWS MCP の接続があればそこから初期値を写す）、リージョンは
固定の既定値ではなく `Caps` の入力（決定 5）とする。また `CPBridge` は経路ではなく構造体で、
ストアにある実体は 1 つ、そのトークンの権限は git OAuth の refresh grant に限られている。
テナント層はそれを借りず、自分の bridge エントリを持つ。

**4. P0 は Codex 経路。ただし守りを固めて叩く。**
`codex -a never -s read-only exec --json --skip-git-repo-check --ephemeral --color never
-C <空の一時dir> -m <小さいモデル> [-i <入力>…] -`。プロンプトは **stdin**（argv だと
プロセス一覧に全文が出る）。テンプレは `$imagegen` トリガで始めシェルの使用を禁じる。
結果は **`$CODEX_HOME/generated_images/<thread_id>/` の差分で回収**し、モデルの文章からは
決して拾わない。回収後 `~/.cache/agent-fleet/generated/<sid>/` へ移す。

この形は用心ではなく、根拠が 2 つある。ひとつ、上流には **`image_gen` が使えない状態に
なったセッションが、代わりにスクリプトでダミーの PNG を書いて済ませる**という報告がある。
`-s read-only` ならモデルはそのファイルを書けず、生成物ディレクトリしか見ない回収器は
それを拾えない——偽物を返す代わりに正直に失敗する。ふたつ、先行事例のプラグインは標準出力の
`SAVED: <path>` を解析しているが、それは driver モデルが黙って破れる契約である。

**5. `Caps` は provider 単位ではなく (provider, モデル) 単位。** `gpt-image-2` は背景透過に
対応せず `gpt-image-1.5` は対応する。透過は生成では preview、編集では非対応。Bedrock の
カタログは**リージョンで違う**（上の実測）。固定 1 組の能力を返す provider は、モデルか
リージョンを切り替えた最初の日に嘘をつく。

**6. `Request.Op` を初日から持つ。** `generate` / `edit` / `inpaint` / `outpaint` /
`remove-background` / `upscale` と `Mask`。Bedrock と Stability のカタログは大半が編集系
である。この軸を後から入れると、その時にはどの provider も固有パラメータを生やし終えて
おり、共通語彙が死ぬ。

**7. できないことは隠さず報告する。** Codex 経路しか無い時期でも `size` / `background` /
`count` は語彙に残し、満たせなかった要求は「実際にどうなったか」とともに
`Result.Warnings` で返す（実測: どちらのサイズ要求でも 1254×1254。パラメータを明示しても
同じだった——下の未解決 2。つまり Codex 経路ではこの警告経路が恒久的な姿であり、一時的な
ものではない）。**コアが黙って縮小
することはしない**——サムネイル用の整数倍ボックス縮小は既にあるが 1254→1024 は整数倍では
なく、厳密サイズのためにリサンプラを足すのは、provider が約束していない品質の保証と実在の
依存関係を引き換えにすることになる。厳密サイズはそれを本当に持つ provider とともに来る。

**8. 道具は `af` ビルトイン MCP サーバで配り、opt-in・既定 OFF。** ゲートは peer messaging
（ADR 0041）と完全に同型: ui-prefs のキー → mcpreg が読むフック → `builtinRunArgsFor` が
`--image-gen` を付与 → 変更時に `MaterializeAll()` → 次に起動したセッションから有効。
既定 OFF の理由は、**codex ではないセッションから、利用者の ChatGPT プラン枠を目に見えない
まま消費する**からである。決定 9 が入った時点で既定を見直す。

⚠️ 起動引数は **opt-in のスイッチだけ**を担い、kind を載せてはならない。初稿は
`builtinRunArgsFor` に `kind` を通せと書いた。レビューで分かったのは、kind は 1 呼び出し先に
ある（`Materialize(kind)` → `ForSession(kind)` → `builtinDefs(s)`）が、それを引数に載せると
`af` サーバの argv が kind ごとに違ってしまうことだ。所有台帳（`managed.Kinds`）と drift
テストは「起動ごとに af の定義は 1 つ」を前提に書かれており、`BuiltinRunArgs(id)`
（`chatx` のアシスタント経路）には差し出せる kind が無い。したがって「このセッションに
`generate_image` を見せるか」はツール一覧を組む場所で決める。`mcpStdioToolList` は
`tools/list` のたびに計算され、サーバは自分のセッションを知っており（`AF_SESSION_NAME`、
codex なら thread config）、`agentBaseURL()` 経由で Agent にそのセッションの kind と実効
provider を尋ねる——他のセッション用ツールが既に使っている継ぎ目である。条件は「実効
provider が codex **かつ** セッションが codex のときだけ配らない」。経路が Gemini や Bedrock
になれば codex セッションもフリート側の道具を欲しがるし、一覧時に評価するのでその切替に
再 materialize は要らない。

**9. 使用量は記録する。測れないものは測れないままにする。** 新しい feature タグ
`tool.imagegen` を、ADR 0029 §2 が凍結した列挙に足す。レビューで、この列挙が既に一度、注記
なしにずれていることが分かった: `plan.update`（`usagex/ledger.go` の `FeaturePlanUpdate`）は
コードにあって ADR に無い。よって ADR 0029 への追記は 2 件を記す——`plan.update` は出荷済み、
`tool.imagegen` は本 ADR で追加——凍結された表を `ledger.go` と再び一致させるためである。Codex
経路の `turn.completed` の usage は、チャットの一発実行が既にやっているのと同じ要領で記録
する。ただし**画像が消費するプラン枠はトークンでは表現できず**、3〜5 倍という値は文書で
あってテレメトリではない。枚数とピクセルを記録し、**この経路ではプラン消費が測れないと
明記する。0 で埋めない。**

**10. Codex の非公開バックエンドを自前で叩くことはしない。** 先行 CLI はそれをやっており、
厳密な `--size` / `--quality` / `--background` が手に入るのもそれだが、README 自身が壊れうると
警告している非公開契約であり、そこに製品機能を固定することになる。厳密な制御が要る場面の
ためにこそ、第 2・第 3 層の provider がある。レビューは未解決 2 の決着を知ったうえで、つまり
ChatGPT ログイン経路で厳密サイズを得る**唯一の**方法が非公開エンドポイントだと分かったうえで、
この判断を維持した。本 ADR 自身の実測が、公開面ですら動く（出力ファイル名が 0.144 と 0.153 で
変わった）ことを示しており、非公開面はそれより速く動く。

**11. 由来は結果の一部。** `Result` は provider・モデル・リージョン・概算コストを持ち、
プロンプトの送り先が監査できるようにする。画像が 1 枚生成されたということは、プロンプトが
名前のあるサービスへコンテナの外に出たということである。provider 単位の管理者許可が要る
テナントには `tts_engine` の前例をそのまま当てる。

## 未解決の 2 点——決着（2026-09-06）

2 点とも 2026-09-06 に同じコンテナで実測した。タイムアウトのハーネスは、`wait` という 1 つの
ツールだけを持つダミーの stdio MCP サーバである（N 秒 sleep して返答し、返答を書けたかを
ログに残す）。各 CLI の非対話モードから、いちばん安いモデルで叩いた。画像の試行は予算どおり
1 回だけ行った。

1. **クライアント側の MCP ツール呼び出しタイムアウト——kind ごとに違い、それぞれに手がある。**

   | クライアント | 素の呼び出し | 見つかった上限 | 手 |
   |---|---|---|---|
   | Claude Code 2.1.261 | 60 秒 ok、**300 秒 ok** | 到達せず。文書上の `MCP_TOOL_TIMEOUT` は呼び出し単位の壁時計で既定約 28 時間、stdio の無応答中断は 30 分 | 何も要らない（`.mcp.json` にサーバ単位の `timeout` はある） |
   | Codex CLI 0.153.4 | 90 秒 ok、**300 秒で切断**——サーバは 300.0 秒で返答済みなのに `timed out awaiting tools/call after 300s` | 300 秒＝`tool_timeout_sec` の既定 | codex の materializer で `mcp_servers.<af>.tool_timeout_sec` を書く（今は `startup_timeout_sec` しか書いていない） |
   | opencode 1.18.29 | **60 秒で切断**、60.0 秒ちょうどで `MCP error -32001: Request timed out` | 60 秒＝MCP SDK の既定。設定に `mcp.<name>.timeout` はあるが `opencode mcp add` が書けないので materializer は `TimeoutMS` を落とす | **サーバからの `notifications/progress`**: opencode は `progressToken` を送り、進捗通知のたびに時計をリセットする——10 秒ごとのハートビートで 90 秒が成功 |

   これで決まること: **P0 は同期のまま。** `generate_image` は呼び出しの中で画像を返す。`af`
   サーバは provider が走っている間、その呼び出しの `progressToken` に対して 10 秒ごとに
   `notifications/progress` を出し（これだけで opencode は 60 秒から無制限になる）、codex の
   materializer は af ビルトインに `tool_timeout_sec` を書く——600 秒なら 235 秒の報告に余裕が
   ある。codex が進捗でリセットするかは測っていない。claude には何も要らない。実装上の注意:
   `RunStdio` のループは要求 1 つに応答 1 つを書く形なので、処理中の `tools/call` の間に
   ハートビートを出すには stdout の writer を mutex で共有する必要がある。

   job＋ポーリングは P0 では**やらない**。ポーリング 1 回は driver モデルの 1 ターン——呼び手の
   プラン枠でトークンと待ち時間を払う——であり、P0 の provider に本来非同期のものは無い。
   最初の「投げて待つ」型 provider（Replicate・FLUX）とともに、置き換えではなく 2 本目の道具
   として入れる。`Generate` は最初から `context.Context` を取るので、その分割は後で費用が
   かからない。

   副産物: `codex exec -a never` は MCP ツール呼び出しをそもそも拒む（`MCP tool call requires
   approval, but approval policy is never`）。サーバに `default_tools_approval_mode="approve"`
   が付いていれば通る。フリートは codex 向けに materialize する全サーバへこれを付けている
   （`mcpreg/attach.go`）のでセッションには影響せず、手で叩く探針だけが引っかかる。

2. **`codex exec` 経由でサイズ・品質・背景を効かせられるか——効かない。** `$imagegen`
   トリガに `size="1024x1024"`、`quality="low"`、`background="opaque"`、`n=1` をツール
   パラメータとして明示した 1 回: 37 秒、input 49.6k トークン、848 KB の PNG が 1 枚、
   **1254×1254**。driver 自身の言葉は「このツールはここではプロンプト文と参照画像しか受け
   付けない」で、制約はプロンプト文に畳み込まれた。つまりパラメータはバックエンドには存在
   するが、driver が見ている `image_gen` ツールはそれを露出していない。決定 7 の警告経路は
   Codex 経路の恒久的な姿であり、厳密サイズは第 2・第 3 層の性質である。有益な副産物が 1 つ:
   `$imagegen` テンプレでは `num_last_images_to_include` のエラーが出ず、最初の 2 回で見た
   無駄なリトライは消えた。

## 退けた案

| | 何をするか | 退けた理由 |
|---|---|---|
| **A. 「Bash から `codex exec` を叩く」で済ませる** | 実装ゼロ。今日動く | 発見可能性が無く（毎回 28 秒かけて起動形を再発見する）、ゲートも Console のカードも使用量記録も無く、2〜3 MB のファイルを掃除する主体も居ない。「できるか？」の答えとしては十分だが、機能ではない |
| **B. kind ごとに skill / スラッシュコマンド** | 先行事例の Claude Code プラグインと同じ: `codex exec --full-auto` ＋ `SAVED:` 解析 | claude 専用、書き込み可能サンドボックス、そして driver モデルが破れる標準出力契約。kind ごとに作り直しになる——`af` MCP サーバは既に全 kind へ届いている |
| **C. Codex の非公開バックエンドを自前実装** | `~/.codex/auth.json` で内部エンドポイントを叩く | 厳密なサイズ・品質・背景が手に入る代わりに、作者自身が壊れうると書いている非公開契約に製品機能を固定する。決定 10 |
| **D. TTS と同じく全部 CP に置く** | provider を CP 側の 1 サービスに集約 | Codex 経路をそもそも載せられず（CLI とログインはコンテナの中）、生成物は Console が開くためにコンテナのディスクへ落ちる必要がある。CP は**テナント鍵の層**として残す（決定 3） |
| **E. コンテナ内でローカル SD / ComfyUI** | 外部サービス不要・枚数課金なし | GPU が無く、ホストは共有でメモリ制約がある——ビルドメモリの規則が防いでいる事故そのもの。外部でホストされた ComfyUI なら、それは第 2 層の HTTP provider に過ぎない |
| **F. Midjourney** | 人気のモデル | 公式 API が無く、Discord 経由の自動化は規約違反 |

## 確認した出典（2026-09-06）

- [Codex `imagegen` SKILL.md](https://github.com/openai/codex/blob/main/codex-rs/skills/src/assets/samples/imagegen/SKILL.md)
  ——組み込みツールの挙動、`$CODEX_HOME/generated_images/`、任意パス不可、gpt-image-2 の
  透過非対応、CLI フォールバック。
- [Using Codex with your ChatGPT plan](https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan)
  と [Codex pricing](https://developers.openai.com/codex/pricing)——Free では使えない、
  枠を 3〜5 倍速く消費する。
- [openai/codex#19133](https://github.com/openai/codex/issues/19133)——`image_gen` が使えず、
  スクリプトでダミー PNG を書く挙動。
- [codex-imagegen-cli](https://github.com/jdmnk/codex-imagegen-cli)——先行事例。非公開
  バックエンド、`--size` / `--quality` / `--background`、参照画像 1〜5 枚、「内部は変わりうる」。
- [codex-image-in-cc](https://github.com/KingGyuSuh/codex-image-in-cc)——先行事例。
  `codex exec --full-auto` ＋ `SAVED:` 解析による Claude Code への橋渡し。
- [OpenAI image generation guide](https://developers.openai.com/api/docs/guides/image-generation)
  と [Create image](https://developers.openai.com/api/reference/resources/images/methods/generate)
  ——現行モデル、サイズ規則、透過の状態。
- [Gemini 3 Pro Image の価格](https://pricepertoken.com/pricing-page/model/google-gemini-3-pro-image-preview)
  ——枚数課金と `gemini-3-pro-image` の ID（実装時に Google 公式の価格表で取り直すこと）。
- [Amazon Nova image generation](https://docs.aws.amazon.com/nova/latest/userguide/image-gen-access.html)
  と [Nova pricing](https://aws.amazon.com/nova/pricing/)——Nova Canvas のタスク、サイズ
  規則、`InvokeModel` の形。
- [Claude Code — MCP](https://code.claude.com/docs/en/mcp)——`MCP_TOOL_TIMEOUT`（呼び出し
  単位の壁時計。進捗では延びない）、`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`（stdio は 30 分）、
  `.mcp.json` のサーバ単位 `timeout`。上の 300 秒の実測と整合する。codex の
  `tool_timeout_sec` / `default_tools_approval_mode`、opencode の `mcp.<name>.timeout` /
  `resetTimeoutOnProgress` は文書ではなく、入っているバイナリから読み取った。

## 実装メモ（P0・2026-09-06）

置き場: `workspace/agent/internal/imagegen/`（`Provider` インターフェース、
`chooseImageProvider`、Codex provider、保存と保持期限、REST 2 本）／ツールの面は
`internal/mcpx`（`--image-gen`・`generate_image`・進捗ハートビート）／opt-in ゲートは
ui-prefs `imageGeneration` → `mcpreg.ImageGenEnabled` → `builtinRunArgsFor` →
`MaterializeAll()`／feature タグは `usagex/ledger.go`。

上の設計が書いていなかった、あるいは書いたのと違う点が 6 つ。

1. **起動コマンドに `--ignore-user-config` を足した**（2026-09-06 の実測では付けていない）。
   `~/.codex/config.toml` は `mcpreg/materialize_codex.go` が登録済み MCP サーバを——
   **af 自身のものも含めて**——書き込む場所で、読ませると画像 1 枚のために利用者の MCP
   一式が起動し、この exec 自身に `generate_image` が生えてしまう。認証は従来どおり
   `CODEX_HOME` から来ることを実走で確認した。
2. **MCP ツールが返すのは画像そのものではなくパス。** 実測の PNG は 563 KB〜848 KB、
   base64 にすると 0.7〜1.1 MB で、それが以後そのセッションの文脈に居座り続ける。ファイルは
   セッションが読めるディスクにあるので、絵を見る必要があるときだけモデルが開けばよい。
   ここでの「同期」は「呼び出しが返った時点で画像が存在する」という意味であって、バイト列が
   呼び出しに乗るという意味ではない。
3. **`tool_timeout_sec` は未解決 1 が挙げた 2 箇所ではなく 3 箇所に要った**——
   `materialize_codex.go`（config.toml）、`attach.go`（チャットの `-c` 上書き）、
   `thread_codex.go`。スレッド設定はファイル側のエントリを**まるごと置き換える**ので、
   config.toml にだけ書いた値は managed な codex セッションには継承されず、消える。
4. **台帳の行に列を 2 つ足した**——`images` と `pixels`（ADR 0029 §1 を同じコミットで改訂）。
   成功した生成も `measured=partial` で記録する: 駆動ターンのトークンは正確だが、画像自体が
   消費したプラン枠はそこに入っていないため。
5. **保持期限は 30 日**（生成のたび、最短 1 時間間隔で掃除）。隣のサムネイルキャッシュより
   長いのは意図的で、これは派生データではなく、作り直しはプラン枠をもう一度使う。Codex
   provider は回収元（`$CODEX_HOME/generated_images/<thread_id>/`）のコピーも削除するので、
   この経路は上で実測した 80 MB の堆積をもう増やさない。
6. **ツールの `op` 列挙は tools/list の時点で実効 provider の能力から組み立てる**ので、
   inpaint できない経路が inpaint を宣伝することはない。`Op` の語彙は Go の型としては
   全部そろっており（決定 6）、Codex 経路は `generate` / `edit` を申告し、mask は明示的に断る。

検証: 単体テストで provider 選択、ディレクトリ差分での回収（回収してはいけない既存ファイル、
1 枚も出なかった run を含む）、Codex セッション×Codex 経路の tools/list 除外、ハートビート、
3 経路すべての `tool_timeout_sec`、台帳の行を押さえている。実 CLI に対する実走は 1 回——
34 秒、1024×1024 を頼んで 1536×1024。決定 7 の実例がもう 1 つ増えたことになり、しかも上で
実測した 1254×1254 とは**また違う**外し方だった。

1 つ明記しておく制約: `RunStdio` のループは直列に処理するので、生成が走っている間 af サーバは
stdin から何も読まない（`notifications/cancelled` を含む）。そのパイプの向こうにいるのはこの
呼び出しを待っているエージェント自身なので実害は出ないが、無期限に待たせず上限を切っている
（provider 側 8 分・MCP 層 10 分）のはこのためである。

意図して未着手: 第 2・第 3 層の provider（決定 3・次は Bedrock）と job+poll（未解決 1）。

## 実装メモ（P1——会話の中に絵を出す・2026-09-06）

P0 の時点では、生成物は「届いてはいるが誰も知らせてくれない」状態だった: モデルには JSON の
結果が返り、利用者にはうっすらしたツール痕跡とパスが残るだけ。P1 はその痕跡を**画像カード**に
変える。**フロントエンドの変更は 1 行も要らなかった** — `kind:"userfile"` の part はミラーの
`UserFileBlock`/`FileCard` が既にサムネイル付きで描くからで、codex 自身の `image_gen` /
`view_image` が通っているのと同じ経路である。

- 判定は共有関数 1 本（`internal/transcript/generated_image.go`）。af のツールは**定数ではなく
  形**で見分ける——サーバ名は起動ごとに回るため。`mcpreg.IsAFToolName` は生成側が検証に使う
  パターンをそのまま埋め込んでいるので、両者がずれることはない。`mcp__af_<8桁hex>__generate_image`
  は claude の綴りで、稼働中のセッションの実物から読んだ。アンダースコア 1 本の形と裸の形は
  「そう主張する」のではなく「許容する」扱い。
- **パスはツールの結果から取る**。出力ディレクトリの列挙は使わない。だから 1 つの会話で 3 枚
  生成すればカードは 3 枚それぞれ正しい位置に出て、失敗（JSON でなく文章）には 1 枚も出ない
  ——Codex provider 自身が守っている「文章はファイルの根拠にならない」と同じ規則。
- **キャプションには warnings を載せる。** これまで決定 7 の報告はモデルにしか届いておらず、
  利用者は 1024×1024 を頼んで 1536×1024 を渡されたことに気付く手がかりが無かった。
- **claude にはカーソル保留が要った。** 呼び出し時に tool_use 行を書き、tool_result は 30 秒
  ほど後に——しかもそれは tool_result だけの user 行で、それ自体はターンにならない行に——
  書かれる。したがってライブのポーリングは呼び出しを配ってから窓を先へ進めてしまい、カードは
  永遠に出ない。`/messages` は**未決着の** generate_image 行の手前でカーソルを保留するように
  した（保留中の質問で既にやっているのと同じ仕組み）。Console 側は idx でマージして持っている
  ターンを差し替える。空振りに終わった呼び出しは「未決着」ではなく「決着済み」なので、失敗が
  永久に保留を続けることはない。後方ページングでは保留しない——そこでの結果は「まだ書かれて
  いない」のではなく「窓の外」だから。
- **opencode にはどれも要らない**: ツールの出力が同じ part に載っているので、解析したその場で
  カードを出せる。
- 他の kind は共有関数を 1 回呼ぶだけだが、当て推量では足さない。各 CLI が MCP ツール名を
  どう綴るかは実データで裏を取ってからで、裏が取れているのは claude だけである。
