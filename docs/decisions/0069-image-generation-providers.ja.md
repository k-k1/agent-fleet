# 0069. 画像生成をフリートの道具にする——provider 抽象は 1 つ、層は「鍵の持ち主」で切る

[English](0069-image-generation-providers.md) | 日本語

- 状態: **採用（P0/P1/P2/P3 実装済み）**（P0/P1 は 2026-09-06、P2/P3 は 2026-09-07）。以下の
  数値はすべて、それぞれ明記した日に Workspace コンテナで実測したもの、または同日に各社の
  公式資料から取得したもの（出典は末尾）。2026-09-06 にレビュー済み: 未解決だった 2 点は
  実測で決着し（末尾「未解決の 2 点——決着」）、決定 3・8・9 は名指ししているコードの現物に
  合わせて訂正した。P0——Codex 経路・opt-in ゲート・MCP ツール・使用量の行・掃除——は実装済み、
  P1 で絵が会話の中に出るようになり、**P2 で 2 本目の provider（agy）と、それに伴って縦横比の
  軸・優先順位の設定 UI・「別のエージェント CLI は筋が悪い」という余談への訂正が入り、P3 で
  呼び出し側が provider を名指しできるようにした（`generate_image` の `provider` 引数）**。
  各段階が意図して落としたものは末尾の各「実装メモ」にある。第 2・第 3 層（決定 3）は未着手。
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
| **既存接続を流用** | Codex（`image_gen`）、**agy**（`generate_image`・2026-09-07 追加）、**Bedrock**（Nova Canvas・Stability 一式） | 利用者の ChatGPT ログイン／agy が既に持っている Antigravity の OAuth トークン／AWS の資格情報チェーンを `CloudWatchConn` / `AWSConn` と同じ流儀で参照する（`AWSProfileRef`: プロファイル名＋任意のリージョン、秘密は保存しない） | provider ファイルのみ | 各 CLI の既存経路／`.amazonaws.com` は**既に allowlist にある** |
| **メンバーの鍵** | Gemini・OpenAI Images・Stability・FLUX・Ideogram・Recraft・Replicate | メンバーが鍵を貼る（`secrets.Opencode` と同じ流儀） | ＋ Connections のカード 1 枚 | **allowlist 追加が必要** |
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
ものではない）。🔴 **訂正（2026-09-07）: 「driver に仲介されて消える」は経路の性質であって、
エージェント的な形そのものの性質ではない。** agy 経路の `aspect_ratio` は実際にツールまで
届く（16:9 → 1376×768、2 回とも実測）。そこで `Request`/`Caps` に 4 本目の軸を足し、MCP
ツールは **provider 自身がその一覧を持つときだけ**この引数を出すようにした。規則自体は
そのまま——厳密な寸法は誰も約束せず、ずれは報告する——だが「警告経路が恒久的な姿」は、
CLI を駆動する経路すべてではなく Codex 経路に固有の話になった。詳細は P2 の実装メモ。
**コアが黙って縮小
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
- **どの kind にカードを出すかは、手間ではなく証拠で決める。** 下の綴りはすべて 2026-09-06 に
  このコンテナの実データから読んだもの——各 CLI 自身のストア、または稼働中セッションの
  ツール一覧——で、証拠が無い kind は当て推量で足さずに外してある。

  | kind | 転写に記録される MCP ツール名 | ツールの結果 | カード |
  |---|---|---|---|
  | claude | `mcp__af_40ed9852__af_report`（稼働中セッションのツール一覧） | tool_result を対応付け＋カーソル保留 | **出す** |
  | opencode | `af_786de7cb_af_report`（セッションストア） | 同じ part に載る | **出す** |
  | copilot | `probe-structured_probe`——`<server>-<tool>`、ハイフン（`~/.copilot/session-state`） | `tool.execution_complete` を toolCallId で対応付け | **出す** |
  | kiro | 不明 | 不明 | 出さない |
  | cursor | — | **JSONL に一度も現れない**（出力は store.db にしか無い） | 出さない |
  | agy | — | ツール欄はステップの**種別** enum（`RUN_COMMAND` / `VIEW_FILE`）でツール名ではない | 出さない |

  kiro だけは「塞がっている」のではなく「まだ測っていない」。v2 の JSONL ストアは結果を
  呼び出しに対応付ける仕組みを既に持っているのでカードは安いのだが、このコンテナの v2 ストア
  では MCP ツールが一度も呼ばれておらず（唯一の tool_use が `"name":"shell"`）、名前の綴りも
  結果の形も読めない。**クラシック**ストアには 1 件ある（`"name":"structured_probe"`——裸で、
  `orig_name` も同じ。結果は `{"Json":{"content":[{"type":"text","text":…}]}}` の形）。示唆的
  ではあるが、まさにそれが足りない理由でもある: パーサが読むのは v2 であってそちらではない。
  ダミー MCP サーバに対して kiro を 1 ターン走らせれば決着する。

  cursor と agy は測るだけでは済まない。cursor は store.db を読む必要があり、agy はそもそも
  ツール名を記録していない。

- 裸の形をマッチャが受け付けるのは、kiro のクラシックストアが「名前空間を付けないクライアント
  が実在する」ことを示しているから。実害はまず無い（誤検知するには、別サーバが同名ツールを
  持ち、かつ af とまったく同じ結果の形を返す必要がある）が、名前空間を付けるクライアントには
  不要な緩みではある。

- カードは**ターンの最後にまとめて差し込む**。パーサが歩いている最中には挿入しない。copilot も
  kiro も、結果を呼び出しに貼るために parts のインデックスを保持している
  （`toolCallId` / `toolUseId` → 位置）ので、途中で挿入すると「あとから来るツールの出力を
  書き込む先の行」がずれ、その出力が画像の上に載る。

### 通しで動かした（2026-09-06）

`imagegen_live_test.go`（ビルドタグ `clicontract`・`AF_IMAGEGEN_LIVE=1` で門番。実際の
プラン枠を使うため）が、全経路をサンドボックスで走らせる: 実物の `workspace-agent mcp-stdio
--self-report --image-gen` 子プロセスがパイプ越しに MCP を話し、その後ろに実物のルート表
（`buildMux()` を `httptest` で。main の起動処理は 1 行も走らないので稼働中の Agent に
触れない）、実物の Codex CLI、そして最後に実物の絵。HOME・CODEX_HOME・使用量台帳・Claude の
設定ディレクトリはすべて一時ツリーへ向け、実ホームから借りるのは Codex ログインへの
シンボリックリンク 1 本だけ（読むだけで、そこへ書きはしない）。

1 回走らせて分かったこと。

- claude セッションに対する `/imagegen/status`: `enabled ready provider=codex ops=[generate edit]`。
- `tools/list` は `generate_image` を出した。同じ一覧を **codex** セッションで取ると出ず、
  しかし `af_report` は入っている——この対照があって初めて「出ない」に意味がある。
- 呼び出しは **47 秒**かかり、**進捗通知が 4 本**出た。つまりハートビートは生成のあいだ
  10 秒間隔で本当に刻んでいる。opencode の上限回避が乗っている仕組みそのもので、この実走まで
  は自前サーバの中で理屈として確かめただけだった。
- 結果はパスを持ち、その先のファイルはデコードできる **1254×1254 の PNG**（要求は
  1024×1024）で、warnings に `size=1024x1024 requested, 1254x1254 produced` が入っていた。
  同じ日の provider 単体の実走では 1536×1024 が返っていることに注意——**外れ方は固定値では
  なく予測できない**。決定 7 が「約束せず報告する」形にしてある理由がこれである。

これでも覆えていない（そして今のところ何も覆っていない）のは、ブラウザで実際に描かれた
カードの見た目である。サーバ側が part を出しファイルを配れるところまでは検証済みで、描画は
既存の `UserFileBlock` とそのテストに乗っている。

### 消費がどこに出るか、そして実際に何が測れるか（2026-09-07）

生成が使うのは **ChatGPT のプランであって Claude のものではない**（Anthropic への呼び出しは
1 回も無い）。面は 3 つあり、食い違って見えるのには理由がある。

- **Claude の使用量チップ**は生成そのものでは動かない。ただしツールを**呼ぶ** claude のターンは
  普通に Claude トークンを使う。ツールが画像バイト列ではなくパスを返す理由はここにもある。
- **台帳**には `feature=tool.imagegen` の行が出るが `kind=codex`——「誰が頼んだか」ではなく
  「何が実際に走ったか」（ADR 0029 §1）。頼んだセッションは `ref` に載るので、「この claude
  セッションはいくら使ったか」は `kind` ではなく `ref` の問いである。claude で集計してしまうと、
  ChatGPT の消費を Claude の行に付けることになる。
- **codex の使用量チップ**はまったく動かなかった。これは欠陥だった。チップは最新の rollout
  JSONL から `rate_limits` を読むが、生成は `--ephemeral` で走るので rollout を書かない
  （実測: 実際の生成のあと `~/.codex/sessions` に新しいファイルは 1 つも増えていない）。
  つまり枠が本当に減っている間、チップは最後の対話セッションの数字を映したままになる。

直し方は 2 つの実測で決まった。

1. **`codex exec --json` は枠の情報を運ばない。** 1 回の生成のストリーム全体が
   `thread.started` / `turn.started` / `item.completed` ×2 / `turn.completed` で、
   `turn.completed` が持つのはトークンだけ（`input_tokens` … `reasoning_output_tokens`）。
   `rate_limits` も `used_percent` も無い。**チップはストリームからは直せない。**
2. **アカウント自身のビューを直接読める。** `GET
   https://chatgpt.com/backend-api/wham/usage` は、fleet がリセットクレジットで既に使っている
   ログインで、`rate_limit.primary_window.used_percent` /
   `secondary_window.used_percent`（ほかに `plan_type`・クレジット残高・リセットクレジット）を
   返す。rollout を必要としないので、チップの**第 3 の情報源**に加えた。ローカルの読みより
   新しいときだけ採り、呼び出しが失敗したときは何も変えない。

### 順序と、次への送り（2026-09-07）

「codex が応えられないとき何が起きるか」には半分が 2 つあり、最初の実装は片方しか扱って
いなかった。**未ログイン**は呼ぶ前に答えが出る Ready の問いで、auto は既に飛ばせていた。
**枠切れ**は呼んで初めて分かるもので、provider を 1 つだけ選ぶ形では行き先が無い。両方を塞いだ。

- `chooseImageProvider` は `chooseImageProviders` になり、最初の 1 つではなく「使える provider
  を順に並べたリスト」を返す。`Run` はそれを歩き、最初に成功したところで返す。明示指定は今も
  必ず 1 つに解決し、次へは送らない——呼び手はサービスを名指ししたのであって、別のアカウントに
  黙って請求するのはこの規則が防いでいる失敗そのものである。
- **試行ごとに台帳の行が出る。** 次へ送ると正直な行が 2 本になる（失敗した方も駆動トークンを
  使っている）。1 本にまとめると無駄になった試行が消える。
- **Ready が枠切れを含むようになった。** Codex provider は `codex.PlanExhausted` を見る（上で
  足したアカウントのビューを読む）。数えるのはアカウント自身の `limit_reached` だけで、
  「分からなかった」は codex を ready のままにして自分で答えさせる——口が一時的に不通なだけで
  機能が止まることはない。
- **順序は利用者の設定**（ui-prefs `imageProviderOrder`）。main の `agentOrderPref` と同じく
  **全順序に正規化**する: 不明な id と重複は落とし、言及されなかった provider は既定の順で
  後ろに足す。provider が増える前に書かれたリストでもその新顔をちゃんと順位づけできる——
  さもないと、利用者がたまたま設定を保存し直すまで新しい provider に到達できない。
  `/imagegen/status` は実効順序を返すので、「なぜそこへ流れたか」がファイルを当て推量しなくても
  読める。

**既定の順序が何であるべきか**は、2 つ以上になったときに決めることとして意図的に決めていない:
自前エンジンは 1 枚あたりの費用がゼロだが GPU の起動を待ち、Codex 経路は速いが利用者のプランを
使う。Console の設定 UI をまだ出さないのも同じ理由で、1 つしかないリストの並べ替えは設定ではない。

ついでに片付いたこと: **provider を足す手段として別のエージェント CLI を使うのは筋が悪い。**
候補は agy（Gemini の画像モデル）だったが、ここでは 2 つ障害があった。ADR 0008 の訂正にある
RDRAND マスクを当てるまでそもそも起動せず、当てた今も「サインインしてください」と答えるので、
画像能力については何ひとつ測れていない。ただし設計上の反対はそれとは別に成り立つ: 別の
エージェント CLI を駆動するのは、Codex 経路が既に抱えている妥協——driver がパラメータを仲介
する・文章はファイルの根拠にならない・ディレクトリ差分で回収する——を CLI ごとに繰り返すこと
になる。Gemini を第 2 層の provider として画像 API に直接あてれば、そのどれも要らない。

🔴 **訂正（2026-09-07）: 障害は消えており、設計上の反対も半分は間違っていた。** 同日に
このホストで agy のログインが通り（`test(agy)` の手動 OAuth ハーネス）、画像能力が測れる
ようになった——そして測った結果、上の主張の中心がひっくり返った。妥協は**全部が繰り返される
わけではない**: **依頼した縦横比は本当にツールまで届く**。これは Codex 経路には決してできず、
決定 7 の警告経路を「恒久」と呼ばせた当のものである。繰り返されるのは残り——間に driver が
いる・文章はファイルの根拠にならない・出力ディレクトリを読む——であり、生き残る主張はここに
書いたものより狭い: *エージェント CLI は API 直叩きより劣る provider であり、何も無いよりは
良い provider である。* Gemini の鍵を持たない claude / opencode のセッションにとって、
実際に机の上にあった選択肢は「何も無い」だった。agy 経路は下に実装した。メンバーが鍵を持つなら、
Gemini API を直接叩く第 2 層の provider が依然として正解である。

**「画像 1 枚がいくらか」を測れるかは別の問いで、答えは「測れない」。** 1 回の生成の直前直後に
`wham/usage` を読んだが**何も動かなかった**: 5 時間窓は 0%、週次は 41% のまま、変わったのは
両窓のカウントダウンの時計だけ。この口の `used_percent` は整数なので、画像 1 枚はこの口が
出すどのカウンタの分解能も下回る（クレジット残高も、残メッセージ数の概算も同じ）。「1 枚
いくら」の数字を出すには連続で何枚も生成して 5 時間窓が動くのを見るしかなく、それは測ろうと
しているものをそのまま消費する。よって決定 9 のままでよい: 枚数と画素を数え、駆動側の
トークンを記録し、プランの消費は**数字を作らずに未計測と明記する**。

## 実装メモ（P2——agy 経路・2 本目の provider・2026-09-07）

第 1 層の 2 本目（決定 3）。`agy` はコンテナが既に持っている Antigravity の OAuth トークンで
動くので、新しい秘密も egress の追加も要らない——そして **codex とは別のプランを消費する**。
codex の代わりではなく隣に置く価値は、まさにそこにある。

以下はすべて 2026-09-07 にこのコンテナで実測した（agy 1.1.5・サインイン済み・driver は
`gemini-3.8-flash-low`）。画像は provider 単体の測定に 1 枚、通し実行に 1 枚を予算にし、
どちらも報告する。

**内蔵ツールの実物。** `generate_image` は `Prompt`（必須）・`ImageName`（必須。小文字＋
アンダースコア・3 語まで）・`AspectRatio`（既定 `1:1` / `2:3` / `3:2` / `3:4` / `4:3` /
`9:16` / `16:9`）・`ImagePaths`（絶対パス 3 枚まで。「編集・合成・参照用」）。**寸法・品質・
背景のパラメータは 1 つも無い。** 絵を作るのは `gemini-3.1-flash-image` だが、これは agy 自身の
step ストアから読んだ値で、本パッケージが解析する経路には流れてこないため provenance として
report しない。

**肝心の発見: 縦横比は driver に消されない。** 16:9 を頼むと **1376×768** が返った（比 1.792
対 1.778——0.8% 横広）。provider 単体でも、MCP 経路を通した通し実行でも同じ。この ADR の中で
呼び出し側の要求が driver を生きて通り抜けたのは、これが初めてである。決定 7 の撤回ではなく
修正であることに注意:

- **厳密な寸法は依然としてどこでも選べない。** 1376×768 は 16:9 ではないし、画素を要求する
  パラメータが存在しない。よって provider は**比を 5% の許容で**比較し、要求が丸ごと無視された
  ときだけ警告する。0.8% のずれを警告にすると、「何度やっても同じ生成」を呼び出し側に
  やり直させることになり、やり直すたびにプランが減る。
- この経路の `size` と `background` は、黙って捨てるのではなく `warnings` に「この経路には
  無い」と入れる。
- `aspect_ratio` は **`Size` の別表記ではなく `Request`/`Caps` の新しい軸**（決定 5）。MCP
  ツールがこの引数を出すのは **provider 自身がその一覧を持つときだけ**で、これが「回しても
  何も動かないつまみ」が 2 つ目になるのを止めている。

**測定上の罠を明記しておく: `--output-format stream-json` はツールのパラメータを欠落させて
echo する。** 呼び出しの step イベントには `ImageName` と `Prompt` しか出ず `AspectRatio` が
無い。つまり最初の読みは「driver が落とした」——Codex とまったく同じ筋書き——だった。実際の
呼び出しは会話ストア（`~/.gemini/antigravity-cli/conversations/<id>.db`）にあり、
`{"AspectRatio":"16:9","ImageName":…,"Prompt":…}` である。**stream は進捗の通知であって
監査ログではない。** これだけを根拠に能力を結論づけていたら間違えていた。

**起動の形**（`internal/imagegen/agy.go`）と、各要素が答えている失敗:

1. **呼び出しごとに隔離した `$HOME`。共有するのは OAuth トークンへの symlink だけ。** agy は
   設定一式を `$HOME` から解決し、`--ignore-user-config` に当たるものが無い。MCP 設定は
   グローバル専用である。実 HOME で走らせると、絵 1 枚のために利用者の materialize 済み MCP
   一式が起動し、しかもこのターンに自前の `generate_image` を渡すことになる。`chatAgyHome`
   （chat_providers.go）と同じ手であり、後片付けも兼ねる: 会話ストアも presence ロックも回収元
   のファイルもディレクトリごと消えるので、`$CODEX_HOME/generated_images` が 80 MB に育った
   ような溜まり方をしない。トークンが更新されていた場合は先に実体へ畳み戻す（agy は
   tmp+rename で回すので、symlink が実ファイルに置き換わる）。
2. **`--dangerously-skip-permissions` を渡さず、`permissions.allow` はツール 1 つだけ。**
   print モードは尋ねられないので、allow に無いものは自動拒否になる——`run_command` を
   わざと呼ばせて実測: `denied_actions:[{action:"command"}]`、実行は `CANCELED` で終わる。
   これが codex の `-s read-only` に当たるものであり、「モデルはダミー PNG をスクリプトで
   作れなかった」を願望ではなく事実にしている。通し実行で肯定側も確認できた: `generate_image`
   自身は allow を通り、追加の許可を必要としない。
3. **プロンプトは `--input-format stream-json` の 1 メッセージとして stdin から。** `--print`
   はプロンプトを**フラグの値**として取る＝argv に載る＝共有ホストのプロセス一覧に全部出る。
   封筒は `{"event":"user","message":{"content":…}}`（形はどちらも無料で当てた——壊れた
   メッセージはモデル呼び出しの前に弾かれる）。
4. **回収は `brain/<conversation_id>/` の直下のみ。文章からは拾わない。** agy 自身のツール
   結果は `Generated image is saved at %s.` とパスを書いてくるが、それこそ Codex 経路が
   依存を拒んだ契約である。会話のディレクトリには `.system_generated/`・`.user_uploaded/`・
   `scratch/` も同居しており、どれも呼び出し側が欲しい絵ではない。事前スナップショットとの
   差分は取らない——真新しい home に過去の実行のファイルは入り得ないからで、Codex 経路で
   スナップショットが与えていた「自分のものだけ」を、ここでは home 自体が与えている。
5. **RDRAND マスクは子プロセスの環境にだけ**（`OPENSSL_ia32cap=~0x4000000000000000`・ADR 0008
   の訂正）。RDRAND の無いホストで agy を**種別として**出すべきかは別セッションで検討中の
   別問題であり、`internal/hostcaps` とセッション側の guard は意図的に触っていない。この
   provider がその判断を偶然先取りしないためである。

   🔴 **訂正（2026-09-07）: その別問題は決着した**（[0008](0008-antigravity-cli-agent-kind.ja.md)
   の「RDRAND 非提示ホストへの正式対応」）。よってこの経路もマスクを自前で綴らず、他の全
   spawn と同じ `internal/agents/agy` の seam から受け取る。実利は 2 つある。**(a)** マスクを
   拒否するデプロイ（`AF_AGY_RDRAND_MASK=0`）ではこの経路も当てなくなる——自前の定数では
   拒否が貫通しなかった。**(b)** RDRAND が生きているホストには当たらなくなる——自前の定数は
   無条件に当てており、健全なホストからハードウェア RNG を取り上げていた（害はないが、
   0008 の「動いているデプロイの挙動は変えない」に反する）。

**使用量。** `result` イベントに `input_tokens` / `output_tokens` / `thinking_tokens` /
`cache_read_tokens` / `total_tokens` が載る。関係は仮定せず実測した: `input_tokens` は
キャッシュ分を**含まない**（26896 + 75 = total 26971 で、cache_read 16289 はその外——codex の
rollout の慣習とは逆）。`output_tokens` は `thinking_tokens` を**含む**（output 421 のうち
thinking 418、total = input + output）。足すと推論分を二重に数えることになる。台帳の行は
`feature=tool.imagegen`・`kind=agy`・`measured=partial`——codex の行と同じ形で、理由も同じ:
agy の残枠は TUI を掻き取るしか読めないので、1 枚が Antigravity プランをどれだけ食うかは
Codex 経路と同様に未計測である。

**Ready は `exec.LookPath` とトークンファイルだけ。** codex の `PlanExhausted` に当たる枠切れ
判定は入れていない——agy の残枠は数秒かかる TUI スクレイプでしか取れず、tools/list の経路では
不可能だからである。枠切れの agy は自分で答え、`Run` の次送りが次の provider へ回す。

**`MaxCount` は 1、`MaxInputs` は 3。** 1 回の呼び出しで 1 枚（実測）。それ以上は 2 回目の
呼び出し＝2 単位目のプラン消費なので、正直な数は 1 であり、足りない分はコアが警告にする。
`edit` はツール自身の `ImagePaths` スキーマを根拠に申告している——実測ではなく契約であり、
その旨をコードにも書いた。

### provider が 2 本になって開いたこと

- **組み込みの既定順は `codex, agy`。理由は優劣ではない。** 要求を honour する度合いでは agy が
  上だが、codex が先なのは先に出荷されたからである。既定を入れ替えると、既に使えている利用者の
  画像生成が黙って別のアカウント・別のプランの枠に移る。改善がひとりでにやってはいけないのは
  まさにそれである。

  🔴 **同日に訂正（下の P3）: 既定順は `agy, codex` である。** 上の反対そのものは正しく、
  前提だけが違った——**まだ誰も使っていない**ので、移される「既に使えている設定」が存在しない。
  居ない利用者を守るために選んだ既定は、これから来る利用者から良い経路を取り上げるだけである。
  まだ無料でできるうちに、中身で決める。
- **Console の設定はここで出した**（設定 > エージェント > セッション、ON/OFF の直下、ON の
  ときだけ）。1 つのリストの並べ替えは設定ではなかったが、**別々のプランを消費する 2 つの
  アカウント**の順序は設定である。アシスタントの優先順位と同じ `OrderList` を使い、
  `normalizeImageProviderOrder` が `effectiveOrder()` の規則をフロント側で繰り返すので、
  利用者がドラッグしたリストと "auto" が歩くリストは一致する。
- **tools/list の除外規則を一般化した。** 「codex 経路の codex セッションには出さない」から
  「**その経路が駆動するのがセッション自身の CLI なら出さない**」へ。agy が特例なしで入る。
- 利用者向けの文言から Codex 前提を外した。`agents.note_image_generation`（ja/en）・
  `guide/member/02-sessions`（ja/en）・`workspace/workspace-notes.md` は、2 つの provider を
  名指しし、どちらがどのプランを消費するかを書き、縦横比だけは例外だと明記している。

### 通しで動かした（2026-09-07）

`TestImagegenLiveAgyEndToEnd`（ビルドタグ `clicontract`・`AF_IMAGEGEN_LIVE=1`）は、codex の
live テストと同じサンドボックスで全経路を走らせる。ui-prefs に
`imageProviderOrder: ["agy","codex"]` を書くのが唯一の舵で、MCP ツールには意図的に provider
引数が無いためである。1 回実行:

- `/imagegen/status`: `ready provider=agy model=gemini-3.8-flash-low ops=[generate edit]
  aspectRatios=[1:1 2:3 3:2 3:4 4:3 9:16 16:9] order=[agy codex]`——保存された設定が、組み込み
  順では 2 番目の provider を本当に繰り上げている。
- `tools/list` は `generate_image` を **`aspect_ratio` 付きのスキーマで**出した。モデルから
  見た 2 経路の違いは、これが目に見える形になったものである。
- 呼び出しは **23 秒**・進捗通知 2 回。`aspect_ratio: "16:9"` に対して **warnings 無しの
  1376×768 JPEG** がデコードできる形で返った——要求が MCP 層を通して honour された。
- 使い捨て home は 1 つも残らなかった。

意図的に測っていないもの: 実物の `ImagePaths` を伴う `edit`（スキーマが既に述べていることを
知るのに画像 1 枚かかる）、`count > 1`、そして 1 枚が Antigravity プランをどれだけ食うか
（ChatGPT 側とまったく同じ測定不能性であり、答えも同じ——未計測のままにする）。

## 実装メモ（P3——provider の名指しと既定順・2026-09-07）

同じ問い——**経路が 2 本になった今、誰が選ぶのか**——から出た 2 つの変更。

### 既定順を `agy, codex` にした

P2 で codex を先にしたのは、既に使えている利用者の生成を別のプランへ移さないためだった。
反対そのものは正しく、前提だけが違った: **まだ誰も使っていない**ので、守るべき「動いている
設定」が存在しない。居ない利用者のために選んだ既定は、これから来る利用者から良い経路を
取り上げるだけである。縦横比を honour するのは agy だけで codex は何も honour しないので、
中身で決めれば agy が先——まだ無料で決められるうちに決めた。保存済みの `imageProviderOrder`
はどちらの場合も組み込み順より優先されるので、既に好みを表明した人はそのままである。

### `generate_image` に `provider` 引数を足した

バックエンドは最初から対応していた: `chooseImageProviders` は明示指定を「ちょうど 1 本・
次送りなし」で honour し、`/imagegen/generate` は最初から `provider` を取る。無かったのは
ツールの面で、それは意図的だった——「サービスを名指しするのは呼び出し側の仕事であって
モデルの仕事ではない」。これをひっくり返したのが**比較**である。「同じプロンプトで両方作って
違いを見せて」はセッションの中で利用者が実際に頼むことなのに、セッション側に言う手段が無かった。

- **enum はこのセッションが実際に名指しできる provider**。tools/list の時点で、
  `/imagegen/status` が新たに返す provider ごとの一覧から組み立てる。1 本しかないときは
  引数ごと消えるので、飾りとして現れることはない。
- **決定 8 の除外は「有効な経路」単位から provider 単位になった。** 「codex 経路の codex
  セッションには出さない」から「**セッション自身の CLI を駆動する経路は、そのセッションには
  差し出さない**」へ。agy が使える codex セッションは、選択肢が agy だけのツールを受け取る——
  旧規則は丸ごと拒否していた——そして何も残らないときにだけツールが消える。同じ検査は REST
  側にも置いた（`imagegen_own_cli`）。広告した集合が権限の境界であり、`tools/call` に書かれた
  当てずっぽうの名前がそれを越えてはいけないからである。
- **`op` と `aspect_ratio` は差し出した provider の和集合になった。** provider ごとに別々の
  スキーマは 1 つのツールでは表現できず、和集合は嘘をつかない: 名指しされた provider が
  できない op は名指しで拒否され、honour できない縦横比は既に warnings に入る。これは初版が
  静かに間違えていた点も直す——auto は「2 番目の provider だけができる op」を実際にその
  provider へ回していたのに、ツールは 1 番目の op しか広告しておらず、その op に到達できなかった。
- **説明文にコストを書いた。** provider を名指しすると 1 つのプランに固定され、2 つ比較すれば
  別々のアカウントで 1 枚ずつ減る。だから「利用者が名指ししたときだけ指定する」と書いてある。

検証: mcpx の suite が provider 単位の除外（agy が使えるとき codex セッションはツールを保ち、
使えないとき失う）・和集合・enum の中身・「実在するときだけ出す」2 つの規則を押さえ、
`internal/imagegen` が status の一覧と REST の拒否を押さえる。live suite は agy の生成を
`provider: "agy"` の明示指定で走らせ、codex セッションのテストは両側を走らせる——agy があれば
ツールは残り、無ければ消える（`af_report` を陽性対照として）。

## 追記 — `seed` を語彙に足した（2026-09-11）

この ADR の語彙は provider 中立で、`seed` は意図的に無かった——「絵の見た目を決めるつまみ」は
provider ごとに違いすぎて共通語彙にならない、という判断である。それでも 1 つだけ足す。理由は
見た目ではなく**比較可能性**にある。

ADR 0072 のフェーズ P3 の完了の定義は「**同じ prompt・同じ seed**で LoRA の有無が絵を変える」
であり、実機検証（0072 の 2026-09-11 の補遺）はそこで詰まった。provider は要求ごとに乱数で
seed を選ぶので、2 枚の絵が違ったとき「LoRA が効いた」のか「seed が違った」のかを分けられない。
**1 つだけ変えた 2 回の要求を比べる**という、検証の最小単位が成立しない。これは見た目の好みでは
なく、この ADR が決定 7 で掲げている「実際に何が起きたかを言う」の前提条件である。

**形。** `Request.Seed *int64`、`Caps.Seed bool`。ポインタなのは **0 が正当な seed だから**で、
「0 は未指定」と読む実装は、最も明示的にふるまった呼び出し側にだけ乱数を返すことになる。
ツールの引数の上限は sampler の範囲ではなく **JavaScript の安全整数 (2^53-1)** にした——この数は
JSON で運ばれ、double に落とす client では 2^53 を超えると**送った値と違う seed が返ってくる**。
seed が唯一提供する性質が黙って壊れる。

**どの経路が受け取るか。** comfy だけ。グラフを組み立てているのがこのパッケージ自身なので、
seed は「vendor の API が公開している欄」ではなく「こちらが書く入力」である。
- **sdcpp は受け取れない。** sd-server の OpenAI 互換 `/v1/images/generations` は本文から
  `prompt` / `n` / `size` しか読まない（上流 `examples/server/routes_openai.cpp` で確認）。
  seed を運べる唯一の経路は prompt 本文に埋める `<sd_cpp_extra_args>` で、それは
  **ADR 0072 決定 5 が「利用者の prompt にあれば拒否する」と決めた穴**である（`lora.path` という
  サーバ側ファイルパスも通るため）。こちらから書き込めば、決定が塞いだ注入面を自分で作ることに
  なる。だから渡さず、**落としたことを warnings で言う**。
- agy / codex も同じく受け取れず、同じ warning になる。

**黙って落とさない**のは 3 つの引数（aspect_ratio・loras・seed）で共通の規則だが、seed の
取りこぼしが一番見えにくい: 絵は正常に出るし、呼び出し側が気づくのは**2 回目**——seed を
固定した理由そのもの——のときである。

### キャッシュの警告は「当たったときだけ」出す

ComfyUI はノードの出力を入力でキャッシュする（0072 実測）。同じ seed・同じ prompt・同じ
グラフの 2 回目は、生成せずに 0.5 秒で同じ絵を返す。これを毎回警告するかを考え、**当たった
ときだけ言う**ことにした。

- seed を固定した呼び出し側にとって「同じ絵が返る」は**望んだ結果**であり、それを毎回
  警告するのは正しい経路に対する雑音になる。ツールの説明文と同じく、warnings も
  全セッションが払う固定費に近い。
- 一方で紛らわしい形が 1 つある: **グラフが運ばない何かを変えて呼び直した**ときである。
  この ADR の語彙には negative prompt も steps も cfg もないので、それらを「変えたつもり」の
  呼び出しは前回と同一のグラフになり、0.5 秒で同じ絵が返る。**エンジンが無視した**ように
  見えるが、実際には変更がこの経路に届いていない。
- 判定は**推測ではなく事実**にできた。ComfyUI は `execution_cached` という status メッセージで
  **飛ばしたノード id を自分で列挙**しており（v0.34.0 の `execution.py`）、それは `/history` の
  `status.messages`——このパッケージが既にエラー表示のために読んでいる欄——に載る。SaveImage の
  ノードがその一覧にあれば、絵は 1 枚も作られていない。経過時間が短いことを根拠にする
  ヒューリスティクスは要らなかった。
- 部分的な再利用（チェックポイントのロードやテキストエンコードだけがキャッシュに当たる）は
  **言わない**。絵は作られているので、伝えることが無い。

実機ではまだ押していない。同じ prompt・同じ seed の 2 枚が LoRA の有無だけで変わることの確認は、
0072 の edit / inpaint の 5 族とまとめて次の配備の回（H3）に当てる。
