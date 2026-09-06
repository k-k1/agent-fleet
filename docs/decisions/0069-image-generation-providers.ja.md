# 0069. 画像生成をフリートの道具にする——provider 抽象は 1 つ、層は「鍵の持ち主」で切る

[English](0069-image-generation-providers.md) | 日本語

- 状態: **提案（設計のみ・未実装）**（2026-09-06）。以下の数値はすべて同日に Workspace
  コンテナで実測したもの、または同日に各社の公式資料から取得したもの（出典は末尾）。
  **P0 に着手する前にレビューを受けるための文書**であり、形が変わりうるのは末尾の
  「未解決の 2 点」だけである。
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
| **既存接続を流用** | Codex（`image_gen`）、**Bedrock**（Nova Canvas・Stability 一式） | 利用者の ChatGPT ログイン／秘密を保存しない `AWSConn`（資格情報チェーン） | provider ファイルのみ | Codex の既存経路／`.amazonaws.com` は**既に allowlist にある** |
| **会員の鍵** | Gemini・OpenAI Images・Stability・FLUX・Ideogram・Recraft・Replicate | 会員が鍵を貼る（`secrets.Opencode` と同じ流儀） | ＋ Connections のカード 1 枚 | **allowlist 追加が必要** |
| **テナントの鍵** | Vertex AI・Azure OpenAI | 管理者が一度設定 | ＋ CP 側 provider（`CPBridge` 経由） | **不要**——CP の通信は制限の外（ADR 0047・`tts.go`） |

したがって **2 つ目の provider は Bedrock にする**。新しい秘密が要らず、ホストは既に
allowlist にあり、決定 5 を早期に実物で検証させる編集系の操作が一気に入る。

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
`Result.Warnings` で返す（実測: どちらのサイズ要求でも 1254×1254）。**コアが黙って縮小
することはしない**——サムネイル用の整数倍ボックス縮小は既にあるが 1254→1024 は整数倍では
なく、厳密サイズのためにリサンプラを足すのは、provider が約束していない品質の保証と実在の
依存関係を引き換えにすることになる。厳密サイズはそれを本当に持つ provider とともに来る。

**8. 道具は `af` ビルトイン MCP サーバで配り、opt-in・既定 OFF。** ゲートは peer messaging
（ADR 0041）と完全に同型: ui-prefs のキー → mcpreg が読むフック → `builtinRunArgsFor` が
`--image-gen` を付与 → 変更時に `MaterializeAll()` → 次に起動したセッションから有効。
既定 OFF の理由は、**codex ではないセッションから、利用者の ChatGPT プラン枠を目に見えない
まま消費する**からである。決定 9 が入った時点で既定を見直す。

⚠️ ここだけ既存構造の改修が要る。`builtinRunArgsFor` は kind を受け取らないので、
「codex セッションにはこの道具を配らない」が今の形では書けない。`kind` を通すこと。条件は
「codex には常に配らない」ではなく「**実効 provider が codex のときだけ配らない**」——
経路が Gemini や Bedrock になれば、codex セッションもフリート側の道具を欲しがる。

**9. 使用量は記録する。測れないものは測れないままにする。** 新しい feature タグ
`tool.imagegen` を、ADR 0029 §2 が凍結した列挙に足す（あちらに追記の注記を入れる）。Codex
経路の `turn.completed` の usage は、チャットの一発実行が既にやっているのと同じ要領で記録
する。ただし**画像が消費するプラン枠はトークンでは表現できず**、3〜5 倍という値は文書で
あってテレメトリではない。枚数とピクセルを記録し、**この経路ではプラン消費が測れないと
明記する。0 で埋めない。**

**10. Codex の非公開バックエンドを自前で叩くことはしない。** 先行 CLI はそれをやっており、
厳密な `--size` / `--quality` / `--background` が手に入るのもそれだが、README 自身が壊れうると
警告している非公開契約であり、そこに製品機能を固定することになる。厳密な制御が要る場面の
ためにこそ、第 2・第 3 層の provider がある。

**11. 由来は結果の一部。** `Result` は provider・モデル・リージョン・概算コストを持ち、
プロンプトの送り先が監査できるようにする。画像が 1 枚生成されたということは、プロンプトが
名前のあるサービスへコンテナの外に出たということである。provider 単位の管理者許可が要る
テナントには `tts_engine` の前例をそのまま当てる。

## 未解決の 2 点——P0 を書く前に潰す

1. **クライアント側の MCP ツール呼び出しタイムアウト。** 実測の Codex 経路で 1 枚 28 秒、
   鍵経路は高品質・高解像度で最大 235 秒という報告がある。claude のバイナリからは
   `MCP_TOOL_TIMEOUT` 相当の文字列を見つけられず、上限が不明である。**まず実セッションで
   測ること。** 同期呼び出しが保たないなら、P0 は `generate_image` → `{job_id}` ＋ポーリング
   の形になる。Replicate や FLUX がもともと投げて待つ型である以上、**非同期こそ正解である
   可能性が高い**。いずれにせよ `Generate` は最初から `context.Context` を取る。
2. **そもそも `codex exec` 経由でサイズ・品質・背景を効かせられるのか。** バックエンドは
   受け付ける（先行事例）が、driver モデルは渡さなかった（実測）。`$imagegen` トリガと
   `size=1024x1024` の明示を入れたテンプレで 1 回試せば、決定 7 の警告経路が Codex 経路に
   とって恒久的な姿なのか一時的な姿なのかが決まる。

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
