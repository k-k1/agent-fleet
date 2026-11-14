---
audience: "Kubernetes クラスターに Agent Fleet を構築して運用する人（クラスターとクラウドの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと `deploy/gcp/gke/` の Terraform（食い違ったらそちらが正）"
---

# Kubernetes 構成の全体像

[English](overview.md) | 日本語

Kubernetes クラスターで Agent Fleet を動かす前に、何がどこに作られ、クラスターが何を満たしていなければならないかを確かめます。
Control Plane（CP）はクラスター内の Deployment として動きます。各ワークスペースは、レプリカ数 0 または 1 の StatefulSet と、PersistentVolumeClaim（claim）2 本で構成されます。

> [!WARNING]
> **プレビューです。** GKE Standard のクラスターで受け入れを確かめていますが、本番での利用はまだサポートしていません。既知の制約は、このランタイムではブラウザ機能が使えないこと（下記）と、GKE のリージョン単位の SSD ディスク割り当て（[ディスクの割り当て（GKE）](#ディスクの割り当てgke)）です。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `deploy/gcp/gke` で Terraform を適用する（GKE のとき） | VPC・サブネット・固定アドレスの Cloud NAT、クラスターと 2 つのノードプール、Secret を暗号化する KMS キー、Cloud SQL、ロードバランサーのアドレス・証明書・DNS レコード、StorageClass、IAM の付与ができる |
| オーバーレイを `kubectl apply -k` する | 2 つの Namespace、CP とその Service、CP 自身のディスク（`af-cp-data`）、RBAC、NetworkPolicy、クォータ、Gateway とそのポリシーができる |
| メンバーが Console でワークスペースを起動する | CP がワークスペースごとに StatefulSet・Service・Secret・claim 2 本を作る。ときどき、1 回限りの消去用 Pod も作る。どれにも `agent-fleet.io/workspace=<名前>` のラベルが付く |

## ディレクトリの中身

| パス | 中身 |
|---|---|
| `deploy/kubernetes/base/` | どのクラスターでも使う素のマニフェスト。2 つの Namespace、CP の Deployment・Service・サービスアカウント・RBAC、ワークスペースの NetworkPolicy、ResourceQuota と LimitRange の例 |
| `deploy/kubernetes/components/params/` | オーバーレイの `deployment.yaml` の値（Namespace、アドレス範囲、StorageClass、イメージ）をマニフェストに書き込む |
| `deploy/kubernetes/components/gke/` | GKE で足すもの。Cloud SQL Auth Proxy のサイドカー、Workload Identity、ワークスペースのノードプール、グローバル外部アプリケーションロードバランサー（Gateway API）、CP の Namespace の受信ポリシー |
| `deploy/kubernetes/overlays/gke/` | GKE の配備でコピーするオーバーレイ |
| `deploy/kubernetes/overlays/generic/` | ほかのクラスターでコピーするオーバーレイ |
| `deploy/gcp/gke/` | GKE クラスターのまわりに要るものを作る Terraform |

## ブラウザ機能はない

ワークスペースの Pod では Chromium のサンドボックスが起動できません（Pod Security が `restricted` のため）。サンドボックスなしで動かすこともしません。

- CP は、この構成で起動するすべてのワークスペースにそのことを伝えます（`AF_BROWSER_UNAVAILABLE`）
- Console はブラウザペインを理由つきでグレーアウトします。Agent のブラウザ機能と af のブラウザツールは `browser_unavailable` を返します
- `workspace-agent browser-smoke` は、Chromium の EOF ではなくこの説明で失敗します
- 軽量プレビューは使えます
- クラスター側で設定するものはありません。回避のために seccomp プロファイルや capability を足さないでください

## 前提条件

CP はどの項目も確かめられません。1 つでも欠けたクラスターでも Agent Fleet は動きますが、ワークスペースは隔離されません。GKE では、下の項目はすべて Terraform が満たします。

| # | 前提条件 | 欠けるとどうなるか |
|---|---|---|
| P1 | Kubernetes **1.33 以降**。Terraform はリリースチャンネルの既定のバージョンでクラスターを作り、この下限（`min_master_version`）を plan のたびに確かめる | 破棄が頼る PersistentVolume の削除保護 finalizer は 1.33 で安定版になった |
| P2 | **NetworkPolicy を強制する** CNI（GKE では Dataplane V2） | ポリシーは受け付けられても、何も止めない |
| P3 | `volumeBindingMode: WaitForFirstConsumer`、`allowVolumeExpansion: true`、`reclaimPolicy: Delete` で、**ブロックストレージ**（NFS や Filestore ではない）を使う StorageClass | `Retain` だと、破棄のあともディスクとその請求が残る |
| P4 | **Secret のアプリケーション層暗号化**（GKE では Cloud KMS） | 各ワークスペースの DEK とトークンは Secret に入っている |
| P5 | コントロールプレーンのエンドポイントが**非公開か、Pod の範囲を含まない承認済みネットワークに限られている**。誰にでも開いた公開エンドポイントはサポートしない | ワークスペースから API サーバーに届く |
| P6 | kubelet の**読み取り専用ポートが閉じている**（GKE の既定で、Terraform も閉じる）。ワークスペースのノードで **`hostNetwork` のサービスが動いていない** | Pod は自分のノードにはいつでも届く |
| P7 | GKE では、**ワークスペースが動きうるノードプールはすべて `GKE_METADATA` を使い**、ワークスペースはそこにだけ置かれる（`AF_K8S_NODE_SELECTOR`） | ノードの認証情報にワークスペースから届く |
| P8 | **ワークスペースの Namespace やそのサービスアカウントを指定する IAM の付与がない**（`principalSet` 経由も含む）。Namespace の名前には、この配備だけの接頭辞が付いている | Workload Identity は、プロジェクト内のどのクラスターでも同じ名前を同じ ID として扱う |
| P9 | **メンバーはワークスペースの Namespace の Kubernetes API に届かない** | そこで Pod を作れることは、全メンバーのホームディレクトリと Secret を読めることと同じ |
| P10 | クラスターが **IPv4 のシングルスタック** | 外向きのポリシーは `::/0` を許可しないので、IPv6 の外向き通信は素通りではなく拒否になる |
| P11 | **NodeLocal DNSCache がオフ**（GKE は新しいクラスターでオンにする。Terraform はオフにする）か、そのアドレス向けのポリシーを足してある | DNS のポリシーは `kube-dns` の Pod だけを許可し、`169.254.0.0/16` は拒否する。キャッシュがオンだと、ワークスペースからの名前解決がすべて失敗する |
| P12 | 配備が用意する **Postgres**（GKE では Cloud SQL） | CP のデータベース |
| P13 | Console の名前の **DNS ゾーン**と、Terraform の**ステート用バケット**。どちらも運用担当者のもの | Terraform が証明書とステートに使う |
| P14 | `AF_MASTER_KEY` を作り、**データベースとそのバックアップの外に**保管してある | ほかの形態と同じく、失うと暗号化したものが読めなくなる |
| P15 | **ノード・Pod・Service・コントロールプレーンの範囲が RFC 1918 か `100.64.0.0/10` の中にある**（Terraform が plan のたびに確かめる） | 下記 |

P15 について:

- 外向きのポリシーは、それらの範囲と `169.254.0.0/16` の外にあるすべてのアドレスを、`except` を使わない固定のブロックとして許可します。ブロックは `deploy/kubernetes/egress-blocks.py` が書きます
- Dataplane V2 では、ノードのアドレスを含むブロックを 1 つでも許可すると、ワークスペースから自分のノードにもほかのノードにも届きます。`except` では止まりません
- 範囲がほかにあるクラスター（公開アドレスを内部で使っている、GKE の既定の Service 範囲 `34.118.224.0/20` など）は、その範囲を自分でブロックから外します

P11 を既存のクラスターで切り替えると、約 10 分かかります。GKE のバージョンによってはノードが作り直されます。1.35 で見たときは、`node-local-dns` の Pod が消えただけで、ノードの作り直しもワークスペースの再起動もありませんでした。

## ディスクの割り当て（GKE）

各ノードのブートディスクと各 claim は `pd-balanced` のディスクです。`pd-balanced` は、リージョンの **Persistent Disk SSD の割り当て**（`SSD_TOTAL_GB`）を使います。新しいプロジェクトでは、リージョンあたり 500 GB です。

使い切ると、オートスケーラーはノードを足せません。スケールアップは `QUOTA_EXCEEDED` で失敗し、ワークスペースの Pod は `Pending` のままになります。

定常状態で配備が使いうる量を、Terraform の変数（`deploy/gcp/gke/variables.tf`）、CP の claim のサイズ、既定の `pd-balanced` から見積もると次のようになります。

```
zones x (system_node_count x 100 + workspace_max_nodes x workspace_boot_disk_gb)
  + 20                                    the CP's disk (af-cp-data)
  + workspaces x (AF_K8S_HOME_GIB + AF_K8S_STATE_GIB)    50 + 5 by default, more after a resize
  + 20 during a disk rehearsal            the restored copy of the CP's disk
```

- システムプールの 100 GB は GKE の既定のブートディスクで、Terraform は変えません
- 既定値で 2 ゾーン（`system_node_count = 1`、ゾーンあたり `workspace_max_nodes = 4`、`workspace_boot_disk_gb = 200`）なら、ノードだけで 2 x (100 + 4 x 200) = 1800 GB に届きます
- 新しいプロジェクトの 500 GB は、システムノード 2 台、ワークスペースノード 1 台、ワークスペース 1 つ（2 x 100 + 200 + 20 + 55 = 475 GB）でほぼ使い切ります。2 台めのワークスペースノードは追加されません

本番の前に割り当てを引き上げるか、`workspace_max_nodes` / `workspace_boot_disk_gb` を下げます。合計ちょうどではなく、余裕を残してください。

| 余裕が要る理由 | 中身 |
|---|---|
| ノードのアップグレード | 両方のプールが自動でアップグレードされる。サージアップグレードでは、そのプールのブートディスクを持つノードがゾーンごとに 1 台（GKE の既定のサージ数）追加される。余裕がないとアップグレードが `QUOTA_EXCEEDED` で失敗する |
| ほかのディスク | 同じリージョンのほかの配備や VM のディスクも、同じ割り当てを使う |
| 止まっているワークスペース | 停止中も claim は残る。動いているものだけでなく、すべてのワークスペースを数える |

`storage_class_disk_type` をほかの種類にすると、claim はその種類の割り当てを使います（たとえば `pd-standard` なら `DISKS_TOTAL_GB`）。

割り当てを確かめます。

```bash
gcloud compute regions list --project <project> --flatten=quotas \
  --filter='name=<region> AND quotas.metric=SSD_TOTAL_GB' \
  --format='table(name,quotas.metric,quotas.usage,quotas.limit)'
```

引き上げは、Cloud Console の割り当てのページ（IAM と管理 > 割り当てとシステム上限、そのリージョンの「Persistent Disk SSD (GB)」）か、Cloud Quotas API で行います。

```bash
gcloud services enable cloudquotas.googleapis.com --project <project>
gcloud beta quotas preferences create --project <project> --service compute.googleapis.com \
  --quota-id SSD-TOTAL-GB-per-project-region --dimensions region=<region> \
  --preferred-value <GB> --preference-id <prefix>-ssd-total-gb \
  --justification "Agent Fleet on GKE: node boot disks and workspace volumes"
gcloud beta quotas preferences describe <prefix>-ssd-total-gb --project <project>   # 認められた値
```

2000 GB への引き上げが 1 分ほどで承認された例がありますが、約束ではありません。大きな要求は審査に回ることがあります。

## 手元に要るツール

| ツール | 用途 |
|---|---|
| `gcloud` と `gke-gcloud-auth-plugin` コンポーネント | `kubectl` が GKE に認証するのに要る（`gcloud components install gke-gcloud-auth-plugin`） |
| `terraform` 1.6 以降 | `deploy/gcp/gke` |
| `kubectl` | 組み込みの kustomize で足りる |
| `psql` | 最初の 1 回だけのデータベースの権限付与 |

## マニフェストを変えるときに守ること

- **ワークスペースの Namespace に、ここにあるものより広い NetworkPolicy を足さないでください。** ポリシーは足し算です。「すべての Pod」により多くを許可するポリシーが 1 つでもあると、隔離が失われます。配備が必要とする内部の宛先（LAN の推論エンジン、社内の git ホストなど）には、宛先ごとにポリシーを 1 つ作ります。そのポリシーはワークスペースの Pod を選択し、宛先のアドレスとポートを指定します
- **CP の Role や ClusterRole を広げないでください。** Namespace や NetworkPolicy への権限も与えません。乗っ取られた CP は、そのロールが及ぶ範囲すべてに手が届きます
- **メインのポート（8099）を `af-cp-internal` に足さないでください。** 内部の Service はワークスペースから CP への戻り経路で、ワークスペース専用のリスナーにしか届いてはいけません
- **ワークスペースの Namespace の Pod Security のレベルを下げないでください。** `restricted` より多くの権限が要るワークスペースの機能は、この形態では提供しません（Chromium のサンドボックスの `SYS_ADMIN` がその 1 つです）
- **ワークスペースの Pod を強制削除しないでください**（`--force --grace-period=0`）。プロセスが止まったことを確かめないまま名前を解放するので、2 つのエージェントが 1 つのホームディレクトリを共有しかねません。応答しなくなったノードの扱いは [ノードを入れ替える・止まったノードに対処する](nodes.ja.md#応答しなくなったノード) にあります

## このグループのページ

運用ページのコマンドは、GKE のページで設定するシェル変数（`PROJECT`・`REGION`・`LOCATION`・`PREFIX`・`TF`・`OVERLAY`）が設定済みで、`kubectl config current-context` がこのクラスターであることを前提にしています（[GKE に構築する](gke.ja.md#3-kubectl-をこのクラスターに向ける)）。

| ページ | できること |
|---|---|
| [GKE に構築する](gke.ja.md) | Terraform からロードバランサーの確認まで、GKE に構築する |
| [ほかのクラスターに構築する](other-clusters.ja.md) | GKE 以外のクラスターで、汎用のオーバーレイから構築する |
| [CP をアップグレードする](upgrade.ja.md) | バックアップを取って新しいバージョンに上げ、必要なら戻す |
| [バックアップ・復元する](backup.ja.md) | 残すべきものを知り、スナップショットの予定を入れ、復元をリハーサルする |
| [アラートを設定する](alerts.ja.md) | Cloud Monitoring に監視を設定する |
| [ノードを入れ替える・止まったノードに対処する](nodes.ja.md) | ノードを計画どおり入れ替え、応答しないノードから回復し、破棄が残したものを片付ける |
| [一時停止する・費用を見る](pause.ja.md) | データを残したまま停止し、費用の内訳を知る |
| [撤去する](teardown.ja.md) | 配備を消し、手で消すものを片付ける |

## 関連

- [デプロイ先を選ぶ](../choose.ja.md)
- [配備形態](../../ref/deploy-targets.ja.md)
- [配備のキーを扱う](../secure/deployment-keys.ja.md)
