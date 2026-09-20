# 0094. 指示で編集するモデルを画像エンジンに入れる——Qwen-Image-Edit と、`op=edit` がファミリーで意味を変えること

[English](0094-instruction-edit-image-models.md) | 日本語

- 状態: **P0 実装済み・実機受け入れ済み**（2026-09-20）。完了条件 (1)〜(4) を開発配備で 4 つとも
  通した——記録は末尾「P0 の実機受け入れ」。
  🔴 本文の `file:line` は **P0 が入った後の住所に引き直してある**（起草時は `ae069aaf` で取った）。
  P1 以降で行が動いたら、引いた人が直す。
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

`base_model` に `qwen-image-edit-2509` と `qwen-image-edit-2511` を足す（綴りの根拠は決定 6）。どちらも
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
- 呼び出し側が `strength` を渡したら **400 で断る**（`bad_strength` と同じ形）。🔴 **断るのは
  「解決後のモデルがこのファミリーのとき」** ——provider 未指定の要求は comfy に来るとは限らないので、
  「`strength` が付いていたら常に 400」は誤りになる。受付時に model → ファミリーを解く
  （`comfyFamilyFor` は同じ package にある）。警告に留めない:
  実測 C が「無警告で誤った絵」だったのだから、同じ値を受け取って黙って捨てる経路を残すと、
  利用者から見た失敗の形は変わらない。ADR 0081 決定 4 の「黙らせず報告する」はカタログ側が
  宣言した値（運用者の宣言）に対する規則で、**呼び出し側が今まさに打った値は断るほうが早い**。
- 🔴 **400 にできない経路が 1 本ある。そこは警告で拾う——ただし provider ではなく core で。**
  `model` も `provider` も名指ししない要求は受付時にファミリーを解決できないので 400 にできず、
  comfy に届いて denoise 1 で `strength` は捨てられる。`requestWarnings`（`imagegen.go:1019`）には
  `!caps.Strength` の枝があるのに鳴らなかった原因は、**呼び出し側が「要求が名指ししたモデル」の
  Caps を渡していた**ことで、空なら決定 11 の union＝true になる。

  したがって **`requestWarnings` に渡す Caps は「実際に走った行」のもの**にする——`Run`
  （`imagegen.go:922`）とキューの `finish`（`jobs.go:486`）が `Caps(res.Model)` を渡す。
  🟢 **provider 側に strength 版の警告を足す案は採らない**（この ADR の初稿はそう書いていた）:
  それは core の取り違えを comfy だけで塞ぐ形で、**同じ取り違えは `negative` でも起きており**、
  次に per-model になる能力でまた忘れる。core で 1 か所直せば全 provider が同じ保証を得る。
  代償は文面がファミリー名入りから汎用（「この経路は入力をどれだけ残すか変えられない」）に
  なることだけで、ファミリー名を出したいなら置き場所は core のメッセージか `Caps` が理由を運ぶ形
  （別の決定）。
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
**正方形しか試していないので比率表の裏取りにはなっていない**）。`comfySizesFor`（`comfy.go:575`）が
このファミリーに返す候補は**空**にし、呼び出し側が `size` を渡したら決定 2 と同じ形で断る。

🔴 **行の `sizes` も受け付けない。** `comfySizesFor` は `conn.Sizes[model]` があればファミリーより先に返すので
（`comfy.go:576`）、ファミリーが空を返すだけでは運用者が行に書いた候補がそのまま出てしまう。このファミリーは
**行の宣言よりファミリーが勝つ**唯一の例にする——効かない値を選ばせないためであり、理由は本文のこの行に書く。
- Console 側も同じ穴を持つ: `sizeOptions`（`families.ts:145`）は `familyCard(f)?.sizes ?? DEFAULT_SIZES`
  なので、**ファミリーカードで `sizes` を省くとメガピクセル表が出る**。カードは `sizes: []` を明示し、
  空のときは欄そのものを描かない分岐を入れる。

🔴 **`FluxKontextImageScale` を外して `size` を尊重する案は採らない**（却下した案を見よ）。

### 決定 5 — `MaxInputs` をファミリーの属性にする。P0 は 1 枚のまま、2 枚目は経路ごと P3 で

`TextEncodeQwenImageEditPlus` は `image1..image3` を取り、**2 枚目が効くことは実測 D で確かめた**
（2 枚目の鉢植えが 1 枚目の場面に、色と形を保ったまま入った）。`Caps.MaxInputs`（`comfy.go:125` の
固定 1）をファミリーごとにする。

🔴 **2 枚目は「宣言」だけでは動かない。経路ごと同じフェーズに入れる。** いまの実装は
`p.uploadImage(…, req.Inputs[0])`（`comfy.go:1014`）で **1 枚しか上げず**、`comfyParams.Image` は
単数の文字列（`comfy_workflows.go:134`）。`comfyCheckInputs`（`comfy.go:1092`）は
`len(req.Inputs) > caps.MaxInputs` しか見ないので、**宣言だけ 2 にすると 2 枚目は検査を通って使われない**
——実測 C と同じ「無警告で誤った絵」である。したがって:

- **P0 では `MaxInputs` は 1 のまま**。2 枚目は **P3 で経路ごと開ける**（`comfyParams` を複数形にし、
  `image2` に配線し、`comfyCheckInputs` の拒否文の単数形も直す）。
- 🔴 **3 枚目は測ってから。** ノードは `image3` を取るので「同じ機構だから 3」と書きたくなるが、それは
  決定 3 が inpaint に対して禁じた推論そのもの（測っていない能力は宣言しない）。P3 で 3 枚を 1 回測る。

⚠️ **`MaxInputs` はワイヤに載っていない**（`providerStatus` に欄が無く、外に出るのは
`comfy.go:1092` の拒否文だけ）。ペインが枚数の上限を出すには status に欄を足す決定が要る（P3）。

### 決定 6 — ファミリーの単位は**版ではなくトポロジ**。2509 と 2511 は配線が違うので 2 つになる

2511 のテンプレートは 2509 と**トポロジが違う**: 両方の条件付けに
`FluxKontextMultiReferenceLatentMethod(index_timestep_zero)` が挟まり、`ModelSamplingAuraFlow` の
shift が 3.0 → 3.1 になる（steps / cfg は switch の false 枝＝40 / 4）。**ノードの繋ぎ替えは行の
`params` では表現できない**（`params` は steps / cfg / sampler / scheduler の 4 語だけ）。

🔴 **規則は「版ごとに 1 ファミリー」ではなく「トポロジごとに 1 ファミリー」。** この言い方の違いが効く場面は必ず来る:
2512 が 2511 と同じ配線で出れば**ファミリーは増えず、行の `params` だけ**で足りる。ファミリーを増やす条件は
「上流のテンプレートがノードを挿抜している」ことであって、版番号が上がったことではない。

この規則を支える一番強い根拠は props にある: `comfyFamilyFromPrefix`（`props.go:489`）は
`SaveImage` の `af-<family>` からファミリーを復元するので、**1 ファミリーに 2 トポロジを同居させると、絵がどちらの
グラフで描かれたか後から言えなくなる**（ADR 0081 決定 3 の「この絵の設定」が嘘になる）。

- **綴りは両方に版を付ける**: `qwen-image-edit-2509` と `qwen-image-edit-2511`。`base_model` は
  `engine_models` に残る文字列で、後から変えると配備済みカタログの移行になるので**いま決め切る**。
  片方だけ版を持つ非対称（`qwen-image-edit` / `…-2511`）は、2512 が 2509 の配線で来た日に意味不明に
  なる。**ファミリー名は「そのトポロジを最初に出した版」を指し、版の別名ではない**——この一文を取り込み UI の
  ファミリーセレクタの説明に出す。
- ⚠️ **代償: LoRA がファミリーごとに 1 行要る。** `comfyResolveLoras`（`comfy.go:647`）は
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
測定条件は **1024²・batch 1・参照画像 1 枚**。`comfyMaxBatch` は 4（`comfy.go:632`）で、**その範囲は
測っていない**——参照 2 枚を開ける P3 で測り直す（決定 5）。

見積りの式は**変えない**（テキストエンコーダを引く式は他のファミリーで嘘になる）。では誰が `vram_mib` を書くか:

🔴 **取り込みには書かせない。** `vram_mib` は `engine_class.go:236` が「運用者自身の実測」と定義した欄で、
書けるのは管理 API の PATCH だけ（`engine_admin.go:1140` の `SetEngineModelVram`・0 は「実測の撤回」）。
ファミリーの既定を機械が流し込むと、**その欄の意味が「誰かが測った数字」から「誰かが書いた数字」に変わる**——
ADR 0074 が「測っていない値を既定にすると決定 1 の前提ごと嘘になる」と釘を刺した所である。

代わりに **この ADR と取り込み UI が測定値を提示し、運用者が 1 回入れる**。**押すのは 1 回**で、
その 1 回に「何を測ったか」（1024²・batch 1・参照 1 枚）が添うほうが、黙って通るより強い。

🔴 **実機で 2 つ訂正が要った**（2026-09-20・受け入れ時）:

- **宣言しても `confirm_vram` は消えない。** 門（`engineVramGuardRow`）が比べる相手は
  **「選択中のクラス」**で、梯子が未固定の配備ではその先頭＝T4（14,500 MiB）である。正直な
  20,862 MiB を入れても `engine_vram_confirm` が返った（実測）。**消すにはクラスの固定も要る**。
  したがって P1 の完了条件は「入れれば出なくなる」ではなく「**入れた値が梯子に効く**」で書く。
- **合計の見積りは、確認を出すだけでなく買う箱を一段上げる。** 受け入れ時、行は合計
  28,676 MiB で有効化されていたため **22,000 の段（L4）が候補から外れ、L40S（45,458 MiB・
  ホスト RAM 33.2 GB）が買われた**（実測）。20,862 を宣言したあとは L4 段が候補に戻る。
  つまりこの欄は**費用の欄**でもある。

🔴 **3 つ目の訂正——測定値はカードに依存し、この手順には循環がある**（2026-09-20・P1 受け入れ）:

2511 を実機で走らせて `/system_stats` を読んだ実測は、**L40S 48GB で使用 28,358 MiB**
（`vram_total` 47,665,709,056 B ＝ 45,458 MiB、`vram_free` 17,930,132,506 B ＝ 17,100 MiB）。
ファイル合計 28,774 MiB とほぼ等しい＝**3 部品が丸ごと同時に載ったまま、退避が一度も起きなかった**。

2509 の 20,862 MiB は **L4 24GB** で測った値で、上の決定 8 自身が「テキストエンコーダは
エンコード後に**退避される**」と書いている——**退避は VRAM が苦しいから起きる**。したがって
この 2 つは**別の問いの答え**であり、並べて比較してはならない。`engineFamilyVram.ts` の
測定条件に**カードを含めていなかった**のは、この ADR の見落としである。

🔴 **そして手順自体が循環している**: 合計の見積り（28,774）が 22,000 の段を候補から外す →
L40S が買われる → その L40S で測る → 28,358 が出る → それを宣言すると L40S を買い続ける。
**この順序のままでは「2511 も L4 に載るのか」を永久に発見できない。** 測るには、先に
`vram_mib` を仮説として宣言して目当ての段を候補に戻す、という**逆順**が要る。

⇒ **28,358 は `engineFamilyVram.ts` に入れない**（利用者の裁定）。あの表は運用者が `vram_mib` に
1 押しで入れる値の出所であり、この数を入れると 2511 は以後 44,000 以上の段に固定される。
族が**測られていないとき表から不在にする**という既存の方針は、ここでも正しい。

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

🟢 ファミリーの復元そのものは追随する: `comfyFamilyFromPrefix`（`props.go:489`）は `comfyFamilies` を回すので、
決定 1 でファミリーを足せば自動で読めるようになる。

### 決定 11 — ファミリーの属性は**判定**に使う。**広告はモデル横断の union** にする

🔴 **これを書かないと決定 2〜5 は「編集した直後から生成が別プロバイダに落ちる」を生む。** 能力を外に
出す経路は `http.go:217` の `caps := p.Caps("")`＝**warm な既定モデル 1 つ**で、そこから
`st.Ops`（`http.go:227`）と `st.Strength`（`http.go:234`）が出て、MCP のツール定義（`mcp_stdio.go:1132`
の `op` enum）とペインの欄になる。さらに `chooseImageProviders`（`imagegen.go:742`）は
`caps(id).Supports(req.Op)` で **provider ごと候補から落とす**ので、qwen の行が warm の間、
`op=generate` は comfy を候補から外し、**会員の課金プランを持つ provider に落ちる**。

同じ罠は `negative_prompt` で既に踏んであり、`http.go:237-247` が**モデル横断の union** で直して
理由までコメントに書いてある。`Ops` / `Strength` / `Sizes` / `MaxInputs` も同じ形にする:

- 🔴 **union を置くのは `comfyProvider.Caps("")` そのもの**——ルート側ではない。`Caps` は
  `comfy.go:121-125` で空のモデルを `DefaultModel()`＝warm な 1 行に解決するので、**この 1 か所を
  有効行にわたる union にすると、広告（`http.go:217`）と候補選び（`imagegen.go:842` の `capsOf` →
  `imagegen.go:742`）の両方が同時に直る**。モデルを名指しした `Caps(model)` はファミリーどおりのまま
  ＝判定は厳密なまま。
  ⚠️ **手本にした negative の先例（`http.go:242-247`）はルート側の union なので、同じ形を真似るだけでは
  足りない。** そちらだけ直すと `capsOf` は warm な行のままで、`op=generate` は候補選びの時点で
  comfy を落とし、下の 3 つ目の箇条（`Generate` 内の解決）には**到達しない**。
  ⚠️ **union で正しい面と、per-model が要る面は違う。** MCP のツール定義（`mcp_stdio.go:1132` の
  `op` enum）は**接続時のスナップショット**なので原理的に per-model にできない＝union が正しく、
  モデル固有の拒否は決定 2 の 400 で返すしかない。**ペインは per-model が要る**（決定 12）。
- **判定（生成時）は `Caps(model)`**——`imagegen.go:842` の `capsOf` は既に `p.Caps(req.Model)` なので、
  モデルを名指しした要求はそのまま正しい。
- 🔴 **モデル未指定のときは、comfy のモデル解決が `req.Op` を見る。** union は「候補に残す」までしか
  効かない: `comfy.go:647-656` は `req.Model` が空なら `DefaultModel()`＝warm な 1 行に解決し、
  `!caps.Supports(req.Op)` で `the self-hosted image engine cannot do generate` を返す。`Run` は
  それを attempts に積んで **`continue`**（`imagegen.go:898-900`）＝**次の provider（会員の課金プラン）へ
  落ちる**。おまけに `recordUsage`（`imagegen.go:894`）が失敗の行を 1 本刻む。
  したがって warm な行のファミリーがその op を名乗らないときは、**名乗る最初の有効な行に落として
  `comfySwitchWarning`（`comfy.go:782`）を出す**。チェックポイントの切り替えは実測 1〜2.5 分かかるが、
  黙って別プランに課金するより説明できる。**P0 完了条件 (3) はこれが入って初めて検証できる。**

### 決定 12 — ファミリーの属性は 6 つ。`cfg` と `negative` を落とすと**嘘の警告**が出る

`comfyFamilyKnobs`（`comfy.go:277`）と `comfyFamilyTakesNegative`（`comfy.go:387`）はファミリーをハードコードで
列挙し、**未登録のファミリーは既定に落ちる**。新しいファミリーをここに書き忘れると:

- `comfyFamilyKnobs` は `["steps"]` だけを返し、`comfyIgnoredParamWarnings`（`comfy.go:212`）が
  「cfg=4 was not applied: the qwen-image-edit family folds its guidance into the conditioning」と
  **実測 A（cfg 4 で編集が成った）と正反対の文**を返す。
- `comfyFamilyTakesNegative` は false を返し、`Caps.Negative` もペインのネガティブ欄も消える。

したがってファミリー属性化の対象は 4 つではなく **6 つ**（`Ops` / `MaxInputs` / `Strength` / `Sizes` ＋
`knobs`（steps・cfg・sampler・scheduler）／ `negative`）。このファミリーは **cfg 4 で回る guided なファミリー**なので
`negative` は **true**（グラフは負側にも同じ `TextEncodeQwenImageEditPlus` を置く）。

🔴 **per-model の信号を 2 つワイヤに足す。決定 11 の union はこれと対で初めて成立する。** union を
`Caps("")` に置くと `st.Ops`（`http.go:227`）と `st.Strength`（`http.go:234`）は provider 単位の
「どれか 1 行ができること」になり、**どのモデルにも当てはまらない値**になる。ところが `modelStatus`
（`http.go:142-176`）には `ops` も `strength` も無く、per-model の線は `Knobs` だけ（定義は
「`steps cfg sampler scheduler negative` の部分集合」）。このままでは影響の Console 行が命じている
2 つ——`jobs.ts:203` の `strength` 無条件送信を止める・`op` 候補を status から引く——が**条件にできる
信号を持てない**。SDXL と qwen が同居する配備では `strength` は常に true・`ops` は常に 3 種になり、
qwen を選んでいてもペインは滑り台を出し `generate` を候補に出す＝決定 2 の 400 が会員の目の前で毎回
出る。

- **`Knobs` に `strength` を足す**（ペインは既に `model.knobs.includes("negative")` で欄を落として
  いるので、フォーム側は同じ形で書ける）。
- **`modelStatus` に `ops` を足す**。
- ⚠️ **`knobs` が無い＝古い Agent のときは全部使えるままにする**、という既存の規則を壊さないこと
  （`GenerateForm.dom.test.tsx` が明文で守っている）。
- 🔴 **下書きの `op` は候補から外れたら読み替える。** 編集専用の行へ切り替えても `draft.op` は
  `"generate"` のまま残る（`draft.ts` の既定）ので、**選択欄に無い値が選ばれた状態**になり、押すと
  決定 2 の 400 を踏んだうえで他 provider へフォールスルーする。候補に無くなったら
  **そのモデルの先頭の op に読み替え、読み替えたことを一言出す**（黙って変えるのも黙って落ちるのも
  同じ穴——ADR 0081 決定 4 の「黙らせず報告する」）。

### 決定 13 — モデルを名指しした要求は、そのモデルを持つ provider に**固定する**。op が無ければ落とさずに断る

🔴 **決定 3 が開けた扉で、ADR はここを見ていなかった。** `chooseImageProviders`（`imagegen.go:742`）は
`caps(id).Supports(req.Op)` で候補を絞り、`capsOf` は名指しのとき `p.Caps(req.Model)`＝**厳密**。
したがって `model=qwen-image-edit-2509` ＋ `op=generate`（provider 未指定）では **comfy が候補選びの
時点で落ち**、`codexProvider.Caps(string)` / `agyProvider.Caps(string)` は**モデルを見ない**ので
generate を名乗ったまま残る——**会員の ChatGPT / Antigravity プランで描かれる**。`fallbackWarnings` は
「試して失敗した provider」しか数えないので**警告も出ない**。決定 3 の前は comfy の `Caps` が常に
3 op を名乗っていたので、この扉は無かった。

- **名指しされたモデルを列挙する provider があれば、候補はその 1 つに固定する**（`ModelLister` の
  `Models()` に一致する id があるか。codex / agy は `ModelLister` ではないので、**その 2 つのために
  モデル名を書く今の経路は壊れない**）。
- 固定した provider がその op を名乗らないなら、**落とさずに断る**——`ErrNoProvider` の形で、
  **モデル名と、そのモデルができる op** を添えて。「名指しは落とさない」は決定 11 の精神そのもので、
  黙って別のプランに課金するより、できない op を名前付きで言うほうが短い。
- ⚠️ `pref`（provider 名指し）がある要求は今も `chooseImageProviders` の先頭で 1 つに絞られる
  （`imagegen.go:743-745`）ので、この決定は **auto のときだけ**の話である。
  実装後の住所: 固定そのものは `modelOwner`（`imagegen.go:761`）と、それを呼ぶ `Run` の
  `pref == "" || pref == "auto"` の門（`imagegen.go:858`）。

## 却下した案

- **既存の edit 経路にそのまま載せる。** 実測 C——既定 0.6 で**無編集の絵**が静かに返る。
- **1 つのファミリーに 2 トポロジを入れ、行の宣言で切り替える。** 差（参照法ノード・shift）は 4 語の `params`
  では表現できないうえ、`comfyFamilyFromPrefix`（`props.go:489`）が `af-<family>` からファミリーを復元する
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
  参照画像の引数は `images` ではなく **`inputs`**・`maxItems` 5 固定）。🔴 **`strength` の説明文
  （`mcp_stdio.go:1219-1221`）も直す**——union で offer される以上、「0.6 when omitted」だけでは
  エージェントが毎回 400 を踏む。「取らないチェックポイントがあり、そのときは 400 で返る」を書く。
  **P3 の 2 枚目**はここに乗る: `comfyParams` の複数形化（`comfy_workflows.go:134` の `Image string`）・
  `uploadImage`（`comfy.go:1014` は `req.Inputs[0]` の 1 回だけ）の複数回呼び出し・`image2` への配線・
  `comfyCheckInputs`（`comfy.go:1092`）の拒否文の単数形。
- **ワイヤ**: `modelStatus` に `ops`、`Knobs` に `strength`（決定 12）。Console 側の写しも同時に:
  `wire.ts:35` の `Knob` は**閉じた union**（`"steps" | "cfg" | "sampler" | "scheduler" | "negative"`）
  なので型ごと直さないとコンパイルが通らず、`ImagegenModel`（`wire.ts:86` が `knobs?: Knob[]`）には
  `ops` が無い。🔴 **語彙を書いたコメントが 3 か所ある**——`providerStatus.Strength`
  （`http.go:108-112`「No union is needed: it is per provider, not per model.」＝決定 2 と 11 が
  これを嘘にする）・`comfyFamilyKnobs`（`comfy.go:267-276`）・`modelStatus.Knobs`
  （`http.go:161-164`）。どれも「5 語の部分集合」と書いてあるので、**同じ変更で 3 つとも書き替える**。
- **CP**: `engine_catalog.go`（語彙 2 語・必須フラグ）、`engine_family_parts.go`（部品表）、
  `engine_class.go` は**触らない**（決定 8）。
- **Console**: `families.ts` にファミリーカード 2 枚（dialect は `sentences`・quality チップ無し・
  steps は 2509 が 20 / 2511 が 40・`sizes: []` ＋ **size 欄そのものを出さない**）、
  `wire.ts:39` の `Family` 型、`families.test.ts` のファミリー一覧、
  **`jobs.ts:203` の `strength` 無条件送信**（`op !== "generate"` なら必ず送る＝決定 2 が効くと
  編集が毎回 400 になる）、`op` 候補（`draft.ts` の定数 `OPS` を `GenerateForm.tsx:417` が回すだけで
  status の `ops` を見ていない）、`GenerateForm.dom.test.tsx`（`knobs` で欄を落とす試験——`strength`
  と `ops` の分を足す。`knobs` を送らない古い Agent では全部使えるまま、という既存の主張は壊さない）。
- **拒否の置き場所は 2 か所**（決定 2）: ブロッキングの `/imagegen/generate`（`http.go:465`）と
  ペインが使うキュー経路（`jobs_http.go:115`）。どちらも今は値域（0 < s ≤ 1）しか見ていないので、
  **両方に足さないとペインからは 400 ではなく「失敗したジョブ」になる**＝P0 完了条件 (2) が経路に
  よって成立しない。
- **ガイド**: `guide/operate/07-image-engine.{md,ja.md}`（ファミリーごとの挙動を列挙している面）。
- **配備**: 本体 1 つで 20 GB 増える。箱のモデル置き場は NVMe なので容量は足りるが、
  **コールドスタートの同期時間が増える**（実測: 30 GB で 348.9 秒。これは箱の購入込み）。
- **テスト**: `comfy_workflows_test.go` の golden に 2 本、`engine_catalog_test.go` の一致検査、
  `families.test.ts`。

## フェーズ

- **P0** — ファミリー `qwen-image-edit-2509` と決定 2・3・4・5・11・12 のファミリー属性化。
  **Console のファミリーカードもここに入れる**——決定 4 の半分（size 欄を出さない）は
  `sizeOptions`（`families.ts:145`）がカードを引くので、カードが無いとメガピクセル表が残る。
  いまそれが無害なのは寸法欄が `disabled={isEdit}` だからという偶然に依っており、偶然を仕様の
  一部にしない。golden とドキュメント。
  完了の定義は 3 つ: (1) **実機で A を再現**（編集される）、(2) **qwen を名指しした C が 400 で断られる**
  （ブロッキングとキューの両経路で。`strength` を受け取らない）、(3) 🔴 **qwen の行を warm にしたまま `op=generate` が comfy に残る**
  （決定 11 の union が効いている＝課金 provider に落ちない）、(4) **`model` に qwen を名指しして
  `op=generate` を頼むと、モデル名入りで断られる**（決定 13＝名指しは落とさない）。
- **P1** — 2511（決定 6）・部品表（決定 7）・`vram_mib` の導線（決定 8）。
  完了の定義は 4 つ: (1) **1 押しの取り込みで 3 部品が正しいディレクトリに入る**、
  (2) **2511 が実機で編集を成す**（実測 E の再現）、(3) **取り込みの画面と行の「編集」が
  測定値を測定条件つき（寸法・batch・参照枚数・測ったファイル）で出し、運用者が 1 押しで
  入れられる**、(4) 🔴 **入れた値が梯子に効く**——合計の見積り（2509 で 28,676 MiB）ではなく
  実測（20,862 MiB）で段が選ばれる。
  🔴 **「入れれば `confirm_vram` が出なくなる」ではない。** 門（`engineVramGuardRow`）が
  比べるのは選択中のクラスで、梯子が未固定の配備ではその先頭＝T4（14,500 MiB）だから、
  正直な 20,862 を入れても確認は出る（P0 の受け入れで実測）。決定 8 の 🔴 と同じことを、
  フェーズ節にも書く——実装者が最初に読むのはここなので。

  **実機受け入れの結果（2026-09-20）**: 🟢 (1) と (2) は達成、🔴 (3) と (4) は**未達**。
  - (1) 1 押しの取り込み——部品 3 つは正しいディレクトリに入った。ただし**1 押しでは終わらず、
    行の「揃える」がもう 1 押し要った**（部品の follow-up が黙って落ちた。その継ぎ目は
    ADR 0085 側で塞いだ＝PR #802/#804/#806）。
  - (2) 2511 の編集——`POST /imagegen/generate`（ブロッキング経路）が 200 / 823.8 秒、
    看板だけが CLOSED になり他は不変＝**実測 E の再現**。
  - (3)(4) は決定 8 の 3 つ目の 🔴 のため**成立しなかった**: 測れた 28,358 MiB は L40S 上の値で
    表に入れられず、入れられないので梯子も動かせない。**2511 を L4 で測り直すまで P1 は閉じない。**
- **P2** — props（決定 10）。完了の定義: 生成ペインからの 1 枚で「この絵の設定」にプロンプト・
  negative・寸法が出る。
- **P3** — 参照画像 2 枚目・3 枚目の導線（ペインと MCP の `inputs`）。
  完了の定義: 実測 D をペインから再現できる。

## 未解決

1. **2511 の 40 steps は高い**（実測 393.8 s）。Lightning LoRA（4 steps）を行として入れたときの
   品質は測っていない。**P1 では測らない**（利用者の裁定）ので、未解決のまま残す。
2. **inpaint を名乗れるか**（決定 3 で見送り）。`SetLatentNoiseMask` を denoise 1 の指示編集に足した
   ときの挙動は未測定。
3. **ホスト RAM 16.1 GB は薄い**（空き 2.2 GB）。2509 と 2511 を両方有効にした箱で、載せ替えが
   続いたときの挙動は測っていない（実測では E が 1 回だけ載せ替えた）。
4. **決定 9 の引き金**（トポロジ 3 つ目／宣言 15 か所）は、12 か所という実数からは引いたが、
   「15」そのものは測った数字ではない。次にファミリーを足すときに宣言箇所を数え直す。
   🔵 **数え直した（2511 を足しながら実測）＝17 か所。**

   | どこ | 数 | 宣言箇所 |
   |---|---|---|
   | Agent | 9 | `comfyFamily` の定数 / `comfyFamilies` / `comfyBuildGraph` の switch / テンプレートの入口 / `comfyQwenEditWirings` / `comfyFamilyRecipes` / `comfyFamilyInstructionEdit` / `comfyFamilyKnobs` / `comfyTrialSteps` |
   | CP | 4 | `engineComfyFamilies` / `engineComfyRequiredFlags` / `engineFamilyUpstreams` / `engineFamilyParts` |
   | Console | 3 | `wire.ts` の `Family` / `FAMILY_CARDS` / `families.test.ts` の一覧 |
   | golden | 1 | `testdata/comfy_<family>.golden.json` |

   起草時の 12 が数え落としていたのは 5 つ: 定数そのもの・テンプレートの入口・
   `engineFamilyUpstreams`（**無いと `TestFamilyUpstreamsCoverTheVocabulary` が赤**）・
   `families.test.ts`・（P1 で増えた）配線の表。P1 の実装では `comfyFamilyInstructionEdit`
   （ops・strength・sizes・negative の 4 か所を 1 つに畳む）を入れて **20 → 17** に抑えた。
   これは同じ能力の族が続く限りの延命で、次の**別能力の**トポロジには効かない。

   🔴 **決定 9 の 2 つ目の引き金（「12 か所が 15 を超える」）は、これで引かれている。**
   決定 9 の 🟡 が挙げた中間段（ファミリー＝データ行）を別 ADR で起こすかどうかは利用者の判断で、
   **まだ起こしていない**。
5. **`vram_mib` を運用者が 1 回入れる**（決定 8）のは、取り込みの直後に画面が要求しない限り
   忘れられる。取り込み UI のどこに測定値を出すかは P1 で決める。
6. 🔴 **2511 を L4 24GB で測り直す**（P1 の積み残し）。L40S での 28,358 MiB は退避が起きない
   条件の値なので使えない（決定 8 の 3 つ目の 🔴）。測るには `vram_mib` を仮説として先に
   宣言して 22,000 の段を候補に戻す**逆順**が要り、載らなければ仮説が誤りだと分かる。
7. 🔴 **測定条件に「どのカードで測ったか」を含める**。`FamilyVramMeasurement` は寸法・batch・
   参照枚数・ファイル名を持つがカードを持たない。2509 の 20,862（L4）と 2511 の 28,358（L40S）
   が並ぶと、**同じ欄の値に見えて別の問いの答え**になる。欄を足すか、表の契約を
   「この族が**最小で**要る量」に書き換えるか、どちらかが要る。

## 実測（2026-09-20・開発配備・g6.xlarge / L4 24GB）

グラフは公式テンプレート（サブグラフ）を API 形式に写した 10〜12 ノード。UI 専用ノード
（`ComfySwitchNode` / `Primitive*`）は畳んだ。ノードの実在は動いているエンジンの `/object_info` で
確認した（`CLIPLoader` の `type` に **`qwen_image` が在る**——28 種のうちの 1 つ。
`TextEncodeQwenImageEditPlus` / `FluxKontextImageScale` / `CFGNorm` も在る）。エンジンは
**ComfyUI 0.35.2**（ピン留めのとおり）。

- **L4 24GB に載る。** `/system_stats` の生値で `vram_total` 22,563 MiB・`vram_free` 1,701 MiB＝
  **使用 20,862 MiB**（1024²・batch 1・参照 1 枚）。VRAM の門は 28,676 MiB を要求と判定するが、
  実際にはテキストエンコーダがエンコード後に退避されるので載る（決定 8）。
  ⚠️ `comfyMaxBatch` は 4——**その範囲は測っていない**。
- **出力寸法の規則はノード定義から**であって、この 5 本からではない: 入力も出力も 1024² だけで、
  `PREFERRED_KONTEXT_RESOLUTIONS` の比率表は**正方形以外を試していない**。
- **`comfyFamilyRecipes` の 4 欄は公式テンプレートの KSampler の widget から取る**——どちらのファミリーも
  `sampler=euler` / `scheduler=simple`、steps と cfg は switch の **false 枝**（LoRA 無しの側）で
  2509 が **20 / 4**、2511 が **40 / 4**。実測 A・E はこの値で回した。
  ⚠️ 2509 の公式テンプレートは switch の既定が **true**＝4 steps / cfg 1 の Lightning 経路である。
  ファミリーの既定に採るのは**LoRA 無しの側**（却下した案の「Lightning を既定にしない」と同じ理由）。
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
- 🟡 **決定 4 は行の `sizes` と Console の既定表に負けていた**（`comfy.go:576` / `families.ts:145`）。
- 🟡 **決定 9 の引き金「ファミリー 12」は軸が合っていなかった**（編集と無関係なファミリーで発火する）。宣言の
  重複量に変え、中間段（テーブル駆動テンプレート）を候補として書いた。
- 🟡 **コードの名前 3 つが違った**: `op` の enum は `mcp_imagegen.go` ではなく `mcp_stdio.go:1132`、
  参照画像の引数は `images` ではなく `inputs`、寸法を読むのは `textBehind` ではなく `props.go:411`。
- 🟡 **影響に 5 つ抜けていた**: `comfyTrialSteps`（書かないとテストが赤）・`comfyFamilyRecipes`・
  `wire.ts` の `Family` 型・`families.test.ts`・`guide/operate/07-image-engine`。
- 🟡 **決定 5 は決定 3 と規則が逆だった**: 「3 枚目は同じ機構だから開ける」は、inpaint に対して
  禁じた推論そのもの。**2 枚に留め、3 枚目は P3 で測ってから**に改めた。

### 2 巡目（同じセッション・`a2ba1b22` に対して）

反映は 9 件中 8 件が意図どおり。残った 🔴 3 件はどれも「決定は正しいが、**経路が 1 本足りない**」形だった。

- 🔴 **決定 11 の union だけでは P0 完了条件 (3) は緑にならない。** モデル未指定の要求は warm な行に
  解決されてから op で落ちるので、候補に残しても**次の provider に落ちる**。モデル解決が `req.Op` を
  見る、を決定 11 に足した。
- 🔴 **決定 1 の綴りが旧いままだった**（ja だけ `qwen-image-edit`）。実装者が最初に読む所なので直した。
- 🔴 **決定 5 の `MaxInputs=2` は宣言だけでは動かない**（アップロードは 1 枚・`comfyParams.Image` は単数）。
  **P0 は 1 枚のまま**にし、2 枚目は経路ごと P3 へ移した。「宣言と経路は同じフェーズに入れる」。
- 🟡 P3 の引数名が `images` のまま／`comfyFamilyRecipes` の sampler・scheduler の出典が無い／
  P1 の完了条件が決定 8 と噛み合っていない、の 3 件も直した。
- 🟢 **VRAM の数字はレビュー側が撤回し、こちらの 20,862 MiB が正しいと確認された。** 裏取りも増えた:
  `PARAMETERS-60-engines.md:659` が「`Total VRAM 22563 MB`, so **22000 is the number**」と書いており、
  段の 22,000 は実測と同じ出所から来ている。

### 3 巡目（`8d37d827` に対して）

残った 🔴 は 1 件で、**union を置く場所**だった。

- 🔴 **決定 11 の union を「広告」として書いたのは、置き場所を消費者側で定義していた。** 手本にした
  negative の先例はルート側（`http.go:242-247`）の union なので、文字どおり真似ると `capsOf`
  （`imagegen.go:842`）は warm な行のままで、`op=generate` は**候補選びの時点で** comfy を落とし、
  3 つ目の箇条（`Generate` 内の op を見た解決）に到達しない。union は
  **`comfyProvider.Caps("")` そのもの**に置く、と関数名で書き直した。1 か所で広告と候補選びの両方が
  直り、名指しの `Caps(model)` は厳密なまま。
- 🟡 決定 8 の本文が決定 5 の後退（P0 は 1 枚）に追随していなかった／影響の Agent 行に 2 枚目の経路
  （`comfyParams` の複数形化・`uploadImage` の複数回・`image2` の配線・拒否文の単数形）が無かった。
  どちらも直した。`comfySwitchWarning` の行番号も 632 → 633 に。

### 4 巡目（`f0c4a79a` に対して）

🔴 は 1 件で、**union の代償**だった。`Caps("")` を union にすると `st.Ops` / `st.Strength` は
provider 単位の「どれか 1 行ができること」になり、**どのモデルにも当てはまらない値**になる。ところが
`modelStatus` には `ops` も `strength` も無いので、ペインは「qwen を選んでいるときだけ滑り台を隠す」を
書けない。**per-model の信号 2 つ（`Knobs` に `strength`・`modelStatus` に `ops`）を決定 12 に足した**。
`providerStatus.Strength` のコメント（`http.go:108-112`）が「No union is needed: it is per provider,
not per model.」と書いており、決定 2 と 11 がそれを嘘にすることも影響に入れた。

🟢 レビュー側が「`Caps("")` を union にして壊れる読み手」を全部当たった結果も記録しておく:
モデル未指定で `Caps` に来るのは 5 か所（`http.go:217` / `imagegen.go:842` / `:821` / `:856` /
**`jobs.go:486`**——最後の 1 つはこの ADR が名前を挙げていなかった読み手）。

⚠️ **このときの「落ちる警告は無い」は誤りだった。** `strength` が per-model になった分を数え落として
おり（負側は正しかった）、後ろ 2 つ＝警告の経路は union を読んではいけない読み手だった。P0 の実装は
そこを `Caps(res.Model)` に変えて閉じている（決定 2）。**いま union を読むのは広告（`http.go:217`）と
候補選び（`imagegen.go:842` / `:821`）の 3 か所だけ**で、警告の 2 か所は読まない。

### 5 巡目（`a1583944` に対して）

**🔴 は無くなった。** 残った 🟡 3 件はどれも「影響の粒度」で、3 件とも直した。

- 🟡 **ワイヤを足すと直る場所がもう 3 つあった**: `wire.ts:35` の `Knob` は閉じた union なので型ごと
  直さないとコンパイルが通らず、`ImagegenModel` に `ops` が無い。語彙を書いたコメントも
  `comfy.go:267-276` と `http.go:161-164` の 2 つが残っていた（影響に書いていたのは
  `http.go:108-112` だけ）。
- 🟡 **決定 2 の 400 は置き場所が 2 か所**（`http.go:465` と `jobs_http.go:115`）。片方だけだと
  ペインからは「失敗したジョブ」になり、P0 完了条件 (2) が経路によって成立しない。あわせて
  **「解決後のモデルがこのファミリーのときだけ 400」**（provider 未指定の要求は comfy に来るとは
  限らない）も決定 2 に明記した。
- 🟡 日英不一致が 1 か所（ja の Console 行に `GenerateForm.dom.test.tsx` が無かった）。

🟢 レビュー側が「ペインが per-model にしたい面」を全部当たった結果、**`ops` と `strength` の 2 つで
尽きている**ことも確認された（size は `modelStatus.Sizes`、negative は `Knobs`、LoRA は
`loraStatus.baseModel`、seed / aspect ratios / samplers はファミリー非依存、`MaxInputs` は決定 5 の
⚠️ で既に P3 送り）。

## P0 の実装（2026-09-20・develop `f57e82dd`）

決定 1〜5・11・12・13 が入った（#773 と #775）。**実機未検証**——完了条件 (1)〜(4) は配備待ち。
主な継ぎ目の住所だけ残す（P1 以降が最初に読む所）:

- 決定 11 の union は `comfyProvider.capsUnion`（`comfy.go:165`）で、`Caps("")` がそれを返す
  （`comfy.go:121-125`）。
- 決定 13 の固定は `modelOwner`（`imagegen.go:761`）と、それを呼ぶ `Run` の門（`imagegen.go:858`）。
- 決定 2 の断りは `bad_strength_family`（`http.go:479` / `jobs_http.go:124`）＝範囲外の
  `bad_strength` とは**別の符号**にした。決定 4 の `size` も同じ形（`bad_size_family`）。
- 400 にできない経路の警告は **core 側**——`requestWarnings` に「実際に走った行」の Caps を渡す
  （`imagegen.go:922` と `jobs.go:486`）。ADR の初稿が求めた provider 側の双子は #779 で削除された
  （comfy.go −73 行）。
- 決定 12 の op 読み替えは Console の純関数 `remappedOp`（`draft.ts:65`）。

実装レビュー（opus・読み取り専用）は 2 巡: 1 巡目 🔴3・🟡5、2 巡目 🔴 0・🟡 4（すべてコード水準で
実装側に直接渡した）。**ADR が黙っていた穴を 2 件見つけたのはこのレビュー**で、どちらも決定に
なった——決定 13（名指しモデルが課金 provider に落ちる）と、決定 2 の「400 にできない 1 経路」。

## P0 の実機受け入れ（2026-09-20・開発配備）

完了条件 4 つを、**配備した Agent の実経路**（HTTP API 直叩き）で通した。エンジンは ComfyUI 0.35.2、
カードは **L40S**（45,458 MiB・ホスト RAM 33.2 GB）。

| 条件 | 結果 |
|---|---|
| (1) A の再現 | ✅ 看板が `CLOSED`・マグ／テーブル／書体は不変。`provider=image` / `model=zz-exp-qwen-image-edit-2509` / 1024² / seed 42 / **実時間 474 秒**（箱の購入＋30 GB 同期＋初回ロード込み） |
| (2) `strength` の拒否 | ✅ **両経路とも 400 `bad_strength_family`**（`/imagegen/generate` と `/imagegen/jobs`）。文面は「the qwen-image-edit-2509 family fixes its denoise at 1 by construction」 |
| (3) warm でも generate が comfy に残る | ✅ `provider=image` のまま `abyssorangemix2_hard_8832` に読み替え、**agy に落ちない**。切り替え警告も出た |
| (4) 名指し＋できない op | ✅ `imagegen_no_provider`「model … cannot do generate (it can: edit)」・**agy に落ちない** |

ワイヤの per-model（決定 12）も確認した——qwen 行は `ops=["edit"]` / `strength` なし / `sizes` なし、
既存行（SDXL・krea2）は `ops` 3 種 / `strength` あり / `sizes` あり。**対照を取ってから**の確認である。

🔴 **受け入れ中に踏んだ 2 件**（どちらも仕様ではなく手順と門の話）:

- **陽性対照でキュー経路に投げたら、実ジョブが積まれて箱を買った。** 「SDXL 行なら拒否されない」を
  見るのに `/imagegen/jobs` を選んだのが誤りで、受理＝キュー投入＝需要の記録である。**受理を
  見たいだけなら実行まで行かない経路を選ぶ。** なお `DELETE /imagegen/jobs/{id}` は 200 を返すが、
  起床待ちのジョブは**箱が上がるまで `waking` のまま残り**、上がった時点で `cancelled` になった。
- **決定 8 の 2 つの訂正**（決定 8 本文に反映済み）——宣言だけでは確認は消えない／合計の見積りは
  買う箱を一段上げる。

