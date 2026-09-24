# 0083. sdcpp の**エンジン**は退役させ、クライアントは `openai-compat` として残す — OpenAI 互換の画像サーバ全般のための provider

[English](0083-openai-compat-image-provider.md) | 日本語

- 状態: **受理**（2026-09-14 起草、同日のレビューで訂正を反映して受理。レビューは以下の主張を
  `bf70083e` のコードとテンプレートに 1 つずつ当てて裏取りした。直した箇所には *（レビュー）* と
  記す — 決定 1 の CFN パラメータ名 2 つ、決定 2 の行き先の文言、決定 7 の引用、維持する ADR 0069
  の番号、サイズ推測の 2 つめの理由）
- Follow-ups: #962
- **この文書のために何も実測していない。** すべての記述は 2026-09-14 にこのリポジトリのコードと
  テンプレートから読んだ事実（file:line は「確認したソース」）で、例外は運用者が述べた 2 点 —
  **稼働中のどちらの配備も sdcpp を使っていない**（sandbox の借用行は `image (images/comfy)`、
  acrt でも使っていない）、そして **OpenAI 互換の画像サーバには対応したい**（当てる先の具体的な
  候補は今は無い。運用者・2026-09-14）。
- 決めること: stable-diffusion.cpp は最初の画像エンジンで
  （[ADR 0071](0071-self-hosted-inference-engines.ja.md) P1）、
  [ADR 0072](0072-engine-model-catalog.ja.md) 決定 4 がその隣に ComfyUI を「選択」として置いた。
  いま sdcpp を動かしている配備は無いので、エンジンは退役する。この ADR が答えるのは、木の中で
  唯一の OpenAI 互換画像クライアントである**その client をどうするか**である。

## 背景

### 「sdcpp」と呼ばれてきたものは 2 つある

ADR 0071 P1 で一緒に入って以来ひと続きの言葉だったので、「sdcpp を退役させる」が 1 つの動作の
ように聞こえる。

1. **この配備が買って動かすエンジン。** GPU のインスタンスの上の `sd-server` コンテナ、その ECR
   リポジトリ、`standup.sh` の 2.31 GB の crane の写し、それを選ぶ `ImageEngine` パラメータ。
   誰も動かしていないのはこちらである。
2. **OpenAI Images API を話すクライアント。** 要求を組み、応答を読む provider。

ADR 0076（運用者の LAN のエンジン）・ADR 0079（他の fleet のエンジン）・
[ADR 0082](0082-many-image-engines-at-once.ja.md)（行が経路である）を経て、この 2 つは同じ問い
ではなくなった。行は**どこにあり誰が動かすか**を言い、provider は**何を話すか**を言う。自分が
出荷するエンジンを退役させることは、話せる言語を捨てるかどうかについて何も言っていない。

### client は本当に OpenAI Images のクライアントで、welded された前提こそが退役したもの

`POST /v1/images/generations` に `{prompt, n, size}`、応答は `{data:[{b64_json}]}`、加えて
`POST /v1/images/edits` は multipart で `mask` は任意（`sdcpp.go:567-632`・`:673-711`）。これは
sd.cpp の発明ではなく OpenAI Images API である。

🔴 ただし 1 つの前提が全体に溶接されていて、それはエンジンを陳腐化させたものそのものである —
**サーバは起動時に選んだ checkpoint を 1 枚だけ持つ。**

- `model` を**意図的に送っていない**。コメントが理由を言っている: 「サーバは起動時に選んだ 1 枚
  しか持たないので、使わないモデル id を送ると切り替えられるように見えてしまう」
  （`sdcpp.go:563-566`）。解決済みの id は既にある（`sdcppRequestModel`・`:342-349`）が、報告に
  使われるだけで body には入らない。
- `DefaultModel` は「stack が宣言した最初の id。それは sd-server が起動に使った checkpoint でも
  ある」（`:264-272`）。
- サイズはカタログが何も宣言していないとき **model id から Stable Diffusion のファミリーを推測**して
  埋める（`sdcppDeclaredSizes`/`sdcppSizes`・`:314-336`）。その fallback の理由として挙がって
  いるのは ADR 0071 の stack から種を取ったカタログ — まさにここで退役する配備である。

つまりコードの 3 分の 2 は汎用 provider に必要なもので、3 分の 1 が「1 枚しか持たないサーバ」の
化石である。これが削除ではなく改名である理由そのもので、**汎用化のために変える量は、作り直す量
より小さい**。

### 🔥 `sdcpp.go` は共有のエンジン輸送層の住所でもある

`comfy.go` が sdcpp 名の補助関数を少なくとも 8 箇所で呼んでいる（`sdcppURL`・`sdcppErrText`・
`sdcppRetryable`・`sdcppGaveUp`・`sdcppClient`・`sdcppTimeout`・`engineHTTPAttempt`）。さらに
どの画像エンジンも使う型もこのファイルにある: `EngineConn`・`EngineLookup`・`engineLookupFor`・
`EngineParams`・`EngineLora`・`EngineFile`・`EngineLicense`（`sdcpp.go:57-235`）。ファイルの
およそ 4 割は sdcpp ではなく、他の provider が走っている道である。この ADR の後は provider が
2 本そこに乗るので、これはもう整頓の話ではない。

### id をただ消した場合に見える壊れ方

エンジン表の行は、その `provider` 文字列を名乗る provider が serve する
（`workspace/agent/engines.go:435-441`）。`sdcpp` と書いてある行を名乗る者が居なくなると、
provider は永久に `Ready` にならず、そのセッションで `generate_image` が **`tools/list` に
出ない**だけになる — エラーも、ログも、パネルの印も無い。ADR 0082 の背景が「モデル行の無い
`AF_ENGINES_JSON` の行」について記録したのと同じ沈黙である。`ImageEngine` に触らずに更新した
配備は、この退役を「画像生成が消えた」として体験する。

## 決定

### 1. エンジンは全部退役させ、クライアントは全部残す

`ImageEngine` は `AllowedValues: [comfy]`・`Default: comfy` になり、sdcpp 専用の 2 つの
パラメータ — `ImageImageTag`（af-sdcpp の ECR タグ）と `ImageExtraArgs`（sd-server の追加フラグ）
— が消える（*（レビュー）*: 初稿は `ImageSdTag` と書いていたが、その名前のパラメータは木に無い。
comfy 側のタグは `ImageComfyImageTag` という別のパラメータなので、消す 2 つと残す 1 つを名前で
区別できないと、テンプレートを触る者が grep で当たらない）。`image` のタスク定義とエンジン表の
行の `!If [ImageIsComfy, …]` は comfy 側に畳まれ、ECR リポジトリと crane の写しも消える
（決定 9 の順序が効く）。

要求と応答の半分は何も削除しない。ADR 0072 決定 4 が stack の**形**について述べた理由付け
（1 役・1 タスク定義・1 サービス）はそのままで、いまも正しい。消えるのはその中の分岐である。

### 2. provider は `openai-compat` に改名し、「1 枚だけ」の前提を外す

変更は 4 つ。3 つは sd-server にしか意味が無かった行を外すもので、4 つめは外した結果として嘘に
なる文面の書き直しである。

- **行がモデルを宣言しているとき `model` を body に入れる。** 値は `sdcppRequestModel` が既に
  解決しているもの（呼び出し元の `model`、無ければ行の先頭）。宣言が無ければ何も送らない —
  それは今の挙動そのままである。**運用者の宣言がスイッチ**であり、これは ADR 0072 決定 2 がファミリーに
  ついて適用している規則と同じ: カタログが宣言であり、1 枚しか持たないサーバは「1 枚を名前で
  挙げる行」で記述される。
- **`response_format: "b64_json"` を明示的に要求する。** sd-server が base64 で答えていたのは
  それしか無かったからで、本物の API は一部のモデルで URL を既定にする。`url` しか運んでこない
  応答は、取りに行くのではなくフィールド名を挙げて拒む — 取りに行くのは、この provider が担う
  理由のない egress である。
- **サイズは行から取り、ファミリーの推測は必要としていたエンジンと共に死ぬ。** サイズを宣言していない行
  はサイズ一覧を出さない。呼び出し元の `size` はそのまま通る（`size` は素の `WIDTHxHEIGHT` で
  endpoint が受けるし、この一覧はもともと制限ではなかった・`sdcpp.go:325`）。id から
  「xl / sd3 / flux ⇒ 1024²」と当てるのは Stable Diffusion の checkpoint についての言明であり、
  この provider はもう「向こうがそれを持っている」ことを知らない。
  その推測を生かしている理由はコメントに 2 つ書いてある。1 つめ（ADR 0071 の stack から種を取った
  カタログ）はここで退役する配備そのものである。2 つめ（カタログより古い Control Plane はサイズを
  何も送らない）も一緒に死ぬ: そういう配備の行が名乗る provider は `sdcpp` であり、改名後それを
  serve する者は居ないので、決定 5 の拒否が先に当たってサイズの段まで届かない *（レビュー）*。
- 🔴 **行き先の文言を書き直す。** `serviceLabelOf` は `sdcpp` を「Stable Diffusion（このフリート
  自身の GPU。外部サービスではない）」と名乗り（`http.go:283-300`）、`destination` の継ぎ目にも
  同じ主張がある（「`sdcpp` はフリート自身の GPU のインスタンスでありベンダではない」・
  `mcp_imagegen.go:219-222`）。どちらも ADR 0069 決定 11 の面であり、id が `openai-compat` に
  なった瞬間その文は**行ごとに真偽が変わる**: `external` の LAN のインスタンスなら今の文でほぼ正しいが、
  未解決 4 が挙げている鍵付き従量課金の endpoint はまさに外部サービスである。`providerIsFleet`
  が true を返すこと（＝配備の金）と「外部サービスではない」は別の主張であり、後者は provider の
  id からは言えなくなる。文面は行を指して言うか、言えないことを言わないかのどちらかにする
  *（レビュー）*。

id を `openai` ではなく `openai-compat` にする理由: これが話す **API** の名前であって、その
サービスを提供する会社の名前ではない。効いている語は「compat」で、結果の
`provider: openai-compat` を読んだ利用者が「OpenAI が描いた」と結論してはいけない。

**足さないもの**: `quality`・`style`・`background`・seed。ここが送る形の Images API はどれも
定義していないし、seed の拒否は `Caps` が記録している理由のまま残る — sd-server で seed を運べた
唯一の通路はプロンプト本文の穴で、そこはサーバ側のファイルパスも通す。ADR 0072 決定 5 が閉じた
ものである。

### 3. 相手にする行は `external` と `remote` だけ。そしてそれが「着地した日から使える」を意味する

この配備は OpenAI 互換のインスタンスを買わないので、`managed` の行がこの provider を名乗ることは無い。
運用者に必要なのは行 1 本である。

```json
{"key":"oai-image","api":"images","provider":"openai-compat",
 "url":"http://<host>:<port>","health":"/v1/models","lifecycle":"external"}
```

これがファイルを化石として残すこととの違いである: **能力は「誰かが削除された 700 行を復活させた
日」ではなく P1 から存在する**し、ADR 0082 のもとでは LAN の ComfyUI や借用エンジンを置き換える
のではなくその隣に並ぶ。そういうサーバが普通に欲しがる bearer も既に宣言できる —
`AF_ENGINE_API_KEY_OAI_IMAGE`（ADR 0079 決定 11）。

Control Plane 側の変更は不要である: `engineFileFlagsFor` と `engineBaseModelsFor` は comfy 以外
の provider に nil を返し（`engine_catalog.go:231-246`）、ここでは nil が正しい答えである —
向こうが自分の重みを持っているので、置くファイルも、ディスパッチするファミリーも無い。

### 4. 🔥 共有の輸送層を**先に抽出**する。その変更は何も削除しない

P0 は純粋な移動である: 背景に挙げた型と retry/URL/error の補助関数を
`workspace/agent/internal/imagegen/engine.go` へ、エンジン中立な名前で移す（`engineURL`・
`engineErrText`・`engineRetryable`・`engineGaveUp`・`engineClient`・`engineTimeout`）。16 分の
予算がなぜその値かを言うコメントも一緒に運ぶ。移動の前後で試験は緑のまま、その後で初めて改名や
削除に入る。

書き留める理由は、自然に見える順序が誤りだからである: ファイルを先に消すか改名すると ComfyUI が
コンパイルを止める。provider 2 ファイルに触る改名は「大きい変更に畳める種類」にも見えるが、畳むと
その大きい変更は誰もレビューできなくなる。

### 5. この build が serve できない provider の行は、**声を出して**拒む

この build が実装する画像 provider の語彙は `{comfy, openai-compat}` になる。それ以外を名乗る行は
レジストリ構築時に行ごとに 1 回ログを出し、admin パネルがその行を「serve 不能」として名乗られた
provider 名とともに印を付ける。Agent 側の解決も、未知の images provider のカタログ行が通り過ぎた
ときに自分のログで同じことを言う。

黙った `Ready() == false` にはしない。上の壊れ方こそ、この ADR が改名以上である理由である:
stack がまだ `sdcpp` と言っている運用者は、利用者からの「ツールが消えた」という報告ではなく、
既に開いているパネルからそれを知るべきである。

### 6. Control Plane の `provider == "comfy"` 分岐は**条件のまま残す**

上流のパス接頭辞（`engine_gateway.go:949-956`）、ファイル flag の語彙と `base_model` のファミリー語彙
（`engine_catalog.go:231-246`）は、この fleet が買うエンジンが comfy だけになれば無条件にできそう
に見える。してはならない。そして `openai-compat` はその 2 つめの理由になる: この provider は
`/v1/` の前置を必要とし、ファイル flag もファミリー語彙も持たない — まさに各分岐の「comfy でない側」が
答えているものである。

「images ⇒ comfy」は**この配備の stack が買うもの**については真だが、**行が言えること**について
は真ではない。`external` 行は運用者が書いたまま（ADR 0076）、`remote` 行は far が宣言したまま
（ADR 0079 決定 2）である。

### 7. 別名も保存順序の移行も不要。過去の使用量の行は id をそのまま持つ

この変更より前に保存された `imageProviderOrder` は `sdcpp` を名前で持っている。その id が語彙から
抜けると `normalizeImageProviderOrder` は未知として落とし、`comfy` と `openai-compat` が「保存値が
一度も名前を挙げなかった id」になる。そして**無名の fleet provider は先頭に置かれる**
（`console/src/lib/settings.ts:825-837` の `normalizeImageProviderOrder`、`imagegen.go:554-586` の
`effectiveOrder` が対応・*（レビュー）*: 初稿は同じファイルの畳み込みの段を指していた）。つまり
`["sdcpp","agy","codex"]` は `["comfy","openai-compat","agy","codex"]` に正規化され、移行のために
何も書かずにそれが正しい答えである。

これは ADR 0072 の 2026-09-11 追試が実測した事故（`comfy` が存在する前に保存された順序が `auto` を
個人プラン 2 つへ先に通した）の**不在**が原因だったのと同じ規則である。provider が増えるために
作られた規則が、改名のときにもちょうど正しい。

既に `provider: "sdcpp"` で書かれた使用量の行はそのままにする。台帳は起きたことの記録であり、
何もそれを書き換えない — この id はどこへの外部キーでもない。

### 8. 設定リストは fleet 自身のエンジンを 1 行に保つ。ただし理由が変わる

このブランチで足した畳み込み（`IMAGE_PROVIDER_FLEET_GROUP`）の理由は「1 つのエンジンの 2 つの
綴りで、配備が serve するのは一方だけ」だった。この ADR の後、2 つは本当に別のものになるので
その文は真でなくなる。それでも規則は残り、理由が新しくなる: **リストがエンジンの行から来るように
なるまで（ADR 0082 決定 3）、行が無い provider は「利用者が順位を付けられるのに何も routing
できない段」になる。** ComfyUI だけの配備で「Agent Fleet (self-hosted)」1 行は正直であり、
2 行のうち 1 行が死んでいるのはこのブランチがいま直した欠陥そのものである。

0082 の行ごとのリストで畳み込みは消える。そこでは各エンジンが自分のキーで現れ、そのリストの
どの行も実在するので、利用者が行動できる区別になる。

### 9. ECR リポジトリは消す。ただし 2 つの stack 更新の**順序は強制される**

`60-engines.yaml:785` が `${PlatformStackName}-EcrSdcppUri` を import し、
`20-platform.yaml:464` がそれを export している。CloudFormation は他の stack がまだ import して
いる export の削除を拒むので:

1. **先に 60-engines** — export が残っているうちに import と sdcpp 側の枝を落とす。
2. **次に 20-platform** — `EcrSdcpp` とその出力を落とす。

逆順だと platform の更新が失敗してロールバックする。リポジトリには既に `EmptyOnDelete: true` が
付いている（`20-platform.yaml:112`）ので、2.31 GB のイメージも一緒に消え、手で空にする作業は
無い。

### 10. far の sdcpp 行は借用できなくなる。そしてそれを言う

ADR 0079 決定 2: どのエンジンが存在し何を話すかは **far 側**が宣言するもので、こちらは決して
推測しない。まだ sdcpp を動かしている far の fleet は、この build が実装しない id を名乗る image
役を差し出すことになり、それは起動メニューの静かな穴ではなく決定 5 の拒否である。他人の宣言を
こちらで指し替えるのは筋ではない。両端を持っている運用者は、向こうで変える。

## 却下した案

- **クライアントも消す。** 2026-09-14 まではこの草稿自身の推奨だったが、OpenAI 互換への対応を
  したいという理由で運用者が却下した。削除側の論拠（エンジンの無い継ぎ目は腐る・git が戻せる）は
  どちらも真である。見落としているのは、**このコードは継ぎ目ではなく機能である**こと — 決定 2 の
  小さな変更 3 つで運用者が自分のサーバに当てられるものになり、git の化石はその同じ作業に加えて
  回収作業も要る。
- **`sdcpp` の id のまま、手を入れずに残す。** すると無関係な OpenAI 互換サーバのために行を書く
  運用者が `provider: "sdcpp"` と、そこに無いバイナリの名前を書くことになる。そして「1 枚だけ」の
  前提（body に `model` を入れない）が、そのサーバを起動時のモデルに黙って固定する。
- **「本物のサーバが要求するまで 1 枚だけの前提を残す」。** それはエンジンを退役させた前提その
  ものであり、残せば provider は「これから消えるエンジン以外に対して検証できない」ままになる。
- **`openai` または `openai-images` と名付ける。** 前者は OpenAI 自身のサービスと読める（結果の
  `provider:` 行は利用者が読む）。後者は正確だが、効いている語を落としている: これが話すのは
  API であり、主張しているのは互換性である。
- **Stable Diffusion のサイズ推測を便利だから残す。** そのコメント自身が ADR 0071 の stack から
  種を取ったカタログに紐付けている。それはここで退役する配備であり、残せばこの provider が
  「未知のサーバが SD の checkpoint を持っている」と言い張ることになる。

## 覆す決定・維持する決定

- **ADR 0072 決定 4 の「選択」を覆す。** 「sdcpp か comfy」には答えが 2 つあったが、エンジンの側は
  1 つになった。その決定の残りはそのまま。
- **ADR 0071 P1 の画像エンジンを覆し**、その実測は維持する: 冷間起動・GPU の段・16 分の予算は
  GPU のインスタンスと S3 の取得の実測で、あのバイナリの実測ではない。
- **ADR 0072 決定 2 を維持** — 行が何を持つかは運用者が宣言する。上の決定 2 はそれに乗っている
  （行のモデルこそが `model` を送れるようにする）。
- **ADR 0072 決定 5 の拒否を維持** — 呼び出し元のプロンプト中の `<sd_cpp_extra_args>`、そして
  この経路にいまも seed が無い理由。
- **ADR 0069 の 2026-09-11 追記「保存済みの順番に無い provider の入れ場所」を維持** — fleet 自身の
  ハードは利用者個人のプランより前。*（レビュー）*: 初稿はこれを ADR 0069 決定 3 に帰していたが、
  決定 3 は「層は鍵の持ち主で切る」であって順序の規則ではない。順序はその追記が `providerRanks`
  の `fleet` 旗として決めたものである。
- **ADR 0069 決定 11**（プロンプトの行き先を言う `destination`）**を維持** — ただし維持するには
  文面の書き直しが要る。決定 2 の 4 つめを見よ。
- **ADR 0076・ADR 0079 はそのまま維持。** 決定 6 はそれらを真のままに保つために存在する。
- **ADR 0082 の P0 より前に着地させるべき。** 0082 は provider id をエンジン行のキーにする変更で、
  動かす語彙は `sdcpp` が抜けたあとのほうが小さく、0082 決定 8 の過渡的な畳み込みも二度書きに
  ならない。

## 未解決（測ってから決める）

1. **最初の実機は、どの OpenAI 互換サーバで走らせるのか。** どちらの配備にも存在しないので、
   P1 の witness は sd-server の代役を既に務めている試験用の偽サーバであり、最初の実物は
   ADR 0076 の LAN 実機と同じように**負債として残る**（網を持っているのは運用者だけ）。それまで
   この provider は「宣言済み・試験済み・未実証」であり、その一文はここだけでなくリリースノートに
   属する。
2. **1 モデルしか持たないサーバは body の `model` をどう扱うか。** 決定 2 は行の宣言をスイッチに
   していて、それが正しい既定である。自分が持っている `model` に対して 400 を返すサーバが居れば
   行ごとの「model を送らない」欄が必要になるが、まだ何も拒まれていないうちにその欄を作るのは
   推測である。
3. **`url` しか返さない応答。** 決定 2 は取りに行かずに拒む。本物のサーバがそれしか返さないなら、
   選択は「この provider が egress する」か「その能力を持たない」かで、抽象的に決めるべきでは
   ない。
4. **行の向こうが従量課金の API であるとき、台帳に値段が無い。** 鍵付きの従量課金 OpenAI 互換
   endpoint にこれを当てることを妨げるものは無い。`providerIsFleet` は true を返す — 順位付けに
   とって意味のある解釈（**配備の**金であって利用者個人のプランではない）では正しいが、使用量の
   側はエンジン行について件数を数え、金額は数えていない（ADR 0079 決定 9）。従量課金の外部画像
   API がいくらかかるのかは、コスト模型にまだ訊いていない問いである。
5. **例として sdcpp を名指しているコメント群は書き換えか削除か。** 「cancel を持たない provider
   （sdcpp）」（`jobs.go:734`）、タイムアウトの連鎖（`mcp_imagegen.go:141`）、モデルの union
   （`mcp_stdio.go:1256`）。新しい名前で例が残るものもあるが、失うものもある。それは文を削るのでは
   なく書き換える理由である。

## フェーズ

- **P0 — 抽出。** 決定 4 だけ: 共有の型と輸送層をエンジン中立な名前で `engine.go` へ。完了条件は
  両方の Go スイートが挙動変更なしで緑（両モジュールで `go test ./...`・`-count=1`）。
- **P1 — 改名と汎用化。** 決定 2・5・7・8: Agent・Console の 2 つの語彙・ペインの
  `FLEET_PROVIDERS` で `sdcpp` → `openai-compat`、`model` と `response_format` とサイズの規則、
  行き先の 2 つの文面（`serviceLabelOf` と `destination`・*（レビュー）*）、両側の声を出す拒否。
  完了条件は、`provider: "openai-compat"` を宣言した表の行が試験用の偽サーバ
  に対して `model` を名指して生成・編集・inpaint できること、`sdcpp` を宣言した行が CP のログ 1 行
  とパネルの印を生むこと、そしてその隣の comfy 行で `generate_image` が引き続き動くこと。
- **P2 — テンプレート。** 決定 1 のパラメータと決定 9 の 2 つの更新をその順序で。加えて
  `standup.sh`・harness の 2 本のプローブ・`PARAMETERS-60-engines.md`・
  `deploy/aws/ecs/README.md`。完了条件は、空のパラメータファイルからの stand-up が comfy で立ち、
  sd のイメージを 1 度も写さないこと。
- **P3 — リリースノート。** 両言語で: 変えるべきパラメータ、変えなかった場合に何が起きるか
  （決定 5 の拒否）、そして決定 3 の新しい能力 — 本物のサーバに対して未実証であることも含めて。
- **P4 — 最初の実機（負債。予定ではない）。** 未解決 1。ADR 0076 の完了条件と同じく運用者の網。

## 確認したソース（2026-09-14・このリポジトリのコードとテンプレート）

- `workspace/agent/internal/imagegen/sdcpp.go:57-235` — 共有の型・`EngineLookup`・
  `engineLookupFor`・`sdcppClient`・`sdcppTimeout`、`:262-349` — `DefaultModel`・`Caps`（seed の
  拒否とその理由）・`sdcppDeclaredSizes`/`sdcppSizes`・`sdcppRequestModel`、`:567-590` — 生成の
  body と「`model` が無い理由」のコメント、`:591-632` — multipart の編集、`:673-711` —
  `b64_json` の復号、そして `comfy.go` が借りている補助関数（`sdcppURL`・`sdcppRetryable`・
  `sdcppErrText`・`sdcppGaveUp`・`engineHTTPAttempt`）。
- `workspace/agent/internal/imagegen/comfy.go:55, 596, 760, 827-848, 885-977, 1132, 1226` — その
  補助関数への呼び出し。
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — provider id と `providerRanks`、
  `:553-584` — `effectiveOrder` の「無名の fleet は先頭」規則、`:588-590` — `Providers()`。
- `workspace/agent/internal/imagegen/http.go:283-318` — `serviceLabelOf` の provider ごとの分岐と、
  `sdcpp` の「外部サービスではない」（決定 2 の 4 つめ・*（レビュー）*）、
  `jobs.go:454, 562, 734` — sdcpp を例に説明されている cancel の規則。
- `workspace/agent/internal/mcpx/mcp_imagegen.go:36-38` — ツールの enum は「そのセッションが実際に
  名指せる provider」から組まれる＝休眠中の provider は説明文のトークンを増やさない、
  `:141` と `mcp_stdio.go:1256` — タイムアウトの連鎖とモデルの union、`:219-222` — ADR 0069
  決定 11 の `destination` と、そこが `sdcpp` について主張していること（*（レビュー）*）。
- `workspace/agent/engines.go:435-441` — 行と provider の一致、したがって沈黙の原因。
- `control-plane/engine_gateway.go:949-956`・`engine_catalog.go:231-246` — 決定 6 の 3 分岐。
- `control-plane/engines.go:929-975` — `engineEnvAPIKey` と行ごとの bearer の変数名。
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`・ラベル・畳み込み・
  `normalizeImageProviderOrder`、`console/src/features/imagegen/wire.ts:311-315` —
  `FLEET_PROVIDERS`。
- `deploy/aws/ecs/cfn/60-engines.yaml:111-127` — `ImageEngine` と `sdcpp` の既定、および sdcpp
  専用の `ImageImageTag`・`ImageExtraArgs` と comfy 専用の `ImageComfyImageTag`（*（レビュー）*）、`:195` —
  `ImageIsComfy`、`:785` — `EcrSdcppUri` の import、`:891` — エンジン表の `provider`。
- `deploy/aws/ecs/cfn/20-platform.yaml:103-113` — `EmptyOnDelete: true` 付きの `EcrSdcpp`、
  `:464-466` — 決定 9 が回る export。
- `deploy/aws/ecs/standup.sh:67, 399-422` — crane の写しと 2.31 GB。
- `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md:493-524` — `ImageEngine` の表（エンジンごとの
  `health` と `provider` を含む）。
- sdcpp を fixture に使っている試験。P1 が触るのは `control-plane/` の 9 ファイル
  （`engine_admin_test.go`・`engine_catalog_test.go`・`engine_external_test.go`・
  `engine_family_guess_test.go`・`engine_gateway_test.go`・`engine_gateway_remote_test.go`・
  `engine_offer_test.go`・`engine_remote_catalog_test.go`・`engine_table_reload_test.go`）と
  `workspace/agent/` の 4 ファイル（`engines_test.go`・`imagegen/comfy_test.go`・
  `imagegen/http_test.go`・`imagegen/imagegen_test.go`）、加えて `imagegen/sdcpp_test.go`
  （その偽サーバが P1 の witness になる）。
