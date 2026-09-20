# 0094. 指示で編集するモデルを画像エンジンに入れる——Qwen-Image-Edit と、`op=edit` が族で意味を変えること

[English](0094-instruction-edit-image-models.md) | 日本語

- 状態: **起草**（2026-09-20）。実装前。以下の「ある」「無い」は `ae069aaf` で grep して裏取りした。
  🟢 **グラフと能力は実機で測ってから書いた**（開発配備・g6.xlarge / L4 24GB・ComfyUI 0.35.2・
  2026-09-20 に 5 本）。「実測」節がその記録で、**決定 2・3・4・5 はどれも実測が根拠**である。
  🟢 番号は確定（0093 まで develop にある）。
- 関連: [0072](0072-engine-model-catalog.ja.md)（族テンプレートと `base_model` ディスパッチ・
  ファイル役の語彙——この ADR が増やすのはその語彙 2 語） /
  [0069](0069-image-generation-providers.ja.md)（`generate_image` の語彙・`strength` の追記——
  **決定 2 はその追記の規則をこの族に当てて逆の答えを出す**） /
  [0081](0081-image-generation-pane.ja.md)（生成ペイン・決定 4 の「族が読まない摘みは黙らせず報告する」・
  **却下一覧の「カスタムワークフロー」——決定 9 はそれを維持する**） /
  [0085](0085-model-ledger-and-one-press-ingest.ja.md)（取り込みは 1 押し・部品は主語にならない——
  決定 7 はその部品表に 1 族足すだけ） /
  [0074](0074-engine-instance-classes.ja.md)（クラスと VRAM の門——決定 8）

## 背景

利用者の依頼（2026-09-19）は「ComfyUI に qwen-image-edit のワークフローを載せて、**カスタマイズ
しながら**使えないか」である。上流のチュートリアルは
`https://docs.comfy.org/tutorials/image/qwen/qwen-image-edit`、公式テンプレートは
`Comfy-Org/workflow_templates` の `image_qwen_image_edit_2509` / `image_qwen_image_edit_2511`。

**この族は、この配備が今まで持っていたどの族とも種類が違う。** 既存 8 族はすべて「テキストから絵を
作る」モデルで、`op=edit` はその族のグラフに `LoadImage` → `VAEEncode` を足し、サンプラーの
`denoise` を 1 未満にして「元の絵をどれくらい変えるか」を決める——**部分デノイズの img2img** である
（`comfy_workflows.go:345` の `comfyRequestLatent`、既定は `comfyEditDenoise = 0.6`・同 300 行）。

Qwen-Image-Edit は**指示編集**で、保存力の出所がまるで違う。入力画像は
`TextEncodeQwenImageEditPlus` を通って (a) 視覚トークン（384²）と (b) `reference_latents`（≒1 MP）
の**二重で条件付けに入り**、サンプラーは **`denoise` 1.0**（公式テンプレートの KSampler の widget が
そう）で回る。`VAEEncode` の latent は denoise 1 では実質「出力キャンバスの寸法」しか決めない。
（`comfy_extras/nodes_qwen.py` の `execute` を v0.35.2 のソースで読んで確認。）

つまり**同じ `op=edit` という語が、族によって別の機構を指す**。ここを揃えずに載せると何が起きるかは
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

### 決定 1 — 族を 2 つ足す。ファイル役の語彙は増やさない

`base_model` に `qwen-image-edit`（2509）と `qwen-image-edit-2511` を足す。どちらも
`--diffusion-model` / `--clip_l` / `--vae` の 3 役で、**`EngineFile` のフラグ語彙は 1 つも増えない**
（`engineComfyRequiredFlags`・`control-plane/engine_catalog.go:196` に 2 行足すだけ）。テキストエンコーダは
Qwen2.5-VL-7B（`--clip_l` を「唯一のエンコーダ」に使う既存の約束どおり）、VAE は Qwen-Image のもので、
**anima / krea2 が既に持っている同じ鍵を共有する**。

語彙は CP（`engine_catalog.go:170`）と Agent（`comfy_workflows.go:443`）の二重宣言で、
`engine_catalog_test.go` が Agent のソースを読んで一致を検証する。**両方に足す**。

### 決定 2 — この族では `strength` を受け取らない。`denoise` は 1 に固定する

🔴 **実測 C がこの決定の全部である。** 同じ seed・同じプロンプトで `denoise` を 0.6 にしただけで、
看板の文字は元のままの絵が返り、**エラーも警告も出ない**。現行の `op=edit` は
`comfy_workflows.go:313` の `denoise()` が既定 0.6 を返すので、**この族をそのまま既存の edit 経路に
載せると、利用者から見て「編集を頼んだのに何も起きない」が既定の挙動になる**。

- `Caps.Strength` を族ごとにし（今は `comfy.go:138` で全族 true）、この族では **false**。
- 呼び出し側が `strength` を渡したら、`comfyIgnoredParamWarnings` と同じ形で**声に出して**断る
  （ADR 0081 決定 4「族が読まない摘みは黙らせず報告する」）。
- ADR 0069 の語彙追加の規則（「利用者側に回避手段が無いか」）は、この族では**逆向きに効く**:
  回避手段が無いのではなく、**摘みそのものが意味を持たない**。

### 決定 3 — `Ops` は族の属性にする。この族は編集専用

`comfy.go:121` はいま全モデルに `[generate, edit, inpaint]` を返す。Qwen-Image-Edit は
**入力画像が無いと成立しない**（`TextEncodeQwenImageEditPlus` の image 入力が空だと素のテキスト条件付けに
なり、公式にそういう使い方は無い）。族ごとの `Ops` にし、この族は `[edit]` だけを名乗る。

`inpaint` は**名乗らない**。マスクを `SetLatentNoiseMask` で足す形は文法上は書けるが、この配備の誰も
走らせていない——**測っていないものを能力として宣言しない**（ADR 0072 が SD3.5 で払った授業料）。

### 決定 4 — `size` はこの族では候補を出さない

出力寸法は `FluxKontextImageScale` が**入力画像のアスペクト比**から候補表で決める（実測: 入力 1024²
→ 出力 1024²・5 本とも）。`comfySizesFor`（`comfy.go:429`）がこの族に返す候補は**空**にし、
呼び出し側が `size` を渡したら決定 2 と同じ形で報告する。

🔴 **`FluxKontextImageScale` を外して `size` を尊重する案は採らない**（却下した案を見よ）。

### 決定 5 — 参照画像は最大 3 枚。`MaxInputs` を族の属性にする

`TextEncodeQwenImageEditPlus` は `image1..image3` を取り、**2 枚目が効くことは実測 D で確かめた**
（2 枚目の鉢植えが 1 枚目の場面に、色と形を保ったまま入った）。`Caps.MaxInputs`（`comfy.go:125` の
固定 1）を族ごとにし、この族は 3。3 枚目は同じ機構なので同時に開けるが、**枚数ごとの品質は測っていない**
と説明文に書く。

### 決定 6 — 2509 と 2511 は**別の族**にする

2511 のテンプレートは 2509 と**トポロジが違う**: 両方の条件付けに
`FluxKontextMultiReferenceLatentMethod(index_timestep_zero)` が挟まり、`ModelSamplingAuraFlow` の
shift が 3.0 → 3.1、既定が 40 steps になる。**ノードの繋ぎ替えは行の `params` では表現できない**
（`params` は steps / cfg / sampler / scheduler の 4 語だけ）。したがって版ごとに族を 1 つ足す。

⚠️ **これは気持ちの良い答えではない。** 上流は 2509 → 2511 → 2512 と数か月で版を重ねており、この規則の
ままだと「族＝グラフ」が版の数だけ増える。決定 9 の引き金条件はここから来る。

### 決定 7 — 取り込みは部品表に 1 族分書く（1 押しのまま）

`engine_family_parts.go:48` の表に `qwen-image-edit` の 2 部品を足す（`--clip_l` は
`Comfy-Org/Qwen-Image_ComfyUI` の `split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors`、
`--vae` は既存の共有鍵 `image/vae/qwen_image_vae.safetensors`）。2511 も同じ 2 部品を指す。
**どちらも 2026-09-20 に実際に取り込んで存在を確かめた鍵である**（ADR 0085 の「表の行は必ず実測」）。

- fp8 の**スケール済み**エンコーダで良い。krea2 の注記（「fp8 変換は vision tower を壊す」）は
  この族には当てはまらない——公式テンプレートが名指しでこのファイルを使い、実測 A/D が参照画像を
  読めている。
- 本体は fp8（2509: 20.43 GB・2511: 20.53 GB）。bf16（40.86 GB）は L4 に載らない。

### 決定 8 — VRAM の門の見積りは変えない。族の既定 `vram_mib` で黙らせる

`engineModelVramNeed`（`control-plane/engine_class.go:253`）は行の**全ファイルの合計**を下限にする。
この族は 20.43 + 9.38 + 0.25 GB ＝ **28,676 MiB を要求**と判定され、L4（22,000 MiB 宣言）では必ず
`confirm_vram` を要求する。**実測では載った**（23.7 GB のカードに本体が載り、空き 1.8 GB）。

見積りの式は**変えない**（テキストエンコーダを引く式は他の族で嘘になる）。代わりに、取り込みが作る行に
**族の既定 `vram_mib`（実測値 22,000 相当）を書く**——`m.VramMiB > 0` が合計より優先される既存の枝を
使う。門を弱めるのではなく、**測った数字を宣言する**のが ADR 0074 決定 6 の形である。

### 決定 9 — 「カスタムワークフロー」はこの ADR では入れない。引き金条件だけ決める

ADR 0081 の却下一覧「Console から生の ComfyUI グラフを投げさせる」は**維持する**。理由は当時のまま
（テンプレートこそが `base_model`・`params`・行のネガティブ・LoRA の族検査に意味を与える契約であり、
生のグラフはその門を全部迂回する）。加えて実測で分かった具体の危険が 2 つある:

- **`steps` の上限が消える。** `validateRequestParams` は摘みの経路にしかなく、生グラフは 1 時間の
  ジョブを共有 GPU に投げられる。
- **ピン留めが無意味になる。** この ADR のグラフは v0.35.2 のノード定義を読んで書いた。ノード API に
  版間の互換の約束は無い（ADR 0072 決定 4）。行に貼られた JSON は、エンジンを上げた日に**黙って**壊れる。

**引き金**: 同じ能力の**版が 3 つ目**を要求したとき（例: 2512 世代の編集モデルが 2511 と別トポロジで来る）、
または**族の数が 12 を超えた**とき、カスタムワークフローの ADR を起こす。それまでは決定 6 で払う。

### 決定 10 — 再現情報（props）は 2 ノードを読めるようにする

`props.go:446` の `textBehind` は `CLIPTextEncode` に着いたときだけプロンプトを返し、寸法は
`Empty*LatentImage` から読む。この族のグラフは**どちらも持たない**ので、いま入れると
**プロンプト空・寸法空**で記録される（ギャラリーの「この絵の設定」が嘘になる）。
`TextEncodeQwenImageEdit` / `…Plus` を prompt の出所に足し、寸法は `SaveImage` に届いた画像から取る。

## 却下した案

- **既存の edit 経路にそのまま載せる。** 実測 C——既定 0.6 で**無編集の絵**が静かに返る。
- **1 つの族に 2 版を入れ、行の `params` で切り替える。** トポロジの差（参照法ノード・shift）は
  4 語の `params` では表現できない。宣言で分岐するテンプレートは、族ごとのグラフを読めば答えが出る
  という現在の性質を壊す。
- **`FluxKontextImageScale` を外して `size` を尊重する。** 出力寸法は自由になるが、上流が「この比率で
  学習した」と言っている候補表から外れる。`size` を効かせるために品質を賭ける取引で、しかも**外した
  結果を測っていない**。決定 4 は「効かないと言う」ほうを選ぶ。
- **Lightning LoRA（4 steps）を族の既定にする。** 速いが cfg 1 が前提で、ネガティブが効かなくなる
  （`comfyModelTakesNegative` が false を返す行になる）。**行の宣言**に任せる——krea2 Turbo と同じ扱い。
- **Qwen-Image 2512（text-to-image）を同時に入れる。** 別の能力（生成）で別の族。今回の問いは編集で、
  測っていないものは入れない。
- **bf16 を既定にする。** 40.86 GB は L4 にも L40S にも載らない。
- **ComfyUI の Web UI をペインに埋める。** ADR 0081 の却下を維持（資格情報・カタログの迂回・スマホ）。

## 影響

- **Agent**: `comfy_workflows.go`（族 2 つ・テンプレート 1 本＋2511 の差分・denoise の族分岐）、
  `comfy.go`（`Ops` / `MaxInputs` / `Strength` / `Sizes` を族の属性に）、`props.go`（決定 10）、
  `mcp_imagegen.go`（`op` の enum と説明文——族で使える op が変わる最初の例）。
- **CP**: `engine_catalog.go`（語彙 2 語・必須フラグ）、`engine_family_parts.go`（部品表）、
  `engine_class.go` は**触らない**（決定 8）。
- **Console**: `families.ts` に族カード 2 枚（dialect は `sentences`・quality チップ無し・
  steps は 2509 が 20 / 2511 が 40・**size 欄は出さない**）。
- **配備**: 本体 1 つで 20 GB 増える。箱のモデル置き場は NVMe なので容量は足りるが、
  **コールドスタートの同期時間が増える**（実測: 30 GB で 348.9 秒。これは箱の購入込み）。
- **テスト**: `comfy_workflows_test.go` の golden に 2 本、`engine_catalog_test.go` の一致検査、
  `families.test.ts`。

## フェーズ

- **P0** — 族 `qwen-image-edit`（2509）と決定 2・3・4・5 の族属性化。golden とドキュメント。
  完了の定義: **実機で A と C を再現**——A は編集され、C は「この族は strength を読まない」と
  警告が出ること。
- **P1** — 2511（決定 6）・部品表（決定 7）・`vram_mib` の既定（決定 8）。
  完了の定義: 1 押しの取り込みで 3 部品が正しいディレクトリに入り、有効化に `confirm_vram` が要らない。
- **P2** — props（決定 10）と Console の族カード。完了の定義: 生成ペインからの 1 枚で
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
4. **族の数の上限**（決定 9 の引き金 12）は根拠のある数字ではない。次の版が来たときに測り直す。

## 実測（2026-09-20・開発配備・g6.xlarge / L4 24GB）

グラフは公式テンプレート（サブグラフ）を API 形式に写した 10〜12 ノード。UI 専用ノード
（`ComfySwitchNode` / `Primitive*`）は畳んだ。ノードの実在は動いているエンジンの `/object_info` で
確認した（`CLIPLoader` の `type` に **`qwen_image` が在る**——28 種のうちの 1 つ。
`TextEncodeQwenImageEditPlus` / `FluxKontextImageScale` / `CFGNorm` も在る）。エンジンは
**ComfyUI 0.35.2**（ピン留めのとおり）。

- **L4 24GB に載る。** 生成後 `vram_free` 1.8 GB / 23.7 GB。VRAM の門は 28,676 MiB を要求と判定するが、
  実際にはテキストエンコーダがエンコード後に退避されるので載る（決定 8）。
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
