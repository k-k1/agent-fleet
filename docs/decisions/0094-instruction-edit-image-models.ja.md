# 0094. 指示で編集するモデルを画像エンジンに入れる——Qwen-Image-Edit と、`op=edit` がファミリーで意味を変えること

[English](0094-instruction-edit-image-models.md) | 日本語

- 状態: **起草**（2026-09-20）。実装前。以下の「ある」「無い」は `ae069aaf` で grep して裏取りした。
  🟢 **着工前レビュー済み**（2026-09-20・別セッション）。剥がれた 9 件は本文に反映し、
  何が剥がれたかは末尾の「レビューで剥がれた点」に残した。
  🟢 **グラフと能力は実機で測ってから書いた**（開発配備・g6.xlarge / L4 24GB・ComfyUI 0.35.2・
  2026-09-20 に 5 本）。「実測」節がその記録で、**決定 2・3・4・5 はどれも実測が根拠**である。
  🟢 番号は確定（0093 まで develop にある）。
- 関連: [0072](0072-engine-model-catalog.ja.md)（ファミリーテンプレートと `base_model` ディスパッチ・
  ファイル役の語彙——この ADR が増やすのはその語彙 2 語） /
  [0069](0069-image-generation-providers.ja.md)（`generate_image` の語彙・`strength` の追記——
  **決定 2 はその追記の規則をこのファミリーに当てて逆の答えを出す**） /
  [0081](0081-image-generation-pane.ja.md)（生成ペイン・決定 4 の「ファミリーが読まない摘みは黙らせず報告する」・
  **却下一覧の「カスタムワークフロー」——決定 9 はそれを維持する**） /
  [0085](0085-model-ledger-and-one-press-ingest.ja.md)（取り込みは 1 押し・部品は主語にならない——
  決定 7 はその部品表に 1 ファミリー足すだけ） /
  [0074](0074-engine-instance-classes.ja.md)（クラスと VRAM の門——決定 8）

## 背景

利用者の依頼（2026-09-19）は「ComfyUI に qwen-image-edit のワークフローを載せて、**カスタマイズ
しながら**使えないか」である。上流のチュートリアルは
`https://docs.comfy.org/tutorials/image/qwen/qwen-image-edit`、公式テンプレートは
`Comfy-Org/workflow_templates` の `image_qwen_image_edit_2509` / `image_qwen_image_edit_2511`。

**このファミリーは、この配備が今まで持っていたどのファミリーとも種類が違う。** 既存 8 ファミリーはすべて「テキストから絵を
作る」モデルで、`op=edit` はそのファミリーのグラフに `LoadImage` → `VAEEncode` を足し、サンプラーの
`denoise` を 1 未満にして「元の絵をどれくらい変えるか」を決める——**部分デノイズの img2img** である
（`comfy_workflows.go:345` の `comfyRequestLatent`、既定は `comfyEditDenoise = 0.6`・同 300 行）。

Qwen-Image-Edit は**指示編集**で、保存力の出所がまるで違う。入力画像は
`TextEncodeQwenImageEditPlus` を通って (a) 視覚トークン（384²）と (b) `reference_latents`（≒1 MP）
の**二重で条件付けに入り**、サンプラーは **`denoise` 1.0**（公式テンプレートの KSampler の widget が
そう）で回る。`VAEEncode` の latent は denoise 1 では実質「出力キャンバスの寸法」しか決めない。
（`comfy_extras/nodes_qwen.py` の `execute` を v0.35.2 のソースで読んで確認。）

つまり**同じ `op=edit` という語が、ファミリーによって別の機構を指す**。ここを揃えずに載せると何が起きるかは
測った——**黙って何も編集されない絵が返る**（実測 C）。

### 実測で解けた点（開発配備・2026-09-20）

| # | 条件 | 実時間 | 結果 |
|---|---|---|---|
| A | 2509・steps 20・cfg 4・**denoise 1**（公式レシピ） | 226.2 s | 指示どおり編集された |
| B | 2509・**steps 8** | **63.2 s** | 同じく編集された |
| C | 2509・**denoise 0.6**（＝現行 `op=edit` の既定） | 152.8 s | **編集されない**（元の絵のまま） |
| D | 2509・**参照画像 2 枚** | 268.0 s | 2 枚目の物体が 1 枚目の場面に移植された |
| E | **2511**・steps 40・shift 3.1・参照法ノードあり | 393.8 s | 編集された |

A は本体 20.43 GB の初回ロード込み、E は 2511（20.53 GB）への載せ替え込み。箱の購入＋30 GB の同期は
別に 348.9 秒。カードは **L4 24GB**（`vram_total` 23.7 GB・生成後の空き 1.8 GB）、ホスト RAM 16.1 GB。

## 決定

### 決定 1 — ファミリーを 2 つ足す。ファイル役の語彙は増やさない

`base_model` に `qwen-image-edit`（2509）と `qwen-image-edit-2511` を足す。どちらも
`--diffusion-model` / `--clip_l` / `--vae` の 3 役で、**`EngineFile` のフラグ語彙は 1 つも増えない**
（`engineComfyRequiredFlags`・`control-plane/engine_catalog.go:196` に 2 行足すだけ）。テキストエンコーダは
Qwen2.5-VL-7B（`--clip_l` を「唯一のエンコーダ」に使う既存の約束どおり）、VAE は Qwen-Image のもので、
**anima / krea2 が既に持っている同じ鍵を共有する**。

語彙は CP（`engine_catalog.go:170`）と Agent（`comfy_workflows.go:443`）の二重宣言で、
`engine_catalog_test.go` が Agent のソースを読んで一致を検証する。**両方に足す**。

### 決定 2 — このファミリーでは `strength` を受け取らない。`denoise` は 1 に固定する

🔴 **実測 C がこの決定の全部である。** 同じ seed・同じプロンプトで `denoise` を 0.6 にしただけで、
看板の文字は元のままの絵が返り、**エラーも警告も出ない**。現行の `op=edit` は
`comfy_workflows.go:313` の `denoise()` が既定 0.6 を返すので、**このファミリーをそのまま既存の edit 経路に
載せると、利用者から見て「編集を頼んだのに何も起きない」が既定の挙動になる**。

- `Caps.Strength` をファミリーごとにし（今は `comfy.go:138` で全ファミリー true）、このファミリーでは **false**。
- 呼び出し側が `strength` を渡したら **400 で断る**（`bad_strength` と同じ形）。警告に留めない:
  実測 C が「無警告で誤った絵」だったのだから、同じ値を受け取って黙って捨てる経路を残すと、
  利用者から見た失敗の形は変わらない。ADR 0081 決定 4 の「黙らせず報告する」はカタログ側が
  宣言した値（運用者の宣言）に対する規則で、**呼び出し側が今まさに打った値は断るほうが早い**。
- ADR 0069 の語彙追加の規則（「利用者側に回避手段が無いか」）は、このファミリーでは**逆向きに効く**:
  回避手段が無いのではなく、**摘みそのものが意味を持たない**。

### 決定 3 — `Ops` はファミリーの属性にする。このファミリーは編集専用

`comfy.go:121` はいま全モデルに `[generate, edit, inpaint]` を返す。Qwen-Image-Edit は
**入力画像が無いと成立しない**（`TextEncodeQwenImageEditPlus` の image 入力が空だと素のテキスト条件付けに
なり、公式にそういう使い方は無い）。ファミリーごとの `Ops` にし、このファミリーは `[edit]` だけを名乗る。

`inpaint` は**名乗らない**。マスクを `SetLatentNoiseMask` で足す形は文法上は書けるが、この配備の誰も
走らせていない——**測っていないものを能力として宣言しない**（ADR 0072 が SD3.5 で払った授業料）。

### 決定 4 — `size` はこのファミリーでは候補を出さない

出力寸法は `FluxKontextImageScale` が**入力画像のアスペクト比**から `PREFERRED_KONTEXT_RESOLUTIONS`
の最近傍を選ぶ（ノード定義を v0.35.2 のソースで読んだ結果。実測は 1024² → 1024² が 5 本で、
**正方形しか試していないので比率表の裏取りにはなっていない**）。`comfySizesFor`（`comfy.go:429`）が
このファミリーに返す候補は**空**にし、呼び出し側が `size` を渡したら決定 2 と同じ形で断る。

🔴 **行の `sizes` も受け付けない。** `comfySizesFor` は `conn.Sizes[model]` があればファミリーより先に返すので
（`comfy.go:430`）、ファミリーが空を返すだけでは運用者が行に書いた候補がそのまま出てしまう。このファミリーは
**行の宣言よりファミリーが勝つ**唯一の例にする——効かない値を選ばせないためであり、理由は本文のこの行に書く。
- Console 側も同じ穴を持つ: `sizeOptions`（`families.ts:145`）は `familyCard(f)?.sizes ?? DEFAULT_SIZES`
  なので、**ファミリーカードで `sizes` を省くとメガピクセル表が出る**。カードは `sizes: []` を明示し、
  空のときは欄そのものを描かない分岐を入れる。

🔴 **`FluxKontextImageScale` を外して `size` を尊重する案は採らない**（却下した案を見よ）。

### 決定 5 — 参照画像はまず 2 枚。3 枚目は測ってから。`MaxInputs` をファミリーの属性にする

`TextEncodeQwenImageEditPlus` は `image1..image3` を取り、**2 枚目が効くことは実測 D で確かめた**
（2 枚目の鉢植えが 1 枚目の場面に、色と形を保ったまま入った）。`Caps.MaxInputs`（`comfy.go:125` の
固定 1）をファミリーごとにし、このファミリーは **2**。

🔴 **3 枚目は測ってから開ける。** ノードは `image3` を取るので「同じ機構だから 3」と書きたくなるが、
それは決定 3 が inpaint に対して禁じた推論そのものである（測っていない能力は宣言しない）。P3 で
3 枚を 1 回測り、そこで 3 に上げる。

⚠️ **`MaxInputs` はワイヤに載っていない**（`providerStatus` に欄が無く、外に出るのは
`comfy.go:786` の拒否文だけ）。ペインが枚数の上限を出すには status に欄を足す決定が要る（P3）。

### 決定 6 — ファミリーの単位は**版ではなくトポロジ**。2509 と 2511 は配線が違うので 2 つになる

2511 のテンプレートは 2509 と**トポロジが違う**: 両方の条件付けに
`FluxKontextMultiReferenceLatentMethod(index_timestep_zero)` が挟まり、`ModelSamplingAuraFlow` の
shift が 3.0 → 3.1 になる（steps / cfg は switch の false 枝＝40 / 4）。**ノードの繋ぎ替えは行の
`params` では表現できない**（`params` は steps / cfg / sampler / scheduler の 4 語だけ）。

🔴 **規則は「版ごとに 1 ファミリー」ではなく「トポロジごとに 1 ファミリー」。** この言い方の違いが効く場面は必ず来る:
2512 が 2511 と同じ配線で出れば**ファミリーは増えず、行の `params` だけ**で足りる。ファミリーを増やす条件は
「上流のテンプレートがノードを挿抜している」ことであって、版番号が上がったことではない。

この規則を支える一番強い根拠は props にある: `comfyFamilyFromPrefix`（`props.go:485`）は
`SaveImage` の `af-<family>` からファミリーを復元するので、**1 ファミリーに 2 トポロジを同居させると、絵がどちらの
グラフで描かれたか後から言えなくなる**（ADR 0081 決定 3 の「この絵の設定」が嘘になる）。

- **綴りは両方に版を付ける**: `qwen-image-edit-2509` と `qwen-image-edit-2511`。`base_model` は
  `engine_models` に残る文字列で、後から変えると配備済みカタログの移行になるので**いま決め切る**。
  片方だけ版を持つ非対称（`qwen-image-edit` / `…-2511`）は、2512 が 2509 の配線で来た日に意味不明に
  なる。**ファミリー名は「そのトポロジを最初に出した版」を指し、版の別名ではない**——この一文を取り込み UI の
  ファミリーセレクタの説明に出す。
- ⚠️ **代償: LoRA がファミリーごとに 1 行要る。** `comfyResolveLoras`（`comfy.go:524`）は
  `base_model` の完全一致で適合を見るので、Lightning LoRA は 2 ファミリーぶん 2 行の登録になる。
  「Lightning は行に任せる」（却下した案）は、ファミリーが割れた瞬間 1 行では成立しない。
- 実数: ファミリー 1 つを足すと宣言が **約 12 か所**（`engineComfyFamilies` / `engineComfyRequiredFlags` /
  `engineFamilyParts` / `comfyFamilies` / テンプレートの switch / `comfyFamilyKnobs` /
  `comfyFamilyTakesNegative` / `comfyFamilyRecipes` / `comfyTrialSteps` / `wire.ts` の `Family` /
  `FAMILY_CARDS` / golden）。決定 9 の引き金はこの実数から引く。

### 決定 7 — 取り込みは部品表に 1 ファミリー分書く（1 押しのまま）

`engine_family_parts.go:48` の表に 2 部品を足す。`engineFamilyPart` は **Flag / Repo / File / S3Key の
4 欄**で、S3 鍵だけでは足りない:

| Flag | Repo | File | S3Key |
|---|---|---|---|
| `--clip_l` | `Comfy-Org/Qwen-Image_ComfyUI` | `split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors` | `image/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors` |
| `--vae` | `circlestone-labs/Anima` | `split_files/vae/qwen_image_vae.safetensors` | `image/vae/qwen_image_vae.safetensors` |

🔴 **VAE の出所は anima / krea2 と同じ `circlestone-labs/Anima` にそろえる。** 表の上の 🔴 が名指しで
警告しているとおり、再利用の判定は artifact identity（`hf:<repo>@<rev>/<path>#sha256:…`）であって
ハッシュではない——**同じバイトでも別リポジトリから宣言すると 2 回落ちて 2 つの鍵になる**。2511 も同じ
2 部品を指す。
**どちらも 2026-09-20 に実際に取り込んで存在を確かめた鍵である**（ADR 0085 の「表の行は必ず実測」）。

- fp8 の**スケール済み**エンコーダで良い。krea2 の注記（「fp8 変換は vision tower を壊す」）は
  このファミリーには当てはまらない——公式テンプレートが名指しでこのファイルを使い、実測 A/D が参照画像を
  読めている。
- 本体は fp8（2509: 20.43 GB・2511: 20.53 GB）。bf16（40.86 GB）は L4 に載らない。

### 決定 8 — VRAM の門の見積りは変えない。実測値は**運用者が 1 回入れる**

`engineModelVramNeed`（`control-plane/engine_class.go:253`）は行の**全ファイルの合計**を下限にする。
このファミリーは 20.43 + 9.38 + 0.25 GB ＝ **28,676 MiB を要求**と判定され、L4 の段（この配備の `ImageOffers` が宣言した 22,000 MiB。段はカードの実寸より低く宣言する——理由は
`engine_class.go:41` のコメント）では必ず `confirm_vram` を要求する。

**実測（`/system_stats` の生値）**: `vram_total` 23,659,151,360 B ＝ **22,563 MiB**、
`vram_free` 1,783,934,774 B ＝ 1,701 MiB、したがって **使用 20,862 MiB**。段の 22,000 に収まっている。
測定条件は **1024²・batch 1・参照画像 1 枚**で、`comfyMaxBatch` は 4（`comfy.go:482`）、決定 5 は 2 枚まで
開ける——**その範囲は測っていない**。

見積りの式は**変えない**（テキストエンコーダを引く式は他のファミリーで嘘になる）。では誰が `vram_mib` を書くか:

🔴 **取り込みには書かせない。** `vram_mib` は `engine_class.go:236` が「運用者自身の実測」と定義した欄で、
書けるのは管理 API の PATCH だけ（`engine_admin.go:1140` の `SetEngineModelVram`・0 は「実測の撤回」）。
ファミリーの既定を機械が流し込むと、**その欄の意味が「誰かが測った数字」から「誰かが書いた数字」に変わる**——
ADR 0074 が「測っていない値を既定にすると決定 1 の前提ごと嘘になる」と釘を刺した所である。

代わりに **この ADR と取り込み UI が測定値を提示し、運用者が 1 回入れる**。入れれば
`m.VramMiB > 0` の枝が合計より優先され、以後 `confirm_vram` は出ない。**押すのは 1 回**で、
その 1 回に「何を測ったか」（1024²・batch 1・参照 1 枚）が添うほうが、黙って通るより強い。

### 決定 9 — 「カスタムワークフロー」はこの ADR では入れない。引き金は数でなく宣言の重複量

ADR 0081 の却下一覧「Console から生の ComfyUI グラフを投げさせる」は**維持する**。理由は当時のまま
（テンプレートこそが `base_model`・`params`・行のネガティブ・LoRA のファミリー検査に意味を与える契約であり、
生のグラフはその門を全部迂回する）。加えて実測で分かった具体の危険が 2 つある:

- **`steps` の上限が消える。** `validateRequestParams`（`jobs_http.go:177`）は摘みの経路にしかなく、生グラフは 1 時間の
  ジョブを共有 GPU に投げられる。
- **ピン留めが無意味になる。** この ADR のグラフは v0.35.2 のノード定義を読んで書いた。ノード API に
  版間の互換の約束は無い（ADR 0072 決定 4）。行に貼られた JSON は、エンジンを上げた日に**黙って**壊れる。

**引き金はファミリーの「数」ではなく宣言の「重複量」で引く。** ファミリーの総数は編集と無関係なファミリーが増えても動くので
軸が合わない（いま 8＋2＝10 で、無関係な 2 つが増えただけで発火してしまう）。決定 6 の実数——**ファミリー 1 つ
＝約 12 か所の宣言**——を使い、次のどちらかが起きたら ADR を起こす:

- **トポロジの違うファミリーが 3 つ目**を要求したとき（同じ能力で 3 種類の配線を同時に抱える状態）。
- **ファミリー 1 つあたりの宣言箇所が増えたとき**（12 か所が 15 を超える＝ファミリーを足す作業そのものが重くなった）。

🟡 **生グラフの前に中間段がある。** 「ファミリー＝データ行」（挿入するノード・recipe・knobs・sizes・ops を
テーブルで持ち、テンプレートはそれを読む）にすれば、2 ファミリーの差が**ノードの挿抜だけ**で書けるなら
宣言 12 か所は 1 行になる。決定 9 が挙げた危険 2 件（`steps` の上限・ピン留め）は**どちらも残る**ので、
カスタムワークフローより先に検討すべきはこちらである。

### 決定 10 — 再現情報（props）にこのファミリーのノードを教える

`textBehind`（`props.go:446`）は `CLIPTextEncode` に着いたときだけ**プロンプトと negative** を返し、
寸法は呼び出し側（`props.go:411`）が `Empty*LatentImage` から読む。このファミリーのグラフは**どちらも持たない**
ので、いま入れると **プロンプト空・negative 空・寸法空**で記録される（ギャラリーの「この絵の設定」が
嘘になる）。`TextEncodeQwenImageEdit` / `…Plus` を prompt と negative の出所に足し、寸法は
`SaveImage` に届いた画像から取る。

🟢 ファミリーの復元そのものは追随する: `comfyFamilyFromPrefix`（`props.go:485`）は `comfyFamilies` を回すので、
決定 1 でファミリーを足せば自動で読めるようになる。

### 決定 11 — ファミリーの属性は**判定**に使う。**広告はモデル横断の union** にする

🔴 **これを書かないと決定 2〜5 は「編集した直後から生成が別プロバイダに落ちる」を生む。** 能力を外に
出す経路は `http.go:206` の `caps := p.Caps("")`＝**warm な既定モデル 1 つ**で、そこから
`st.Ops`（`http.go:215`）と `st.Strength`（`http.go:223`）が出て、MCP のツール定義（`mcp_stdio.go:1132`
の `op` enum）とペインの欄になる。さらに `chooseImageProviders`（`imagegen.go:741`）は
`caps(id).Supports(req.Op)` で **provider ごと候補から落とす**ので、qwen の行が warm の間、
`op=generate` は comfy を候補から外し、**会員の課金プランを持つ provider に落ちる**。

同じ罠は `negative_prompt` で既に踏んであり、`http.go:237-247` が**モデル横断の union** で直して
理由までコメントに書いてある。`Ops` / `Strength` / `Sizes` / `MaxInputs` も同じ形にする:

- **広告（`/imagegen/status`・ツール定義・ペインの欄）は union**——このエンジンのどれか 1 つの行が
  できることは、全部載せる。
- **判定（生成時）は `Caps(model)`**——`imagegen.go:806` の `capsOf` は既に `p.Caps(req.Model)` なので、
  モデルを名指しした要求はそのまま正しい。`req.Model` が空のときは**ファミリーではなく provider の union**で
  候補に残し、実際に選ばれた行で断る（決定 2 の 400）。

### 決定 12 — ファミリーの属性は 6 つ。`cfg` と `negative` を落とすと**嘘の警告**が出る

`comfyFamilyKnobs`（`comfy.go:155`）と `comfyFamilyTakesNegative`（`comfy.go:255`）はファミリーをハードコードで
列挙し、**未登録のファミリーは既定に落ちる**。新しいファミリーをここに書き忘れると:

- `comfyFamilyKnobs` は `["steps"]` だけを返し、`comfyIgnoredParamWarnings`（`comfy.go:212`）が
  「cfg=4 was not applied: the qwen-image-edit family folds its guidance into the conditioning」と
  **実測 A（cfg 4 で編集が成った）と正反対の文**を返す。
- `comfyFamilyTakesNegative` は false を返し、`Caps.Negative` もペインのネガティブ欄も消える。

したがってファミリー属性化の対象は 4 つではなく **6 つ**（`Ops` / `MaxInputs` / `Strength` / `Sizes` ＋
`knobs`（steps・cfg・sampler・scheduler）／ `negative`）。このファミリーは **cfg 4 で回る guided なファミリー**なので
`negative` は **true**（グラフは負側にも同じ `TextEncodeQwenImageEditPlus` を置く）。

## 却下した案

- **既存の edit 経路にそのまま載せる。** 実測 C——既定 0.6 で**無編集の絵**が静かに返る。
- **1 つのファミリーに 2 トポロジを入れ、行の宣言で切り替える。** 差（参照法ノード・shift）は 4 語の `params`
  では表現できないうえ、`comfyFamilyFromPrefix`（`props.go:485`）が `af-<family>` からファミリーを復元する
  ので、**絵がどちらの配線で描かれたか後から言えなくなる**。宣言で分岐するテンプレートという形自体は
  既に一部ある（`comfyModelTakesNegative` と `comfyFamilyRecipes` は行の `params` で答えが変わる）が、
  それは**数値**の分岐であって配線の分岐ではない。
- **`FluxKontextImageScale` を外して `size` を尊重する。** 出力寸法は自由になるが、上流が「この比率で
  学習した」と言っている候補表から外れる。`size` を効かせるために品質を賭ける取引で、しかも**外した
  結果を測っていない**。決定 4 は「効かないと言う」ほうを選ぶ。
- **Lightning LoRA（4 steps）をファミリーの既定にする。** 速いが cfg 1 が前提で、ネガティブが効かなくなる
  （`comfyModelTakesNegative` が false を返す行になる）。**行の宣言**に任せる——krea2 Turbo と同じ扱い。
- **Qwen-Image 2512（text-to-image）を同時に入れる。** 別の能力（生成）で別のファミリー。今回の問いは編集で、
  測っていないものは入れない。
- **bf16 を既定にする。** 40.86 GB は L4 にも L40S にも載らない。
- **ComfyUI の Web UI をペインに埋める。** ADR 0081 の却下を維持（資格情報・カタログの迂回・スマホ）。

## 影響

- **Agent**: `comfy_workflows.go`（ファミリー 2 つ・テンプレート 1 本＋2511 の差分・denoise のファミリー分岐・
  `comfyFamilyRecipes` 2 行。**2511 の shift 3.1 は `comfyRecipe` の 4 欄に無い**のでテンプレート内の
  literal になる——どちらに置くかを実装時に決める）、`comfy.go`（決定 11・12 の 6 属性）、
  `props.go`（決定 10）、`jobs.go:105` の `comfyTrialSteps`（**書かないと
  `TestEveryFamilyHasTrialSteps`（`comfy_test.go:1585`）が赤**。実測 B の 8 steps／63.2 秒がそのまま
  根拠になる）、`mcpx/mcp_stdio.go:1132`（`op` の enum と説明文——ファミリーで使える op が変わる最初の例。
  参照画像の引数は `images` ではなく **`inputs`**・`maxItems` 5 固定）。
- **CP**: `engine_catalog.go`（語彙 2 語・必須フラグ）、`engine_family_parts.go`（部品表）、
  `engine_class.go` は**触らない**（決定 8）。
- **Console**: `families.ts` にファミリーカード 2 枚（dialect は `sentences`・quality チップ無し・
  steps は 2509 が 20 / 2511 が 40・`sizes: []` ＋ **size 欄そのものを出さない**）、
  `wire.ts:39` の `Family` 型、`families.test.ts` のファミリー一覧、
  **`jobs.ts:203` の `strength` 無条件送信**（`op !== "generate"` なら必ず送る＝決定 2 が効くと
  編集が毎回 400 になる）、`op` 候補（`draft.ts` の定数 `OPS` を `GenerateForm.tsx:417` が回すだけで status の `ops` を見ていない）。
- **ガイド**: `guide/operate/07-image-engine.{md,ja.md}`（ファミリーごとの挙動を列挙している面）。
- **配備**: 本体 1 つで 20 GB 増える。箱のモデル置き場は NVMe なので容量は足りるが、
  **コールドスタートの同期時間が増える**（実測: 30 GB で 348.9 秒。これは箱の購入込み）。
- **テスト**: `comfy_workflows_test.go` の golden に 2 本、`engine_catalog_test.go` の一致検査、
  `families.test.ts`。

## フェーズ

- **P0** — ファミリー `qwen-image-edit-2509` と決定 2・3・4・5・11・12 のファミリー属性化。golden とドキュメント。
  完了の定義は 3 つ: (1) **実機で A を再現**（編集される）、(2) **C が 400 で断られる**
  （`strength` を受け取らない）、(3) 🔴 **qwen の行を warm にしたまま `op=generate` が comfy に残る**
  （決定 11 の union が効いている＝課金 provider に落ちない）。
- **P1** — 2511（決定 6）・部品表（決定 7）・`vram_mib` の既定（決定 8）。
  完了の定義: 1 押しの取り込みで 3 部品が正しいディレクトリに入り、有効化に `confirm_vram` が要らない。
- **P2** — props（決定 10）と Console のファミリーカード。完了の定義: 生成ペインからの 1 枚で
  「この絵の設定」にプロンプトと寸法が出る。
- **P3** — 参照画像 2 枚目・3 枚目の導線（ペインと MCP の `images`）。
  完了の定義: 実測 D をペインから再現できる。

## 未解決

1. **2511 の 40 steps は高い**（実測 393.8 s）。Lightning LoRA（4 steps）を行として入れたときの
   品質は測っていない。P1 で 1 枚測る。
2. **inpaint を名乗れるか**（決定 3 で見送り）。`SetLatentNoiseMask` を denoise 1 の指示編集に足した
   ときの挙動は未測定。
3. **ホスト RAM 16.1 GB は薄い**（空き 2.2 GB）。2509 と 2511 を両方有効にした箱で、載せ替えが
   続いたときの挙動は測っていない（実測では E が 1 回だけ載せ替えた）。
4. **決定 9 の引き金**（トポロジ 3 つ目／宣言 15 か所）は、12 か所という実数からは引いたが、
   「15」そのものは測った数字ではない。次にファミリーを足すときに宣言箇所を数え直す。
5. **`vram_mib` を運用者が 1 回入れる**（決定 8）のは、取り込みの直後に画面が要求しない限り
   忘れられる。取り込み UI のどこに測定値を出すかは P1 で決める。

## 実測（2026-09-20・開発配備・g6.xlarge / L4 24GB）

グラフは公式テンプレート（サブグラフ）を API 形式に写した 10〜12 ノード。UI 専用ノード
（`ComfySwitchNode` / `Primitive*`）は畳んだ。ノードの実在は動いているエンジンの `/object_info` で
確認した（`CLIPLoader` の `type` に **`qwen_image` が在る**——28 種のうちの 1 つ。
`TextEncodeQwenImageEditPlus` / `FluxKontextImageScale` / `CFGNorm` も在る）。エンジンは
**ComfyUI 0.35.2**（ピン留めのとおり）。

- **L4 24GB に載る。** `/system_stats` の生値で `vram_total` 22,563 MiB・`vram_free` 1,701 MiB＝
  **使用 20,862 MiB**（1024²・batch 1・参照 1 枚）。VRAM の門は 28,676 MiB を要求と判定するが、
  実際にはテキストエンコーダがエンコード後に退避されるので載る（決定 8）。
  ⚠️ `comfyMaxBatch` は 4、決定 5 は参照 2 枚まで——**その範囲は測っていない**。
- **出力寸法の規則はノード定義から**であって、この 5 本からではない: 入力も出力も 1024² だけで、
  `PREFERRED_KONTEXT_RESOLUTIONS` の比率表は**正方形以外を試していない**。
- **箱の購入＋同期 348.9 秒**（`engine_waking` の 503 を 15 秒間隔でリトライ）。`X-AF-Model` を付けた
  要求は「あと 1 ファイル」と数えて待たせてくれる＝`pendingGuard` は実機で期待どおり働いた。
- **取り込みは HF → S3 で約 31 MB/s**（20.43 GB で約 11 分）。

### 途中で見つけた穴 2 件（どちらもこの ADR の外）

- 🔴 **移設（`MODE=move`）が IAM で必ず失敗していた。** 取り込みロールに `s3:GetObjectTagging` が無く、
  `aws s3 mv` が既定でコピー元のタグを読むため、**マルチパート閾値を超える全オブジェクト**で
  `AccessDenied`。ADR 0085 の「別の鍵にある → 移設」＝誤配置の唯一の修復経路が、押せるのに必ず失敗する
  状態だった。**PR #763 で修正し、当てた直後に 9.38 GB の移設が 1 分で成功**することを確かめた。
- 🟡 **揃える の `replace` が `Append` に落ちる組がある。** 既に埋まっているスロットへ `choices` +
  `replace` で移設を指示すると 500（`engine model file slot is already taken`）。回避は「行の
  スロットを空にしてから押す」。追いきれていないので**この ADR では直さない**が、再現手順は残す。

## レビューで剥がれた点（2026-09-20・着工前レビュー・別セッション）

**引用は全部当たっていた**（`file:line` と「ある／無い」の主張）。剥がれたのは**決定の射程**と、
コードの名前が 3 つである。

- 🔴 **決定 2〜5 は「広告」の経路を見ていなかった。** 能力は warm な 1 行の `Caps` で外に出るので、
  ファミリーごとの `Ops` は「qwen で編集した直後から `op=generate` が課金 provider に落ちる」を生む。
  決定 11 を足した。`negative_prompt` で同じ罠を踏んで union で直した跡（`http.go:237-247`）が
  既にあったのに、それを読んでいなかった。
- 🔴 **ファミリー属性は 4 つではなく 6 つだった**（決定 12）。`cfg` と `negative` を落とすと、実測 A と
  正反対の警告文が出る。
- 🔴 **決定 8 の「22,000」は段の数字で、実測ではなかった。** 生値から計算した使用量は
  **20,862 MiB**。加えて `vram_mib` は「運用者自身の実測」と定義された欄なので、取り込みに
  書かせる案は欄の意味を変える——**書かせない**に改めた。
  🟡 レビュー側の「実測 22,426 MiB は段を超える」は **GiB と GB の取り違え**で、こちらの保存した
  生バイト値（23,659,151,360 / 1,783,934,774）から計算し直すと段に収まる。**数字の指摘こそ
  生値に戻る**。
- 🔴 **決定 7 は VAE の Repo / File を書いていなかった。** 再利用は artifact identity で判定するので、
  S3 鍵だけでは別リポジトリから 2 回落ちる。
- 🔴 **決定 6 の規則は「版ごと」ではなく「トポロジごと」**と書くべきだった。綴りの非対称
  （片方だけ版付き）も、2512 が 2509 の配線で来た日に破綻する。代償（LoRA がファミリーごとに 1 行）も
  書き足した。
- 🟡 **決定 4 は行の `sizes` と Console の既定表に負けていた**（`comfy.go:430` / `families.ts:145`）。
- 🟡 **決定 9 の引き金「ファミリー 12」は軸が合っていなかった**（編集と無関係なファミリーで発火する）。宣言の
  重複量に変え、中間段（テーブル駆動テンプレート）を候補として書いた。
- 🟡 **コードの名前 3 つが違った**: `op` の enum は `mcp_imagegen.go` ではなく `mcp_stdio.go:1132`、
  参照画像の引数は `images` ではなく `inputs`、寸法を読むのは `textBehind` ではなく `props.go:411`。
- 🟡 **影響に 5 つ抜けていた**: `comfyTrialSteps`（書かないとテストが赤）・`comfyFamilyRecipes`・
  `wire.ts` の `Family` 型・`families.test.ts`・`guide/operate/07-image-engine`。
- 🟡 **決定 5 は決定 3 と規則が逆だった**: 「3 枚目は同じ機構だから開ける」は、inpaint に対して
  禁じた推論そのもの。**2 枚に留め、3 枚目は P3 で測ってから**に改めた。
