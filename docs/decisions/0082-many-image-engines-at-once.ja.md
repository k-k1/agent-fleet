# 0082. 画像エンジンを同時に複数持つ — 絵がどの経路で描かれるかを、provider の種類ではなくエンジン表の「行」で決める

[English](0082-many-image-engines-at-once.md) | 日本語

- 状態: **proposed**（2026-09-14）
- **この文書のために何も実測していない。** すべての記述は (a) 2026-09-14 にこのリポジトリの
  コードから読んだ事実（行は「確認したソース」に列挙）か、(b) 先行 ADR の実測（使う場所で
  引用）のいずれか。設計にとっていちばん効く数字（電源の入っていない LAN ホストへの接続に
  何秒かかるか）は**コードで上限が決まっているだけで未実測**であり、未解決 1 に置いた。
- 依頼は運用者の言葉でこうである: 他配備のエンジンを借用している配備
  （[ADR 0079](0079-remote-engine-from-another-deployment.ja.md)）が、**それに加えて**自分の
  LAN 上の ComfyUI（[ADR 0076](0076-external-image-engine-on-lan.ja.md)）も使えるようにしたい。
  **LAN を優先し、その PC が落ちていたら借用側へ落ち、どちらかを名指しで選ぶこともできる**状態
  で、かつ LAN エンジンのモデルは**パネルへ手入力ではなく ComfyUI から読み取れる**のが望ましい。

## 背景

### 依頼の 4 つのうち 3 つは、エンジン表の 1 層上に既にある

Agent の画像側は単一の選択ではなく provider の**リスト**を軸に作られていて、そこに書かれた理由
がそのまま今回の依頼の理由になっている。

| 依頼 | 既にある実装 |
|---|---|
| 優先順位 | 保存設定 `imageProviderOrder`（`workspace/agent/internal/uiprefs/prefs.go:237`）を全順序に畳む `effectiveOrder`（`imagegen.go:553`）、並べ替え UI は 設定 > エージェント（`console/src/features/settings/agents/AgentsTab.tsx:148`） |
| 失敗したら次へ | `Run` は候補を順に回り、失敗した provider の先へ進む（`imagegen.go:683-730`）。`fallbackWarnings` が「別の経路で・誰の勘定で描いたか」を明示する（`imagegen.go:731-756`） |
| 名指しで選ぶ | 明示された `provider` はそのまま使われ、**決してフォールバックしない** — 名指しした経路自身のエラーを返すほうが、黙って別の勘定を使うより良い（`imagegen.go:595-613`） |

4 つめだけが新規である。ComfyUI に「何を持っているか」を訊くコードは両モジュールのどこにも
なく、`/object_info` は木のどこにも現れない。

### 失敗経路がフォールバック機構として使えるほど安い理由

順位が役に立つのは、死んでいるエンジンが素早く道を譲る場合だけで、外部行はまさにそうなっている。
ECS アダプタを持たない行に対して `ensureStarted` は即座に拒否し（「nothing here can start it」＋
見に行くべき URL）、落ちている LAN の ComfyUI が **GPU の箱を買うために存在する** `engine_waking`
のリトライ予算に入らないようにしている（`control-plane/engine_gateway.go:995-1001`、ADR 0076
決定 4）。その拒否の前に費やされるのはヘルスチェック 1 回で、上限は **5 秒**
（`engine_gateway.go:1073-1075`）。

つまり「PC が落ちているので借用側を使う」は、既にある機構で、上限のある待ちと 1 行の正直な警告
だけで済む。この ADR が小さいのはそれが理由である。

### 邪魔をしているもの: 上記すべての単位が provider の「種類」であって「行」ではない

2026-09-14 に読んだ 4 つの事実。これで障害の全部である。

1. **Agent はエンジンを provider id で解決し、最初に当たった行を採る。** `engineImageConn` は
   `api` が `images` で `Provider` が呼び出し元の id に一致する行をカタログから探し、最初の 1 本
   を返す（`workspace/agent/engines.go:435-441`）。
2. **provider は種類ごとに 1 インスタンス。** `Providers()` はちょうど 4 つを返し
   （`imagegen.go:588-590`）、`newComfyProvider` は単一の id `comfy` に自分を束ねる
   （`comfy.go:55`）。継ぎ目自身の doc が理由を書いている — sdcpp と comfy は「1 配備では排他」
   （`sdcpp.go:195-200`）。
3. **衝突は決定的で、借用行が勝つ。** `registry.list()` は `llm`・`image`・`comfy` を先に並べ、
   それ以外のキーを後に付ける（`control-plane/engines.go:457-466`）ので、借用した `image` 行は
   2 本目の images 行がどんな名前であっても必ず前に来る。
4. **語彙はコンパイル時に、しかも二重に宣言されている。** `providerRanks`
   （`imagegen.go:504-509`）と Console の `IMAGE_PROVIDERS_RANKED`
   （`console/src/lib/settings.ts:733-745`）は同じリストの 2 つの宣言で、`providerIsFleet` は
   そこに無い id には **false** を返す（`imagegen.go:516`）。

エンジン表そのものは邪魔をしていない。キーは自由文字列で、`engineAPIKeyEnvName` の doc が
「`parseEngineTable` は運用者が `image-2` と書くのを止めない」と明言している
（`control-plane/engines.go:966-975`）。`registry.list()` は未知のキーを運び、admin の経路は
すべて `{key}` 汎用（`engine_admin.go:77-145`）、借用側の衝突検査はキーだけを見る
（`engine_remote_catalog.go:205-216`）。**2 本目の images 行を宣言することは既に可能で、
そこへ届くことだけができない。**

### 今日の運用者が得られるもの（コード変更なし）

- `lifecycle:"external"` の 2 本目の行（`key:"comfy-lan"`）を `AF_ENGINES_JSON` で足しても
  **何も起きない**。有効なモデル行が無い役は Workspace が読むカタログから落とされ
  （`engine_gateway.go:259-265`）、モデルを宣言した後も上の事実 3 によって選ばれない。見える
  変化は admin パネルのカードが 1 枚増えることだけ。
- `AF_COMFY_URL` は借用中の image 役と**併置できない**。固定キー `image` の行を合成し
  （`engines.go:620-632`）、`engineTableWithEnvRow` はそのキーの行がここの管理下でなければ
  置き換える（`engines.go:645-659`）。借用行は `remote` なので負ける。
- したがって対応済みの形は**どちらか一方**である: `AF_REMOTE_ENGINE_KEYS=llm` ＋
  `AF_COMFY_URL`。会話エンジンを借用し、画像は LAN で動かす。どちらの向きにもフォールバックは
  無い。

### ADR 0072 が決めたこと、そして実際に広げているもの

[ADR 0072](0072-engine-model-catalog.ja.md) 決定 4 は、`60-engines.yaml` の `ImageEngine`
パラメータが 1 つであることを根拠に、1 配備は image 役を sdcpp **か** comfy のどちらかで動かし
両方は無いと述べている。これは**この配備の stack が買うエンジンの数**についての言明であり、
今も正しい。この ADR が広げるのは別の数 — **1 つの Control Plane が同時に serve できる images
行の数**であり、増えるのはここでは誰も起動しない行（`external`、ADR 0076）と、他の fleet が
起動する行（`remote`、ADR 0079）である。

## 決定

### 1. images 行はそれ自身が 1 つの画像 provider であり、provider id は行のキーである

`Providers()` は固定の 4 本をやめ、ベンダ経路（`codex`・`agy`）＋ **Agent が既に取得している
カタログの `api:"images"` の行 1 本につき 1 provider** になる。利用者・保存された順序・MCP の
呼び出し・使用量の行がそろって運ぶ id は、その行の**キー**（`image`・`comfy-lan`）であって、
後ろにいるエンジンの種類ではない。

種類は消えない。行の `provider` フィールドに残り、その行にどの provider **実装**を割り当てるか
を決める（`comfy` → グラフ組み立て、`sdcpp` → OpenAI 互換クライアント）。同じ種類の 2 行は、
接続先の違う同じコードの 2 インスタンスであり、それが現状の設計では表現できないものである。

種類に添字を付ける（`comfy2`）のではなくキーを採る理由: キーはゲートウェイの両側に既に存在する
唯一の名前（パス片 `/engine/<key>/v1`、トークンの claim、設定の接頭辞、admin 経路の `{key}`）で
あり、運用者が自分で選んだ名前でもある。結果に `provider: comfy-lan` と出れば、利用者はどの機械
が描いたのかを知ることができる。

### 2. Control Plane の形は一切変えない

ゲートウェイ・カタログ・admin パネル・remote のミラーは既に行を鍵にしている。とくに
`provider == "comfy"` で分岐している 3 箇所 — 上流のパス接頭辞（`engine_gateway.go:949-956`）、
ファイル flag の語彙（`engine_catalog.go:231-234`）、`base_model` の族語彙
（`engine_catalog.go:239-246`）— は **行の `provider` フィールドを読み続けること。キーを渡しては
ならない。** これが「自然に見えて全部壊す」唯一の置き換えである: `provider` が `comfy-lan` に
なった行は ComfyUI のグラフ POST に `/v1/` を前置され、族の検証も失われ、しかも検証の欠落は数分後
の生成失敗としてしか現れない。

### 3. 順序は既存の設定のまま。Console はリストを宣言するのをやめて行を読む

`imageProviderOrder` は意味を変えない（provider id の上の順位）。fleet の行も他と同じようにそこ
に並ぶので、新しい設定を 1 つも増やさずに「LAN が先・借用が後」が得られる。帰結は 2 つ。

- **Console の `IMAGE_PROVIDERS_RANKED` はリストの出所をやめる。** 運用者のエンジンキーを知り
  ようがないので、行とその `fleet` 旗は Agent から来る（画像生成ペインが既に読んでいるのと同じ
  答え）。静的なリストは Workspace が動いていないときの控えとしてのみ残る。
- **この ADR より前に保存された順序は `comfy` / `sdcpp` を名前で持っている。** この 2 つは
  「その種類を `provider` に持つ images 行」の別名として、1 つの関数の中で、両側で正規化する。
  代わりに捨てると、fleet 自身のエンジンが個人プラン 2 つの後ろに再配置される — ADR 0072 の
  2026-09-11 の追試が実測した事故であり、`normalizeImageProviderOrder` がそもそも存在する理由
  である。

### 4. `providerIsFleet` はすべてのエンジン行に true を返さねばならない

誰も宣言していない id は fleet のものではない（`imagegen.go:516`）— 綴り間違いに対しては正しい
規則だが、エンジンキーはまさにそういう id であり、ここを誤ると上の実測事故を再現する: 無名の
provider は外部扱いで個人プランの経路の**後ろ**に挿入されるので、`auto` は配備が既に払っている
GPU に届く前に利用者の利用枠を使う。したがって旗は名前のリストではなく、カタログの行
（`api:"images"` ⇒ fleet）から来る。

借用でも答えは変わらない。借用エンジンは他人のハードだが、利用者個人のプランではないし、借用で
描いた絵を**こちら**で数えることは ADR 0079 決定 9 が既に決めている。

### 5. 落穂は Agent 側のまま・変更なし。そして決して黙らない

`Run` は既に失敗した provider の先へ進み、別の勘定で描いたことを警告する
（`imagegen.go:683-756`）。fleet の行が 2 つになると、この警告に今まで無かった場合が増える:
配備自身のエンジンからもう一方へ落ちるときは誰の利用枠も減らないので、現在の文面（「好ましい
経路とは別の勘定のプランで実行した」）は**嘘になる**。文面は 2 つの経路が**誰が払うか**で違うか
どうかで選ぶ — fleet → fleet はどのエンジンが代わりに答えたかを言い、fleet → ベンダは今の文面
のままにする。

明示された `provider` は今と同様フォールバックしない（`imagegen.go:605`）。エンジンキーを名指す
ことが、「どこかで絵を作る」ではなく「LAN のを使え、落ちていたらそう言え」を得る方法である。

### 6. 外部 comfy 行のモデルは `/object_info` から**提案**する。採り込みはしない

CP は LAN の行へ直接届くので、その機械が持つ checkpoint・LoRA・VAE のファイル名は読める:
`GET /object_info/CheckpointLoaderSimple` が `ckpt_name` の列挙を返し、LoRA と VAE のローダも
同様である。パネルはそれを「追加する行の候補」として出す。

🔴 **読めないのは、その行が生成のために欠かせない唯一の欄である。** `base_model` は表示名では
なく、comfy provider がワークフローのグラフをディスパッチする鍵で（`engine_catalog.go:239-246`）、
ComfyUI はそれを公開していない。しかも誤った値は `base_model_missing` — その行が絵を出せない
ことを示す唯一の印 — を黙らせるので、失敗は数分後の生成時にしか来ない。よって ADR 0072 決定 2
（族は運用者が宣言する）は維持する: 発見はファイル名と files を埋め、`engineFamilyGuess`
（`control-plane/engine_family_guess.go`）がファイル名から分かる場合に族を事前選択し、
**人が確認する**。ingest の流れが既に採っている形である。

### 7. 発見はボタンであり、ポーリングではない

外部行の向こうにいるのは誰かの私物の PC である。定期的に取りに行くのは、運用者が頼んでいない
頻度で運用者のネットワークに通信を載せることになるし、パネルにその必要は無い（その機械の
モデルファイルは人が置いたときに変わる）。プローブは隣のヘルスチェックと同じく上限とキャッシュ
を持ち（2 秒・10 秒、ADR 0076 決定 8）、ホストが落ちている行には admin パネルが既に出している
のと同じ文が返る。

借用行に発見するものは無い。そのカタログは far 配備からのミラーで、ここでは読み取り専用である
（ADR 0079 決定 7）。

### 8. 設定のリストはエンジン 1 行につき 1 行。sdcpp/comfy の畳み込みは過渡的

並べ替えのリストは**同じラベルを持つ行を 2 つ**出していた（「Agent Fleet (self-hosted)」）。
静的な語彙が両方の種類を並べるのに対し、配備が serve するのは一方だけだからで、利用者にとって
意味のある選択になり得ない順位である。決定 3 が入るまでは 2 つを 1 行として描き、保存値は両方の
id を持ち続ける（`IMAGE_PROVIDER_FLEET_GROUP`、`console/src/lib/settings.ts`）。リストが行ごとに
なった時点でこの畳み込みは消える: 各エンジンが自分のキーで現れ、それは利用者が行動できる区別で
ある。

## 却下した案

- **Control Plane の中でのフェイルオーバー: 1 本の `image` 行に上流を複数持たせる。** Workspace
  への wire が一切変わらず、必要なヘルスチェックも既にあるので魅力的。運用者の 2 つめの要件で
  却下: 下流のどこからも上流を**名指しできない**ので、選択は「片方にしか無いモデルを選ぶ」と
  してしか表現できず、統合カタログはモデル id で振り分けるほかなく、両方にある id では曖昧に
  なり、使用量の台帳は 2 台の機械を 1 行に潰す。
- **provider の種類を増やす（`comfy2`、あるいは行の `provider` を `comfy-lan` にする）。**
  語彙はコンパイル時のままなので実際には何も得られない。しかも行の `provider` を新しい文字列に
  すると決定 2 の CP 側 3 分岐から外れ、拒否ではなく**黙った誤生成**になる。
- **`/object_info` の行を自動で採り込む。** 決定 6 のとおり、族は発見できず、推測した族はその行
  が生成できないことを示す唯一の印を黙らせる。
- **Control Plane 側で健全性による並べ替え**（両方を叩いて生きている方を先に出す）。失敗経路は
  既に上限付きのプローブ 1 回で済むのに、運用者の LAN への定期通信と、「生きているか」に対する
  リクエスト自身より古い 2 つめの答えを買うことになる。
- **順序のための新しい設定。** 設定は `imageProviderOrder` であり、UI もあり、その正規化規則は
  実測した事故に対して書かれている。2 つめの順位は同じ問いへの 2 つめの答えになる。

## 覆す決定・維持する決定

- **ADR 0072 決定 4 を広げる。** 「sdcpp か comfy、両方は無い」は、image エンジンを 1 つ買う
  この配備自身の stack の記述としては引き続き有効。1 つの Control Plane が serve できる images
  行の数の記述ではなくなる: ここでは誰も起動しない行（`external`）と他の fleet が起動する行
  （`remote`）をその隣に足せる。
- **ADR 0072 決定 2 は維持** — 族は運用者が宣言する。上の決定 6 は提案の流れであって導出では
  ない。
- **ADR 0076 決定 1・2・4 は維持** — `external` は宣言であって推論しない、managed 行が
  `AF_COMFY_URL` に勝つ、外部エンジンはここでは誰も起動しない。`AF_COMFY_URL` は固定キー
  `image` のままで、2 本目の LAN エンジンは表で宣言する（キーを選べるのはそちらだから）。
- **ADR 0079 決定 7・9 は維持** — 借用行のカタログは読み取り専用のミラー、ローカル行が既に持つ
  キーは借用しない、借用して描いた絵はこちらで数える。
- **ADR 0069 決定 3 の順序**（ベンダ経路のあいだ）と、fleet 自身のハードが利用者個人のプランより
  前に来るという規則は維持。

## 未解決（測ってから決める）

1. **電源が落ちている LAN ホストへの接続は実際に何秒か。** 接続拒否なら即時だが、電源の落ちた
   機械は SYN がどこにも届かず、上限はヘルスチェックの 5 秒
   （`engine_gateway.go:1074`）になる — 優先経路の上で、絵 1 枚ごとに、毎回。これが全部の絵の
   前に座らせるには長すぎるなら、答えは「待ちを延ばす」ではなく「行に短い否定キャッシュを置く」
   であり、それは P0 の実測に属する。この文書で推測として書くべきものではない。
2. **画像生成ペイン（ADR 0081）は fleet provider が N 本でも描けるか。** モデル一覧は 1 つの
   provider の `Studio` の答えから来る（`imagegen.go:376-435`）。同じ種類の 2 行は id が重なり
   得る 2 つのモデル一覧を出すし、どのエンジンで描かれたのかが結果に残らなければならない。
3. **ローカル行と借用行のキー衝突は、もっと大きな声で答えるべき運用者の誤りか。** 今は poll ごと
   にログ 1 行を繰り返す（`engine_remote_catalog.go:205-216`）。行が N 本になると、運用者はそれを
   （見る手段のないログではなく）パネルで見る必要がある。
4. **利用者は fleet の行を個別に順位付けする必要があるのか、「配備自身のエンジン、表の順」で
   足りるのか。** 後者は設定のリストが 1 行で保存順序の移行も不要、前者が決定 3 の記述である。
   これは 2 本持つ配備を運用する者への問いであり、そういう配備は今日ちょうど 1 つある。

## フェーズ

- **P0 — 行が経路になる。** 決定 1・2・4・5: images 行ごとの provider、行を鍵にした解決、
  カタログ由来の `fleet`、`comfy`/`sdcpp` の別名正規化、落穂の警告の 2 つめの文面。完了条件は、
  1 つの配備が借用行の隣に LAN 行を宣言し、`generate_image` が**それぞれを名指しで**到達でき、
  かつ `auto` が停止中の LAN 行から借用行へ落ちて警告が両方を名指すこと。未解決 1 に答える。
- **P1 — Console。** 決定 3 の「Agent から来るリスト」、決定 8 の行ごとの設定リスト、画像生成
  ペインのピッカー（未解決 2）。
- **P2 — 発見。** 決定 6・7: admin パネルのボタンの下の `/object_info`、族を事前選択した行の提案、
  人による確認。
- **P3 — 運用者自身のネットワーク。** これを走らせられるのは運用者だけ: LAN の ComfyUI を優先し、
  セッション中に PC の電源を落とし、次の 1 枚を借用エンジンが拾う。P0 も P1 もこれの代わりには
  ならない（ADR 0076 が未達のまま残したのと同じ完了条件）。

## 確認したソース（2026-09-14・このリポジトリのコード）

- `workspace/agent/engines.go:435-441` — `engineImageConn`: `api` が `images` で `Provider` が
  呼び出し元の id に一致する最初の行。
- `workspace/agent/internal/imagegen/sdcpp.go:195-200` — `EngineLookup` の継ぎ目と、そこに
  書かれた前提。
- `workspace/agent/internal/imagegen/comfy.go:55`・`sdcpp.go:224` — 種類ごとに 1 インスタンス。
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — provider id と `providerRanks`、
  `:516` — 未知の id に対する `providerIsFleet`、`:553-584` — `effectiveOrder`、
  `:588-590` — `Providers()`、`:595-613` — `chooseImageProviders`、`:683-756` — `Run` の落穂と
  `fallbackWarnings`、`:376-435` — `Studio`。
- `workspace/agent/internal/uiprefs/prefs.go:237` — 保存された順序。
- `control-plane/engines.go:49-110` — `engineDef`、`:457-466` — `registry.list()` の固定の
  先頭、`:520-556` — `AF_ENGINES_JSON` と `parseEngineTable`、`:620-659` — `engineComfyEnvRow`
  と `engineTableWithEnvRow`、`:929-975` — `engineEnvAPIKey` と自由文字列のキー。
- `control-plane/engine_gateway.go:249-265` — カタログの答えと、有効なモデルが無い役が落ちること、
  `:949-956` — `provider == "comfy"` による上流の接頭辞、`:962-1001` — `ensureReady` と外部行に
  対する `ensureStarted` の即時拒否、`:1073-1092` — `engineHealthy` の 5 秒上限。
- `control-plane/engine_catalog.go:231-246` — ファイル flag と `base_model` の語彙。どちらも
  `provider == "comfy"` を鍵にしている。
- `control-plane/engine_family_guess.go` — 族の提案と、推測ではなく "" を返す理由。
- `control-plane/engine_remote_catalog.go:205-216` — 借用行の衝突検査。キーだけを見る。
- `control-plane/engine_admin.go:77-145` — admin の経路。すべて `{key}` 汎用。
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`・`imageProviderLabel`・
  `normalizeImageProviderOrder`、`console/src/features/settings/agents/AgentsTab.tsx:148` —
  並べ替えの UI。
