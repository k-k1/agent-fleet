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
| **Start は確定させるだけで、Agent を待たない** | `Start` は HTTP リクエストの中で走るので、起動が確定した時点で返り、入口のアイドルタイムアウトを越えて待たない。準備の遅れは**エラーではない**——エラーを返すと、動いている起動中のワークスペースを失敗扱いにする（[03 §3.3](03-control-plane.ja.md)）|
| **`starting` を正直に報告する** | 4 本とも報告する。docker と native では、プロセスは上がったが entrypoint がまだ Agent に届いていない間。ECS では、サービスが収束中の間。**`starting` の間、呼び出し側は再 Start もアイドル停止もしてはいけない**。アダプタは期限を切ること——収束しない `starting` は誰も操作できないワークスペースになる |
| **Runtime の値に状態を持たない** | マネージャは DB の行から Runtime を組み（`manager.runtimeFor`）、キャッシュすることも、いつ組み直すこともある。再起動した CP や 2 台目のレプリカは別の値を持つ。すべてを土台の上で、決まった名前・タグ・ファイルで見つけ、別のリクエストが読み戻すものも土台に書く。既存のアダプタは `docker inspect`・pidfile・ECS のサービス・EC2 のタグを使う（[09 §9.5](09-deploy.ja.md)）|
| **停止は二段の graceful** | シグナル → `AF_STOP_GRACE_SEC` 待つ → kill。Agent には**それより短い**猶予（`AGENT_STOP_GRACE_SEC`）を渡し、pane を中断して tmux を先に終わらせる（[03 §3.3](03-control-plane.ja.md)）|
| **CP が常に届くエンドポイント** | `Endpoint()` は、CP より後に作られたワークスペースも含め、どの CP レプリカからも解決できなければならない。ECS では、後から足したサービスに Service Connect のエイリアスが効かず、`agent_dial.go` はその穴を埋めるために在る |
| **組まれたときの env を、毎回の起動で渡す** | ファクトリの `New(ws, secretKey, extraEnv)` は DEK とワークスペースごとの変数を運び、それは起動ごとに違う（スケジューラの無人起動、プレビューのスラグ、egress プロキシの変数）。コンテナの env は起動の瞬間に固定されるので、そこで入れるしかない。**DEK と `AGENT_TOKEN` を、土台が見せられる場所に置かない**——docker はコマンドラインではなく 0600 の env ファイルで、ECS は値ではなく SSM の参照で渡す（[09 §9.5](09-deploy.ja.md)、[07 §7.6](07-security.ja.md)）|
| **停止をまたいで残る 2 つの領域** | `/home/dev` のホームと、`/var/lib/af/claude`（`CLAUDE_CONFIG_DIR`）の Claude の状態。後者をホームから離しているのは、ファイルブラウザから届かないようにし、ホームを初期化しても Claude のログインに触れないようにするため。**ホームを変える操作は、実際のホームに届かなければならない**——あなたの形態がホームをどこに置くにせよ。既存の各形態の置き方は [01 §1.6](01-architecture.ja.md) と [07 §7.2](07-security.ja.md) |
| **Destroy** | `runtimeDestroyer` は全アダプタに必須で、`runtime.go` で表明している。ホームと、作ったメンバーシップごとのリソースを全部消す。戻り値の `[]string` は、消せなかったと**分かっている**ものの一覧で、運用者が「データは消えた」と思い込む代わりに監査ログへ届く |
| **ユーザー毎に隔離する。できないなら共有で動かすのを拒否する** | [07 §7.2](07-security.ja.md) の全行に、あなたの形態の答えを書く。書けないなら `native` と同じにする——ファクトリが `dev` 以外の `AUTH` を拒否する。コンテナ境界が無ければ、ユーザーを隔てるものが何も無いから |

## 21.3 任意の能力——本当のことだけ名乗る

CP は形態ごとの振る舞いの多くを型アサーション（Runtime なら `rt.(X)`、ファクトリなら
`m.rtFactory.(X)`）で尋ね、その答えで分岐します。**能力を名乗らないアダプタもコンパイルは
通り、機能が黙って無くなるだけです**。だから能力を名乗ることは、あなたの土台についての主張です。

以下は執筆時点で 4 本が名乗っているもの。本当の一覧はインタフェースそのものです——
`control-plane/*.go` を `rt.(` と `rtFactory.(` で grep してください。

| 能力 | 宣言の場所 | 名乗っているもの | 無いとどうなるか |
|---|---|---|---|
| `SizingProfile()`: CPU・メモリ・ディスクがここで何を意味するか | `workspace_sizing.go`（`sizingProfiler`）| 4 本すべてのファクトリ | docker として説明される |
| `CostProfile()`: 請求があるか、何を含むか | `cost_profile.go`（`costProfiler`）| 4 本すべてのファクトリ | コストの画面が無く、バージョン情報はランタイムを `local` と報告する |
| `WorkspaceImage()` | `main.go` と `version_info.go` のインラインのインタフェース | `ecs`、`ecs-ec2` | 起動時のバナーは docker のテンプレートのイメージを出し、バージョン情報は Workspace イメージを載せない |
| `DocsMounter`: ガイドを bind mount する | `internal/runtime/runtime.go` | docker、native | コンテナが `GET /internal/docs` から取得する（[04 §4.9](04-agent.ja.md)）。コンテナから見えるホストのパスが無いのに名乗ると、ガイドが空になる |
| `Stale()`: 停止して起動し直すと別のコードが動くか | `workspace_stale.go` | 4 本すべて | 古いと報告されることがない |
| `BootPhase()` | `workspace_handlers.go` のインラインのインタフェース | native、`ecs-ec2` | 起動ダイアログにフェーズが出ない |
| `AcquireOperationFence` / `StartFencer` | `internal/runtime/runtime.go` | native | DB のリースだけになる。ライフサイクルの資源が CP のホスト上にあるアダプタは、OS レベルの柵も要る |
| `MachineProfile()`、`ResizeHome()` | `workspace_machine.go`、`workspace_home_resize.go` | `ecs-ec2` | 名指す箱も、広げるディスクも無い |
| `BeginHibernate()`、`BackupHome()` | `reaper.go`（アイドルの段 3 と 4）| `ecs-ec2` | その段があなたには存在しない（[03 §3.7](03-control-plane.ja.md)）|
| `GoldenBakePool` / `GoldenSeedRuntime` | `internal/runtime/runtime_ecs_ec2_golden.go` | `ecs-ec2` | ゴールデンスナップショットは焼かれない |
| `PoolStatus`、`TerminateQuarantinedSlot`、`MaxSlots` | `workspace_lifecycle.go`、`limits.go` | `ecs-ec2` | プールの画面が無く、テナントの上限を固定プールと突き合わせる検査も無い |

名乗る側は、宣言の隣の `var _ X = (*T)(nil)` で固定しています。名乗らない側はその書き方が
できないので、`internal/runtime/capabilities_test.go` が表明しています。両方にあなたの
アダプタを足してください。

## 21.4 アダプタの仕事ではないもの

- **アイドルの判断**。reaper は共通で、アダプタは `State` を提供して `Stop` を実行するだけ。
  専用の土台が要る段（ハイバネート、別ゾーンへのバックアップ）だけが能力になっている
  （[03 §3.7](03-control-plane.ja.md)）。
- **認証とテナント解決**。どちらも Runtime が組まれるずっと前に終わっています。
- **エンジン**。配備がエンジンを持つのはエンジン表がそう宣言するからで、ランタイムのせいでは
  ありません。`control-plane/engine_*.go` のどのファイルも `AF_RUNTIME` を読みません
  （[09 §9.2](09-deploy.ja.md)、[03 §3.9](03-control-plane.ja.md)）。
- **egress のポリシー**。プロキシの変数は `extraEnv` で全ワークスペースに届きます。あなたの
  形態が持つのは、ワークスペースが置かれるネットワークのほう——何に届くか、誰がその Agent に
  届くか——です（[07 §7.2](07-security.ja.md)、[07 §7.8](07-security.ja.md)）。
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
- **`internal/runtime/runtime_test.go`** は、各プロファイルがどのアダプタを組むかと、未知の
  プロファイルが拒否されることを確かめます。あなたのプロファイルの場合を足してください。
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
