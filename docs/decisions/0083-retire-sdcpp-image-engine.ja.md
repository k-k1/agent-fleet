# 0083. sdcpp 画像エンジンを退役させる — 画像の実装を 1 つにし、serve できない行は声を出して言う

[English](0083-retire-sdcpp-image-engine.md) | 日本語

- 状態: **proposed**（2026-09-14）
- **この文書のために何も実測していない。** すべての記述は 2026-09-14 にこのリポジトリのコードと
  テンプレートから読んだ事実（file:line は「確認したソース」）で、例外は運用者が述べた 1 点 —
  **稼働中のどちらの配備も使っていない**（sandbox の借用行は `image (images/comfy)`、acrt でも
  sdcpp は使っていない。運用者・2026-09-14）。
- 決めること: stable-diffusion.cpp は最初の画像エンジンで
  （[ADR 0071](0071-self-hosted-inference-engines.ja.md) P1）、
  [ADR 0072](0072-engine-model-catalog.ja.md) 決定 4 がその隣に ComfyUI を「選択」として足した。
  その選択には答えが出た（sdcpp を動かしている配備は無い）ので、問いは「2 本目の経路の維持費を
  払い続けるか」と「退役の途中で何が壊れるか」である。

## 背景

### sdcpp とは何で、木に何が残っているか

`sd-server` プロセス 1 本が **1 つ**の checkpoint を起動フラグで選んで持ち、OpenAI 互換の
`/v1/images/…` を話す。ComfyUI が置き換えた理由は ADR 0072 決定 4 が記録している: 複数の
checkpoint を持ち**要求ごとに**切り替えるので、`model` が「配備について固定された事実」ではなく
本当の選択になる。

残っているのは provider のファイル 1 枚ではない。この「種類」は **4 箇所**で宣言されていて、
しかも 4 つの言語である。

| どこ | 何を言っているか |
|---|---|
| `workspace/agent/internal/imagegen/imagegen.go:504-509` | `providerRanks` — Agent の組み込み順、`sdcpp` が先頭 |
| `console/src/lib/settings.ts:733-745` | `IMAGE_PROVIDERS_RANKED` — 設定の並べ替え UI 用に同じリストをもう一度 |
| `console/src/features/imagegen/wire.ts:315` | `FLEET_PROVIDERS = ["comfy", "sdcpp"]` — 画像生成ペインがどの provider を駆動するか |
| `deploy/aws/ecs/cfn/60-engines.yaml:111-127` | `ImageEngine`、**`Default: sdcpp`**、加えて `ImageSdTag` と sd-server の追加フラグ |

🔴 **出荷されている既定がいまも退役エンジンである。** パラメータファイル無しで今日 stand-up
すると `sdcpp` で立つ（`60-engines.yaml:113`）。`ImageEngine=comfy` は 0.18.0 からの opt-in。

イメージは自前で焼いていない。`standup.sh` が `ghcr.io/leejet/stable-diffusion.cpp` を crane で
`af-sdcpp` の ECR に写すだけ（2.31 GB。しかも checkpoint を置いたときだけ。
`standup.sh:399-422`・`20-platform.yaml:103-113`）。

### 🔥 `sdcpp.go` は共有のエンジン輸送層の住所でもある

作業順を決めるのはこの事実である。`comfy.go` は sdcpp 名の補助関数を少なくとも 8 箇所で呼んで
いる（`sdcppURL`・`sdcppErrText`・`sdcppRetryable`・`sdcppGaveUp`・`sdcppClient`・
`sdcppTimeout`・`engineHTTPAttempt`）。さらに**どの画像エンジンも使う型**もこのファイルにある:
`EngineConn`・`EngineLookup`・`engineLookupFor`・`EngineParams`・`EngineLora`・`EngineFile`・
`EngineLicense`（`sdcpp.go:57-235`）。ファイルのおよそ 4 割は sdcpp ではなく、comfy が走っている
道そのものである。

ファイルを消せば ComfyUI はコンパイルを止める。これは管理すべきリスクではなく、従うべき順序で
ある。

### provider をただ消した場合に見える壊れ方

エンジン表の行は、その `provider` 文字列を名乗る provider が serve する
（`workspace/agent/engines.go:435-441`）。sdcpp provider を消すと、まだ `sdcpp` と書いてある行を
名乗る者が居なくなる: provider は永久に `Ready` にならないので、そのセッションで
`generate_image` が **`tools/list` に出ない**だけになる。エラーも、ログも、パネルの印も無い —
ADR 0082 の背景が「モデル行の無い `AF_ENGINES_JSON` の行」について記録したのと同じ沈黙である。
`ImageEngine` に触らずに更新した配備は、この退役を「画像生成が消えた」として体験する。

## 決定

### 1. `image` 役の実装は、この fleet が出荷する限り comfy 1 つだけ

`ImageEngine` は `AllowedValues: [comfy]`・`Default: comfy` になり、`ImageSdTag` と sd-server の
追加フラグは消える。`image` のタスク定義とエンジン表の行にある `!If [ImageIsComfy, …]` は comfy
側に畳まれる。provider・その `Caps`・サイズの推測・要求と応答の形も一緒に消える。

ADR 0072 決定 4 が stack の**形**について記録した理由付けはそのままで、いまも正しい（1 役・
1 タスク定義・1 サービス）。消えるのはその中の分岐である。

### 2. 🔥 共有の輸送層を**先に抽出**する。その変更は何も削除しない

P0 は純粋な移動である: 背景に挙げた型と retry/URL/error の補助関数を
`workspace/agent/internal/imagegen/engine.go` へ、エンジン中立な名前で移す（`engineURL`・
`engineErrText`・`engineRetryable`・`engineGaveUp`・`engineClient`・`engineTimeout`）。16 分の
予算がなぜその値なのかを言っているコメントも一緒に運ぶ。移動の前後で試験は緑のままで、その後で
初めて削除に入る。

書き留める理由は、自然に見える順序が誤りであることと、provider 2 ファイルに触る改名が「削除に
畳める種類の変更」に見えることである。畳むと、その削除は誰もレビューできなくなる。

### 3. この build が serve できない provider の行は、**声を出して**拒む

Control Plane はレジストリ構築時に行ごとに 1 回ログを出し、admin パネルはその行を「serve 不能」
として、名乗られた provider 名とともに印を付ける。Agent 側の解決も、未知の images provider の
カタログ行が通り過ぎたときに自分のログで同じことを言う。

黙った `Ready() == false` にはしない。この ADR が単なる削除以上である理由は上の壊れ方そのもの
で、stack がまだ `sdcpp` と言っている運用者は、**利用者からの「ツールが消えた」という報告では
なく**、既に開いているパネルからそれを知るべきである。これは誰にも制御できない場合も覆う —
決定 8 を見よ。

### 4. Control Plane の `provider == "comfy"` 分岐は**条件のまま残す**

上流のパス接頭辞（`engine_gateway.go:949-956`）、ファイル flag の語彙
（`engine_catalog.go:231-234`）、`base_model` の族語彙（`engine_catalog.go:239-246`）は、comfy が
唯一の種類になれば無条件にできそうに見える。してはならない。

「images ⇒ comfy」は**この配備の stack が買うもの**については真だが、**行が言えること**について
は真ではない。`external` 行は運用者が表に書いたものそのままであり（ADR 0076）、`remote` 行は far
の fleet が宣言したものそのままである（ADR 0079 決定 2）— `sdcpp` も、まだ存在しない provider も
含めて。3 つを無条件にすると、ComfyUI ではない行に ComfyUI のグラフ接頭辞と ComfyUI の族語彙を
渡すことになり、族の側は宣言時ではなく数分後の生成時に落ちる。

### 5. 別名も保存順序の移行も要らない — 既存の規則が仕事をする

この変更より前に保存された `imageProviderOrder` は `sdcpp` を名前で持っている。id が語彙から
抜けると `normalizeImageProviderOrder` はそれを未知として落とし、`comfy` が「保存値が一度も名前
を挙げなかった id」になる。そして**無名の fleet provider は先頭に置かれる**
（`console/src/lib/settings.ts:767-800`、`imagegen.go:553` の `effectiveOrder` が鏡）。つまり
`["sdcpp","agy","codex"]` は `["comfy","agy","codex"]` に正規化される。移行のために何も書かずに、
それが正しい答えである。

これは ADR 0072 の 2026-09-11 追試が実測した事故（`comfy` が存在する前に保存された順序が `auto`
を個人プラン 2 つへ先に通した）の**不在**が原因だったのと同じ規則である。provider が増えるために
作られた規則が、減るときにもちょうど正しい。

設定リストの 1 行畳み込み（`IMAGE_PROVIDER_FLEET_GROUP`。2 つの種類が同じラベルを持っていたので
足した）は fleet の id が 1 つになると何もしなくなるので、同じ変更で削除する。

### 6. 4 つの宣言を 1 回の変更で動かす

決定 1 のテンプレート、Agent の `providerRanks`、Console の `IMAGE_PROVIDERS_RANKED`、画像生成
ペインの `FLEET_PROVIDERS`。1 つの事実の 4 つの写しで、CI はどれも比べていない。4 つのうち 3 つ
だけ直すと、設定リストが差し出し・順序が尊重し・しかし誰も serve できない id が残る。
[ADR 0082](0082-many-image-engines-at-once.ja.md) 決定 3 がリストをエンジンの行から引くことで
この二重化を根絶する。この ADR はそこへ向かう途中で写しを半端に残さないことだけを負う。

### 7. ECR リポジトリは消す。ただし 2 つの stack 更新の**順序は強制される**

`60-engines.yaml:785` が `${PlatformStackName}-EcrSdcppUri` を import し、
`20-platform.yaml:464` がそれを export している。CloudFormation は他の stack がまだ import して
いる export の削除を拒むので:

1. **先に 60-engines** — export が残っているうちに import と sdcpp 側の枝を落とす。
2. **次に 20-platform** — `EcrSdcpp` とその出力を落とす。

逆順だと platform の更新が失敗してロールバックする。リポジトリには既に
`EmptyOnDelete: true` が付いている（`20-platform.yaml:112`）ので、2.31 GB のイメージも一緒に
消え、手で空にする作業は無い。

### 8. sdcpp の image 役は借用できなくなる。そしてそれを言う

ADR 0079 決定 2: どのエンジンが存在し何を話すかは **far 側**が宣言するもので、こちらは決して
推測しない。したがって、まだ sdcpp を動かしている far の fleet が、この配備では serve できない
image 役を差し出し得る。それはこちらからどうにもできない（他人の stack である）ので、この ADR が
負うのは起動メニューの静かな穴ではなく、決定 3 の拒否である。

## 却下した案

- **既定だけ変えてコードは残す。** テンプレート 1 行で、いちばん悪い事実（まっさらな stand-up が
  退役エンジンで立つ）は確かに直る。答えの全体としては却下: 4 つの語彙・2 つの要求形・ECR
  リポジトリ・`standup.sh` の 2.31 GB の写しが残り、そして高い部分 — **誰も動かしていない
  エンジンの名前が付いたままの共有輸送層**が残る。それは ADR 0082 の P0 が自分の変更の途中で
  やらざるを得なくなる改名である。
- **`sdcpp.go` を消して何が壊れるか見る。** ComfyUI がコンパイルを止める（背景）。「provider の
  ファイルを消す」がこの作業の自然な読みなので挙げておく。
- **将来の第三者サーバのための継ぎ目として OpenAI 互換の画像経路を残す。** これはこの決定の
  本当の代償で、否定ではなく**受け入れ**である: これ以降 fleet が serve する画像 API の形は
  ちょうど 1 つになり、A1111 風や OpenAI 互換の画像サーバを足すときは要求と応答の半分を作り
  直すことになる。残るのは輸送層（決定 2 の抽出ファイル）と `/v1/` の前置（llamacpp が使い
  続ける）。試すエンジンの無い継ぎ目はその場で腐るし、2 つめの形を足す場所としては ADR 0082 の
  行ごとの provider のほうが良い — 足すものが実際に現れたときに。
- **`sdcpp` の行は早く拒まず、生成時に分かりやすいエラーで落とす。** 利用者は既に待っており、
  まだそれを宣言している配備ではそのメッセージが絵 1 枚ごとに来る。決定 3 は運用者が行動できる
  場所に置く。

## 覆す決定・維持する決定

- **ADR 0072 決定 4 の「選択」を覆す。** 「sdcpp か comfy」には答えが 2 つあったが、いまは 1 つ。
  その決定の残り（1 役・1 タスク定義・1 サービス、そして 2 つめの container/service の組が何も
  買わない理由）はそのまま。
- **ADR 0071 P1 の画像エンジンを覆す。** sdcpp が最初の 1 つだった。それに対して採られた実測
  （冷間起動・GPU の段・16 分の予算）は意味を保つ: あれは GPU の箱と S3 の取得の実測で、あの
  バイナリの実測ではない。
- **ADR 0069 決定 3 を維持** — fleet 自身のハードが利用者個人のプランより前に来る — と
  **決定 11**（プロンプトがどこへ行ったかを言う `destination`）。
- **ADR 0076 と ADR 0079 はそのまま維持。** 上の決定 4 は、まさにそれらを真のままに保つために
  存在する。
- **ADR 0082 の P0 より前に着地させるべき。** 0082 は provider id をエンジン行のキーにする変更
  で、種類が 2 つより 1 つのほうが小さい。0082 決定 8 が「過渡的」と呼んだ設定リストの畳み込みも
  二度書きにならない。

## 未解決（測ってから決める）

1. **この 2 配備の外に `ImageEngine=sdcpp` を動かしている人は居るか。** こちらは居ない（運用者・
   2026-09-14）が、公開したどのリリースでもそれが**既定**だったので、リリースノートは「変える
   べきパラメータ名」と「変えなかった場合に何が起きるか」（決定 3 の拒否）を 1 文で負う。他人の
   stack をここから列挙する手段は無い。
2. **決定 3 のパネルの印は独自の行状態に値するか、既存の「off」の形で運べるか。** serve 不能な行
   は off ではない（管理者が選んだのではない）。違いが効くのは、新しい語彙を増やさずにパネルが
   それを出せる場合だけ。
3. **例として sdcpp を名指しているコメント群は書き換えか削除か。** 「cancel を持たない provider
   （sdcpp）」（`jobs.go:734`）、タイムアウトの連鎖（`mcp_imagegen.go:141`）、モデルの union
   （`mcp_stdio.go:1256`）のように、能力を欠く provider を名指して規則を説明しているものがある。
   いくつかは木の中に例が 1 つも残らなくなる。それは規則を残す価値がある印であり、文を削るのでは
   なく書き換える理由である。

## フェーズ

- **P0 — 抽出。** 決定 2 だけ: 共有の型と輸送層をエンジン中立な名前で `engine.go` へ。完了条件は
  両方の Go スイートが挙動変更なしで緑（両モジュールで `go test ./...`・`-count=1`）。
- **P1 — コードの削除。** provider とその試験、`http.go` の `ProviderSdcpp` の分岐、
  `providerRanks`、Console の 2 つの語彙、設定の畳み込み（決定 1・5・6）、加えて決定 3 の拒否を
  両側に。完了条件は、`sdcpp` を宣言した表の行が CP のログ 1 行とパネルの印を生み、その隣の
  comfy 行では `generate_image` が引き続き提供されること。
- **P2 — テンプレート。** 決定 1 のパラメータと決定 7 の 2 つの更新をその順序で。加えて
  `standup.sh`・harness の 2 本のプローブ・`PARAMETERS-60-engines.md`・
  `deploy/aws/ecs/README.md`。完了条件は、空のパラメータファイルからの stand-up が comfy で立ち、
  sd のイメージを 1 度も写さないこと。
- **P3 — リリースノート。** 未解決 1 の 1 文を `deploy/release/notes/` に、両言語で。

## 確認したソース（2026-09-14・このリポジトリのコードとテンプレート）

- `workspace/agent/internal/imagegen/sdcpp.go:57-235` — 共有の型・`EngineLookup`・
  `engineLookupFor`・`sdcppClient`・`sdcppTimeout`、`:238-712` — provider 本体と `comfy.go` が
  借りている補助関数（`sdcppURL`・`sdcppRetryable`・`sdcppErrText`・`sdcppGaveUp`・
  `engineHTTPAttempt`）。
- `workspace/agent/internal/imagegen/comfy.go:55, 596, 760, 827-848, 885-977, 1132, 1226` — その
  補助関数への呼び出し。
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — provider id と `providerRanks`、
  `:553-584` — `effectiveOrder` の「無名の fleet は先頭」規則、`:588-590` — `Providers()`。
- `workspace/agent/internal/imagegen/http.go:293-318` — provider ごとの分岐、
  `jobs.go:454, 562, 734` — sdcpp を例に説明されている cancel の規則。
- `workspace/agent/internal/mcpx/mcp_imagegen.go:141, 220`・`mcp_stdio.go:1256` — タイムアウトの
  連鎖とモデルの union。どちらも sdcpp を軸に書かれている。
- `workspace/agent/engines.go:435-441` — 行と provider の一致、したがって沈黙の原因。
- `control-plane/engine_gateway.go:949-956`・`engine_catalog.go:231-246` — 決定 4 の 3 分岐。
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`・ラベル・畳み込み・
  `normalizeImageProviderOrder`、`console/src/features/imagegen/wire.ts:311-315` —
  `FLEET_PROVIDERS`。
- `deploy/aws/ecs/cfn/60-engines.yaml:111-127` — `ImageEngine` と既定、`:195` — `ImageIsComfy`、
  `:785` — `EcrSdcppUri` の import、`:891` — エンジン表の `provider`。
- `deploy/aws/ecs/cfn/20-platform.yaml:103-113` — `EmptyOnDelete: true` 付きの `EcrSdcpp`、
  `:464-466` — 決定 7 が回る export。
- `deploy/aws/ecs/standup.sh:67, 399-422` — crane の写しと 2.31 GB。
- `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md:493-524` — `ImageEngine` の表（エンジンごとの
  `health` と `provider` を含む）。
- sdcpp を fixture に使っている試験。P1 が触るのは `control-plane/` の 9 ファイル
  （`engine_admin_test.go`・`engine_catalog_test.go`・`engine_external_test.go`・
  `engine_family_guess_test.go`・`engine_gateway_test.go`・`engine_gateway_remote_test.go`・
  `engine_offer_test.go`・`engine_remote_catalog_test.go`・`engine_table_reload_test.go`）と
  `workspace/agent/` の 4 ファイル（`engines_test.go`・`imagegen/comfy_test.go`・
  `imagegen/http_test.go`・`imagegen/imagegen_test.go`）、加えて `imagegen/sdcpp_test.go`
  （主題と同じように割れる）。
