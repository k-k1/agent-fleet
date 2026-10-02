# 0106. Agent Fleet は `kubernetes` ランタイムプロファイルとして Kubernetes で動かし、最初は GKE で検証する。それが出るまで、Google Cloud での答えは GCE VM 上の compose とする

[English](0106-kubernetes-runtime.md) | 日本語

- 状態: **proposed**（2026-10-02）。まだ何も作っておらず、何も測っていない。以下の事実はコード・文書・Kubernetes と
  Google Cloud の資料を読んで得たもので、動かしていないものはその旨を書いている。
- 追跡: #1092
- 見直す決定: [docs/log/35](../log/35-packaging.md) §35.3-5 と `docs/log/roadmap.md` P3-10 の Kubernetes の棚上げ
  （「Helm chart は需要が出るまで棚上げ。AWS の答えは ECS + CFN」、2026-07-21）。**Helm chart そのものは棚上げのまま**（決定 12）。
- 関連: [0045](0045-ec2-persistent-workspace.ja.md)（ecs-ec2、その keep 領域、決定 10-1「新しい基盤は新しいプロファイル」）/
  [0047](0047-tenant-network-restriction.ja.md)（プロキシの後ろでのクライアントアドレス）/
  [0087](0087-efs-metadata-io.ja.md)（ネットワークファイルシステムが home に課すコスト）/
  [0104](0104-long-lived-member-workspace.ja.md)（Workspace が永続 home を持つ理由）/
  [docs/build/21](../build/21-add-a-deploy-target.ja.md)（配備先が満たすべきもの）

## 背景

### 棚上げが待っていた需要

Google Cloud と Kubernetes を基盤にしている利用者から、対応しているかという問い合わせがあった。その利用者にとって障害は AWS
の配備先そのもので、今 Agent Fleet を動かすには、すでに運用している基盤の横に AWS アカウント・CloudFormation・ECS を立てる
必要がある。docs/log/35 の棚上げは「需要が出るまで」だった。これがその需要で、Google Cloud と Kubernetes の両方を名指している。

### すでに移植できているもの

- **配備先ごとの意味のある違いは `Runtime` ポートの後ろにある**（`control-plane/internal/runtime/runtime.go` と
  [21 §21.3](../build/21-add-a-deploy-target.ja.md) の任意の能力）。プロファイル名を比べている少数の箇所（native のセッション
  上限、Console のスロットプール画面）は [21 §21.1](../build/21-add-a-deploy-target.ja.md) に挙がっており、ここで要るものは
  無い。Workspace イメージはどこでも同じ成果物。
- **CP は `AF_RUNTIME=docker` なら AWS の認証情報なしで起動する。** ストアは SQLite か素の Postgres で、RDS 固有なのは
  任意の Secrets Manager ローテーション読み取りだけ。ログインは汎用 OIDC で、Google はすでに一級のプロバイダ。
- **ランタイム以外の AWS 依存はすべて環境変数かプロファイルで切られ**、compose と native では無効になる。Cost Explorer の
  ショーバック（[0048](0048-member-cloud-cost.ja.md)）、Polly、マネージド GPU エンジン、Cloud Map への接続フォールバック
  （`agent_dial.go`）。

### AWS 専用のもの

`ecs` と `ecs-ec2` アダプタ、CloudFormation スタック、コスト表示、Polly、マネージド GPU エンジン。このうち Kubernetes クラスタと
動く配備の間に立つのはアダプタだけで、残りは配備に無くても成り立つ機能である。

### docker プロファイルはすでに任意の Linux VM で動く

`deploy/aws/ec2-single` は「1 台の VM で compose」に AWS 向けの構築スクリプトを付けたもの。docker アダプタは標準ライブラリと
docker CLI しか使わない。同じ compose バンドルを GCE VM で動かすのにアダプタのコードは要らず、要るのは手順書、egress の既定値
1 つ、前に置くネットワークの Google Cloud 固有の事情だけ（決定 1）。

### 新しい基盤が満たすべきこと

- **イメージに init プロセスが無い。** docker は `--init`、ECS は `initProcessEnabled` で与えている。無いと agent が PID 1 になり、
  CLI や tmux が終わるたびに残るプロセスを誰も回収しない。
- **永続領域が 2 つある。** `/home/dev` の home と `/var/lib/af/claude` の Claude の状態。ファイルブラウザが後者に届かず、
  home を初期化してもログインが残るように分けてある（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。
- **Recreate と Clean home は「home を消す」ではない。** Recreate は `~/repos` を消し、Clean home は `homeKeep` の 7 項目
  （ログイン・接続・ID）以外をすべて消す（`internal/runtime/home_wipe.go`、`runtime_docker.go`）。ecs-ec2 はこの 7 項目を
  home の外の keep 領域へ移しており（`AF_WS_KEEP`、`workspace/entrypoint.sh` が扱う）、だから home のボリュームを丸ごと消せる。
- **起動ごとの環境は bearer トークンを運ぶ。** DEK と `AGENT_TOKEN` だけではない。`workspace_lifecycle.go` が内部 git・メモ・
  スケジュール・MCP・docs・エンジン・OAuth のトークンを発行して入れる。ECS は今それらをタスク定義の素の環境変数で渡しており、
  SSM を通すのは DEK と `AGENT_TOKEN` だけ。
- **Workspace は CP を呼び返す。** 宛先は `AF_CP_BASE_URL` で、これは公開のベース URL
  （`workspace_lifecycle.go`、`workspace/agent/docs_sync.go`）。egress プロキシを使うときも、それは CP の中で動く。
  同じ変数が Discord と Slack の通知の「Console で開く」リンクも組み立てる（`workspace/agent/internal/bridge/format.go`）
  ので、公開 URL のままでなければならない。
- **`starting` は Start を止める。** 起動のハンドラは状態が `running` か `starting` なら `Runtime.Start` を呼ばずにすぐ返り
  （`workspace_handlers.go`）、Recreate と Clean home は Stop・消去・そのハンドラを 1 つの要求の中で続けて呼ぶ。したがって進行中の
  停止を `starting` と読ませてはならない。読ませると、その後の起動が捨てられる。
- **クラウドの配備先では能力を足さない。** docker は Chromium のサンドボックスのために `SYS_ADMIN` を足すが、Fargate は何も
  足さない（[07 §7.2](../build/07-security.ja.md)）。能力の追加を禁じる基盤は Fargate と同じ水準であり、それより下ではない。

## 決定

### 1. Google Cloud には 2 段階で答え、最初は GCE VM 上の compose

最初の答えは 1 台の GCE VM 上の docker プロファイルで、`deploy/aws/ec2-single` の対になるもの。`deploy/gcp/gce-single/` に
手順書と `gcloud` だけで動く構築スクリプトを置く。アダプタのコードは要らないので、問い合わせた利用者は Kubernetes プロファイル
を作っている間も自分の基盤で Agent Fleet を動かせる。コードの変更は egress の既定値 1 つだけ。

- **egress の許可リスト**に `.googleapis.com` を足す。`.amazonaws.com` が AWS の道具のためにあるのと同じ
  （`control-plane/egress_policy.go`）。

手順書は前段を 2 通り定め、重ねる構成は対象にしない。

| 前段 | `AF_TRUSTED_PROXY_HOPS` | 出口アドレス | 注記 |
|---|---|---|---|
| **VM 上の Caddy**（既定） | compose と同じ 1。標準の Caddy は受け取った `X-Forwarded-For` を、自分が見た相手で置き換える | VM の予約済み静的外部 IP。外部 IP を持つ VM の通信は Cloud NAT を通らないので、これが出口にもなる | プレビューのサブドメイン用のワイルドカード証明書には DNS-01 が要るが、`caddy:2-alpine` にはそれが無い。手順書にこの制約を書く |
| **グローバル外部アプリケーションロードバランサ**（Caddy は外す） | 2。ロードバランサは `<client>, <load balancer>` を足し、`clientip.go` は右から数える | 予約した静的 IP を持つ Cloud NAT。VM は外部 IP を持たない | Certificate Manager の DNS 認証でワイルドカード証明書を得る。**通信中**の WebSocket は設定にかかわらず 24 時間で、**アイドル**のものはバックエンドサービスの `timeoutSec`（既定 30 秒）で閉じられるので、手順書でこれを延ばす |

従来型のアプリケーションロードバランサは使わない。通信中の WebSocket まで `timeoutSec` で閉じるからだ。

**Workspace を VM のメタデータサーバに届かせない。** EC2 では docker アダプタの `AWS_EC2_METADATA_DISABLED` と、ホストのホップ数
上限 1 が、Workspace をインスタンスロールから遠ざけている（`runtime_docker.go`、`ec2-single/cfn.yaml`）。GCE にはホップ数の上限も
SDK 共通の無効化スイッチも無く、`Metadata-Flavor: Google` を付けて `169.254.169.254` に届くプロセスは誰でも VM のサービス
アカウントのトークンを得る。そのため構築スクリプトは 2 つのことをする。VM はサービスアカウント無しか、ロールを何も持たない
ものにする。Caddy は HTTP-01 を使い、DNS レコードは運用者が自分の認証情報で作るので、VM に権限は要らない。そして起動のたびに
入れるホストのファイアウォール規則で、すべての Docker ブリッジから、VM が持つすべてのメタデータアドレスへの通信を落とす。
`169.254.169.254` と、VM が IPv6 を持つなら `fd20:ce::254`。サービスアカウントを外しても、インスタンスとプロジェクトのカスタム
メタデータは非公開にならないので、Workspace をそこから遠ざけるのはこの規則である。段階 0 は、Workspace の中からそれぞれの
アドレスへの要求が失敗するまで完了としない。

この段階で得られないのは compose がもともと与えないもの、つまり 1 台限り・スケールアウトなし・Workspace を止めても VM は
課金され続ける、の 3 つ。

### 2. プロファイルは `gke` ではなく `kubernetes`

新しいプロファイルは Kubernetes の標準 API だけを話し、Google Cloud の API は一つも呼ばない。GKE に固有のもの（ディスクの種類、
ノードプール、Workload Identity、ロードバランサ）は、アダプタではなく StorageClass・IaC・手順書で選ぶ。

問い合わせは Google Cloud と並べて Kubernetes を名指していた。Kubernetes を運用するチームはどこかでそれを運用しており、EKS・AKS・
オンプレのクラスタも同じプロファイルで済む。`gke` プロファイルにすると、最初の段階では要らない Google Cloud API を直接使える
代わりに、他のすべてのクラスタに「非対応」と答えることになる。最初に検証するクラスタは GKE Standard で、他で測るまでは
検証済みはそれだけとする。

[0045](0045-ec2-persistent-workspace.ja.md) の決定 10-1 に従い、独立したプロファイルにする。別名として `k8s` も受け付ける。

### 3. Workspace はレプリカ 0 か 1 の StatefulSet と、専用の Service

各 Workspace は Workspace キーから決定的に名付けた StatefulSet で、0 と 1 の間でスケールする。ECS サービスの desired 0/1 と
同じスケール・トゥ・ゼロの形。素の Pod でも Deployment でもなく StatefulSet にするのは、コントローラが通常どおり動く限り
**序数ごとに Pod を高々 1 つしか動かさない**からだ。古い Pod が消えるまで代わりを起動しない。ECS アダプタは、古いタスクが新しい
タスクの後ろで抜けきるまで `starting` を返す。Service Connect が 1 人のクライアントの要求を 2 つの agent に振り分けうるからだ
（`runtime_ecs.go` の `serviceRolledOut`）。ここではその重なりが起きない。

**Stop** は `replicas: 0` にし、`terminationGracePeriodSeconds` を `stopGraceSec()` から、Pod の環境の `AGENT_STOP_GRACE_SEC` を
`agentStopGraceSec()` から決める。他のどのアダプタも同じく注入している（`runtime_docker.go`）。agent がすでに想定している 2 段階の
停止と同じで、上限 120 秒も同じにし、1 つの `AF_STOP_GRACE_SEC` がどの配備先でも有効なままにする。Stop は `docker stop` と同じく **停止が収束してから返る**。収束とは次の 3 つを、この順で確かめたものをいう。
コントローラが Stop の書いた世代を観測したこと（`status.observedGeneration`）、レプリカが 0 と報告されていること
（`status.replicas` が 0）、そして Workspace の Pod が存在しないこと。前の 2 つが要るのは、Stop の直前に `replicas: 1` を読んだ
コントローラがまだ Pod を作成中でありえ、その Pod は「無い」と確かめた後に現れうるからだ。Stop は猶予に短い余裕を足した時間だけ
待つので、Recreate の Stop・消去・Start は ingress のタイムアウトに収まる。その時点でも収束していなければエラーを返す（下記の
応答しなくなったノード）。

**Start** はまず同じ収束の条件（または StatefulSet が無いこと）を求め、満たさなければエラーを返す。次に Workspace の Secret（決定 6）を書き、それから Pod
テンプレートと `replicas: 1` を 1 回の更新で書いて返る。ECS アダプタと同じく agent の `/healthz` は待たず、収束は `State` が追う
（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。テンプレートには起動の世代を示す注釈と、ダイジェストで固定したイメージ（決定 9）
を載せるので、他に何も変わらなくても起動のたびに新しいコントローラ revision ができる。Secret を書き直すときに Pod は存在しない
ので、前の起動のコンテナが新しい値を読むことは無い。Secret を書いた後で失敗した Start は Pod を動かしておらず、次の Start が
Secret を書き直す。

**State** は呼ばれるたびに基盤から読むので、再起動した CP や 2 台目の CP も名前ですべて取り戻せる
（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。

| 状態 | 条件 |
|---|---|
| `none` | StatefulSet が無い |
| `running` | `replicas` が 1、`status.observedGeneration` が `metadata.generation` に達しており、`status.updateRevision` のもので、テンプレートの現在の起動世代を持ち、`deletionTimestamp` が無く、Ready な Pod がある |
| `starting` | `replicas` が 1 で、`running` の条件が成り立たない。スケジュール・取得・起動中の Pod、最新のテンプレートをまだ観測していないコントローラ、ロールアウト中の場合を含む |
| `stopped` | `replicas` が 0。終了中の Pod が残っていても同じ |

readiness は agent 自身の `/healthz` を readiness probe にしたもので、Ready は他の配備先の `running` と同じ意味、つまり agent が
応答することを指す。古い agent を新しいものとして通さないのは世代の照合である。Start がテンプレートを書いた直後は、コントローラが
まだそれを処理しておらず、`status.updateRevision` は古い revision を指したままでありうる。観測済みの世代と Pod の起動世代を
求めることで、コントローラの処理のタイミングにかかわらずこの窓が閉じる。

終了中の Pod が残る `replicas: 0` を `starting` ではなく `stopped` と読むのは、ハンドラの早期 return で Start が捨てられないように
するためだ。その場合 Start 自身の確認が古い Pod の上での起動を断り、メンバーには黙って起動しなかったのではなく、やり直せる
エラーが見える。Stop が収束を待つので、この状態が見えるのは Stop が失敗したときだけである。

代償は、`stopped` がもはや何も動いていない証明にならないことだ。そして CP の呼び出し側は、状態をまさにその証明として使う。
Stop が失敗しても、`runtime.WorkspaceAlive(State())` がそうでないと言わない限り home の消去に進む（`workspace_handlers.go`、
`workspace_lifecycle.go`）。そのためアダプタ自身の破壊的な操作は状態を信用しない。`WipeHome` は印を記録するだけで、消去は次の
Start で行われ、その Start 自身が停止の収束を求める。`EraseHome` と `Destroy` はボリュームに触れる前に自分で収束の条件を確かめ、
成り立たなければエラーを返す。

**応答しなくなったノード上の Pod は置き換わらない。** 高々 1 つという保証の代償である。CP の開始期限（`start_deadline.go`）は
これを終わらせない。タスクがまだ動いていると数えられる Workspace は止めず、レプリカを 0 にしても届かないノード上の Pod は
消えないからだ。CP は Pod を強制削除しない。プロセスが死んだ証拠なしに名前を空け、1 つの home に 2 つの agent を走らせうるからだ。
復旧は運用者が行い、まさにその証拠から始める。まず古いプロセスが動きえないこと、つまり VM が停止または削除されたことを、
Kubernetes ではなくクラウド事業者の側で確かめ、そのうえで初めて Node を消すか out-of-service taint を付ける。Node オブジェクトを
消しても VM は止まらず、taint はボリュームを切り離す。VM の状態を確かめられないネットワーク分断の間は何もしない。手順書はこの
順序で手順を書く。

init プロセスは `shareProcessNamespace: true` で与える。Pod の pause コンテナが PID 1 になって孤児を回収し、docker 下の tini と
同じ役をする。この ADR では共有イメージに init を足さず、イメージを変えない方を選ぶ。

各 Workspace は ClusterIP の Service を持ち、`Endpoint()` はそのクラスタ DNS 名に対するクラスタ内の HTTP で、どの配備先とも同じく
`AGENT_TOKEN` で守られる。Service Connect の別名と違い、この名前は CP の起動後に作った Service でも引けるので、`agent_dial.go` の
回避策に当たるものはここでは要らない。

### 4. CP が作る 2 つのボリューム: home と、home の初期化を越えて残る状態

各 Workspace は CP が決定的な名前で作る 2 つの PersistentVolumeClaim を持つ。

- **home**: `/home/dev` にマウントする。
- **state**: `subPath` で 2 か所にマウントする。Claude の状態用に `/var/lib/af/claude` へ、もう 1 つは keep パスへ
  （`AF_WS_KEEP` を設定）。これで `workspace/entrypoint.sh` が、ecs-ec2 とまったく同じく `homeKeep` の 7 項目を home の外へ移す。

ログイン類はふだん state ボリュームにあるが、いつもではない。tmp に書いて rename するツールは home のリンクを普通のファイルに
置き換え、entrypoint が新しい方を戻すのは次の起動のときだけだ（`workspace/entrypoint.sh`）。そのため ecs-ec2
（`runtime_ecs_ec2_home_wipe.go`）と同じく、どの消去も home の最上位にある `homeKeep` の 7 つの名前を、種類を問わず残す。
home の操作は次のようになる。

| 操作 | 方法 |
|---|---|
| `WipeHome(repos)`（Recreate） | CP は消去を世代番号付きの注釈として StatefulSet に記録して返る。次の Start は同じイメージの init コンテナを足し、agent が起動する前に `~/repos` を消して、実行した世代を home に書く。後で再起動した Pod はその世代を見つけて何も消さないので、注釈を消すためにテンプレートを変える（Pod がロールする）必要は無い。ecs-ec2 の「印を付け、Start が消す」と同じで、ingress のタイムアウトに十分収まって返る |
| `WipeHome(clean)`（Clean home） | 同じ方法で、home の最上位から `homeKeep` の 7 つの名前以外をすべて消す |
| `EraseHome()`（管理者の Clean home） | 停止の収束を自分で確かめた後、CP が同じイメージから 1 回限りの消去用 Pod を動かす（決定的な名前とラベル、`restartPolicy: Never`）。その Pod は home のクレームをマウントし、`WipeHome(clean)` と同じものを消して終わる。CP はそれを待ち（Destroy と同じくらい時間がかかってもよい）、結果を読んでから Pod を消す。クレームそのものは残す |
| `ResizeHome()` | home のクレームの要求量を上げる（下の StorageClass の要件を参照） |
| `Destroy()` | 決定 5 |

init コンテナと消去用 Pod は、CP が組み立てたシェルコマンドを動かす。ecs-ec2 が SSM で送るコマンドを組み立てるのと同じだ
（`runtime_ecs_ec2_home_wipe.go` の `homeWipeCommand`）。使うのはイメージにある `sh`・`find`・`rm` で、keep の一覧は Go の
`homeKeep` から取る。イメージは変わらず、keep の一覧の出どころは CP の 1 か所になる。テストで、それが entrypoint の既定値
（`AF_WS_KEEP_DIRS`、`AF_WS_KEEP_FILES`。同じ 7 つの名前）と一致することを固定する。

管理者の Clean home は、要求から切り離された後は 5 分の予算で動く（`workspace_lifecycle.go` の `homeEraseBudget`）。それを
越えた消去はエラーになり、消去用 Pod は動き続ける。次の `EraseHome` が名前でそれを見つけ、待つ。

終わった消去用 Pod は誰も消してくれず、存在する間はクレーム保護の finalizer で home のクレームを押さえる。そのため消去用 Pod は、
意味を持つ場面では必ず名前で探す。`EraseHome` は動いているものを待ち、終わったものを消す（途中で落ちた CP はこうして再開する）。
`Start` は動いている間は断り、終わったものを消す。`Destroy` はクレームより先にそれを消す。これは Workspace の Pod ではなく、
停止の収束の確認と `State` は StatefulSet 自身の Pod だけを見る。

クレームを `volumeClaimTemplates` でなく CP が自分で作るのは、テンプレートのサイズは StatefulSet の作成後に変えられず、home は
大きくできる必要があるからだ。明示的なクレームなら、そのライフサイクル（初回起動で作り、停止では残し、Destroy で消す）も保持
ポリシーではなくアダプタのコードに置ける。

**アクセスモードは、CSI ドライバが対応していれば `ReadWriteOncePod`**（GKE の Persistent Disk ドライバについては、段階 1 で固定する版で確かめる）、
そうでなければ `ReadWriteOnce`。`ReadWriteOnce` はボリュームを 1 つのノードに限るだけで、1 つの Pod には限らない。決定 7 の分離は、
各クレームを 1 つの StatefulSet だけが参照すること、そしてメンバーが Kubernetes API の権限を持たないことに依っており、
`ReadWriteOncePod` は使える場合にストレージ自身の保証を足す。

ReadWriteMany（Filestore、NFS、EFS）ではなくブロックストレージにする。一日中 git を回す home にネットワークファイルシステムが
課すコストは [0087](0087-efs-metadata-io.ja.md) で測ってあり、ecs-ec2 が存在するのも EBS の home が小さなファイルの書き込みで
8〜30 倍速かったからだ（[09 §9.5](../build/09-deploy.ja.md)）。

StorageClass は配備の設定で、アダプタは次を求める。

- **`volumeBindingMode: WaitForFirstConsumer`**。ボリュームは Pod がスケジュールされたゾーンに作られる。新しいクレームはそれまで
  `Pending` のままで、それが正常である。Start はクレームと StatefulSet を一緒に作り、先に `Bound` を待つことはしない。
- **`allowVolumeExpansion: true`**。クレームは縮められないので、サイズ設定は `DiskGrowOnly` を返す。`ResizeHome` は要求量を
  上げ、クレームの status と condition から結果を返す。停止中の Workspace ではファイルシステムの拡張は次のマウントで行われる
  ので、完了ではなく拡張中として返す。
- **`reclaimPolicy: Delete`**。そうでないと Destroy がディスクを消せない（決定 5）。

ボリュームはゾーンに閉じる。Workspace はそのゾーンでしか起動せず、ゾーンを失うと戻るまで home に届かない。バックアップの無い
ecs-ec2 の home と同じ露出である。

Pod はイメージの `dev` の uid で動き、`fsGroup` をその gid にする。これで root で動く init コンテナ無しに新しいボリュームへ
書けるはずだが、見込みであって測ってはいない。state ボリュームは `subPath` でマウントし、`subPath` のディレクトリは kubelet
自身が作る。実機ハーネスは新しい Workspace のすべてのマウント（keep のリンクを含む）に `dev` が書けることを確かめる。root で何かを動かすことは代わりの手にならない。決定 7 の `restricted` は init コンテナでもそれを禁じ、その余地を作るために
水準を下げることはしない。`subPath` の構成がこの確認に通らなければ、構成の方を変える。state ボリュームを自分のパスに 1 回だけ
マウントし、`CLAUDE_CONFIG_DIR` と `AF_WS_KEEP` をその下の 2 つのディレクトリに向ける。entrypoint は `AF_WS_KEEP` が書き込める
ディレクトリとしてすでにあるときにだけ keep の項目を移す（`workspace/entrypoint.sh`）ので、同じイメージから `dev` で動く init
コンテナが先に両方のディレクトリを作る。ハーネスは空の state ボリュームからこの構成を起動し、keep の 7 項目がすべてその中への
リンクになることを確かめる。

### 5. Destroy はクレームを消し、確かめられなかったものを報告する

Destroy は Workspace を止め、決定 3 の停止の収束を求める（クレームを使っている Pod はクレーム保護の finalizer でクレームを
押さえ、残った消去用 Pod も同じなので、それは消す）。その後、次の順に進む。

1. StatefulSet に注釈として目録を書く。各クレームの UID と、それに結び付いたボリュームの名前。StatefulSet は Destroy が最後に
   消すものなので、この段より後のどこで CP が落ちても目録は再び見つかる。ボリューム名はプロビジョナが生成するもので、
   namespace に閉じたロールでは一覧から引き戻せない。
2. Service・Secret・両クレームを消し、クレームが消えるのを時間を区切って待つ。
3. 決定 8 の読み取り専用のクラスタ権限で、目録にある各 PersistentVolume のオブジェクトが消えたことを確かめる
   （`reclaimPolicy: Delete` と、PersistentVolume の削除保護 finalizer（Kubernetes 1.33 で stable、CSI プロビジョナが従う）が
   あれば、ボリュームのオブジェクトは背後のディスクが消された後で消える）。時間内に消えなかったか
   読めなかったクレームとボリュームは、既知の残存物として返す（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。
4. StatefulSet を消すのは、**目録にあるすべてのクレームとボリュームが消えたと確かめられたときだけ**。そうでなければ
   StatefulSet を目録ごと残し、それも残存物として返す。CP が残存物を記録するのは Destroy が返った後なので
   （`workspace_lifecycle.go` は Workspace の行を消してから一覧を返す）、それより前にまだディスク上にあるボリュームの唯一の
   記録を消すと、落ちたときにその行方を永久に失う。再実行は既存の目録を読んで書き足し、すでに消えたクレームから作り直した目録で
   置き換えることはしない。

手順書は `Delete` と、その finalizer を持つ Kubernetes の版を前提条件にし、CP は起動時に設定された StorageClass を確かめる。
finalizer の無いクラスタでは、ボリュームのオブジェクトが消えたことはディスクが消えた証拠にならず、Destroy はそれを未確認として
返す。`Retain` だとディスク・データ・課金が Destroy の後も残り、監査ログにそう記録される。

残存物は `statefulset:<namespace>/<name>`、`pvc:<namespace>/<name>`、`pv:<name>` の形の文字列にし、監査ログが探すべきものを
正確に名指すようにする。各段は冪等で基盤に見えるものから始めるので、返る前に止まった Destroy（CP が落ちた、停止が収束しなかった）
はもう一度実行すれば完了する。Destroy が残存物を返した後は CP が Workspace の行を消し、再実行する対象が無くなる。そのときは
目録を持つ StatefulSet が記録であり、それが名指すものを消すのは手順書の手順である。

### 6. 秘密は参照で渡し、Pod の spec には書かない

ファクトリが受け取る起動ごとの環境すべて（DEK、`AGENT_TOKEN`、背景に挙げた発行済みトークン）を Workspace ごとに 1 つの Secret
に入れ、起動のたびに書き直し、`envFrom` でコンテナに届ける。Pod テンプレートには静的で秘密でない設定だけを載せる。これは
どちらの向きにも ECS の配置とは違う。ECS は DEK と `AGENT_TOKEN` を個別の IAM を持つ SSM SecureString の後ろに置き、残りの
トークンはタスク定義の素の環境に置く。ここではそのすべてが 1 つの Secret にあり、namespace の Secret を読める者なら読める。
素の環境よりは狭く、パラメータごとの IAM よりは広い。

`secretKeyRef` は値を Pod の spec から隠すが、その namespace で Secret を読める者や Pod を作れる者からは隠さない。その権限を
メンバーから遠ざけるのは決定 7 である。etcd 上の Secret はクラスタが暗号化しない限り base64 にすぎない。手順書は
アプリケーション層の Secret 暗号化（GKE なら Cloud KMS）を前提条件にし、決定 12 の Terraform はそれを有効にする。CP からはその
設定が見えないので、他のクラスタでは手順書だけが頼りになる。

### 7. 分離は 07 §7.2 の全行を満たす

**Workspace の namespace は配備ごとに 1 つ。** テナントとメンバーは、他のどの配備先とも同じく namespace ではなく CP が分ける。
メンバーには Workspace の namespace に対する Kubernetes API の権限を決して与えない。そこで Pod を作れる者は誰のクレームでも
マウントでき、どの Secret も読めるので、その権限は CP とクラスタの管理者だけのものとする。運用者は namespace に ResourceQuota と
LimitRange を設定し、停止中の Workspace のクレームと動いている Pod が合わせて取れる量に上限をかける。クォータで断られた起動は
起動フェーズとして表に出る。

**Workspace の namespace には Pod Security Standard の `restricted` を強制する。** manifest が付ける namespace のラベルで設定し、CP
にはそれを変える権限が無い。CP はそこで Pod を作れる（StatefulSet と消去用 Pod）ので、乗っ取られた CP は、そうでなければ特権や
`hostPath` の Pod を作ってノードとその上のすべてに届きうる。ラベルがあれば、誰が頼んでも API サーバがそうした Pod を断る。
乗っ取られた CP がなお届くのは namespace の中、つまりすべての Workspace の Secret、すべての DEK と発行済みトークンである。
マスターキーを持つ以上、どの配備先でも同じ到達範囲だ。専用クラスタならそこで止まり、他の負荷と共有するクラスタでは Pod Security
のラベルと namespace に閉じたロールがそこに留める。

| 観点 | `kubernetes` |
|---|---|
| 利用者間のファイル | Workspace ごとに 1 組のクレームで、その Workspace の StatefulSet だけが参照する。対応していれば `ReadWriteOncePod` |
| プロセスとメモリ | Workspace ごとに 1 Pod。CPU とメモリの requests と limits は Workspace のサイズ設定から。`ephemeral-storage` にも requests と limits を付ける。この上限は緩和であって隔離ではない。kubelet は使用量を定期的に測ってから追い出すので、速く書くものは捕まる前にノードのディスクを圧迫しえ、同じノードの他の Workspace に書き込みの失敗や追い出しが起きうる。残りは決定 13 が足す。Workspace のノードの空き容量の余裕、コンテナログの上限、ディスク圧迫の通知。`/tmp` はノードのディスク上の `sizeLimit` 付き `emptyDir` とする。Pod と一緒に消えるので、ecs-ec2 が tmpfs を要する理由はここには無く、tmpfs にはしない |
| ネットワーク | 下のポリシー |
| 権限 | `restricted` が許す範囲だけ。特権なし、`runAsNonRoot`、能力の追加なし、`hostNetwork`・`hostPID`・`hostPath` なし（Fargate の水準）。`restricted` が禁じるので、最初の版では Chromium のサンドボックス用の `SYS_ADMIN` のオプトインを用意しない（未決事項 1） |
| クラウドの ID | `automountServiceAccountToken: false`。Workspace のサービスアカウントはどのクラウド ID にも結び付けない。GKE では Workspace を動かしうるすべてのノードプールで GKE メタデータサーバ（`GKE_METADATA`）を使い、Workspace はそのプールにだけスケジュールする。これでメタデータサーバがホストネットワーク上にない Pod にノードの認証情報を渡すことは無く、`restricted` がどの Workspace の Pod もホストネットワークに載せない。Workspace の namespace やそのサービスアカウントを、直接にも `principalSet` 経由でも主体として名指す IAM 付与は置かない。Workload Identity はプロジェクト内のどのクラスタでも同じ namespace とサービスアカウントの名前を同じ ID として扱うので、namespace 名には配備ごとの接頭辞を付ける。CP 自身の Workload Identity は別の結び付け（決定 8） |
| 機微な状態 | `/var/lib/af/claude` と keep パスの state ボリューム。ecs-ec2 と同じ |

**ネットワークポリシー。** NetworkPolicy は許可しかできない。拒否とは許可が無いことであり、同じ Pod を選ぶ別のポリシーは許可を
足す。そのため Workspace の namespace にはちょうど次のものだけを置き、手順書でより広いものを足すことを禁じる。

| 送信元 → 宛先 | 許可 |
|---|---|
| 任意 → Workspace の agent ポート | CP の Pod からだけ |
| Workspace → クラスタ DNS | 許可 |
| Workspace → CP | 許可。CP の内部 Service の内部用ポートへだけ（決定 8） |
| Workspace → インターネット | 許可。`0.0.0.0/0` から 2 組の範囲を除く。固定の特殊用途の範囲（RFC 1918、`100.64.0.0/10`、`169.254.0.0/16`。メタデータアドレスを含む）と、この配備が実際に使う範囲（manifest がパラメータとして受け取る Pod・Service・ノードの範囲とコントロールプレーンのエンドポイント）。固定の一覧だけでは足りない。GKE Standard の 1.29 以降の既定の Service 範囲は `34.118.224.0/20` で、GKE は Pod とノードに privately used public の範囲も使える |
| Workspace → 配備が必要とするプライベートアドレス（LAN のエンジン、内部の git ホスト） | 運用者が宛先ごとに明示的に足すルールとしてだけ |

NetworkPolicy は、それを実装する CNI（GKE なら Dataplane V2）があるときだけ効く。無いクラスタはポリシーを受け入れ、何も
強制しないので、手順書で前提条件として書く。コントロールプレーンのエンドポイントはプライベートにするか、Pod の範囲を含まない
承認済みネットワークで制限する。誰にでも開いた公開エンドポイントは対象外とする。また NetworkPolicy は、Pod が自分の載っているノードへ届くことを常に許す。そのため
ノード自身が Pod に認証なしのサービスを出してはならない。手順書は kubelet の読み取り専用ポートを閉じること（GKE の既定）と、
Workspace のノードに `hostNetwork` のサービスを置かないことを求め、実機ハーネスは Pod の中からノード・コントロールプレーンの
エンドポイント・Service のアドレス・VPC 内のアドレスへの到達を試す。外向きの通信は ECS と同じく開いている。テンプレート環境がセッションを egress
プロキシに向けていても、プロキシ変数を無視するプロセスは迂回できる（[07 §7.8](../build/07-security.ja.md)）。

### 8. CP はクラスタ内で動き、Workspace は内部アドレスで CP に届く

CP は自分の namespace に置く 1 レプリカの Deployment で、内部 Service を持つ。サービスアカウントは Workspace の namespace に
Role を持ち、読み取りだけの ClusterRole を持つ。

| 種類 | 動詞 |
|---|---|
| StatefulSet、Service、PersistentVolumeClaim、Secret | get、list、create、update、patch、delete |
| Pod | get、list、watch。State・TaskCounter・BootPhase はコントローラが作った Pod を読む。create、delete は決定 4 の消去用 Pod のためだけで、CP が Workspace の Pod を消すことは無い（決定 3） |
| Event | get、list。Pod をスケジュール・取得できない理由を起動フェーズに出すため |
| NetworkPolicy | なし。静的で、manifest と一緒に適用する |
| StorageClass（ClusterRole） | get。`resourceNames` で設定されたクラスだけに限る。決定 4 の起動時の確認のため |
| PersistentVolume（ClusterRole） | get。Destroy でディスクが消えたことを確かめるため（決定 5）。ボリュームのオブジェクトが示すのはディスクとクレームの名前で、中身ではない |

ストアは配備が用意する任意の Postgres である。Google Cloud ではプライベート IP の Cloud SQL を、CP の Pod のサイドカーとして
動かす Cloud SQL Auth Proxy 経由で、自動 IAM データベース認証を使って接続する。プロキシは CP の Workload Identity で接続を認可し、
それに結び付いた IAM データベースユーザーとしてログインする。CP の DSN はループバック上のサイドカーを指し、パスワードは持たない。
その ID は CP のサービスアカウントだけに結び付ける。

**Workspace から CP への戻り道。** `AF_CP_BASE_URL` は今は公開のベース URL で、クラスタの中からだと NAT で出てロードバランサ
経由で戻ってくることになる。もっとも、決定 7 がプライベートアドレスへの経路を閉じる。

内部の経路は、CP そのものを Workspace に開いてはならない。CP は今 ingress を通ってしか届かず（[09 §9.3](../build/09-deploy.ja.md)）、
2 つのことがそれに依っている。`AUTH=proxy` は本人確認ヘッダを信用し、取り除かない。`clientip.go` は `X-Forwarded-For` を、誰が
送ったかを確かめずにホップ数で読む。CP の通常のポートにつながる Workspace は、どの利用者にもどのクライアントアドレスにも
なりすませる。そこで CP は Workspace 専用の **2 つ目の listener** を出し、内部 Service はそのポートだけを向く。そこに載せるのは
agent が呼ぶ経路だけで、どれもそれ自身の bearer トークンで認証される（docs、メモ、スケジュール、MCP、エンジン、git、認証情報の
エンドポイント。一覧は段階 1 で固める）。Console・管理 API・ログインの経路は出さない。本人確認ヘッダも転送ヘッダも無視し、
接続そのもののアドレスをクライアントとする。段階 1 で、そこへ送った偽の本人確認ヘッダと偽の `X-Forwarded-For` が無視されること、
Console の経路がそこでは 404 を返すことをテストする。

変数にも用途が 2 つある。API の呼び出しと、ブラウザで開く通知のリンクだ。そこでアダプタは内部 URL を 2 つ目の変数
`AF_CP_INTERNAL_URL` として、変えない `AF_CP_BASE_URL` と並べて渡す。CP はその URL を自分の環境から読み、決定 12 の manifest が
今 `PUBLIC_BASE_URL` を設定するのと同じく、内部 Service の名前とポートからそれを設定する。

これは [09 §9.3](../build/09-deploy.ja.md) の不変条件（CP には ingress を通ってしか届かない）への例外であり、例外はちょうど 2 つ目の
listener だけである。ブラウザや管理者が使うものは、ほかの経路では届かない。agent で今 `AF_CP_BASE_URL` を読む箇所は 15 ほどある
（認証ヘルパー、docs の同期、エンジン、MCP、チャット、ブラウザ、AWS、ブランチ規則など）。要求を送る箇所は設定されていれば内部 URL
を優先し、人のためにリンクを組み立てる箇所は公開 URL のままにする。どちらにも当たらない 2 種類も同じ作業に含む。codex が stdio の
MCP の子に適用する環境変数の明示的な許可リスト（`internal/mcpreg/attach.go`、`internal/chatx/chat_providers.go`）は新しい変数を
渡す必要があり、ブラウザの到達禁止先の一覧（`internal/browserx/browser_types.go`）は両方の URL を名指す必要がある。その仕分けは
段階 1 に含める。テンプレート環境が egress プロキシを設定する場合は、`NO_PROXY` に内部 Service 名を足し、それらの呼び出しが
プロキシを通らないようにする。テンプレート環境（`Config.ExtraEnv`。今は docker と
native だけが渡す）を Pod に届けるかは、アダプタと一緒に決める（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。

クラスタの外で動かす案は採らなかった。到達できる API サーバ、それへの認証情報、各 Workspace の Service への経路が要り、
クラスタ内ならそのすべてが無償で手に入るからだ。

### 9. イメージ: ノードが取得し、起動時に固定し、指紋で比べる

- **取得はノードの仕事。** GKE ではノードプールのサービスアカウントが Artifact Registry を読み、他のクラスタでは配備が
  `imagePullSecrets` を指定する。Pod 自身の ID は関わらない。
- **Start がダイジェストを固定する。** CP は起動のたびに設定されたタグをダイジェストに解決し、`image@sha256:…` をテンプレートに
  書く。これでノードのキャッシュが同じタグで古いイメージを動かすことは無くなり、テンプレートがこの起動で何を動かしたかを
  記録する。
- **`Stale()`** は `runtime_ecs_stale.go` の冒頭の規則に従う。両側を同じ方法で指紋にし（マルチプラットフォームの index を
  1 段ほどき、attestation の manifest を除く）、起動した指紋をテンプレートの注釈に記録し、レジストリの v2 API でタグの現在の
  指紋と比べ、迷ったら false を返す。CP は自分の ID（GKE なら Workload Identity、他ではプルシークレット）でレジストリに認証し、
  ノードのものとは分ける。

### 10. アダプタは client-go を使わず、標準ライブラリの HTTPS で API サーバと話す

アダプタは標準ライブラリの HTTP クライアントで、クラスタ内の API サーバに HTTPS で接続し、サービスアカウントの CA で検証する。
読み書きするフィールドだけを手書きの型にする。サービスアカウントのトークンは kubelet が更新する projected トークンなので、
起動時に覚えるのではなくファイルから読み直し、401 なら読み直して 1 回だけ再試行する。扱うのは決定 8 の種類だけで、client-go は
その範囲に対して CP の残りより大きな依存を持ち込む。docker アダプタがすでに示しているとおり、ここでの流儀は標準ライブラリと
基盤自身のインターフェースである。

代償は型の正しさを自分で保つこと。テストは記録した API サーバの応答に対してアダプタを動かし、ecs-ec2 と同じく
（`AF_ECS_EC2_LIVE=1`）ゲートした実機ハーネスで実クラスタに対して動かす。確かめるのは、Stop の直後の Start、コントローラが Pod を作成中の
Stop（作成要求を止めておく）、Start の書き込みと
コントローラの次の status 更新の間での State の読み取り、`starting` 中の Stop、起動途中での CP の再起動、keep のファイルを普通の
ファイルに置き換えた状態での 2 種類の home 消去、消去用 Pod の実行中に CP を再起動した `EraseHome`、停止中の拡張、クレームが
消えた後に CP を再起動した Destroy、そしてボリュームが残った状態で Destroy の返却と CP による残存物の記録の境目に CP を
再起動した Destroy。到達できなくしたノード（Stop がエラーを返し、手順書の復旧に従い、Workspace が再び起動する）と、すべての
マウントに `dev` が書ける新しい Workspace（決定 4）も確かめる。さらに、計画外の drain がするように、動いている Workspace の Pod を
CP の知らないところで消したとき、CP が何もしなくても同じ起動世代のまま `starting` を経て `running` に戻り、開始期限・reaper・Start
のどれも干渉しないことを確かめる。計画した更新で、ノードを cordon している間に出した Start が別のノードに載ることも確かめる。

### 11. 最初の版が名乗る能力

| 能力（[21 §21.3](../build/21-add-a-deploy-target.ja.md)） | 最初の版 |
|---|---|
| `Runtime`、`runtimeDestroyer` | あり |
| `SizingProfile()` | あり。CPU とメモリは requests と limits、ディスクはクレームのサイズ、`DiskGrowOnly` |
| `CostProfile()` | あり。`Runtime: "kubernetes"` で、使えるものは無しとする。こうしないと版情報が `local` に落ちる（`cost_profile.go`） |
| `WorkspaceImage()` | あり。起動時の表示と版情報のための、設定されたイメージ |
| `Stale()` | あり（決定 9） |
| `BootPhase()` | あり。Pod の condition、コンテナの待機理由、namespace の event から（スケジュール、クォータ、取得、起動） |
| `TaskCounter` | あり。Workspace のコンテナが動いている Pod の数で、**Ready かどうかは問わない**。応答しなくなった agent もタスクであり、開始期限がそれを止めてはならない。読めなかったときはエラーを返し、開始期限はそれを「動いている」として扱う |
| `WipeHome()`、`EraseHome()`、`ResizeHome()` | あり（決定 4） |
| `DocsMounter` | なし。ガイドは ECS と同じく `GET /internal/docs` から取る |
| `MachineProfile()` | なし。Pod には名指すべき自分のマシンが無い |
| `AcquireOperationFence`、`StartFencer` | なし。Workspace のライフサイクルのどれも CP のホストに無く、Start は未確定の起動を残さない |
| `LaunchBudgeter` | なし。Start は返った後に裏で何もしない |
| `BeginHibernate()`、`BackupHome()`、`HomeBackups()` | なし。VolumeSnapshot が自然な手段で、後の作業 |
| golden による初期化、スロットプール | なし |

各行について、`capabilities_test.go` か `runtime_test.go` に両方向のアサーションを置く。

### 12. 基盤は Google Cloud 側を Terraform、クラスタ内を素の manifest にし、Helm chart は棚上げのまま

- `deploy/kubernetes/` には kustomize の base を持つ素の manifest を置く。namespace、CP の Deployment・Service・サービス
  アカウント・Role、決定 7 のネットワークポリシー、ResourceQuota の例。どのクラスタにも適用できる。
- `deploy/gcp/gke/` には GKE 配備がクラスタの周りに要るものの Terraform を置く。VPC。Dataplane V2・Secret 暗号化・プライベート
  または制限したコントロールプレーンのエンドポイント・GKE メタデータサーバを使う Workspace 用ノードプールを持つクラスタ（決定 5
  のため Kubernetes 1.33 以上）。決定 4 の StorageClass。プライベート IP の Cloud SQL。静的アドレス付きの Cloud NAT。ロード
  バランサと Certificate Manager。そして 1 つの主体に 1 つのリソースずつ与える IAM 付与。CP の ID には Cloud SQL のクライアントと
  インスタンスユーザー、`Stale()` のための Artifact Registry の読み取り。ノードプールのサービスアカウントには Artifact Registry の
  読み取りと、ログとメトリクスの書き込み。state の置き場は運用者が名指すバケットで、DNS ゾーンは運用者が持ち込む前提条件とする。

Terraform にするのは、Google Cloud 自身のテンプレートサービスが終わりつつあるからだ。Deployment Manager は 2026-04-01 に
サポートを終え、2026-06-30 から新規利用者を受け付けず、2027-06-30 の後に停止する
（[Google の廃止のお知らせ](https://docs.cloud.google.com/deployment-manager/docs/deprecations)）。後継の Infrastructure Manager は Terraform を
動かす。リポジトリで最初の Terraform であり、運用者にとっても新しい道具になる。

AWS の配備先は CloudFormation のまま残す。移すと、7 つのスタック（約 3,600 行）、その上に建つ検査（`cfn-equiv.py`、
`cfn-contract.py`、タグ柵のテスト）、standup・update・teardown のスクリプトを書き直すことになり、稼働中のすべての配備が
リソースを Terraform の state に取り込む必要がある。誰も求めていない移行である。IaC が 2 言語になるのは、各クラウドにその流儀で
応える代償であり、共有するのは CP とイメージであってテンプレートではない。

Helm chart は作らない。問い合わせは Kubernetes 対応を求めたのであって chart を求めたのではなく、kustomize の base で同じ
クラスタに対応でき、足並みをそろえるべき 2 つ目のパッケージ形式を持たずに済む。Helm での導入を求められたら再開する。

### 13. 運用: 更新、バックアップ、通知、請求

ランタイムは利用者が求めたものの半分にすぎず、残りの半分は動かし続けることだ。手順は手順書が持ち、この ADR はそれが覆うべきものを
決める。

- **CP の更新。** CP は起動時にマイグレーションを適用し、戻せない（[09 §9.7](../build/09-deploy.ja.md)）。Deployment は `Recreate`
  方式にし、ロールアウト中に 2 つの CP が 1 つのデータベースに対して動くことが無いようにする。更新の前に Cloud SQL のバックアップを
  取り、戻すときはそのバックアップを前のイメージと一緒に復元する。`AF_MASTER_KEY` は、どの配備先とも同じくデータベースとその
  バックアップの外に置く。
- **バックアップ。** Cloud SQL の自動バックアップとポイントインタイムリカバリ。復元は段階 1 で一度練習する。最初の版では home の
  バックアップは取らない（対象外）。
- **Workspace イメージ。** 新しいイメージは次の起動で Workspace に届き、遅れているものは `Stale()` が示す。
- **通知。** ログはクラスタのロギング（GKE なら Cloud Logging）へ送る。CP の `/readyz` は readiness probe であり、データベースの
  警報でもある。手順書は、長く `Pending` の Pod、クォータでの拒否、監査ログに出た Destroy の残存物、証明書の期限切れへの通知を足す。
- **動いているセッションの下のノード。** Workspace の Pod には `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` を付け、
  autoscaler がプールを縮めるために動いているセッションを追い出さないようにする。プールは Workspace が止まるにつれて縮む。drain は Stop では
  ない。Pod は消すが `replicas: 1` は残すので、StatefulSet はすぐに別のノードで Pod を作り直す。セッションは切れ、Workspace は
  ひとりでに戻り、その容量は課金され続ける。そのため計画したノードの更新では、まずノードを cordon して新しいものが載らないようにし、
  次に CP を通してそのノードの Workspace（起動中のものを含む）を止めてそれぞれ停止の収束を待ち、そのうえで drain する。ノードが
  戻ったら uncordon し、メンバーは次に使うときにまた起動する。CP に Node への権限は要らず、cordon は運用者が行う。計画外の drain（ウィンドウ外の自動更新、ノードの修復）では
  切断と再起動が起き、手順書はそう書く。ハーネスは両方を確かめる。
- **ノードのディスク。** Workspace のノードは空き容量に余裕を持たせ、コンテナログに上限とローテーションを設け、Workspace のノードの
  ディスク圧迫で通知する（決定 7）。
- **請求。** その形は [09 §9.8](../build/09-deploy.ja.md) に倣う。床（クラスタ、CP のノード、Cloud SQL、Cloud NAT、ロードバランサ）、
  動いている間の Workspace ごとの容量、そして止まっている間も課金される Workspace ごとの 2 つの永続ディスク（EBS の home と同じ）。
  利用者がすでに運用しているクラスタなら、床からクラスタが消える。数字は段階 1 で、09 §9.8 が AWS についてするのと同じく、
  idle-stop が壊れたときの 24/7 の行も含めて埋める。

### 対象外

- **ecs-ec2 との同等性**: 休止、ゾーンをまたぐバックアップ、golden による初期化。Kubernetes では VolumeSnapshot とクレームの
  `dataSource` が ecs-ec2 の状態機械の大半を置き換えるので、移植ではなく書き直しになる。必要になったときにそれ自体を決める。
- CP が公開している AWS 名の型（`EC2PoolStatus`、`WorkspaceSlot.InstanceType`、`CostProfile` の AWS の明細）の**改名**。
  ここではどれも名乗らないので、改名を迫るものは無い。
- **コスト表示**（Cloud Billing のエクスポートから BigQuery）、**Cloud Text-to-Speech**、**Google Cloud 上のマネージド GPU
  エンジン**。GCE の GPU VM を `external` のエンジン行として宣言する方法は今でも動く。
- Workspace 内で使う**利用者向けの Google Cloud の道具**（`af-aws-exec` に当たる `gcloud` 版）。
- **テナントごとの namespace。** 最初の版は配備ごとに Workspace の namespace を 1 つとする（決定 7）。

## 却下した案

- **`gke` プロファイル。** 決定 2 のとおり。最初の版で要らない API を使うために、他のすべてのクラスタを捨てる。
- **Cloud Run。** home 用の永続ブロックボリュームが無く、寿命が要求単位で、何時間も続くセッションに合わない。
  [0104](0104-long-lived-member-workspace.ja.md) を満たせない。
- **GCE 上の VM プールで ecs-ec2 を移植する案。** ecs-ec2 の状態機械は EBS のアタッチ、SSM のコマンド、インスタンスのタグの上に
  建っている。GCE には SSM SendCommand に素直に対応するものが無く、プールが手で組んでいるスケジューリングとボリュームの
  アタッチは Kubernetes がすでに行う。
- **Filestore 上の ReadWriteMany の home。** 決定 4 と [0087](0087-efs-metadata-io.ja.md) のとおり。
- **Recreate と Clean home で home のクレームを消す案。** ログイン類まで一緒に消え、Recreate は `~/repos` 以外をすべて残す
  ものである。決定 4 は代わりにログイン類を外へ移す。
- **カスタムリソースを持つオペレーター。** CP を調整ループを持つコントローラにし、版を管理すべき CRD を抱えることになる。
  状態を持たず何でも名前で見つけるアダプタのモデルは、すでに 2 つのクラウド配備先で成り立っている。
- **client-go。** 決定 10 のとおり。
- **今すぐの Helm chart。** 決定 12 のとおり。

## 結果

- `Runtime` の契約に追随させ続けるアダプタが 5 つ目になり、docker プロファイルしか起動しない fleet E2E は何も検査しない。
  CI で kind クラスタを使う（ランナーには Docker がある）のがその穴を埋める方法で、ハーネスと一緒に決める。
- CP は自分の 2 つ目のアドレスを、agent はそのための 2 つ目の変数を知ることになる（決定 8）。設定するのはこのプロファイルだけ。
- Stop は Pod が消えるのを待つので、応答しなくなったノードでは、運用者が手を打つまで Stop・Recreate・Clean home がエラーになる。
- 応答しなくなったノードの Workspace は、動かすべき間は `starting` のまま、Stop の後は `stopped` だが Pod が残って Start・
  `EraseHome`・Destroy を妨げる。どちらも運用者が手を打つまで続く。
- CP は Workspace 用の 2 つ目の listener を持つことになる（決定 8）。このプロファイルだけ。
- CP はネットワークポリシーもクラスタの設定も読めないので、後から足された Workspace の到達先を広げるポリシーや、CNI が強制を
  やめたクラスタには製品は気付かない。運用者が繰り返せるのはハーネスの到達試験である。
- Google Cloud の運用者は Terraform と kustomize を覚えることになる。AWS の運用者は AWS CLI だけで済む。
- バックアップが入るまで、home は 1 つのゾーンに閉じる。
- GCE の段階はアダプタのコードなしで出るので、アダプタのコードが依存する前に決定 1 の Google Cloud 固有の事情が実証される。

## 未決事項（測ってから決める）

1. **`SYS_ADMIN` 無しでの Chromium のサンドボックス。** Fargate はそれ無しで動いている。GKE の Pod でブラウザペインが同じく
   振る舞うかは、仮定せずに測る。
2. **起動の待ち時間。** Workspace イメージは数ギガバイトある。ノードのイメージキャッシュと、Autopilot ならノードの増設が、
   起動が CP の想定に収まるかを決める。[09 §9.5](../build/09-deploy.ja.md) が Fargate の起動を分解したのと同じやり方で、
   冷えた起動と温まった起動を測る。
3. **ロードバランサの後ろでの WebSocket。** アイドルのターミナルを覆う `timeoutSec` をいくつにするか、そして 24 時間での切断に
   Console がターミナルを透過的につなぎ直すか。
4. **GKE Autopilot。** Fargate に近い選択肢。`SYS_ADMIN` を禁じ、ノードを需要に応じて増やす。Standard の後で検証する。

## 段階

| 段階 | 内容 | 完了の条件 |
|---|---|---|
| 0 | 決定 1: GCE の手順書とスクリプト、egress の既定値、ガイドのページ | GCE VM 上で、Caddy の後ろでも、グローバル外部アプリケーションロードバランサの後ろでもセッションが動き、アイドルのターミナルが既定の `timeoutSec` を越えて生き残り、切れた接続がつなぎ直される。Workspace からメタデータサーバへの要求が失敗する。[21 §21.5](../build/21-add-a-deploy-target.ja.md) の文書が手順書を載せている |
| 1 | 決定 2〜13: アダプタ、CP の内部用 listener、agent の URL の分離、`deploy/kubernetes/`、`deploy/gcp/gke/` | GKE Standard で決定 10 の実機ハーネスが通る。決定 7 の分離の各行とネットワークポリシーを Pod の中から（ノード・コントロールプレーンのエンドポイント・VPC 内のアドレスへの到達を含めて）確かめてある。手順書が決定 13 を、練習済みの復元と費用の表とともに覆う。[21 §21.8](../build/21-add-a-deploy-target.ja.md) の仕上げの一覧が済み、[09 §9.3](../build/09-deploy.ja.md) が 2 つ目の listener の例外を書いている |
| 2 | Autopilot と、「対象外」のうち求められたもの | それぞれ別の issue |

## 見直す条件

- Helm そのものを必要とする利用者が現れたとき（決定 12）。
- 標準 API では home に足りないクラスタ。たとえば拡張と `WaitForFirstConsumer` に対応した StorageClass が無いもの。
- Kubernetes での起動の待ち時間が ECS から大きく離れていると測れたとき。温めたノードのプールを再び検討することになる。
- テナントを CP ではなくクラスタで分けなければならないとき。テナントごとの namespace を再び検討することになる。

## 追記（2026-10-02）— Workspace 専用リスナーの経路一覧（#1464）

決定 8 の一覧は `control-plane/workspace_listener.go` の `workspaceRoutes` として確定した:
docs・ブランチ規則・MCP レジストリ・AWS プロファイルの取得、Agent が呼ぶメモと定時実行の経路
（Agent のコードが呼ばない `/internal/memo-categories` は含めない）、git OAuth の 2 つの refresh、
エンジンのトークン・カタログ・props・ゲートウェイ、LFS を含む内部 git。リスナーは本来のリスナーの
mux を通して振り分け、一覧に無いパターンは断るので、ハンドラの登録は 1 回で済む。
`AF_CP_INTERNAL_URL` の注入は、ブリッジのトークンが `AF_CP_BASE_URL` と一緒に来るため、それと
並ぶときだけ。内部 git の clone URL は公開のまま（人がクラスタの外から clone する）で、Agent が
`url.<internal>/git/.insteadOf` で Workspace の git を内部 URL へ書き換え、git のトークンを両方の
ホストに保存する。Workspace 専用リスナーで答えた LFS の batch は、転送先もそのリスナーを指す。内部 Service を `NO_PROXY` に入れるのはアダプタに残す。
