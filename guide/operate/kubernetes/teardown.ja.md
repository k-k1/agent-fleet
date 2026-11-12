---
audience: "GKE の配備を撤去する人（Google Cloud のプロジェクトとクラスターの管理権限を持つ SRE）"
source_of_truth: "`deploy/gcp/gke/` の Terraform と `deploy/kubernetes/` のマニフェスト（食い違ったらそちらが正）"
---

# 撤去する

[English](teardown.md) | 日本語

GKE の配備を、データベース・クラスター・ネットワークまで消します。最後に手で消すものも片付けます。
コマンドは [GKE に構築する](gke.ja.md#シェル変数) のシェル変数と、`kubectl` がこのクラスターを指していることを前提にしています。

## 全体像

| あなたがやること | 起きること |
|---|---|
| Console で全メンバーのワークスペースを破棄する | ディスクが claim と一緒に消える |
| 必要ならバックアップを取る | オーバーレイを消すと `af-cp-data` のディスクも消えるので、残したいものを残せる |
| オーバーレイを `kubectl delete -k` する | 名前空間、CP、そのディスクが消える |
| 削除保護を外して `terraform destroy` する | Cloud SQL、クラスター、ネットワークなどが消える |
| スナップショットと予定を手で消す | 費用が残らない |

Cloud SQL、クラスター、KMS 鍵は、わざと削除から守られています。

## 撤去の手順

1. Console で、すべてのメンバーのワークスペースを先に破棄します。ディスクが claim と一緒に消えます
2. あとで要るかもしれないものがあれば、[CP をアップグレードする](upgrade.ja.md#上げる) の手順 2 のバックアップを取ります。オーバーレイを消すと claim `af-cp-data` が消え、`reclaimPolicy: Delete` なのでそのディスクも消えます
3. オーバーレイを消し、配備のボリュームが残っていないことを確かめます。終わっていない復元の `Retain` のボリュームは、ディスクを残します。残っていたら消します
   ```bash
   kubectl delete -k "deploy/kubernetes/overlays/$PREFIX"
   kubectl get pv | grep "$PREFIX-cp"
   gcloud compute disks list --project "$PROJECT" --filter="name~$PREFIX-cp-data"
   ```
4. Terraform が付けた保護をすべて外してから適用し、破棄します

| リソース | 外すもの |
|---|---|
| クラスター | `deletion_protection = false` |
| Cloud SQL のインスタンス | `deletion_protection = false`（Terraform 自身の保護）と `settings.deletion_protection_enabled = false`（Cloud SQL API の保護。これだけでも削除を拒む）の**両方** |
| KMS 鍵 | `prevent_destroy` を消す |

```bash
$TF apply
$TF destroy
```

## destroy がつまずかないための設定

Google Cloud のふるまいで destroy が止まらないように、3 つのリソースに設定があります。

| リソース | 設定 | 理由 |
|---|---|---|
| `google_sql_database.agentfleet`、`google_sql_user.cp` | `deletion_policy = "ABANDON"` | CP のデータベースユーザーは、最初の起動で作るデータベースの持ち主になるので、Terraform はどちらも消せない。どちらも Cloud SQL のインスタンスと一緒に消え、何も残らない |
| `google_service_networking_connection.private_service_access` | `deletion_policy = "REMOVE_PEERING"` | Service Networking は、インスタンスが消えたあとも長いあいだ接続を使用中と報告する。この設定はネットワークから VPC ピアリングを外すので、VPC の削除が止まらない（`ABANDON` だとピアリングが残って止まる） |

`ABANDON` は、全体の destroy に限らず、Terraform がそのリソースを消すときにはいつも API の呼び出しを省きます。

- インスタンスを残したまま、データベースやユーザーを設定から消す、それだけを destroy する、名前を変える（置き換えになる）と、古いデータベースとそのデータ、古い IAM データベースユーザーが、Terraform の管理を離れて Cloud SQL に残ります
- わざと消すときは、持ち主と依存を解き、`deletion_policy = "DELETE"` にして先に適用するか、手で片付けます（また管理するなら `terraform import`）

これらの設定より前に作った配備は、`$TF destroy` の前に上の `$TF apply` で設定をステートに記録します。それでもピアリングのせいで VPC の削除が拒まれたら、手で消して destroy し直します。

```bash
gcloud compute networks peerings delete servicenetworking-googleapis-com --network "$PREFIX-vpc" --project "$PROJECT"
```

## 実際の撤去で見たこと

ゾーンクラスター 1 つ、小さな構成での撤去で見たことです。

- 上の削除の設定があると、destroy はデータベースとユーザーを飛ばし（0 秒）、Cloud SQL のインスタンスを約 2.5 分で消し、サービスネットワーキングの接続、アドレス範囲、サブネット、VPC を、長い停滞も手でのピアリングの削除もなく消しました
- 最初の `$TF destroy` が、`google_certificate_manager_certificate_map` で 1 回失敗することがあります（ターゲット HTTPS プロキシに "already being used by"）。Gateway のコントローラーが、クラスターが消えたあと非同期にそのプロキシを消すためです。1 分ほどおいて `$TF destroy` をもう一度実行します。タイミング次第で、3 回の撤去のうち 1 回で起き、ほかは 1 回で終わりました
- `terraform init` も、プロバイダのダウンロード中に "connection reset by peer" で 1 回失敗することがあります。もう一度実行します
- ワークスペースの名前空間を先に消していると、`kubectl delete -k` がその名前空間のネットワークポリシーについて `NotFound` で終了コード 1 を返します。何も残らないので問題ありません
- KMS のキーリングと鍵の名前は、destroy のあと使い直せません。鍵のバージョンは破棄が予定される（30 日）だけで、キーリングは消せないためです。同じ `name_prefix` でスタックを作り直すと、`kms.tf` で "already exists" になります。別の接頭辞を使うか、キーリングと鍵を新しいステートに import します

## 手で消すもの

- KMS 鍵は Google Cloud が消さず、バージョンの破棄が予定されるだけです
- CP のディスクのスナップショットとスナップショットの予定は、destroy のあとも残ります。いらなくなったら `gcloud compute snapshots delete` と `gcloud compute resource-policies delete` で消します

手で消すものはこれだけです。データベースとユーザーはインスタンスと一緒に消え、Private Service Access の予約アドレス範囲は Terraform のリソースなので destroy が消します。

## 関連

- [一時停止する・費用を見る](pause.ja.md)：データを残したまま費用を下げる
- [バックアップ・復元する](backup.ja.md)
- [アイドル停止と強制停止を使う](../run/idle-stop.ja.md)
