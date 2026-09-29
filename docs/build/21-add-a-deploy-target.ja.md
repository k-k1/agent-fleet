---
audience: "ワークスペースを動かす場所を足す人"
source_of_truth: "`control-plane/internal/runtime/`——`runtime.go` の `Runtime` ポートと `NewFactory`、CP が問い合わせる任意の能力、そしてアダプタ本体（`runtime_*.go`）"
updated: "2026-09"
---

# 21. デプロイ形態を足す

[English](21-add-a-deploy-target.md) | 日本語

デプロイ形態とは、`runtime.NewFactory`（`control-plane/internal/runtime/runtime.go`）が
受け付けるプロファイルの値と、その背後のアダプタのことです。現在のスイッチが知っているのは
`docker`（`local` と空値も同じ）、`native`（`wsl`）、`ecs`（`aws`）、`ecs-ec2` です。
それぞれが何で、どう運用するかは [09](09-deploy.ja.md) と [01 §1.6](01-architecture.ja.md)、
それぞれが何に対応するかは [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)、
運用者の選び方は [operate/01](../../guide/operate/01-choose.ja.md) にあります。この章は、
新しいアダプタが何を用意しなければならないか、その周りで何を変えるかを扱います。

書く前に既存のアダプタを読んでください。互いの差は、新しい 1 本に普通必要な差より大きいです。

- **`runtime_docker.go`** から読み始める。必須のメソッドが全部あり、土台が要らないものは何も無い。
- **`runtime_native.go`** は、コンテナの無い形態が自分でやることを示す。`docker inspect` の
  代わりの pidfile、DB のリースに並ぶ OS レベルのロック、別のリクエストが読み戻せるようにする
  起動フェーズのファイル。
- **`runtime_ecs_ec2*.go`** は任意の能力をほぼ全部名乗っている。形態がどこまで行けるかの例であって、
  最低限の例ではない。

## 21.1 形態とは何か

- **`NewFactory` のスイッチのプロファイル、アダプタ、名乗る任意の能力、`deploy/` の下の木、
  そして出荷の手段**。最初の 3 つは `internal/runtime/` のコードで、残りは §21.5。
- **コアは共通です**。CP・Console・Agent・Workspace イメージは全形態で同じ。出荷している
  `native` パッケージは、そのイメージの rootfs を bubblewrap の中で動かします。イメージを使わず
  ホストでビルドした Agent を動かす素のモード（`AF_NATIVE_AGENT_BIN`）は、`run-dev.sh native`
  の開発用ワークフローです。**あなたの形態が別の Workspace イメージを必要とするなら、止まって
  ください**——移植性を成り立たせている唯一の性質が壊れます。
- **コアはアダプタに尋ね、プロファイル名はめったに見ません**。振る舞いは文字列の比較ではなく、
  §21.3 の任意インタフェースで選ばれます。例外はあり、小さいものです。たとえば `main.go` は
  `mgr.nativeRuntime` を立ててセッション数の上限を外し、Console はランタイムが `ecs-ec2` の
  ときだけスロットプールの画面を出します。自分のプロファイル名がどこにも出てこないと決める前に、
  `control-plane/*.go` を `AF_RUNTIME` で、`console/src` をランタイム ID で grep してください。
- **未知のプロファイルは起動時に fail-fast します**（`unknown AF_RUNTIME profile`）。`docker`
  になるのは空値だけです。これは維持してください。黙って違う土台で動いた配備は、起動を拒否する
  配備よりはるかに悪い。
- **新しい土台は、既存形態のフラグではなく新しいプロファイルにします**。`ecs` と `ecs-ec2` は
  意図して別プロファイルで、配備はコードを戻さず値 1 つを変えるだけで戻れます
  （[decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md) 決定 10-1）。

## 21.2 必須の契約

`Runtime` インタフェースは `Start`・`Stop`・`State`・`Endpoint`・`Token`・`Name` で、
その doc コメントが契約です。見落としやすいのはここです。

| 義務 | 意味 |
|---|---|
| **Start は起動を確定させる。Agent の準備はその結果ではない** | `Start` は HTTP リクエストの中で走る。Agent の `/healthz` を待つ猶予を持ってよい（ローカルのアダプタは `WaitAgentHealthy` でそうしている）が、その待ちは前に立つ入口の予算に収め、越えても**エラーにしない**——エラーを返すと、動いている起動中のワークスペースを失敗扱いにする。ECS はサービスを確定させた時点で返り、収束は裏で進む（[03 §3.3](03-control-plane.ja.md)、`runtime_health.go` の冒頭）|
| **`starting` を正直に報告する** | `starting` は、起動が進行中で Agent にまだ届かないことを意味する。**`starting` の間、呼び出し側は再 Start もアイドル停止もしてはいけない**ので、収束しない `starting` は誰も操作できないワークスペースになる。ポートの doc コメントはアダプタに期限を求めているが、既存の経路がすべてそうなってはいない——ECS が配置を拒むタスクはサービスを無期限に `starting` のままにし、`ecs-ec2` はせめてその理由を起動フェーズとして出す（`notePlacementBlocked`）。あなたのアダプタには期限を持たせるか、収束しないことがあり得ない理由を書くこと |
| **ライフサイクルの状態は土台の上に在る** | マネージャは DB の行から Runtime を組み（`manager.runtimeFor`）、キャッシュすることも、いつ組み直すこともある。再起動した CP や 2 台目のレプリカは別の値を持つ。だからワークスペースが在るか・動いているか・起動中かは、土台の上で、決まった名前・タグ・ファイルから取り戻せなければならない。既存のアダプタは `docker inspect`・pidfile・ECS のサービス・EC2 のタグを使う（[09 §9.5](09-deploy.ja.md)）。失ってよいものはプロセス内の一時的な置き場に持ってよいが、失ったときに何が起きるかを書くこと——native は未確定の spawn を片付けるために値の中に持ち、`ecs-ec2` は起動中の手順を、別のレプリカからは見えないプロセス全体の map に持つ |
| **停止は二段の graceful** | シグナル → `AF_STOP_GRACE_SEC`（既定 30）待つ → kill。Agent には、その猶予から 5 秒の余裕を引いた `AGENT_STOP_GRACE_SEC` を渡し、pane を中断して tmux を先に終わらせる。この導出は 5 秒を下限にするので、猶予を 10 未満にすると余裕が無くなる（`runtime_docker.go` の `stopGraceSec` / `agentStopGraceSec`、[03 §3.3](03-control-plane.ja.md)）|
| **CP が常に届くエンドポイント** | `Endpoint()` は、あなたの形態が CP を動かす場所から届かなければならない——docker と native はループバックのアドレスを返すので同じホストから、ECS ではどの CP レプリカからも。CP より後に作られたワークスペースも含む。ECS では、後から足したサービスに Service Connect のエイリアスが解決されず、`agent_dial.go` はその穴を埋めるために在る |
| **組まれたときの env を、毎回の起動で渡す** | ファクトリの `New(ws, secretKey, extraEnv)` は DEK とワークスペースごとの変数を運び、それは起動ごとに違い得る（スケジューラの無人起動、プレビューのスラグ）。コンテナの env は起動の瞬間に固定されるので、そこで入れるしかない。配備全体のテンプレートの env（`Config.ExtraEnv`：`WS_ENV` と egress プロキシの変数）は別の入力で、現在これを渡すのは docker と native だけ（[09 §9.4](09-deploy.ja.md)）——あなたのアダプタが渡すかどうかは意図して決めること。**DEK と `AGENT_TOKEN` を、土台が見せられる場所に置かない**——docker はコマンドラインではなく 0600 の env ファイルで、ECS は値ではなく SSM の参照で渡す（[09 §9.5](09-deploy.ja.md)、[07 §7.6](07-security.ja.md)）|
| **停止をまたいで残る 2 つの領域** | `/home/dev` のホームと、`/var/lib/af/claude`（`CLAUDE_CONFIG_DIR`）の Claude の状態。後者をホームから離しているのは、ファイルブラウザから届かないようにし、ホームを初期化しても Claude のログインに触れないようにするため。**ホームを変える操作は、実際のホームに届かなければならない**——あなたの形態がホームをどこに置くにせよ。既存の各形態の置き方は [01 §1.6](01-architecture.ja.md) と [07 §7.2](07-security.ja.md) |
| **Destroy** | `runtimeDestroyer` は全アダプタに必須で、`runtime.go` で表明している。ホームと、作ったメンバーシップごとのリソースを全部消す。戻り値の `[]string` は、消せなかったと**分かっている**ものの一覧で、運用者が「データは消えた」と思い込む代わりに監査ログへ届く |
| **ユーザー毎に隔離する。できないなら共有で動かすのを拒否する** | [07 §7.2](07-security.ja.md) の全行に、あなたの形態の答えを書く。書けないなら `native` と同じにする——ファクトリが `dev` 以外の `AUTH` を拒否する。コンテナ境界が無ければ、ユーザーを隔てるものが何も無いから |

## 21.3 任意の能力——本当のことだけ名乗る

CP は形態ごとの振る舞いの多くを型アサーション（Runtime なら `rt.(X)`、ファクトリなら
`m.rtFactory.(X)`）で尋ね、その答えで分岐します。**能力を名乗らないアダプタもコンパイルは
通り**、CP はその能力ごとの代わりの道を黙って選びます——機能を隠すことも、別の届け方に切り替える
ことも、別の形態を説明する既定値を出すこともあります。だから名乗るか名乗らないかは、あなたの
土台についての主張です。

以下は執筆時点の能力と、それが無いとき CP が何をするか。本当の一覧はインタフェースそのものです
——`control-plane/*.go` を `rt.(` と `rtFactory.(` で grep し、実装していて読む価値のある
アダプタはメソッド名で grep してください。利用者から見てどの形態が何に対応するかは
[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) にあります。

| 能力 | 宣言の場所 | 無いとどうなるか |
|---|---|---|
| `SizingProfile()`: CPU・メモリ・ディスクがここで何を意味するか | `workspace_sizing.go`（`sizingProfiler`）| docker として説明される |
| `CostProfile()`: 請求があるか、何を含むか | `cost_profile.go`（`costProfiler`）| コストの画面が無く、バージョン情報はランタイムを `local` と報告する |
| `WorkspaceImage()` | `main.go` と `version_info.go` のインラインのインタフェース | 起動時のバナーは docker のテンプレートのイメージを出し、バージョン情報は Workspace イメージを載せない |
| `DocsMounter`: ガイドを bind mount する | `internal/runtime/runtime.go` | コンテナが `GET /internal/docs` から取得する（[04 §4.9](04-agent.ja.md)）。コンテナから見えるホストのパスが無いのに名乗ると、ガイドが空になる |
| `Stale()`: 停止して起動し直すと別のコードが動くか | `workspace_stale.go` | 古いと報告されることがない |
| `BootPhase()` | `workspace_handlers.go` のインラインのインタフェース | 起動ダイアログにフェーズが出ない |
| `AcquireOperationFence` / `StartFencer` | `internal/runtime/runtime.go` | DB のリースだけになる。ライフサイクルの資源が CP のホスト上にあるアダプタは、OS レベルの柵も要る |
| `MachineProfile()`、`ResizeHome()` | `workspace_machine.go`、`workspace_home_resize.go` | 名指す箱も、広げるディスクも無い |
| `BeginHibernate()`、`BackupHome()` | `reaper.go`（アイドルの段 3 と 4）| その段があなたには存在しない（[03 §3.7](03-control-plane.ja.md)）|
| `GoldenBakePool` / `GoldenSeedRuntime` | `internal/runtime/runtime_ecs_ec2_golden.go` | ゴールデンスナップショットは焼かれない |
| `PoolStatus`、`TerminateQuarantinedSlot`、`MaxSlots` | `workspace_lifecycle.go`、`limits.go` | プールの画面が無く、テナントの上限を固定プールと突き合わせる検査も無い |

固定されているのは一部だけです。`Runtime`・`RuntimeFactory`・`runtimeDestroyer`・ゴールデンの
インタフェース・ハイバネートの連鎖（`runtime_seam.go`）にはコンパイル時の
`var _ X = (*T)(nil)` があり、`internal/runtime/capabilities_test.go` は `DocsMounter` と
`GoldenBakePool` を名乗っては**いけない**アダプタを表明しています。サイジングとコストを含む
残りは、実行時に照合されるだけです。名乗る能力にも名乗らない能力にも、それが変わったら落ちる
表明かテストを足してください。

## 21.4 アダプタの仕事ではないもの

- **アイドルの判断**。reaper は共通で、アダプタは `State` を提供して `Stop` を実行するだけ。
  専用の土台が要る段（ハイバネート、別ゾーンへのバックアップ）だけが能力になっている
  （[03 §3.7](03-control-plane.ja.md)）。
- **認証とテナント解決**。どちらも Runtime が組まれるずっと前に終わっています。
- **エンジン**。配備がエンジンを持つのはエンジン表がそう宣言するからで、ランタイムのせいでは
  ありません。`control-plane/engine_*.go` のどのファイルも `AF_RUNTIME` を読みません
  （[09 §9.2](09-deploy.ja.md)、[03 §3.9](03-control-plane.ja.md)）。
- **egress のポリシー**。許可リスト・プロキシ・enforce のスイッチは CP のものです。あなたの
  形態が持つのは、ワークスペースが置かれるネットワーク（何に届くか、誰がその Agent に届くか）と、
  プロキシの変数がコンテナに届くかどうかです（§21.2、[07 §7.2](07-security.ja.md)、
  [07 §7.8](07-security.ja.md)）。
- **Workspace イメージ**（§21.1）。

## 21.5 アダプタの外——deploy の木、runbook、出荷の手段

- **`deploy/<target>/`** にスクリプトとテンプレートを置き、runbook はその隣の `README.md`
  にします。木の索引は [deploy/README.md](../../deploy/README.md)。
- **runbook がワークスペースに届くのは、`deploy/release/stage-docs.sh` が名指したときだけです**。
  このスクリプトの `RUNBOOKS` マップは glob ではなく意図して明示的で、ガイドから runbook への
  リンクの書き換えと runbook の索引の生成も同じスクリプトがしています。3 か所ともに足してください。
- **出荷**。`deploy/release/build.sh` が compose のバンドルとイメージ（`--compose`）、native の
  tar と rootfs（`--native`）を作り、AWS 形態は `deploy/aws/ecs/release-ecr.sh` でイメージを
  公開します。新しい成果物が要る形態は、自前のスクリプトを育てずにそこへ足します。
- **新しい環境変数**は [09 §9.4](09-deploy.ja.md) へ。

## 21.6 費用とレイテンシは設計の一部

- **自分に対して引用すべき請求は、アイドル停止が壊れたときの額です**。タスク単位で課金される
  形態は、アイドル停止が効いて初めて数字が成り立ちます。[09 §9.8](09-deploy.ja.md) の「24/7」の
  行がその額です。
- **対策を選ぶ前に、数字を内訳に分ける**。Fargate の起動はイメージ pull が支配的だと思われ、
  遅延ロードが検討されました。測ると pull は小さいほうの割合で、採用されませんでした。初回起動の
  504 は、ロードバランサのアイドルタイムアウトより長い同期待ちが原因で、`Start` が待たないのは
  そのためです（測った内訳は [09 §9.5](09-deploy.ja.md)）。

## 21.7 検証

- **`internal/runtime/capabilities_test.go`** は、名乗るべきでないステージ済み docs や
  ゴールデンの焼きをアダプタが名乗ると落ちます。あなたのアダプタをそこへ足してください。
- **`internal/runtime/runtime_test.go`** は、docker の別名と `ecs` / `aws` がそれぞれの
  アダプタを組むことと、未知のプロファイルが拒否されることを確かめます。`native` のファクトリの
  テストは `runtime_native_test.go` にあり、`ecs-ec2` の場合はそこにありません。あなたの
  プロファイルの場合を足してください。
- **`scripts/docs-check.py` は [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) の
  1 列目（両言語）を `NewFactory` の case ラベルと突き合わせます**。ただし対象は
  `unknown AF_RUNTIME profile … (want …)` のエラー文に名前のあるプロファイルだけです。そこに
  名前を足さないと、検査はあなたのプロファイルを見ません。
- **fleet の E2E はあなたのアダプタを通りません**。公開 API だけで CP を動かしますが、起動する
  のは docker プロファイルで、docker とビルド済みの Workspace イメージが要ります
  （`e2e/fleet_test.go`）。AWS のアダプタには専用のハーネスがあります——`ecs-ec2` の実機テスト
  （`AF_ECS_EC2_LIVE=1`、準備は `deploy/aws/ecs/harness/`）と、deploy スクリプトの実行順を守る
  `deploy/local/ecs-lifecycle-stub-test.sh`。
- **実際に建てて実セッションを通すこと**。ECS のアダプタのコメントには、テストでは出ず実配備で
  出た欠陥が記録されています。`agent_dial.go` の Service Connect の穴はその 1 つです。

## 21.8 完了の条件

1. `NewFactory` がプロファイルを受け付け、未知のプロファイルのエラー文がその名前を挙げ、
   `runtime_test.go` がそれを確かめている。
2. [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) の最初の表に行が、能力の表に列が
   在る（**正直な「—」も含めて**、両言語で）。
3. 形態ごとの事実を持つ章にあなたの形態が在る——[01 §1.6](01-architecture.ja.md)、
   [07 §7.2](07-security.ja.md)、[09](09-deploy.ja.md) の形態（§9.1）・ノブ（§9.2）・環境変数
   （§9.4）・対比表（§9.6）。
4. [operate/01](../../guide/operate/01-choose.ja.md) に**いつ選ぶか**——そして自明な選択でない
   なら**いつ選ばないか**——が書いてある。
5. runbook が、それが操作するスクリプトの隣に在り、`stage-docs.sh` が出荷している（§21.5）。
6. [decisions/](../decisions/) に「なぜ既存形態のフラグではなく別形態なのか」の記録が在る——
   この問いは毎回されます。
