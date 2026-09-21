# 0098. 画像ファミリーとしての Qwen-Image 2.1 — 生成と編集を 1 本のテンプレートで、そして何も共有しない VAE

[English](0098-qwen-image-21-family.md) | 日本語

- 状態: **proposed、かつ同じ変更で実装済み**（2026-09-21）。コードは木にあり試験も緑だが、
  **GPU では 1 回も走らせていない**——下の前提条件が着地するまで走らせられないので、
  「実装済み」はテンプレートと語彙のことであって絵のことではない。
- 🔴 **前提条件（P0）: ComfyUI のピンを v0.37.0 へ動かすこと。** `TextEncodeQwenImage21` は
  そのタグより前に存在しないので、この族の行はエンジンまで届いて `/prompt` の検証で拒否される。
  そのバンプは別の変更（「フェーズ」を見よ）で、これ無しにこの ADR は 1 つも機能しない。
- 以下の上流の事実はすべて 2026-09-21 に実 API とピン中のエンジンのソースに対して**実測**した。
  測ったものは「実測したこと」節にある。走行ではなく公開グラフから採った数字は、使う場所で
  そう書いてある。
- 番号: `develop` の最大は 0097 で、0098 を取る未マージの PR は無い。
- 関連: [0072](0072-engine-model-catalog.ja.md)（族ごとのテンプレート・`base_model` の
  ディスパッチ・「族は運用者が宣言する」決定 2）/ [0094](0094-instruction-edit-image-models.ja.md)
  （指示編集と、この族が**版ではない**ほうの Qwen-Image-Edit 2 族）/
  [0082](0082-many-image-engines-at-once.ja.md)（単位は provider であって行ではない）/
  [0069](0069-image-generation-providers.ja.md)（新しい語彙が何をもって認められるか）。

## 背景

Qwen-Image 2.1 は 2026-09-20 に公開された。名前は ADR 0094 が足した Qwen-Image-Edit 2 族の隣に
置きたくなるが、中身は「その版」の正反対である。

| | qwen-image-edit-2509 / 2511 | qwen-image-2.1 |
|---|---|---|
| 拡散モデル | 20B MMDiT | 7B single-stream DiT |
| テキストエンコーダ | Qwen2.5-VL-7B | Qwen3-VL-8B |
| オートエンコーダ | Qwen-Image VAE — 16ch・downscale 8 | 自前 — **64ch・downscale 16**・RGBA |
| op | 編集のみ | **生成と編集** |
| エンコードノード | `TextEncodeQwenImageEditPlus` | `TextEncodeQwenImage21` |
| ライセンス | Apache 2.0 | **Qwen Research License — 非商用** |

ADR 0094 決定 6 の規則は「族は**トポロジ**ごとで、版ごとではない」。上の表の 3 つの事実は
それぞれ単独でも新しい族にする理由になる——エンコードノードが違う、オートエンコーダが違う、
既存のどの族も持っていない op の組み合わせを持つ。

静かに壊れるのはオートエンコーダである。`VAELoader` はどちらも受け、`--vae` はどちらの役も
名乗り、CP の部品表は S3 鍵で配る——だからこの族を他 4 族が共有する Qwen-Image VAE に向けても
どこも拒否せず、ノイズに復号される。部品表の鍵を共有せず**新しい鍵**にしたのはそのためで、
あの 4 族がわざわざ共有するように書かれているのの逆を行っている。

## 実測したこと

すべて 2026-09-21、断りが無ければ匿名で。

**上流のリポジトリ。** `Qwen/Qwen-Image-2.1` は **ungated**（`gated: false`）、
`license: other`、`license_name: qwen-research`、タグに `rgba`。`Comfy-Org/Qwen-Image-2.1` が
ComfyUI 向けのミラーで、これも ungated。この配備のローダが読む単一ファイルの分割を配布している。

```
diffusion_models/qwen_image_2.1_bf16.safetensors            13.25 GiB
diffusion_models/qwen_image_2.1_int8_convrot.safetensors     6.76 GiB
text_encoders/qwen3vl_8b_bf16.safetensors                   16.33 GiB
text_encoders/qwen3vl_8b_int8_convrot.safetensors            8.71 GiB
text_encoders/qwen3vl_8b_w4a8.safetensors                    5.88 GiB
vae/qwen_image_2.1_vae_bf16.safetensors                      0.63 GiB
```

⚠️ 同じリポジトリの `text_encoders/qwen3.5_9b_qwen_image_2.1_pe_{t2i,i2i}.int8_convrot.safetensors`
は**テキストエンコーダではない**——プロンプト強化モデルで、公式テンプレート自身のモデル一覧に
載っていない。

**ピン中のエンジン。** `comfy_extras/nodes_qwen.py` を 3 つのタグで読んだ。
`TextEncodeQwenImage21` は v0.35.2（現在のピン）にも v0.36.0 にも無く、**v0.37.0** にある。
ピンに対して測ると、このバンプは既にあるものにとっても安全である: この Agent の 10 本の
テンプレートが出すノード 30 種はどちらのタグにも存在し、それらに触れる定義の変更は無害な
3 つだけ——`CLIPLoader` の type 一覧に `yue2` が増えた、`EmptyLatentImage` の width/height の
**既定値**が 512 → 1024 に動いた（ここのテンプレートは常に両方を明示する）、
`ModelSamplingAuraFlow` に既定 `flow` の任意入力 `sampling` が増えた。`nodes_qwen.py` は追加のみ。

**モデルの形、v0.37.0 のエンジン自身のソースから。** `comfy/latent_formats.py` の
`QwenImage21` は `latent_channels = 64`・`spacial_downscale_ratio = 16`。
`comfy/supported_models.py` は `shift: 0.69`・`memory_usage_factor: 6.0`。`comfy/sd.py:1955` は
この族のエンコーダに `clip_type == CLIPType.QWEN_IMAGE` **かつ** state dict が
`TEModel.QWEN3VL_8B` と判定されたときだけ到達する——つまり CLIPLoader の `type` はここでは
読まれ（krea2 の側であって anima の側ではない）、同じ type の中でこの族を兄弟から分けるのは
**ファイル**のほうである。

**公開されたグラフ。** Comfy Org は `image_qwen_image_2_1_t2i.json` と
`image_qwen_image_2_1_image_edit.json` を同梱している。並べて読むと同じノードの同じ配線である。
どちらの KSampler も **25 steps・cfg 1・euler・simple・denoise 1**。どちらの注記も
「negative_prompt: unused while cfg is 1. Raise it only if you use a negative prompt」と
「公式パイプラインは euler で 40-50 steps 程度。このテンプレートは 25 から始める」と言う。
編集テンプレートが配線するのは **image_1 … image_10** で、注記は「参照画像は最大 10 枚」。
ノード自身は image_1 … image_16 を受ける。

**Civitai。** model-version 3344134（`Qwen Image 2.1 GGUF`）は `baseModel: "Qwen 2"`、AIR urn
`urn:air:qwen2:…`、4.10 / 7.09 / 7.51 GB の GGUF 3 本。絞り込みは、`types=Checkpoint` で
1 ページ目ではなく**全件**:

```
baseModels=Qwen 2  ->  チェックポイント 5 件、全部 Qwen-Image 2.1
baseModels=Qwen    ->  チェックポイント 20 件超、5 アーキテクチャにまたがる:
                       Qwen-Image / Qwen-Image-2512 / Qwen-Image-Edit・2509・2511 /
                       テキストエンコーダである `Qwen-3-0.6B base/anima`
```

Hugging Face では `filter=base_model:Qwen/Qwen-Image-2.1` が派生物を返す（Comfy-Org のミラーと
GGUF 変換がいくつか）。

## 決定

### 決定 1 — 新しい族 `qwen-image-2.1`、ドット付きで綴る

既存テンプレートに対する行の `params` ではなく新しいテンプレートにする。理由は上の表の 3 つ
すべて。綴りは製品のドットを持つ。CP はその文字列そのもので行を検証し、
`engine_catalog_test.go` は Agent のソースからそれを読むので、2 つの写しは 1 つの綴りである。
（その試験の族定数の正規表現にドットを教える必要があった——無いと `qwen-image-2.1` が
`qwen-image-2` として拾われ、存在しないドリフトを報告する。）

### 決定 2 — 1 本のテンプレートが両方の op を担い、違いは latent だけ

公開された 2 本のテンプレートの違いはちょうど 1 本の配線である。生成は呼び手の寸法の
`EmptyLatentImage` から始まり、編集は `TextEncodeQwenImage21` 自身の第 3 出力——リサイズされた
先頭の参照画像に合わせた全ゼロの latent——から始まる。これが「出力は image_1 に従う」の表現形。
テンプレートは両者の間に `ComfySwitchNode` を置くが、この builder は参照画像の有無で選ぶ。
同じ選択で、ノードが 1 つ少ない。

どちらの配線も入れ替えがきかず、どちらの壊れ方も声を出さない。生成にエンコードの latent を
使うと、何を頼まれても 1024² で答える。編集に `EmptyLatentImage` を使うと、条件付けが想定して
いない画布の上に編集を置く——テンプレート自身の注記が「リサイズ後の image_1 の寸法に近づけること。
さもないと編集がずれる」と言っている。

inpaint は未主張のまま。ADR 0072 決定 3 の根拠——マスクは配線できるが、上流の誰も配布しておらず、
ここの誰も走らせていない。

### 決定 3 — この族が「指示編集」と「編集専用」を分ける

これまでは 1 つの述語が 3 つの問いに同時に答えていた。指示編集の族がたまたま編集専用でもあった
からである。この族が反例なので、軸を分ける。

- `comfyFamilyInstructionEdit` は意味を保ち——画像が sampler を条件付け、denoise は 1 固定——
  この族は**真**と答える。`strength` が届かない（ADR 0094 決定 2）のはこれが理由。
- `comfyFamilyEditOnly` が狭いほうの事実で、この族は**偽**。op の組み合わせと「寸法が sampler に
  届くか」が実際に立っているのはこちらの軸である。
- op の組み合わせと参照画像の枚数は述語ではなく表にした。

🔴 寸法の帰結は明記しておく価値がある: **この族は寸法を持つ**。隣の 2 族に揃えて空にすると、
唯一寸法を読む op である生成側から寸法の操作が消える。編集側で寸法が無視されるのは同じだが、
それを言うのは全族が既に共有している img2img の警告であって、半分の場合に嘘になる
「寸法なし」の族単位の宣言ではない。

### 決定 4 — 参照画像は 10 枚。これは**引用**であって実測ではない

ノードは image_1 … image_16 を取り、公式の編集テンプレートは 10 本配線している。この族が宣言
するのは 10。

🔴 隣の 3 のように黙って置かず、名指しで断る。ADR 0094 決定 5 はあの数字を「配線がループだから
もっと通るはず」で上げることを**意図的にしなかった**——実測 F を待った。ここには走行が 1 つも
無いので、正直な根拠は公開された配線であり、それは recipe についてこのリポジトリが受け入れて
いるのと同じ根拠である（sd15・anima・krea2 はいずれも未走行の recipe を引用付きで持つ）。
10 枚目が無視されると分かった走行があればこの数字は誤りで、直す場所は `comfyFamilyRefInputs`。

### 決定 5 — `resolution` は 1024。公開グラフを素通りさせない唯一の数字

`TextEncodeQwenImage21` は参照画像を `resolution × resolution` の画素予算へ、アスペクト比を
保ったまま 32 の倍数に丸めてリサイズする——そして latent 出力を通じて、それが編集の画布になる。
公開された数字は 2 つあって食い違う: ノード自身の既定は 1024、注記は 1024 を「official default」
と呼び、編集テンプレートの昇格ウィジェットは **0**（「各参照を自分の寸法のまま保つ」）である。

0 はテンプレートの作者にとっては妥当で、題材を選べるからである。この経路では妥当でない:
ここは利用者が上げた任意の写真を受けるので、0 は 8000 画素の写真を 8000 画素で encode する——
VAE の encode も視覚トークン数もこの経路の誰も上限を掛けておらず、しかもカードは同時に
15 GiB 級の重みを抱えている。だから「公開グラフの数字を採る」はこのテンプレートの他のすべてで
守り、ここだけ声を出して破る。

### 決定 6 — ライセンスは既存のテナント別承諾に乗せ、`qwen-research` は分類する

Qwen Research License §2a は "FOR NON-COMMERCIAL PURPOSES ONLY" で権利を付与する。新しい仕組みは
作らない: 取り込みは既にテナント別に `license_accepted_by` / `_at` / `_tenant` / `_license` を
記録しており（ADR 0072）、Flux.2 Klein 9B が既にその経路でカタログに入っている。

1 行だけ要ったのは分類器のほうである。`engineCommercialUse` は**語**を読む（"non-commercial"・
"-nc"）が、"qwen-research" はそのどれも含まないので、正しく埋まった行が `unknown` を返していた。
`unknown` は「誰も読んでいない条項」への本物の答えであって（ADR 0072 決定 10）、
「読んだが名前が違う条項」への答えではない。needle は実測した綴りだけで、それより広くしない——
「research」一般の needle は、読んでいない他社のライセンスまで分類してしまう。

### 決定 7 — 部品は Comfy-Org のミラーから、int8 で、**新しい** S3 鍵に

`--clip_l` は `text_encoders/qwen3vl_8b_int8_convrot.safetensors`、`--vae` は
`vae/qwen_image_2.1_vae_bf16.safetensors`、どちらも `Comfy-Org/Qwen-Image-2.1` から。
publisher 自身のリポジトリでなくミラーなのは `Comfy-Org/Qwen-Image_ComfyUI` を既に使っているのと
同じ理由——どちらも ungated で、ComfyUI のローダが読むのは単一ファイルの分割のほうだから。

bf16 でなく int8 なのは、公式テンプレート 2 本の CLIPLoader ウィジェットがそれを名指すからで、
かつ 8.71 GiB と 16.33 GiB は重みの隣に載るか載らないかの差だから——ここでは引用できる選択と
成立する選択が同じ選択になっている。

🔴 VAE の鍵は Qwen-Image VAE を指す 4 族と**共有しない**。共有は誤ったパスより悪い: 再利用の
検査が「もう置いてある」と誤ったバイト列を見つけ、何もダウンロードしない。

### 決定 8 — Civitai の GGUF 経路は範囲外

この作業のきっかけになったモデルページは GGUF 変換を 3 本配布している。この配備はそれを
読み込めない: **ComfyUI-GGUF はエンジン画像に焼かれていない**し、それは意図的である
（`deploy/aws/ecs/comfyui/Dockerfile`）——ピン留めしてゴールデンで守るエンジンは実行時に
カスタムノードを入れない。だからこの族の GGUF 量子化ラダーは、存在しないローダへ向かう梯子に
なる。取り込みの経路は safetensors のミラーであり、image 役に GGUF 対応を足すのはそれ自身の
供給鎖の議論を持つ別の決定である。

### 決定 9 — 族の推測は `Qwen 2` を全体一致で取り、`Qwen` には依然として規則を置かない

`Qwen 2` は**全体一致**（`engineFamilyRule.equal`、anima と同じ機構）。部分一致にすると正規化後の
`qwen2` が `Qwen 2.5` と `Qwen2-VL` を飲み、そのたびにこのテンプレートが読めない行の
`base_model_missing` を黙らせる。

実測がそのまま論拠であり、それは ADR 0094 が仮定のまま残した問いにも決着をつける。
`baseModels=Qwen` は「Qwen-Image-Edit の文字列」ではない——テキストエンコーダを含む 5 つの
アーキテクチャが入った雑多な引き出しである。だから `Qwen` は意図的な不在を保ち、
チェックポイントの全件がちょうど 1 アーキテクチャである `Qwen 2` が規則と
`engineFamilyUpstreams` の項を得る。

⚠️ 残るリスクは、**将来の** Qwen-Image が同じ `Qwen 2` で公開されること。そのとき推測は
読み込めないトポロジに 2.1 のテンプレートを勧める。これは `Flux.2 Klein 9B-base` と同じ形の罠で
（ADR 0072 の follow-up）、needle では先回りできず、答えになるのはこの表を信じることではなく
測り直すことである。

## 却下した案

### qwen-image-edit-2511 の行にする／配線フラグ付きの共有テンプレートにする

2 つはファイルもエンコードノードもオートエンコーダも共有しない。`params` にはそのどれも表現
できず（ADR 0072 の `params` の語彙は sampler の 4 欄）、違いごとに分岐を持つ共有 builder は
1 つの関数に入った 2 本のテンプレートである。

### 編集テンプレートに倣って `QwenImage21Cache` を出す

あちらでのウィジェットは `auto` / `default` で、モデルは
`transformer_options.get("qwen_image21_cache", {})` を読む——ノードが無いとき device は `auto`、
dtype は `default` に落ちる（`comfy/ldm/qwen_image21/model.py` の `select_prefix_cache`）。
つまりその値でのノードは証明可能に no-op で、しかも `is_experimental` である。出せば
v0.37.0 以降にしか存在しないノードを増やして何も変わらない。

### 両テンプレートに倣って `SaveImageAdvanced` を使う

`SaveImage` はこのモデルが出すアルファを保つ——生の配列をそのまま PIL に渡すので、4ch の絵は
アルファ付き PNG として書かれる（`nodes.py` の `save_images`）。加えて `SaveImage` がやり、
この経路が依存しているのが、`readImageProps` が読む `prompt` PNG チャンクを書くことである
（ADR 0094 P2）。テンプレートとの見た目の一致のために保存ノードを替えるのは、動いている props
経路を何も得ずに手放すことになる。

### `op=edit` の寸法を拒否する

ADR 0094 決定 4 の `comfySizeRefusal` と対称に見えるが、ここでは誤り: この族は寸法を広告するので
ペインは寸法の選択欄を描き、値で拒否するとペインが送る編集を全部拒否することになる。正しい継ぎ目
は既存の img2img 警告で、それは既に鳴る。

⚠️ その警告は**入力画像自身**の寸法を名乗るが、この族は `resolution` の予算にリサイズする——
つまり報告される数字は近似である。0094 の族でも近似である（FluxKontextImageScale がリサイズ
する）ので、これは新しい不正確さではなく既存のものであり、この変更の中で直さず「未解決」に置く。

## フェーズ

- **P0 — ComfyUI のバンプ（別の変更）。** `COMFYUI_REF` を v0.35.2 → v0.37.0 にし、
  `.github/workflows/comfyui-image.yml` で焼き直す。静的な側の確認は済んでいて「実測したこと」に
  ある。それが覆えないのは既存 10 族を載せた GPU での実際のコールドスタートで、それはその変更
  自身の受け入れ条件である。
- **P1 — この変更。** 語彙・部品表・族の推測・ライセンス分類・Agent のテンプレートとゴールデン・
  Console のカード。ハードウェアには一切触れていない。
- **P2 — 実機受け入れ。** パネルから行を取り込み、op ごとに 1 枚ずつ撮る。完了の定義: 寸法を
  名指した `op=generate` がその寸法を返し、参照 2 枚の `op=edit` が指示に従いつつ 1 枚目の枠を
  保った絵を返すこと。
- **P3 — 走らせないと出ない数字。** `engineFamilyVram` 用の `vram_mib` を、ADR 0094 P1 が学んだ
  逆順で取る（仮説を先に宣言して狙う段を候補に残す。さもないとファイル合計の床が大きいカードを
  買い、そのカードで測ることになる）。int8 の組は合計約 16.1 GiB で、何も測らなければ床は
  22,000 の段の近くに来る。

## 影響

- 語彙は 11 族になり、その 4 つの写し（Agent の定数・CP の `engineComfyFamilies`・Console の
  `wire.ts`・Console の `families.ts`）すべてに新しい語が入った。このうち 3 つは
  `engine_catalog_test.go` が互いに固定しているが、**`families.ts` は対象外**で、4 つ目の写しは
  この ADR だけが保つ（ADR 0072 からの継続注記）。
- `comfyFamilyOps` と `comfyFamilyMaxInputs` は表になった。定数に足してここを忘れた族はエラーに
  ならず寛容な既定（op 3 つ・参照 1 枚）を得る。これは `comfyTrialSteps` と同じ形で、同じやり方
  ——語彙全体を舐める試験——で守る。
- エンジン画像のピンが、カタログの語彙に入った族にとって効くものになった。P0 が着地するまで、
  運用者は `qwen-image-2.1` の行を登録して有効化でき、どの画面もその行を「揃っている」と言う。
  落ちるのは `/prompt` で、ノード種別のエラーとして声を出して落ちる。
- `engineCommercialUse` はライセンス名に `qwen-research` を持つ行すべてに `no` と答える。既存の
  Qwen-Image-Edit の行にも届くが、あちらは Apache 2.0 なので影響は無い。今後 research ライセンス
  で公開される Qwen のモデルは、もう 1 回の変更無しで分類される。

## 未解決

1. **何も走らせていない。** recipe も参照枚数も shift も編集経路全体も、公開グラフからの引用で
   ある。ゴールデンが固定するのはグラフの**形**で、それは SD3.5 が 1 枚も出せないまま通っていた
   主張とまったく同じ強さしかない（ADR 0072 P2 残作業 5）。
2. **img2img の寸法警告が入力自身の寸法を報告する。** `resolution` のリサイズ後に実際に絵が
   作られる寸法ではない。既存の問題で、0094 の族と共有している。
3. **native 2K を触っていない。** テンプレートの注記は 2048² を直接出せると言い、そのために
   4 メガピクセルの ResolutionSelector を勧めている。`comfyMegapixelSizes` は 1024 級で止まるので
   利用者は頼めない。この族の寸法一覧を広げるかどうかは、走行が先に答えるべき問いである。
4. **RGBA が端から端まで未検証。** モデルはアルファチャンネルを書き `SaveImage` はそれを保つが、
   エンジンより後ろ——props の読み手・ギャラリー・ミラーのカード——は透過付き PNG に対して
   1 度も確かめていない。
5. **プロンプト強化モデルを使っていない。** `qwen3.5_9b_…_pe_{t2i,i2i}` はエンコーダの隣で配布
   され、公式パイプラインの文書化された一部である。ComfyUI のテンプレートには入っておらず、
   ここでも何も宣言していない。
