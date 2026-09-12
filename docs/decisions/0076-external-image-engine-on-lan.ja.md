# 0076. LAN 上の ComfyUI を native / docker 配備の画像生成に使う——エンジン表に「外部（external）」の行を足し、ゲートウェイの転送経路はそのまま

[English](0076-external-image-engine-on-lan.md) | 日本語

- 状態: **提案・レビュー済み**（2026-09-11）。実装はしていない。レビュー（末尾の節）は
  すべての主張をコードに当て、崩れる前提は無かった。訂正は下の決定に織り込み済みで、P0 に
  入ってよい。
- **この文書のために新しく測ったものは無い。** 根拠はすべて出所を書き分けてある——
  (a) ADR 0069・0071・0072 の実測、(b) 2026-09-11 にこのリポジトリのコードから読んだ事実
  （「確認した出典」に file:line で列挙した）、(c) ComfyUI の公開仕様として知っているだけで
  **この配備では確かめていない**もの（`/system_stats` がモデル読込中も 200 を返すこと、loader
  がサブフォルダ付きの名前を列挙すること）。(c) に依存する決定は「未解決の点」に書いた。
- 利用者の要求は 1 つである——**`generate_image` はいま ecs-ec2 配備でしか動かない。native /
  docker 配備で、ローカルネットワーク内で動いている ComfyUI と通信できるようにしたい。**

## 背景

### 何が ecs-ec2 に縛られているか（2026-09-11 に読んだ）

ADR 0071 は GPU の箱を ECS Managed Instances で買い、ADR 0072 はその箱に載せるモデルを
カタログで宣言した。画像生成の経路は次の 3 段で、**縛られているのは真ん中の登録側だけ**である。

1. **Workspace 側は runtime を知らない。** `generate_image` の provider `comfy` は
   `AF_CP_BASE_URL` に CP が返した `base_url`（`/engine/image/v1`）を足した先にしか行かず、
   認証は CP が発行する engine session token だけである。AWS SDK も S3 参照も無く、ComfyUI の
   `/prompt` → `/history/<id>` → `/view` を CP 越しに叩く。到達先が ECS か LAN かは
   **この層からは区別できないし、区別する必要も無い**。
2. **CP のゲートウェイは health が通れば ECS に触れない。** `ensureReady` はまず
   `engineHealthy` を叩き、通れば転送する。ECS を呼ぶのは engine が起きていないときの
   `ensureStarted` **1 関数**で、そこで `DescribeServices` と `UpdateService` を使う。認証・
   カタログ配布・使用量の計上・comfy 用のパス書き換え（`/v1/` を付けない）は「HTTP の上流 URL が
   1 つある」以外を仮定していない。
3. **登録側が ECS を無条件に生やす。** `parseEngineTable` は全行に `service` を要求し、
   `newEngineRegistry` は AWS の設定を読み、全行に ECS アダプタ `engineECS` と制御ループ
   `engineController` を付け、SSM の active set を publish し、`pending` の監視と GPU クラスの梯子
   （ADR 0074）を繋ぐ。inline の `AF_ENGINES_JSON` は「AWS の無い dev CP 用」として既にあるが
   文書が無く、native で使うと制御ループが毎 tick `DescribeServices` の失敗をログに書く。

### 既にある前例——VOICEVOX の「外部管理」

ADR 0013 は VOICEVOX を「CP が指す URL」と決め（`AF_VOICEVOX_URL`）、ADR 0070 がそれを ECS
でオンデマンドにした。**その分岐は 1 関数である**: `newTTSEngineFromEnv` は
`AF_TTS_ECS_SERVICE` が無ければ nil を返し、制御ループも需要計測も付かず、
`engineMode(v, managed=false)` は既定を `on` にし、管理画面の行は `managed:false` で ECS 由来の
欄（state / desired / box / stop_eta）を**省略する**。Console の推論エンジン画面にも
`managed:false` を「外部管理」と描く分岐が既にある。native の README は「Windows 側の
VOICEVOX を WSL の CP から指す」手順を持っている。**この ADR はその型を画像エンジンに写す。**

### 網の前提

- docker 配備の CP は `network_mode: host` なので、`192.168.x.y:8188` にそのまま届く。native は
  ホストの process である。
- Workspace → CP は `PUBLIC_BASE_URL` 経由の既存の経路で、`/engine/*` は `/mcp` と同じく
  session 免除である。**`no_proxy` も egress の allowlist も増えない**（0071 決定 4 の (a)）。
- 🔴 ただし docker の Workspace コンテナは専用ブリッジから NAT で LAN に出られる。ECS では
  「エンジンの SG は CP からだけ」が防御だったが、**LAN では到達性が防御にならない**（決定 7）。

## 決定

### 1. 外部エンジンはエンジン表の 1 行であり、`lifecycle: "external"` で**宣言する**

`engineDef` に `lifecycle` を足す。空（既定）は今までどおり ECS が管理する行、`external` は
「起動も停止も所有しない、URL があるだけの行」である。`service` 空を暗黙に external と読む
案は退けた（ADR 0053: 導出しない、宣言する）。external 行には次を**付けない**——ECS アダプタ、
制御ループ、SSM の active set、`pending` の監視、GPU クラスの梯子、稼働ヒートマップの
サンプラ。管理 API の行は `managed:false` で公開し、ECS 由来の欄は VOICEVOX と同じく
**省略する**（推測で埋めない）。`parseEngineTable` は external 行に限って `service` を要求しない。

external 行の nil が何に当たるかをコードで読んだ（レビュー）: 行に無いものに触る箇所の大半は
既に nil 安全である——`demand.record` / `units`、`ctrl.warmed` / `noteAdminAction`、
`ecs.logKey`、`controlCfg`、`pendingGuard`（`pending` が nil なら返る）、`publishActiveSet`
（SSM 無しは no-op）、管理行の `e.ecs == nil` と `e.demand != nil` の分岐、梯子の無い
`classStartHeld`、そして `servedModel`（Workspace に渡す `warm` のモデルは「ゲートウェイが
最後に答えを見たモデル」で、制御ループの有無に依らない）。安全でないのは `ensureStarted` の
`e.ecs.view` と管理 API `put` の起動・停止の 2 か所で、それは決定 4・5 が書き換える当の場所で
ある。P0 はこれを、external 行に対して全ハンドラ（管理一覧・`put`・models の経路・上流あり／
なしのゲートウェイ・`/internal/engine/catalog`）を通す 1 本のテストで固定する。

### 2. 運用者の入口は `AF_COMFY_URL` 1 本。CP がそれを表の行に**合成する**

`AF_VOICEVOX_URL` と同型にする。`AF_COMFY_URL=http://192.168.1.20:8188` から CP が
`{key:"image", api:"images", provider:"comfy", health:"/system_stats", lifecycle:"external"}`
を合成する。任意で `AF_COMFY_API_KEY` を取り、ゲートウェイが上流に付ける `Authorization: Bearer`
の鍵にする（ComfyUI 自体は無認証なので、これは前段の reverse proxy が検査するためのもの。
決定 7）。鍵は行に既にある `apiKey` 欄に入れる——managed 行が SSM の `apiKeyParam` から読む
欄で、`dial` と `engineHealthy` の**両方**が既に bearer として付けるので、新しい配管は無い。
その帰結として、前段の proxy は同じ bearer で `/system_stats` も通さなければ health が永久に
通らない。`AF_COMFY_URL` は、いま `AF_ENGINES_SSM_PARAM` も `AF_ENGINES_JSON` も無ければ nil を
返す `newEngineRegistry` の門に加わる。`AF_ENGINES_JSON` は dev 用にそのまま残し、そこにも
`lifecycle` を書ける。

表と env の両方が同じ `key` を持ったら: 表の行が無いか external なら env が勝ち、**表の行が
managed なら表が勝つ**。どちらもログに書く。レビューで草稿の「常に env が勝つ」を逆にした——
制御下の行を差し替えると、その行が指す ECS サービスを止める者が誰もいなくなり、そちらの方が
高くつく。LAN の箱に替えたい運用者はスタックからその役を外す。

SSM の表は 10 秒ごとに読み直される（ADR 0074 の `engineTableReloader`）が、環境変数は起動時に
1 回しか読まないので、URL の変更は CP の再起動である。reloader は **external 行を飛ばす**——
でないと表が変わるたびに「表が変わった・再起動せよ」を合成行に対して書き、梯子の無い行に
GPU の梯子を運ぼうとする。管理画面から URL を入れる案は却下ではなく**後回し**（却下した案の節）。

### 3. Workspace は CP のゲートウェイを通す。Workspace 側は無改修

ADR 0071 決定 4 を維持する。理由 (a) 経路と allowlist が増えない、(c) 使用量を CP が数える、
(d) ComfyUI は無認証で変更系（`/prompt`・`/upload/image`）を持つ、はすべて LAN でも成り立つ。
(b)「起こして待つ場所」だけが外部エンジンでは不要になる（決定 4）。
`comfy` provider は 5 系統（sdxl / sd35 / flux1 / flux2-klein / zimage）のワークフロー雛形を
そのまま使う。

### 4. 起こす相手がいないので、`ensureStarted` は external なら**即時に失敗する**

`ensureReady` は health が通ればそのまま転送する（今もそう）。通らないとき、managed なら
`ensureStarted` が箱を買って待つが、external では待つ理由が無い——**health が落ちている
LAN の ComfyUI は、待っても起きない**。即時に失敗し、非ストリーミング経路は
`503 engine_unavailable` を返す。`engine_waking` にしない理由: provider は `engine_waking` を
16 分再試行する（ADR 0071 決定 5）が、その 16 分は「箱を買って S3 から引く」ための予算で、
落ちている箱に対しては 16 分の沈黙にしかならない。本文には URL と health のパスを書く——
運用者が読む文である。新しいエラーコードは要らない: 非ストリーミング経路は `errEngineWaking`
以外の dial の失敗をすべて既に `503 engine_unavailable` に、その文言を末尾に付けて変換し、
ストリーミング経路も `engine_unavailable` のイベントにする。P0 が足すのは即時に返ることと、
その文言だけである。health は 60-engines と同じ `/system_stats`。ComfyUI はモデル読込中も
これに答え、`/prompt` はキューに積むので、health が通れば待つ理由が無い（未解決 1）。

### 5. モードは `on` / `off` の 2 値。既定は `on`

`engineMode(v, managed=false)` の既定 `on` をそのまま使う。保存済みの `ondemand` は `on` と
読む（切ってよい箱が無い）。管理 API は external への `ondemand` を 400 で拒み、Console は
external の行に `ondemand` のボタンを出さない。`off` の意味は VOICEVOX と同じ「経路を閉じる」
であって、ComfyUI を止めることではない。

両側を別々に作れるように、契約を書いておく: external エンジンの管理行は `managed:false`・
`lifecycle:"external"`・`url`・`warm` を持ち、`state` / `desired` / `box` / `stop_eta` /
`idle_secs` / `window_*` を省略する。Console 側では、モードのセグメントはいま全行に
`off` / `ondemand` / `on` を描いており、`engineStateLabel` と `engineStateTone` は既に `managed`
で分岐する（「外部管理」の文字列は TTS 行のものを流用している）。パネルのポーリング条件は
`state` を読むので、`state` を持たない external 行はポーリングを起こさない。

### 6. モデルはカタログの手入力宣言。ファイル名は ComfyUI の loader が列挙する名前

ADR 0072 決定 1（カタログが宣言の全部）を維持する。取り込みジョブ（S3）は無いので、
管理者が既存の `POST /api/admin/engines/image/models` で id・`base_model`・ファイルを登録する。
`files_missing` の検査は「役ごとのフラグが宣言されているか」であって S3 の存在確認ではない
（`engineMissingFileFlags`。models の経路は自身に「S3 key の存在はここでは確かめない」と
書いている）ので、そのまま使える。wire の欄名 `s3Key` は据え置く——外部では「loader に渡す名前」を入れる。

🔴 **既知の罠**: Agent の `engineImageFiles` は S3 key の最後の `/` 以降だけを ComfyUI に渡す。
取り込み経路では平坦だったので通っていたが、LAN の ComfyUI で `checkpoints/sdxl/x.safetensors`
のようにサブフォルダに置くと loader に渡す名前が `x.safetensors` になり `Value not in list` で
落ちる。P0 では「種別フォルダ（`checkpoints/`・`diffusion_models/`・`clip/`・`vae/`・`loras/`）
直下に置く」と文書化し、P1 で規則を「種別フォルダまでを剥がす」に改める（未解決 2）。
`/object_info` を読んで候補を出す取り込み補助も P1。

### 7. 到達性は防御ではない。LAN では網の責任を運用者が持つ

ECS では SG が「CP からだけ」を保証した。LAN ではそれに相当するものを CP は持たず、
**持たないことを文書に書く**。docker の Workspace コンテナは NAT で LAN に出られるので、
セッションが ComfyUI を直接叩けることを明記する。塞ぎたい運用者には 2 つの手を示す:
egress 統制を enforce にする（RFC1918 は allowlist に無ければ落ちる）、または ComfyUI の前に
reverse proxy を置いて bearer を検査し、その鍵を `AF_COMFY_API_KEY` で CP にだけ渡す。

🔴 このうち今日使えるのは reverse proxy だけである。`guide/operate/04-secure.md` は egress proxy の
出荷済みの範囲を「観測と許可リスト管理まで」とし、遮断（enforce）とコンテナ側の常時配線は
後続の作業と明記している。よって文書は enforce を「方向」、proxy を「答え」として書き、存在
しない防御を約束してはならない（文書レーンが書きながら見つけた）。

### 8. 使用量は `tool.imagegen` の provider `comfy` のまま。費用は付けない。パネルの `warm` は生存確認

Agent が枚数とピクセルを数える経路は変えない（ADR 0069 決定 9・0071 決定 9）。LAN の箱に
時間単価は無いので費用は出さない。制御ループが無い external の `warm` は、パネルを開いた
瞬間の health の結果で答える——1 回の HTTP・**2 秒**上限・結果は 10 秒キャッシュ。ゲートウェイの
5 秒にしない理由: 一覧のハンドラは同期で、Console は読み込みのたびに叩くので、落ちている LAN の
箱は external 行 1 つにつき 5 秒パネル全体を止めてしまう。稼働ヒートマップは P0 では空——
サンプラは制御ループの tick だからで、health だけを 30 秒ごとに書く軽い prober は P1。

## 却下した案

- **Workspace から LAN の ComfyUI に直接行く（`AF_COMFY_URL` を Workspace の env に）。**
  `no_proxy` と egress の allowlist に LAN のアドレスが要り、ComfyUI の無認証の変更系 API を
  全セッションに晒し、`base_model` のカタログは CP の DB にしか無く、使用量も揃わない。
  ADR 0071 決定 4 の理由 (a)(c)(d) がそのまま却下の理由である。
- **ComfyUI を MCP サーバとして登録する。** コミュニティの ComfyUI MCP サーバは存在するが、
  フリートの `generate_image`（由来の記録、CLI 横断の同じ道具、成果物カード）を迂回し、
  各 CLI が別々の道具を持つことになる。ADR 0069 決定「provider 抽象は 1 つ」に反する。
- **`service` 空を external と暗黙に読む。** 表を書く側の打ち忘れが「外部管理」に化ける。
  ADR 0053 の「宣言する」に従い、`lifecycle` を書かせる。
- **CP が LAN の ComfyUI の起動と停止を持つ（systemd / docker を CP から叩く）。** ADR 0070 の
  型を LAN に持ち込む案。所有者が違う——VOICEVOX の「常駐 docker」と同じく、ライフサイクルは
  それを動かす人のものである。GPU が寝ている時間の電気代は ECS の $1.26/時とは桁が違う。
- **管理画面から URL と鍵を入れる（DB に暗号化保存）。** MCP レジストリに前例があり、再起動
  無しで変えられる利点はあるが、Console のフォーム・暗号化・接続テストが要り、P0 の規模を
  数倍にする。**後回し**であって却下ではない。env の 1 変数で運用者が困り始めたら足す。

## 上書きする既存の決定と、維持する決定

- ADR 0071 決定 5（起こして待つ）は **managed 行に限る**——external では決定 4 に置き換わる。
- ADR 0071 決定 4（ゲートウェイを通す）・決定 9（使用量）・ADR 0072 決定 1（カタログが宣言の
  全部）・決定 4（ComfyUI のワークフロー雛形）・ADR 0069 決定（provider 抽象は 1 つ）は維持。
- ADR 0071 決定 13 の「モデルはエンジンに聞かない（寝ているから）」は external では前提が
  崩れる——常に起きている。ただし P0 では聞かない（決定 6）。P1 の `/object_info` 取り込み補助は
  「宣言」ではなく「候補の提示」として、ADR 0053 の枠に収める。

## 未解決の点（測ってから決める）

1. **`/system_stats` はモデル読込中も 200 か**（決定 4 が依存）。公開仕様としてはそう理解して
   いるが、この配備では未確認。違えば health を `/queue` か `/` に替える。
2. **サブフォルダ付きの名前を loader がどう列挙するか**（決定 6 が依存）。`sdxl/x.safetensors`
   の形で列挙するなら「種別フォルダまでを剥がす」規則で足りる。
3. **秘密を `.env` から渡す是非。** `AF_COMFY_API_KEY` は compose の `.env` に平文で置くことに
   なる。既存の OIDC の secret も同じ場所にあるので慣習には合うが、決定 2 の任意性を保つ。
4. **docker 配備で GPU ホスト上にフリートの pinned ComfyUI イメージを起こす形（P2）。**
   `deploy/aws/ecs/comfyui/Dockerfile` はモデルを bind mount で読める素の ComfyUI なので、
   `run-voicevox.sh` 相当のスクリプトで `--gpus all` を付けて起こせるはずだが、未確認。
5. **LAN 越しの `/view` の応答サイズ。** ゲートウェイの非ストリーミング経路は応答を丸ごと
   メモリに読む。FLUX の 2048px 出力が何 MB になるかは測っていない。
6. **ComfyUI Desktop（Windows）の待受け設定。** サーバ版は `--listen` だが、Desktop 版の
   設定名は確認していない。native の README にはサーバ版の手順を書き、Desktop は保留。

## フェーズ

- **P0（CP と文書）**。触るファイルが重ならない 4 レーンに分ける:
  - *CP*（`control-plane/`）: `engineDef.Lifecycle`、決定 2 の優先順位での `AF_COMFY_URL` /
    `AF_COMFY_API_KEY` の合成、AWS を要する行が無ければ AWS の設定を読まない
    `newEngineRegistry`、reloader の external 行スキップ、`ensureStarted` の即時失敗、決定 5 の
    契約どおりの管理行、`ondemand` の 400、2 秒・キャッシュ付きの health 確認。テストは表の
    parse、AWS 無しでの registry 構築、httptest の ComfyUI スタブに対するゲートウェイの通し
    （上流あり → 転送、上流なし → URL を名指しした `engine_unavailable` を即時）、決定 1 の
    nil 総当たり。
  - *Console*（`console/src/features/settings/admin/adminEngines.tsx` とカタログ）:
    `managed:false` の行に `ondemand` ボタンを出さない、URL を表示、ECS 専用の欄（idle・box・
    ヒートマップ）を描かない。決定 5 の形の行に対する dom テスト。
  - *Agent*（`workspace/agent/`）: 同梱で直す既存の穴——`serviceLabelOf` / `driverModelOf` に
    `comfy` の case が無く、ツール説明文に経路名とモデルが出ない（ECS でも同じ）。
  - *文書*: `deploy/compose/.env.example`、`deploy/native/README.md`（VOICEVOX の節の隣）、
    二言語の新ページ `guide/operate/07-image-engine.md` とその棚の README からのリンク、
    `guide/ref/deploy-targets.md` の対応表に「画像生成」の行、`guide/ref/features.md` に自前
    ComfyUI の行を新設。（草稿もレビューも同ページの既存の「GPU クラス」の行を誤参照と呼んだが、
    誤りではない——`guide/admin/04-mcp-egress.md` に「GPU インスタンスクラス」の節が実在する——
    ので据え置く。）
  **完了条件は「LAN の ComfyUI 相手に `generate_image` が 1 枚返る」の実機 1 回**——ベンチが
  緑でも配線は測れていない前例がある（ADR 0072 P2）。この 1 回は ComfyUI のある網が要り、
  それを持つのは運用者だけである——セッションの仕事ではなく運用者の手順。
- **P1**: 先頭に管理画面からの URL と鍵の入力——文書レーンは「入力欄は無い・CP を再起動」を
  2 ページに書く羽目になり、それは決定 2 が「変数で困り始めたら」と言った当の瞬間である。次に
  メンバー向けの節（`guide/member/02-sessions.md` は CLI プランの経路しか説明せず、配備が提供
  するエンジン——ECS でも LAN でも——がセッションからどう見えるかは guide のどこにも無い）。
  そのあと basename 規則の改定、health の prober と稼働ヒートマップ、`/object_info` からの候補
  提示、`AF_COMFY_API_KEY` の reverse proxy 手順の文書。
- **P2**: docker 配備の GPU ホストで pinned イメージを起こすスクリプト（未解決 4）。
- **P3**: 同じ機構で `AF_LLM_URL`（LAN の llama-server / Ollama、chat 役）。`warmPath` の
  `/models` は llama.cpp router 専用なので空にする。この ADR の範囲外。

## 確認した出典（2026-09-11・このリポジトリのコード）

- Workspace 側が CP 経由でしか行かないこと: `workspace/agent/engines.go:169-172`（`AF_CP_BASE_URL`・
  `AF_ENGINE_ISSUE_TOKEN`）、`:395-404`（`api=images && provider` の突合と `base + base_url`）、
  `workspace/agent/internal/imagegen/comfy.go:61-75`（Ready は「URL と token がある」）。
- ゲートウェイの ECS 結合が 1 関数であること: `control-plane/engine_gateway.go:813`（`ensureReady`）、
  `:837`（`ensureStarted`）、`:803`（comfy は `/v1/` を付けない）、`:697`・`:726`（waking 以外の
  dial の失敗は既に `engine_unavailable`）。
- 登録側の結合: `control-plane/engines.go:365`（`service` 必須）、`:382-395`（AWS 設定を先に読む）、
  `:498`（全行に `engineECS`）、`:528`（制御ループ）、`engine_catalog.go:519`（SSM 無しは no-op）、
  `engine_table_reload.go:60`（reloader は SSM 無しなら nil、有れば 10 秒ごとに表を読み直す）。
- 既に nil 安全な箇所（決定 1）: `control-plane/engine_control.go:143`（`demand.record`）、`:587`
  （`ctrl.warmed`）、`engine_ecs.go:429`（`logKey`）、`engines.go:284`（`servedModel`）、
  `engine_gateway.go:516`（`pendingGuard`）、`engine_class.go:831`（`classStartHeld`）。
- VOICEVOX の前例: `control-plane/engine_ecs.go:446`（`newTTSEngineFromEnv`）、
  `engine_control.go:68`（`engineMode(v, managed)`）、`tts.go:128-165`、`engine_admin.go:196`
  （`managed`）、`:297`（`e.ecs == nil` で ECS 欄を省略）、
  `console/src/features/settings/admin/adminEngines.tsx:2914`（`admin.tts_external`。どの行でも
  通る）、`:548`（モードのセグメントは全行に `ondemand` を描く）。
- カタログの検査がフラグ基準であること: `control-plane/engine_admin.go:167`（`files_missing`）、
  `:908`（「S3 key の存在はここでは確かめない」）。
- 網: `deploy/compose/docker-compose.yml:23`（CP は host network）、
  `control-plane/internal/runtime/runtime_docker.go:280-284`（Workspace は NAT で外に出る）、
  `control-plane/workspace_lifecycle.go:378`（`AF_CP_BASE_URL = PUBLIC_BASE_URL`）、`:401`
  （`AF_ENGINE_ISSUE_TOKEN` はどの runtime でも注入される）、
  `control-plane/egress_proxy.go:144-155`（無条件に落とすのは loopback と link-local だけ）。
- 罠: `workspace/agent/engines.go:481`（basename 化）、
  `workspace/agent/internal/imagegen/http.go:165-196`（`comfy` の case 無し）。
- health のパス: `deploy/aws/ecs/cfn/60-engines.yaml:943`（comfy は `/system_stats`）。
- `guide/ref/features.md:124` は `guide/admin/04-mcp-egress.md:93`（「GPU インスタンスクラス」）を
  指しており、それは正しいページである——草稿とレビューの初回はそうでないと書いた。
- egress の遮断は未出荷: `guide/operate/04-secure.ja.md:78-81`（範囲の注記）。

## レビュー（2026-09-11・P0 の前）

0071 のレビューと同じ問い: 各決定は根拠から出ているか、草稿が「コードはこう言っている」と
書いたとおりにコードは言っているか。結論を先に: **P0 に入ってよい。** 崩れる前提は無い——
3 段の構図（Workspace は runtime を知らない・ゲートウェイの ECS 結合は 1 関数・登録側が全行に
ECS を生やす）はコードのとおりで、VOICEVOX の前例も主張どおり 1 関数である。レビューで変えた
ものは上の決定に織り込んだ。この節は何を見つけ、なぜ変えたかの記録である。

- **`file:line` の出典 18 件のうち 6 件が別の行を指していた**（`engines.go:453`、
  `engine_ecs.go:376`、`engine_admin.go:278`、`adminEngines.tsx:2772`、`60-engines.yaml:889`、
  コメント 1 行分ずれた `engine_gateway.go:756`）。すべて `sed -n` で読み直して上で訂正した。
  誰にも見つからない行を名指しする出典は無いより悪い——次の読者が正しい出典まで信じなくなる。
- **決定 2 は「表は起動時に 1 回しか読まない」と書いていた。** ADR 0074 まではそうだったが、
  いまは reloader が SSM を 10 秒ごとに読み直す。草稿の結論（URL の変更は再起動）は環境変数に
  ついては正しいが、reloader は合成行に対して表が変わるたびに再起動要求をログに書き、梯子を
  運ぼうとしただろう。external 行を飛ばすことにした。
- **決定 2 の優先順位を逆にした。**「常に env が勝つ」は、`AF_COMFY_URL` も設定した ecs-ec2
  配備で managed の `image` 行を差し替え、その ECS サービスを制御ループも停止ボタンも無い
  まま残す。managed の表の行が勝つ。
- **決定 2 の `AF_COMFY_API_KEY` に配管は要らない**: `apiKey` 欄は既にあり、`dial` も
  `engineHealthy` も付ける。草稿は health 確認にも bearer が乗ることを書いておらず、それは
  決定 7 の reverse proxy の設定を左右する。
- **決定 4 に新しいエラーコードは要らない**: 非ストリーミング経路は `errEngineWaking` 以外の
  dial の失敗を既に文言付きの `engine_unavailable` にする。草稿は新しい処理を匂わせていたが、
  P0 は即時に返ることと文言だけである。
- **決定 1 の「付けない」を箇所ごとに当たった**: `ecs`・`ctrl`・`demand`・`pending`・`ssm` の
  参照はほぼ全部が既に nil 安全で、安全でない 2 か所は決定 4・5 が書き換える当の関数である。
  P0 が不要なガードを足さないよう記録し、全ハンドラを通す 1 本のテストで固定する。
- **決定 8 の 5 秒の確認は管理パネルを止めていた。** 一覧のハンドラは同期である。2 秒と
  10 秒のキャッシュにした。
- **決定 5 に契約が無かった。** 行の欄を書き下ろし、CP と Console を別々のセッションで作れる
  ようにした。Console の既存の `managed` 分岐が汎用であること（TTS の文字列を流用）と、
  `ondemand` ボタンがいま全行に出ることを確認した。
- **決定 6 の `files_missing` の主張は** models の経路自身のコメントで確認した。
- **文書の一覧は「`guide/operate/` の新しい節」としか書いていなかった。** その棚は 01〜06 の
  番号付き・二言語なので、両言語の `07-image-engine` と README の行になる
  （`scripts/docs-check.py` が対を強制する）。
- **題名は「ゲートウェイはそのまま」だった**が、決定 4 は `engine_gateway.go` の関数を変える。
  正しいのは「転送経路はそのまま」なので、そう改めた。
- **完了条件には運用者の網が要る。** フェーズに明記した: セッションは LAN の ComfyUI を用意
  できないので、実機 1 回は 4 レーンのマージ後の運用者の手順である。

文書レーンが P0 を書きながら見つけたもの（同日 2026-09-11）:

- 🔴 **レビューの「誤った参照先」がそれ自体誤りだった。** `guide/ref/features.md:124` は「推論
  エンジンの GPU クラス」を `guide/admin/04-mcp-egress.md` に送っており、そのページには
  ADR 0074 の内容を持つ「GPU インスタンスクラス」の節がある。レビューは参照先を開かずに草稿の
  主張を繰り返した——誰も確かめなかった出典と同じ過ち。上で撤回し、レーンは代わりに自前
  ComfyUI の行を足した。
- 🔴 **決定 7 の 1 つ目の手はまだ存在しない。** egress の遮断は後続の作業と文書化されており、
  今日運用者が組める防御は reverse proxy だけである。決定 7 に注記した。
- **P1 の項目が 2 つ浮かんだ**: 管理画面の URL 入力欄を P1 の先頭へ、配備が提供するエンジンの
  メンバー向けの節（ECS でも guide に無い）。
