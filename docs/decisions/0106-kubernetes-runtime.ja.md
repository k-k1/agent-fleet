# 0106. Agent Fleet は `kubernetes` ランタイムプロファイルとして Kubernetes で動かし、最初は GKE で検証する。それが出るまで、Google Cloud での答えは GCE VM 上の compose とする

[English](0106-kubernetes-runtime.md) | 日本語

- 状態: **proposed**（2026-10-02）。まだ何も作っておらず、何も測っていない。以下の事実はコードと文書を読んで得たもので、
  動かしていないものはその旨を書いている。
- 追跡: #1092
- 見直す決定: [docs/log/35](../log/35-packaging.md) §35.3-5 と `docs/log/roadmap.md` P3-10 の Kubernetes の棚上げ
  （「Helm chart は需要が出るまで棚上げ。AWS の答えは ECS + CFN」、2026-07-21）。**Helm chart そのものは棚上げのまま**（決定 10）。
- 関連: [0045](0045-ec2-persistent-workspace.ja.md)（ecs-ec2 と決定 10-1「新しい基盤は新しいプロファイル」）/
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

- **コアはどのクラウドにいるかを問わない。** 配備先ごとの違いはすべて `Runtime` ポート
  （`control-plane/internal/runtime/runtime.go`）と [21 §21.3](../build/21-add-a-deploy-target.ja.md) の任意の能力の後ろにある。
  Workspace イメージはどこでも同じ成果物（[21 §21.1](../build/21-add-a-deploy-target.ja.md)）。
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
docker CLI しか使わない。同じ compose バンドルを GCE VM で動かすのにコードは要らず、要るのは手順書と、前に置くネットワークの
Google Cloud 固有の事情だけ（決定 1）。

### 新しい基盤が必ず満たすべき Workspace の 3 つの事実

- **イメージに init プロセスが無い。** docker は `--init`、ECS は `initProcessEnabled` で与えている。無いと agent が PID 1 になり、
  CLI や tmux が終わるたびに残るプロセスを誰も回収しない。Kubernetes にこのフラグは無く、イメージに tini を足すのは
  [21 §21.1](../build/21-add-a-deploy-target.ja.md) で禁じられている。
- **永続領域が 2 つある。** `/home/dev` の home と `/var/lib/af/claude` の Claude の状態。ファイルブラウザが後者に届かず、
  home を初期化してもログインが残るように分けてある（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。
- **クラウドの配備先では能力を足さない。** docker は Chromium のサンドボックスのために `SYS_ADMIN` を足すが、Fargate は何も
  足さない（[07 §7.2](../build/07-security.ja.md)）。能力の追加を禁じる基盤は Fargate と同じ水準であり、それより下ではない。

## 決定

### 1. Google Cloud には 2 段階で答え、最初は GCE VM 上の compose

最初の答えは 1 台の GCE VM 上の docker プロファイルで、`deploy/aws/ec2-single` の対になるもの。`deploy/gcp/gce-single/` に
手順書と `gcloud` だけで動く構築スクリプトを置き、前段は Caddy と Let's Encrypt。CP に手を入れずに出せるので、問い合わせた
利用者は Kubernetes プロファイルを作っている間も自分の基盤で Agent Fleet を動かせる。ランタイムに依存しない Google Cloud 固有の
事情もここで決着し、Kubernetes プロファイルはそれを引き継ぐ。

- **egress の許可リスト**に `.googleapis.com` を足す。`.amazonaws.com` が AWS の道具のためにあるのと同じ
  （`control-plane/egress_policy.go`）。
- **Google Cloud のロードバランサを前に置くと**、WebSocket の寿命がバックエンドサービスの `timeoutSec`（既定 30 秒）で切られ、
  ターミナルがすべて落ちる。手順書でこれを延ばす。また、ロードバランサは `X-Forwarded-For` にクライアントと自分のアドレスの
  両方を足すので、配備は 1 ではなく `AF_TRUSTED_PROXY_HOPS=2` にする。`clientip.go` はすでに右から数えるので、コードは変えない。
- **固定の出口アドレス**は、予約した静的 IP を持つ Cloud NAT。保持している NAT EIP の対になるもの。
- **プレビューのサブドメイン**にはワイルドカード証明書が要る。標準の `caddy:2-alpine` には DNS プラグインが無いので、
  ロードバランサを前に置くときは Certificate Manager の DNS 認証を使い、Caddy のときはその制約を手順書に書く。

この段階で得られないのは compose がもともと与えないもの、つまり 1 台限り・スケールアウトなし・Workspace を止めても VM は
課金され続ける、の 3 つ。

### 2. プロファイルは `gke` ではなく `kubernetes`

新しいプロファイルは Kubernetes の標準 API だけを話す。`apps/v1` の StatefulSet、`v1` の Service・PersistentVolumeClaim・Secret、
`networking.k8s.io/v1` の NetworkPolicy。Google Cloud の API は一つも呼ばない。GKE に固有のもの（ディスクの種類、ノードプール、
Workload Identity、ロードバランサ）は、アダプタではなく StorageClass・IaC・手順書で選ぶ。

問い合わせは Google Cloud と並べて Kubernetes を名指していた。Kubernetes を運用するチームはどこかでそれを運用しており、EKS・AKS・
オンプレのクラスタも同じプロファイルで済む。`gke` プロファイルにすると、最初の段階では要らない Google Cloud API を直接使える
代わりに、他のすべてのクラスタに「非対応」と答えることになる。最初に検証するクラスタは GKE Standard で、他で測るまでは
検証済みはそれだけとする。

[0045](0045-ec2-persistent-workspace.ja.md) の決定 10-1 に従い、独立したプロファイルにする。別名として `k8s` も受け付ける。

### 3. Workspace はレプリカ 0 か 1 の StatefulSet と、専用の Service

各 Workspace は Workspace キーから決定的に名付けた StatefulSet で、0 と 1 の間でスケールする。ECS サービスの desired 0/1 と
同じスケール・トゥ・ゼロの形。正は基盤側にある（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。`State` は StatefulSet の
レプリカ数と Pod のフェーズ・準備状態から読むので、再起動した CP や 2 台目の CP も名前ですべて取り戻せる。

素の Pod でも Deployment でもなく StatefulSet にするのは、**序数ごとに Pod が高々 1 つであることを保証する**から。ECS アダプタは、
古いタスクが新しいタスクの後ろで抜けきるまで `starting` を返し続ける。Service Connect が 1 人のクライアントの要求を 2 つの
agent に振り分けうるからだ（`runtime_ecs.go` の `serviceRolledOut`）。StatefulSet は古い Pod が消えるまで新しい Pod を動かさない
ので、この窓が存在しない。代償は同じ保証の裏側で、応答しなくなったノード上の Pod は、ノードが消えたと判定されて Pod が削除される
まで置き換わらない。CP の開始期限（`start_deadline.go`）が、ほかの居残る `starting` と同じくこれにも上限をかける。

起動のたびに Pod テンプレート（[21 §21.2](../build/21-add-a-deploy-target.ja.md) の起動ごとの環境は起動ごとに違いうる）と
レプリカ数を 1 回の更新で書く。`Stop` はレプリカを 0 にし、`terminationGracePeriodSeconds` を `AF_STOP_GRACE_SEC` から決める。
agent がすでに想定している 2 段階の停止と同じ。

init プロセスは `shareProcessNamespace: true` で与える。Pod の pause コンテナが PID 1 になって孤児を回収し、docker 下の tini と
同じ役をする。イメージは変えない。

各 Workspace は ClusterIP の Service を持ち、`Endpoint()` はそのクラスタ DNS 名。Service Connect の別名と違い、この名前は CP の
起動後に作った Service でも引けるので、`agent_dial.go` の回避策に当たるものはここでは要らない。

### 4. home は CP が作る ReadWriteOnce ボリュームで、共有ファイルシステムではない

各 Workspace は永続領域ごとに 1 つ、計 2 つの PersistentVolumeClaim を持つ。StatefulSet の `volumeClaimTemplates` ではなく CP が
決定的な名前で自分で作る。テンプレートのサイズは作成後に変えられず、そこから作られたクレームは StatefulSet より長生きし、
CP から見える持ち主がいないからだ。StorageClass は配備の設定とし、GKE の手順書では `WaitForFirstConsumer` で拡張を許した
`pd-balanced` のクラスを使う。

ReadWriteMany（Filestore、NFS、EFS）ではなく ReadWriteOnce のブロックストレージにする。一日中 git を回す home にネットワーク
ファイルシステムが課すコストは [0087](0087-efs-metadata-io.ja.md) で測ってあり、ecs-ec2 が存在するのも EBS の home が小さな
ファイルの書き込みで 8〜30 倍速かったからだ（[09 §9.5](../build/09-deploy.ja.md)）。ReadWriteOnce のボリュームは 1 つの Pod に
付いて回り、それはまさに home のあり方でもある。

追加の作業なしで得られるもの: クレームの拡張による `ResizeHome`、停止中にクレームを消して作り直す `WipeHome` と `EraseHome`、
StatefulSet・Service・両クレーム・Secret を消す `Destroy`。ボリュームはゾーンに閉じるので、Workspace はそのゾーンでしか起動
せず、ゾーンを失うと戻るまで home に届かない。バックアップ（決定 9）が入る前の ecs-ec2 の home と同じ露出である。

Pod はイメージの `dev` の uid で動き、`fsGroup` をその gid にするので、root で動く init コンテナ無しで新しいボリュームに書ける。

### 5. 秘密は参照で渡し、Pod の spec には書かない

DEK と `AGENT_TOKEN` は Workspace ごとの Secret に入れ、`secretKeyRef` でコンテナに届ける。namespace を読める誰からも見える
Pod の spec には参照しか載らない。ECS の SSM SecureString 参照（[09 §9.5](../build/09-deploy.ja.md)）の対になるもの。etcd 上の
Secret はクラスタが暗号化しない限り base64 にすぎないので、手順書ではアプリケーション層の Secret 暗号化（GKE なら Cloud KMS）を
前提条件にする。

### 6. 分離は 07 §7.2 の全行を満たす

| 観点 | `kubernetes` |
|---|---|
| 利用者間のファイル | Workspace ごとに 1 組のボリュームで、その Pod だけがマウントする |
| プロセスとメモリ | Workspace ごとに 1 Pod。requests と limits は Workspace のサイズ設定から |
| ネットワーク | Workspace は専用の namespace に置き、既定拒否の NetworkPolicy をかける。agent のポートへの流入は CP の Pod からだけ許し、ノードのメタデータアドレスへの流出は拒否する |
| 権限 | 特権なし、`runAsNonRoot`、既定では能力の追加なし（Fargate の水準）。Chromium のサンドボックス用の `SYS_ADMIN` は、クラスタのポリシーが許す場合のオプトイン |
| クラウドの ID | `automountServiceAccountToken: false`。Workspace のサービスアカウントはどのクラウド ID にも結び付けない。GKE では Workload Identity Federation を必須にし、メタデータサーバが Pod にノードの認証情報を渡さないようにする |
| 機微な状態 | `/var/lib/af/claude` の 2 つ目のボリューム。全配備先と同じ |

NetworkPolicy は、それを実装する CNI（GKE なら Dataplane V2）があるときだけ効く。無いクラスタはポリシーを黙って受け入れ、何も
強制しない。そのため手順書で前提条件として書き、CP は強制が無いと判別できる場合に起動時に警告を出す。

### 7. CP はクラスタ内で動き、namespace に閉じたロールを持つ

CP は自分の namespace に置く 1 レプリカの Deployment。サービスアカウントは Workspace の namespace に、決定 2 の種類だけを扱う
Role を持ち、ClusterRole は持たない。ストアは配備が用意する任意の Postgres で、Google Cloud の手順書では Cloud SQL を使う。

クラスタの外で動かす案は採らなかった。到達できる API サーバ、それへの認証情報、各 Workspace の Service への経路が要り、
クラスタ内ならそのすべてが無償で手に入るからだ。

### 8. アダプタは client-go を使わず、素の HTTP で API サーバと話す

アダプタは標準ライブラリの HTTP クライアントに、クラスタ内のサービスアカウントトークンと CA を使い、読み書きするフィールド
だけを手書きの型にする。扱うのは 5 種類と 4 つの動詞にすぎない。client-go はその範囲に対して CP の残りより大きな依存を
持ち込む。docker アダプタがすでに示しているとおり、ここでの流儀は標準ライブラリと基盤自身のインターフェースである。

代償は型の正しさを自分で保つこと。テストは記録した API サーバの応答に対してアダプタを動かし、ecs-ec2 と同じく
（`AF_ECS_EC2_LIVE=1`）ゲートした実機ハーネスで実クラスタに対して動かす。

### 9. 最初の版が名乗る能力

| 能力（[21 §21.3](../build/21-add-a-deploy-target.ja.md)） | 最初の版 |
|---|---|
| `Runtime`、`runtimeDestroyer` | あり |
| `SizingProfile()` | あり。CPU とメモリは requests と limits、ディスクはクレームのサイズ |
| `Stale()` | あり。レジストリの v2 API からイメージのダイジェストを得る（Artifact Registry も話す） |
| `BootPhase()` | あり。Pod の condition から（スケジュール、取得、起動） |
| `TaskCounter` | あり。StatefulSet の ready なレプリカ数 |
| `WipeHome()`、`EraseHome()`、`ResizeHome()` | あり（決定 4） |
| `DocsMounter` | なし。ガイドは ECS と同じく `GET /internal/docs` から取る |
| `CostProfile()` | なし。Google Cloud のコスト表示は課金エクスポートで、後の作業 |
| `BeginHibernate()`、`BackupHome()`、`HomeBackups()` | なし。VolumeSnapshot が自然な手段で、後の作業 |
| golden による初期化、スロットプール | なし |

各行について、`capabilities_test.go` か `runtime_test.go` に両方向のアサーションを置く。

### 10. 基盤は Google Cloud 側を Terraform、クラスタ内を素の manifest にし、Helm chart は棚上げのまま

- `deploy/kubernetes/` には kustomize の base を持つ素の manifest を置く。namespace、CP の Deployment・サービスアカウント・Role、
  既定拒否のポリシー。どのクラスタにも適用できる。
- `deploy/gcp/gke/` には GKE 配備がクラスタの周りに要るものの Terraform を置く。VPC、Dataplane V2・Workload Identity・Secret
  暗号化を有効にしたクラスタ、Cloud SQL、静的アドレス付きの Cloud NAT、Cloud DNS、ロードバランサと Certificate Manager。

Terraform にするのは、Google Cloud にはもう固有のテンプレート言語が無いからだ。Deployment Manager は廃止され、後継の
Infrastructure Manager は Terraform を動かす。リポジトリで最初の Terraform であり、運用者にとっても新しい道具になる。

AWS の配備先は CloudFormation のまま残す。移すと、7 つのスタック（約 3,600 行）、その上に建つ検査（`cfn-equiv.py`、
`cfn-contract.py`、タグ柵のテスト）、standup・update・teardown のスクリプトを書き直すことになり、稼働中のすべての配備が
リソースを Terraform の state に取り込む必要がある。誰も求めていない移行である。IaC が 2 言語になるのは、各クラウドにその流儀で
応える代償であり、共有するのは CP とイメージであってテンプレートではない。

Helm chart は作らない。問い合わせは Kubernetes 対応を求めたのであって chart を求めたのではなく、kustomize の base で同じ
クラスタに対応でき、足並みをそろえるべき 2 つ目のパッケージ形式を持たずに済む。Helm での導入を求められたら再開する。

### 対象外

- **ecs-ec2 との同等性**: 休止、ゾーンをまたぐバックアップ、golden による初期化。Kubernetes では VolumeSnapshot とクレームの
  `dataSource` が ecs-ec2 の状態機械の大半を置き換えるので、移植ではなく書き直しになる。必要になったときにそれ自体を決める。
- CP が公開している AWS 名の型（`EC2PoolStatus`、`WorkspaceSlot.InstanceType`、`CostProfile` の AWS の明細）の**改名**。
  ここではどれも名乗らないので、改名を迫るものは無い。
- **コスト表示**（Cloud Billing のエクスポートから BigQuery）、**Cloud Text-to-Speech**、**Google Cloud 上のマネージド GPU
  エンジン**。GCE の GPU VM を `external` のエンジン行として宣言する方法は今でも動く。
- Workspace 内で使う**利用者向けの Google Cloud の道具**（`af-aws-exec` に当たる `gcloud` 版）。

## 却下した案

- **`gke` プロファイル。** 決定 2 のとおり。最初の版で要らない API を使うために、他のすべてのクラスタを捨てる。
- **Cloud Run。** home 用の永続ブロックボリュームが無く、寿命が要求単位で、何時間も続くセッションに合わない。
  [0104](0104-long-lived-member-workspace.ja.md) を満たせない。
- **GCE 上の VM プールで ecs-ec2 を移植する案。** ecs-ec2 の状態機械は EBS のアタッチ、SSM のコマンド、インスタンスのタグの上に
  建っている。GCE には SSM SendCommand に素直に対応するものが無く、プールが手で組んでいるスケジューリングとボリュームの
  アタッチは Kubernetes がすでに行う。
- **Filestore 上の ReadWriteMany の home。** 決定 4 と [0087](0087-efs-metadata-io.ja.md) のとおり。
- **カスタムリソースを持つオペレーター。** CP を調整ループを持つコントローラにし、版を管理すべき CRD を抱えることになる。
  状態を持たず何でも名前で見つけるアダプタのモデルは、すでに 2 つのクラウド配備先で成り立っている。
- **client-go。** 決定 8 のとおり。
- **今すぐの Helm chart。** 決定 10 のとおり。

## 結果

- `Runtime` の契約に追随させ続けるアダプタが 5 つ目になり、docker プロファイルしか起動しない fleet E2E は何も検査しない。
  CI で kind クラスタを使う（ランナーには Docker がある）のがその穴を埋める方法で、ハーネスと一緒に決める。
- Google Cloud の運用者は Terraform と kustomize を覚えることになる。AWS の運用者は AWS CLI だけで済む。
- バックアップが入るまで、home は 1 つのゾーンに閉じる。
- GCE の段階はコード変更なしで出るので、アダプタのコードが依存する前に決定 1 の Google Cloud 固有の事情が実証される。

## 未決事項（測ってから決める）

1. **`SYS_ADMIN` 無しでの Chromium のサンドボックス。** Fargate はそれ無しで動いている。GKE の Pod でブラウザペインが同じく
   振る舞うかは、仮定せずに測る。
2. **起動の待ち時間。** Workspace イメージは数ギガバイトある。ノードのイメージキャッシュと、Autopilot ならノードの増設が、
   起動が CP の想定に収まるかを決める。[09 §9.5](../build/09-deploy.ja.md) が Fargate の起動を分解したのと同じやり方で、
   冷えた起動と温まった起動を測る。
3. **ロードバランサの後ろでの WebSocket の寿命。** どの `timeoutSec` にするか、そしてそこに達したときに Console がターミナルを
   透過的につなぎ直すか。
4. **egress の強制。** 許可リストは CP のプロキシにある。テンプレート環境（`Config.ExtraEnv`。今は docker と native だけが渡す）
   を Pod に届けるかは、アダプタと一緒に決める（[21 §21.2](../build/21-add-a-deploy-target.ja.md)）。
5. **GKE Autopilot。** Fargate に近い選択肢。`SYS_ADMIN` を禁じ、ノードを需要に応じて増やす。Standard の後で検証する。

## 段階

| 段階 | 内容 | 完了の条件 |
|---|---|---|
| 0 | 決定 1: GCE の手順書とスクリプト、egress の既定値、ガイドのページ | GCE VM 上で、Caddy の後ろでも、ロードバランサの後ろでもセッションが動き、ターミナルが `timeoutSec` の既定値を越えて生き残る |
| 1 | 決定 2〜9: アダプタ、`deploy/kubernetes/`、`deploy/gcp/gke/` | GKE Standard でメンバーの Workspace が起動・停止・拡張・初期化でき、決定 6 の分離の各行を Pod の中から確かめてある |
| 2 | Autopilot と、「対象外」のうち求められたもの | それぞれ別の issue |

## 見直す条件

- Helm そのものを必要とする利用者が現れたとき（決定 10）。
- 標準 API では home に足りないクラスタ。たとえば ReadWriteOnce で拡張できる StorageClass が無いもの。
- Kubernetes での起動の待ち時間が ECS から大きく離れていると測れたとき。温めたノードのプールを再び検討することになる。
