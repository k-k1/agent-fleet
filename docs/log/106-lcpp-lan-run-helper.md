# 106. モデル目録から「LAN の別ホストで動かす」ための支援——設計(実装なし)

- 状態: **検討のみ。コードは 1 行も変えていない。** 親セッション `sap4wtk`(駆動役)が単独で調査した。
  子セッションは起こしていない。実装着手は利用者の判断を待つ。
- 依頼(利用者の言葉): 「モデルカタログで検索したあと、ローカルネットワークの別ホストで DL して実行する
  ための補助(たとえば Linux のコマンドラインや Windows での実行方法など)するためのツールは作れないか。
  適したアプリがあればそれでもよい。」
- 関連: [0093](../decisions/0093-lcpp-agent-kind.ja.md)(`lcpp` kind——**この文書の契約はここから来る**)/
  [0072](../decisions/0072-engine-model-catalog.ja.md)(モデル目録)/
  [0085](../decisions/0085-model-ledger-and-one-press-ingest.ja.md)(バケツが台帳・取り込みは 1 押し)/
  [0076](../decisions/0076-external-image-engine-on-lan.ja.md)(LAN の外部エンジン——**運用の前例**)/
  [105](105-lcpp-console-toggle-and-lan-endpoint.md)(LAN の llama.cpp へ差し替える設計。**本稿の出口**)
- 読んだ版: `6e2cc688`(2026-09-21 の develop)。上流は llama.cpp の `master` と、この配備が実際に
  動かしていた `b10830`(ADR 0093 §12.3 の実測値)の両方を突き合わせた。

---

## 1. 結論の先出し

1. **既存アプリはどれも、うちの契約を満たさない。** Ollama・LM Studio・Jan・llama-swap・Docker Model
   Runner の 5 本を上流の実コード/実ドキュメントで確かめた。**llama.cpp を内蔵している製品(Jan・
   Docker Model Runner)でも落ちる**——理由は共通で、うちが依存しているのが OpenAI 互換の
   `/v1/chat/completions` **ではなく** llama-server 自身の root 経路だからである(§2)。
   **支援の対象は素の `llama-server` に絞るのが筋**、という依頼の見立ては正しかった。
2. **「DL して実行」は実際に 1 コマンドになる。** `-hf <user>/<repo>[:quant]` は実在し、quant 既定は
   `Q4_K_M`、gated は `HF_TOKEN`/`-hft` で通る(§3)。**しかも lcpp 側の目録検索は Hugging Face 固定**
   (`adminEngineAdd.tsx:332` の source 切替は `image` のときしか描かれない)なので、**LLM の検索ヒットは
   100% この 1 コマンドに写せる**。
3. **材料は全部すでにリポジトリにある。** 起動引数の正本が 2 か所(`buildEngineActiveSet` と
   `fetch-models.sh` の jq)、窓の当て方が 1 か所(`engineFit.ts`)、上流の綴りが 1 か所
   (`EngineModelFile.Source` = `hf:<repo>/<file>`)。**足りないのは「それを 1 か所で文字列に描く」
   ことだけ**で、新しい知識は要らない(§4)。
4. **引き継ぎの見立てのうち 3 件は上流で覆った。**(a) `--jinja` は `b10830` でも `master` でも
   **既定 ON**——「無いと tool call が壊れる」は成立しない、(b) `chat_template` は**単機モードの
   `/props` には出る**(出ないのは router 固有)、(c) `/v1/models` の `meta` は単機だと `n_ctx_train`
   であって `n_ctx` ではない(§3)。
5. **依頼 2(契約テストを素の llama.cpp へ向ける)は、宛先を変えるだけでは通らない。** URL の組み立ては
   そのまま合うが、**3 つの assertion が router 前提**で書かれている(§7)。直しは小さいが、**「宛先だけ
   差し替えれば回る」ではない**ことは着手前に知っておく必要がある。
6. 見積り **4〜6 セッション日**(段 0〜4・段ごとに独立に出せる)。**ADR は要らない**——ただし論点 1 つ
   (「目録が、この配備が持っていないモデルの実行手順を配る場所になる」)だけは決定として記録する
   価値がある(§9)。

---

## 2. 判定基準は「うちの契約を満たすか」——5 本すべて不合格

### 2.1 契約の 6 軸(出典は実コード)

PR #826 の `workspace/agent/internal/harness/live_contract_test.go`(ビルドタグ `manuallive`)が、
自分のファイル先頭のコメントで 6 軸を名指ししている。**これが判定基準の正本**である。

| # | 軸 | 経路 | 誰が依存しているか |
|---|---|---|---|
| 1 | `GET /props` が箱を起こさず答え、`build_info` を持つ | **root**(`/v1` の下ではない) | ADR 0093 決定 10 の版ピン(段 2 負債 7) |
| 2 | router 行では `/props` が router を describe し、実窓は `GET {base}/v1/models` の `data[].meta.n_ctx` | root + `/v1` | `enginePropsAugmentRouterWindow`(段 0 の迂回) |
| 3 | `POST /v1/chat/completions/input_tokens` が**厳密に `input_tokens`** という欄名で答える | `/v1` 配下だが OpenAI 仕様外 | 決定 7 の窓判定(`client.go` の `InputTokens`) |
| 4 | `POST /v1/chat/completions/control` が `model` と `id` の両方を要求する | 同上 | `ThreadHandle.Interrupt`(決定 4) |
| 5 | `tool_calls` が有効な JSON・既知のツール名・必須引数を満たす | `/v1/chat/completions` | ツールループ全体(決定 5) |
| 6 | stream の最終チャンクが `usage` を運ぶ | `/v1/chat/completions` | 決定 8 の exact 使用量 |

🔴 **軸 1・2・3・4 は OpenAI 互換 API では**ない**。** 「OpenAI 互換サーバなら動く」は、この kind に
関しては誤りである。判定の実務はここに尽きる。

### 2.2 判定表

| 候補 | 軸1 `/props` | 軸2 窓 | 軸3 `input_tokens` | 軸4 `/control` | 軸5 tool_calls | 軸6 stream usage | 判定 |
|---|---|---|---|---|---|---|---|
| **素の `llama-server`** | ✅ | ✅(形は単機/router で違う) | ✅ | ✅ | ✅(族依存) | ⚠️(§7.3) | **合格** |
| Ollama | ❌ | △(`/api/show`) | ❌ | ❌ | ✅ | ? | 不合格 |
| LM Studio | ❌ | ❌ | ❌ | ❌ | ✅ | ? | 不合格 |
| Jan | ❌ | ❌ | ❌ | ❌ | ✅ | ? | 不合格 |
| llama-swap | △(`?model=` 必須) | △ | ❌ | ❌ | ✅ | ✅ | 不合格(§2.4 に例外案) |
| Docker Model Runner | ❌ | ❌ | ❌(Anthropic の `count_tokens` のみ) | ❌ | ✅ | ? | 不合格 |

軸 5・6 を候補ごとに実測していないのは、**軸 1〜4 のどれかで既に落ちている**ため——1 本でも欠けると
この kind は成立しないので、そこで打ち切った(費用の理由であって、「たぶん大丈夫」ではない)。

### 2.3 不合格の根拠(実コード・一次資料)

- **Ollama**: `server/routes.go`(main)の経路表が全数の答えになる。登録されているのは
  `/api/*`・`/v1/chat/completions`・`/v1/completions`・`/v1/embeddings`・`/v1/models`・`/v1/responses`・
  `/v1/messages` 等で、**`/props`・`/v1/chat/completions/input_tokens`・`/v1/chat/completions/control` は
  1 つも無い**(`r.POST(`/`r.GET(` を全数確認)。窓は `/api/show` から読めるが、それはうちの経路ではない。
- **LM Studio**: クローズドソースなので**消極的事実**しか取れない。公式ドキュメントの経路一覧
  (`lmstudio.ai/docs/app/api/endpoints/rest` と `/openai`)にあるのは `/api/v1/*` と OpenAI 互換の
  `/v1/{models,chat/completions,completions,embeddings,responses}`、Anthropic 互換の `/v1/messages` のみ。
  **`/props`・トークン計数・生成の中断は一覧に無い。**「無いことの証明」ではないが、公開 API の一覧に
  無いものに依存する設計は取れない。
- **Jan**: 実コードで確定できた。`src-tauri/src/core/server/proxy.rs`(dev)の `proxy_request` は
  `match (method, destination_path)` の**閉じた分岐**で、受けるのは `POST /messages`・
  `POST /chat/completions`・`POST /completions`・`POST /embeddings`・`POST /messages/count_tokens`・
  `GET /models` と静的ファイルだけ。**それ以外は `"Unhandled method/path for dynamic routing"` の
  404**。転送先も `http://127.0.0.1:{port}/v1{path}` 固定なので、`/props` は仮に通っても `/v1/props` に
  なって上流で 404 する。
  ⚠️ **皮肉な事実**: Jan v0.8.0 以降、その内側で動いているのは
  `llama-server --models-preset <router.preset.ini> --models-max N --no-webui`——**この配備とまったく
  同じ形**である。つまり Jan は「不合格の製品」ではなく「合格するエンジンを、合格しない窓口で包んだ
  製品」。Jan を**モデルの入手と起動の GUI として使い、エンジンのポートを直接指す**という使い方は
  成立しうるが、Jan はそのポートを loopback + 自前 api_key で握るので、LAN へ出す方法は未確認。
- **llama-swap**: README の対応経路一覧が明示的で、`/props` は**`?model={model_id}` が必須**。
  うちの `props()` はクエリを付けない(`control-plane/engine_gateway.go` の props 経路)。
  `input_tokens`・`control` は一覧に無い(= 404)。
- **Docker Model Runner**: 公式 API リファレンスの経路は `/engines/v1/*`(OpenAI 互換)・`/models*`・
  `/anthropic/v1/messages{,/count_tokens}`・Ollama 互換の `/api/*`。**`/props` も生成制御も無い。**
  トークン計数は Anthropic 互換の `count_tokens` だけで、軸 3 の欄名とは別物。

### 2.4 一般則(次に「このアプリはどうか」と訊かれたときの答え方)

🔴 **llama.cpp を内蔵しているかどうかは判定に関係がない。判定は「llama-server の root 経路を、
素通しで外に出しているか」だけである。** Jan も Docker Model Runner も内側は llama.cpp で、外側の
窓口が経路を絞るから落ちる。逆に、**素通しのリバースプロキシ(nginx の `proxy_pass` 1 行)は合格する**
——`/props` も `/v1/chat/completions/control` も、うちが叩く形のまま届くからである。

同じ理由で **llama-swap には抜け道がありうる**: `/upstream/:model_id/...` は上流サーバへの直接アクセスと
README が書いているので、`AF_LCPP_LIVE_BASE=http://host:port/upstream/<model>/v1` とすれば 6 軸すべてが
素の形で届く**可能性がある**(未検証・§10)。llama-swap を使いたい理由(モデルごとの ttl 自動退避など)が
出てきたときの最初の実験はこれ。ただし **llama.cpp 自身が router モード(`--models-preset`/`--models-max`/
`--models-autoload`)を内蔵した**ので、複数モデルの出し入れのために外部プロキシを足す理由は薄い。

---

## 3. 引き継ぎの前提のうち、上流で覆ったもの・裏が取れたもの

| 引き継ぎの見立て | 実際 | 出典 |
|---|---|---|
| 🔴 **`--jinja` 必須。無いと tool call が専用パーサに届かない** | **誤り。`--jinja, --no-jinja` の既定は `enabled`**——`master` でも、この配備が実測した **`b10830` でも**同じ。明示しても害は無いが、「無いと壊れる」ではない。`llama-3.1-8b-instruct-q4_k_m` が落ちた真因は `docs/log/99` §12.4 の孤立した `;`(PEG ネイティブパーサの未解析出力)で、jinja の有無ではない。むしろ ADR 0093 段 2 負債 4 は「role 全体の `--jinja` が**常に勝つ**ので legacy 経路へ逃がせない」と書いており、向きが逆である | 上流 `tools/server/README.md`(`master` と tag `b10830` の両方を取得して比較) |
| `-hf user/repo:quant` で HF から直接落とせる **(未確認)** | **実在**。`-hf, -hfr, --hf-repo <user>/<model>[:quant]`。**quant 省略時は `Q4_K_M`**(無ければ repo の先頭ファイル)、mmproj も自動取得(`--no-mmproj` で抑止)、ファイル指定は `-hff`、token は `-hft`/`HF_TOKEN`。`-mu/--model-url` と `-dr/--docker-repo`(Docker Hub)も同列にある | 同上 |
| `-c` は 8192 以上 | **据え置き**(#811 の実測。窓 3500 は 2 族で `ErrCompactionThrashing`)。**上流の根拠が 1 つ増えた**: 単機モードでは `/props` の `default_generation_settings.n_ctx` が実窓で、`-c` はそこに素直に出る | `docs/decisions/0093` 段 2 負債 3・上流 README |
| `--api-key` を付けないと認証なし | **正しい**。`--api-key KEY`(カンマ区切りで複数可)/`--api-key-file`/env `LLAMA_API_KEY`。失効・追跡が効かない共有 bearer 1 本になるという 105 の指摘もそのまま成立する | 上流 README・[105](105-lcpp-console-toggle-and-lan-endpoint.md) |
| `chat_template` は `/props` にも `/v1/models` にも出ない(段 2 負債 6) | **router 固有の話だった。** 上流 README の `GET /props` 応答例には **`chat_template` と `chat_template_caps` がある**。router では「どのモデルも載っていない router 自身」を describe するので消える。🔴 **LAN の単機サーバなら、GGUF に焼かれたテンプレを配備側から読める**——負債 6 は LAN 構成では解消しうる | 上流 README `GET /props` |
| (引き継ぎに無かった) `/v1/models` の `meta` | 🔴 **単機モードの応答例に `n_ctx` は無い**。あるのは `n_ctx_train`(学習時の上限)・`n_vocab`・`n_params`・`size`。実窓は `/props` 側にある。**契約テストは `meta.n_ctx > 0` を要求している**ので、ここが単機では落ちる(§7.2) | 上流 README `GET /v1/models` |
| (引き継ぎに無かった) router の `/props` | **`?model=` を付けると既定で autoload する**(`--no-models-autoload` / `?autoload=false` で抑止)。うちの `props()` はクエリを付けないので今日は安全だが、**「`/props` は箱を起こさない」は付け方次第**という条件付きの事実である | 上流 README router 節 |

---

## 4. いま目録の検索はどこにあるか——実コードの地図

### 4.1 検索(取り込み前)

- 経路: `POST /api/admin/engines/{key}/ingest/search`(`control-plane/engine_admin.go:135`)と、エンジンを
  持たない配備のための `POST /api/admin/engines/search`(`:142`)。どちらも **`withIngestAdmin`**
  ——super_admin か、`allow_engine_ingest` を与えられた tenant_admin だけ。
- ヒット 1 件の中身: `engineSearchHit`(`control-plane/engine_ingest_search.go:84-`)。
  `source`・`ref`(HF はリポジトリ)・`model_ref`・`name`・`downloads`/`likes`/`trending`・
  `gated`/`gated_kind`・ライセンス・`bytes`・**`context_length`**。
- 🔴 **LLM 側の検索は Hugging Face 固定**。ソース切替のセグメントは `image` のときしか描かれない
  (`console/src/features/settings/admin/adminEngineAdd.tsx:332`・`:599`)、カードも
  `Hugging Face` を直書き(`:491` の `LLMCatalogCard`)。**= 検索ヒットはすべて `-hf` に写せる。**
- ファイルの選択: `POST .../ingest/files`(`engine_admin.go:132`)がリポジトリの中身を、このエンジンが
  読めるものに絞って返す。**quant の綴り(`Q4_K_M` 等)はここで確定する。**
- 画面: `EngineAddView`(`adminEngineAdd.tsx:54`)→ `LLMCatalog`(`:150`)→ `CatalogBrowser`(`:188`)→
  `LLMCatalogCard`(`:485`)→ 1 ボタンのフッタ `BrowseCardFooter`(`:520`・ADR 0085 決定 3 の「1 押し」)。

### 4.2 取り込み後(目録の行)

- `store.EngineModel`(`control-plane/internal/store/store.go:224-`)。本稿に効く欄:
  - `Files []EngineModelFile` —— 各ファイルに **`Source`(`hf:<repo>/<file>`)** がある
    (`store.go:394-419`)。🔴 **行の `Source` ではなく、ファイルの `Source` が正**(行の方は「その行を
    作ったファイル」しか説明しない、と同コメントが明記)。**ここが「S3 の鍵 → 上流の綴り」の写像で、
    LAN 用コマンドの生命線である。**
  - `ContextTokens`(= `-c`)・`Args`(そのまま渡す引数)・`ContextCeiling`(`n_ctx_train`)・
    `KVLayers`/`KVHeadsKV`/`KVKeyLen`/`KVValueLen`(KV 見積り)・`VramMiB`・ライセンス一式。
- **起動引数の正本は 2 か所ある**:
  1. `buildEngineActiveSet`(`control-plane/engine_catalog.go:418`)——行 →「箱が読む文書」(SSM・4,096 字
     上限)。`f`(ファイル)・`a`(引数)・`c`(窓)・`lo`(LoRA `<key>:<scale>`)。
  2. `deploy/aws/ecs/engine-tools/fetch-models.sh:116-126` の jq ——その文書 → **`presets.ini`** と
     `cmdline`。`[id]` セクション名が alias、`model =`・`c =`・`lora-scaled =`・`load-on-startup`。
  - 配備の実コマンドは `deploy/aws/ecs/cfn/60-engines.yaml:718-723`:
    `/app/llama-server --host 0.0.0.0 --port 8080 --models-max N <Extra> $(cat /models/cmdline)`、
    `Extra` の既定は **`-ngl,99,--jinja,--no-mmap`**(`60-engines.yaml:51`)。
- 窓とカードの当て方: `console/src/features/settings/admin/engineFit.ts:87` の
  `windowThatFits(weightsMiB, kvPer1k, cardMiB, ceiling)` と `:115` の `windowWhenUnsized`
  (既定 32768)。**「このカードなら `-c` はいくつ」を既に計算できる。**
  KV 幾何は `control-plane/engine_gguf.go`(段 2 負債 5 の 409 はここ)。

### 4.3 帰結

**新しい知識は 1 つも要らない。** 要るのは「いま S3 の鍵で書いている場所を、上流の綴り(`-hf`)と
ローカルのパスで書き直して 1 か所に描く」ことだけである。**未取り込みのヒットからは `-hf repo:quant`、
取り込み済みの行からは `-hf` + 宣言済みの `-c`/`Args`/LoRA**、という 2 経路になる。

---

## 5. 出力物の形(依頼の問い 2)

### 5.1 3 層に分ける——1 行 → INI → 手順書

| 層 | 形 | 入力 | いつ効くか |
|---|---|---|---|
| A | **1 行のコマンド** | 検索ヒット 1 件(または行 1 件) | 「これを試したい」。**依頼の 9 割はここ** |
| B | **`presets.ini` + 起動コマンド** | 有効な行すべて | 複数モデルを 1 台に載せる(=この配備と同じ router 形) |
| C | **手順書(guide の 1 章)** | —— | 導入・網・鍵・落ちているときの見え方 |

**A の中身(欄ごとに理由がある)**:

```
llama-server -hf <repo>:<quant> --alias <目録の id> -c <窓> -ngl 99 --jinja \
             --host 0.0.0.0 --port 8080 --api-key <鍵>
```

- 🔴 **`--alias` は必須級。** 単機モードの `/v1/models` の `id` は **`-m` に渡したパス**で、`--alias` を
  付けたときだけ好きな名前になる(上流 README)。うちのハーネスは毎リクエストで `model:` に**目録の
  id** を送るので、alias が合っていないと届かない。`fetch-models.sh` が `presets.ini` の
  `[<id>]` で同じことをしているのと同型。
- `-c` は §4.2 の `windowThatFits` の答え(無ければ 8192 以上。#811)。
- `--jinja` は既定 ON(§3)だが**明示する**——版が古い箱に貼られたときに効くため。
- `--host 0.0.0.0` が無いと loopback のみ。**`--port` も明示する**: 上流は「既定ポートを将来 9931 に
  変える」予告を起動時に出している(`docs/log/99` §12.3 で実測)。
- `--api-key` が無いと**認証なしで LAN に開く**(§3・105 の警告と同じ)。
- gated なリポジトリなら `HF_TOKEN=...`(または `-hft`)。**ヒットは `gated`/`gated_kind` を持っている
  ので、出すかどうかを画面が判断できる。**
- 任意で `--sleep-idle-seconds N`(アイドルで眠らせて VRAM を返す・`/props` の `is_sleeping` で見える)。
  105 が「LAN 機は常時起動が前提」と書いたトレードオフを、**上流の機能で一部緩められる**。

### 5.2 Linux と Windows で何が違うか

| | Linux | Windows |
|---|---|---|
| 入手(簡単な順) | `docker run ghcr.io/ggml-org/llama.cpp:server-cuda`(**この配備が使っているのと同じイメージ**)/ 配布物 `llama-bNNNNN-bin-ubuntu-cuda-13.3-x64.tar.gz` / `brew` / conda-forge / nix | **`winget install llama.cpp`** / 配布物 `llama-bNNNNN-bin-win-cuda-13.3-x64.zip` **+ `cudart-llama-bin-win-cuda-13.3-x64.zip`(CUDA ランタイム別配布)** / conda-forge |
| GPU | CUDA 版の配布物は**あるが新しい**: 最新 `b11065` には `ubuntu-cuda-12.8/13.3` があり、この配備が動かしていた `b10830` の資産一覧には**無かった**(当時は vulkan/rocm/sycl のみ)。NVIDIA なら Docker イメージが最も確実 | CUDA 版 zip があり、**cudart の zip を同じ場所に展開する必要がある**(2 ファイル) |
| 網 | ホストのファイアウォール(ufw 等)で 8080 を開ける | **Windows Defender ファイアウォールの受信規則**が要る(未実測: `New-NetFirewallRule -DisplayName "llama.cpp" -Direction Inbound -LocalPort 8080 -Protocol TCP -Action Allow`・管理者権限) |
| モデルの置き場 | `-hf` は `LLAMA_CACHE`(既定 `~/.cache/llama.cpp`)。Docker なら volume で永続化しないと**毎回落とし直す** | 同左(`%LOCALAPPDATA%` 配下)。**`--offline` で取得を止められる** |
| WSL2 | —— | 🔴 **WSL2 の中で動かすと LAN からは既定で見えない**(NAT)。「Windows で動かす」には「Windows ネイティブで動かす」と「WSL2 でポート転送を足す」の 2 通りがあり、**別の手順**になる。ADR 0076 の guide も ComfyUI で同じ地点に注釈を置いている |

🔴 **どの形でも「実行するのは利用者の別ホストであって、Workspace の中ではない」**。生成物は
**貼り付けて実行できる文字列**であって、この配備が走らせるものではない。ここを取り違えると、
Workspace のサンドボックス(GPU 無し・root 無し)で動かす設計になって企画ごと無駄になる。

### 5.3 コピーできる形の前例

`console/src/features/settings/admin/adminEngineIssueToken.tsx:217-226` に `CopyButton` があり、
**「値」と「`<ENV>=<値>` の代入行」の 2 つをコピーさせている**。同ファイルのコメントが原則を書いている:

> Only where the CP NAMED the variable — an invented name is a credential pasted into nothing.

**= 貼り付けさせる文字列は、Console が組み立てるのではなく、サーバが名乗ったものを写す。** この原則は
そのまま本件の設計判断(§6)の根拠になる。

---

## 6. どこに置くか(依頼の問い 4)

### 6.1 候補

| 案 | 中身 | 費用 | 問題 |
|---|---|---|---|
| **A. 取り込み画面のカードに 1 ボタン** | 検索ヒット/登録済み行に「LAN で動かす」。押すとコマンドがダイアログに出てコピーできる | 小 | 権限が ingest 管理者に限られる(§6.3) |
| **B. CP がコマンドを描く** | `POST /api/admin/engines/{key}/lan-command`(または resolve の応答に 1 欄)。Console は表示とコピーだけ | 中 | 経路が 1 本増える |
| **C. guide の 1 章** | `guide/operate/` に「自前の llama.cpp を LAN で動かす」。`07-image-engine`(ComfyUI)の写し | 小 | モデルごとの値(窓・alias・quant)は手で埋めることになる |
| **D. CLI** | `workspace-agent` のサブコマンドで印字 | 中 | **検索は Console にある**。別の画面を見ながら別の端末で打つ形になり、動線が切れる |

### 6.2 推奨——**B を中身に、A を入口に、C を文章に。** 3 点セットで 1 つ

- **描画は CP(B)**: `buildEngineActiveSet` の隣に置けば、`-c`・`Args`・LoRA・alias の**語彙が 1 つ**に
  なる。Go の golden テストで文字列を固定できる(`engine_catalog_test.go` と同じ形)。
  §5.3 の原則——「サーバが名乗ったものを写す」——もここを指している。**Console で文字列を組むと、
  `fetch-models.sh` の jq・`buildEngineActiveSet`・TS の 3 か所に同じ知識が散る。**
- **入口は Console(A)**: 動線は「検索 → 見つけた → LAN で動かす」で、ADR 0085 の「1 押し」と並ぶ位置。
  `BrowseCardFooter`(`adminEngineAdd.tsx:520`)に 2 つ目のボタンが増えるが、**取り込み(バケツに置く)と
  LAN で動かす(置かない)は別の行為**なので、1 押し原則には反しない。
- **文章は guide(C)**: 導入・網・鍵・WSL2・落ちているときの見え方は、コマンド 1 行には収まらない。
  ADR 0076 の `guide/operate/07-image-engine.ja.md` が**同じ読者に同じことを書いた前例**で、
  「すでに自分の網の中で動いている機材を指す」という立て付けまで一致する。

### 6.3 🔴 未決の製品判断——読者は誰か

取り込み画面は **`withIngestAdmin`**(super_admin か、grant を受けた tenant_admin)。
**「自分の PC で動かしたい一般メンバー」には、この画面自体が見えない。**

- この配備(利用者が super_admin 兼メンバー)では実害ゼロ。**まずはここで止めてよい。**
- 一般メンバーにも配るなら、読み取り専用の目録閲覧か、guide の静的な手順に寄せる必要があり、
  **別の設計になる**(ライセンス受諾の記録が誰の行為かという ADR 0072 決定 10 の話にも触れる)。
- **この分岐は利用者に訊くべきで、こちらで決めない。**

---

## 7. 依頼 2——契約テストを素の llama.cpp へ向ける

### 7.1 そのまま通るところ

`live_contract_test.go:203-210` の URL 組み立ては、**base の末尾が `/v1` である**ことだけを前提にして
いる。したがって `AF_LCPP_LIVE_BASE=http://<lan-host>:8080/v1` とすると:

| 変数 | 生成される URL | 素の llama-server での実在 |
|---|---|---|
| `propsURL`(`:207`・末尾 `/v1` を落とす) | `http://host:8080/props` | ✅ root にある |
| `modelsURL`(`:208`) | `http://host:8080/v1/models` | ✅ |
| `inputTokensURL`(`:209`) | `http://host:8080/v1/chat/completions/input_tokens` | ✅ |
| `controlURL`(`:210`) | `http://host:8080/v1/chat/completions/control` | ✅ |

**偶然ではなく、gateway が `/v1` を前置する設計(`engineUpstreamPrefix`)と、llama-server の実配置が
一致しているからである。** ここは無改修でよい。

### 7.2 🔴 直さないと通らない 3 点

1. **`props_build_info_and_router_window_asymmetry`(`:372-`)は router 決め打ち。** `:395` が
   `role=="router" && model_path=="none" && n_ctx==0` を要求し、**単機サーバでは必ず落ちる**
   (role 欄自体が無く、`model_path` は実パス、`n_ctx` は実窓)。分岐が要る:
   - router なら今の assertion、
   - 単機なら `default_generation_settings.n_ctx > 0` と `model_path != ""` を見る。
   `build_info` の確認は**どちらでも共通**(軸 1 はそのまま生きる)。
2. **`/v1/models` の `meta.n_ctx`(`:420` 付近)は単機では空の可能性が高い。** 上流 README の単機の応答例に
   あるのは `n_ctx_train`(§3)。**router 経路でだけ実施する assertion に降ろす**のが正しい直し方。
3. 🔴 **`streaming_usage_present`(`:224-`)は落ちる。** `chatRequest`(`client.go:121-129`)は
   **`stream_options.include_usage` を意図的に送っていない**——「gateway が入れるから」とコメントが
   明記している。入れているのは `askForStreamUsage`(`control-plane/engine_gateway.go:901-913`)で、
   その実測コメントが**「付けなければ usage のチャンクはそもそも来ない」**と書いている。
   **= CP を通さず直に叩くと usage は来ない。**
   - **本番には波及しない。** 105 の「LAN 行に差し替える」(`AF_LLM_URL`)を実装しても、`external` 行は
     同じ `serve()` を通る(`engine_gateway.go:1542-1548` の external 分岐は health の扱いだけ)ので、
     **`askForStreamUsage` は効き続ける。** 影響はこの**契約テスト固有**である。
   - 直し方は 2 つ。(a) テスト側で直叩き用に `include_usage` を足す、(b) `chatRequest` に常時付ける
     (二重指定は上流仕様上ただの上書きで無害)。**(b) は「ハーネス単体で素の llama-server にも繋がる」
     という性質を買う**が、`client.go` のコメントの根拠を変える製品判断なので、利用者に訊く。

### 7.3 運用上の前提 2 つ

- **`AF_LCPP_LIVE_TOKEN` が空だと `t.Skip`(`:196`)。** LAN サーバは **`--api-key` を付けて立てる**のが
  前提になる(付けない運用なら、テスト側の skip 条件も緩める必要がある)。
- **`AF_LCPP_LIVE_MODEL` の既定は `qwen3.8-27b-uncensored-q4_k_m`**(`:200`)。LAN では `--alias` に
  合わせて明示する。

### 7.4 利用者が LAN サーバを立てたあとの手順(そのまま貼れる)

```
# LAN 機(例: NVIDIA + Docker)
docker run --rm --gpus all -p 8080:8080 \
  -v "$HOME/.cache/llama.cpp:/cache" -e LLAMA_CACHE=/cache \
  ghcr.io/ggml-org/llama.cpp:server-cuda \
  -hf <user>/<repo>-GGUF:Q4_K_M --alias lan-test \
  -c 8192 -ngl 99 --jinja --host 0.0.0.0 --port 8080 --api-key <鍵>

# Workspace 側(GPU は 1 円も買わない——借用行を通らない)
cd workspace/agent
AF_LCPP_LIVE_BASE=http://<lan-host>:8080/v1 \
AF_LCPP_LIVE_TOKEN=<鍵> \
AF_LCPP_LIVE_MODEL=lan-test \
go test ./internal/harness/ -tags manuallive -run TestManualLiveEngineContract -v -timeout 15m
```

**初回は §7.2 の 3 点で赤くなるのが正しい結果**である(直す前に 1 回走らせると、どれが本当に落ちるかの
陰性対照になる)。

---

## 8. 段と門・見積り(依頼の問い 5)

| 段 | 内容 | 見積り | 次への門 |
|---|---|---|---|
| 0 | **guide の 1 章**(`guide/operate/` に llama.cpp 版。`07-image-engine` の写し)＋ §5.1 A の雛形。コード 0 行 | **0.5 日** | 利用者が 1 台立てられる |
| 1 | **CP がコマンドを描く**: 行/ヒット → 1 行コマンド。`engine_catalog.go` の隣、golden テスト付き | **1.5〜2 日** | 生成した 1 行で実際にサーバが起動する(実機 1 回) |
| 2 | **Console の入口**: カードにボタン 1 つ＋ダイアログ＋`CopyButton`＋i18n(ja/en)＋dom テスト | **1〜1.5 日** | 押してコピーできる |
| 3 | **契約テストを LAN へ**(§7.2 の 3 点)＋実機 1 回 | **0.5〜1 日**＋運用者の実機 | **6 軸のうち何が通り何が落ちたかが実測で出る**——ここが依頼 2 の答え |
| 4 | **`presets.ini`(複数モデル・router 形)の描画** | **1 日** | —— |

**合計 4〜6 セッション日。段はすべて独立に出せる**(段 0 だけで止めても価値がある)。
🔴 **順序は 0 → 3 を先に通してもよい**——依頼 2 の答え(素の llama.cpp で足りるか)は段 1・2 を待たない。
むしろ**段 3 が赤かったら段 1・2 の作り方が変わる**ので、**0 → 3 → 1 → 2 → 4 を推す。**

**やらない判断の条件**: 利用者が LAN 機材を実際に用意しないなら、段 0 の guide だけ書いて止める。
コマンドを描く機構は、貼る先が無ければ何も買わない。

---

## 9. ADR を起こすべきか(判断)

**起こさなくてよい。** 理由:

- 製品の分岐がほとんど残っていない。「既存アプリで代替できるか」は §2 で**実コードによって閉じた**
  (合格 0 本)。「どこに置くか」は前例(ADR 0085 の 1 押し・ADR 0076 の guide 章)に素直に乗る。
- 機構としては **ADR 0072/0085 の目録に読み取りの出口が 1 つ増えるだけ**で、新しい概念も、
  取り消しにくい決定も無い。

🔴 **ただし決定として 1 行残す価値がある論点が 1 つある**:

> **目録は「このバケツが持っているもの」の台帳だった(ADR 0085 決定 2)。LAN 実行支援は、そこに
> 「この配備が持っていないモデルを、よそで動かす手順」を出す。**

検索ヒットから直接コマンドを出す(= 取り込まない)場合、パネルは**この配備が一度も触っていない
バイト列**について語ることになる。ライセンス受諾(ADR 0072 決定 10)は「この配備が配布を受ける」行為に
紐づいているので、**受諾していない gated リポジトリのコマンドを出してよいか**は答えが要る。
**提案**: 出してよい——出すのは公開ページと同じ情報(リポジトリ名と quant)であって、バイト列でも
資格情報でもない。ただし **`gated` のヒットには「あなたの HF アカウントで受諾が要る」を併記**し、
この配備の `HF_TOKEN` は**絶対に埋め込まない**(§5.1 の `HF_TOKEN=` は利用者自身のもの)。
**この 1 点だけ、実装 PR の説明文か docs/log に明記すれば足り、ADR の重さには届かない。**

---

## 10. 分からなかったこと(実行していない検査)

- **llama-swap の `/upstream/:model_id/` 素通しが 6 軸を満たすか**(§2.4)。実験は
  `AF_LCPP_LIVE_BASE=http://host:port/upstream/<model>/v1` で契約テストを 1 回回すだけ。
- **Jan の内側の llama-server を LAN へ出せるか**。Jan は loopback + 自前 api_key で握っており、
  外へ出す設定があるかは未確認。
- **単機モードの `/v1/models` が実際に何を返すか**。上流 README の応答例に `n_ctx` は無いが、
  README の例が古い可能性は残る(router では実測で `meta.n_ctx` が取れている)。**段 3 の実機 1 回で
  同時に確定する。**
- **Windows の受信規則とネイティブ実行**。§5.2 の PowerShell 1 行は一般的な書き方であって、
  この用途で実測していない。WSL2 のポート転送も同様。
- **`-hf` のダウンロードが gated リポジトリでどう失敗するか**(401 か、部分ファイルか)。
- **生成したコマンドで起動したサーバに対して、`lcpp` のセッションが実際に 1 往復するか。**
  §7 は契約テストの話で、セッション 1 往復は 105 の `AF_LLM_URL` 実装(段 0)が要る——**本稿の範囲外**。
