---
audience: "GKE Standard に Agent Fleet を構築する人（Google Cloud のプロジェクトとクラスターの管理権限を持つ SRE）"
source_of_truth: "`deploy/gcp/gke/` の Terraform と `deploy/kubernetes/` のマニフェスト（食い違ったらそちらが正）"
---

# GKE に構築する

[English](gke.md) | 日本語

Terraform でクラスターとそのまわりを作り、マニフェストを適用して、Console が `https://<fqdn>` で応答するところまで進めます。
クラスターが満たすべき前提条件（P1〜P15）は [Kubernetes 構成の全体像](overview.ja.md#前提条件) にあり、GKE ではすべて Terraform が満たします。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 1. プロジェクトにログインし、API を有効にし、ステート用バケットを用意する | Terraform が使える状態になる |
| 2. `terraform apply` する | VPC、クラスターと 2 つのノードプール、Cloud SQL、KMS キー、ロードバランサーのアドレス・証明書・DNS、StorageClass、IAM の付与ができる |
| 3. kubectl をこのクラスターに向ける | 以降の `kubectl` が、ほかのクラスターではなくこのクラスターに届く |
| 4. データベースの権限を付ける（最初の 1 回） | CP の IAM データベースユーザーがデータベースの所有者になる |
| 5. オーバーレイをコピーして書き換える | 配備ごとの設定（URL、サインイン、イメージのバージョン）が決まる |
| 6. オーバーレイを適用し、Secret を作る | CP が起動する |
| 7. ロードバランサーを知る | WebSocket が切れる条件がわかる |
| 8. 確かめる | 権限・隔離・名前解決・ホームディレクトリの所有者が正しいとわかる |

## シェル変数

`LOCATION` はクラスター自体の場所です。リージョンクラスターならリージョン、小さな構成のようなゾーンクラスターならゾーン（`node_zones[0]`）にします。

コマンドはすべて**リポジトリのルートから**実行します。`gcloud` のコマンドはどれもプロジェクトを明示するので、`gcloud` の設定で既定のプロジェクトが別のものになっていても、そのプロジェクトを誤って変更することはありません。シェルごとに一度設定します。

```bash
PROJECT=<project>          # Terraform's project_id
REGION=<region>            # Terraform's region
LOCATION="$REGION"         # the cluster's location: node_zones[0] instead when zonal_cluster = true
PREFIX=<name_prefix>       # Terraform's name_prefix
TF="terraform -chdir=deploy/gcp/gke"
OVERLAY="deploy/kubernetes/overlays/$PREFIX"   # your overlay; point it at the copy once you move it
```

## 1. プロジェクトを準備する（最初の 1 回）

1. 2 回ログインします。下のコマンド用の `gcloud` と、Terraform のプロバイダー用の Application Default Credentials です
   ```bash
   gcloud auth login
   gcloud auth application-default login
   gcloud auth application-default set-quota-project "$PROJECT"
   ```
2. Terraform が使う API を有効にします
   ```bash
   gcloud services enable container.googleapis.com compute.googleapis.com \
     sqladmin.googleapis.com servicenetworking.googleapis.com certificatemanager.googleapis.com \
     dns.googleapis.com cloudkms.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
     --project "$PROJECT"
   ```
3. リージョンの SSD の割り当てを、配備が使いうる量と比べます。足りなければここで引き上げます（[ディスクの割り当て（GKE）](overview.ja.md#ディスクの割り当てgke)）。新しいプロジェクトの 500 GB では、既定値のままだと 2 台めのワークスペースノードを追加できません
4. ドメインの Cloud DNS マネージドゾーンが、プロジェクトにすでにあることを確かめます（P13）
5. ステート用バケットがなければ、バージョニングをオンにして作ります
   ```bash
   gcloud storage buckets create gs://<state-bucket> --project "$PROJECT" --location "$REGION" \
     --uniform-bucket-level-access
   gcloud storage buckets update gs://<state-bucket> --versioning --project "$PROJECT"
   ```
6. イメージの取得元を決めます

| 取得元 | 設定 |
|---|---|
| 自分の Artifact Registry のリポジトリ | `control-plane` と `workspace` をプッシュし、`artifact_registry_repository` を設定する。ノードプールと CP にそのリポジトリの読み取り権限が付く |
| 公開レジストリ（`ghcr.io/k-k1/agent-fleet/…`） | `artifact_registry_repository` は `null` のまま |

## 2. Terraform を適用する

```bash
cp deploy/gcp/gke/terraform.tfvars.example deploy/gcp/gke/terraform.tfvars   # fill it in; gitignored
$TF init -backend-config="bucket=<state-bucket>" -backend-config="prefix=$PREFIX"
$TF apply
```

`terraform.tfvars` で気をつける変数です。

| 変数 | 気をつけること |
|---|---|
| `name_prefix` | すべてのリソースと 2 つの Namespace（`<prefix>-cp`、`<prefix>-ws`）の名前になる。1 つのプロジェクトに 2 つ配備するなら、接頭辞も 2 つ要る（P8） |
| `authorized_networks` | コントロールプレーンのエンドポイントへのアクセスを許可する範囲。StorageClass を作るプロバイダーがそのエンドポイントと通信するので、`apply` は承認済みネットワークから実行する。`enable_private_endpoint = true` なら VPC の中から実行する |
| `node_cidr`・`pod_cidr`・`service_cidr`・`control_plane_cidr`・`private_service_access_cidr` | クラスターを作ったあとは変えられない。最初の 4 つは RFC 1918 か `100.64.0.0/10` の中に置き、外れると plan が失敗する（P15）。Private Service Access の範囲も同じ範囲の中に置く。ワークスペースの外向きのポリシーはこれらの範囲の外をすべて許可するので、この範囲を拒否できるのは範囲がその中にあるときだけで、plan はこの範囲を確かめない |
| `min_master_version` | 作るときのバージョンではなく下限。クラスターはリリースチャンネルの既定のバージョンで始まり、コントロールプレーンがこの下限を下回ると plan のたびに失敗する（P1） |

証明書は、DNS の承認レコードが引けるようになってから発行されます。状態は次で見られ、たいてい 1 時間以内に `ACTIVE` になります。

```bash
gcloud certificate-manager certificates describe $PREFIX-cert --project "$PROJECT"
```

### Terraform が付ける権限

どれも 1 つのプリンシパルに、1 つのリソースについて付けます。ワークスペースの Namespace を指定するものはありません。

| プリンシパル | 付く権限 |
|---|---|
| CP のサービスアカウント | Cloud SQL のクライアントとインスタンスユーザー（このインスタンスに条件付け）、リポジトリの Artifact Registry の読み取り |
| ノードのサービスアカウント | Artifact Registry の読み取りと、ノードの最小のロール（`roles/container.defaultNodeServiceAccount`。ログ、メトリクス、1.33 からはオートスケーラーのメトリクス） |
| GKE のサービスエージェント | Secret のキーでの暗号化と復号 |
| CP の Kubernetes サービスアカウント `<prefix>-cp/af-cp` | CP のサービスアカウントとして動ける |

### 試用向けの小さな構成

試用や 1 チームで使うときの構成です。`terraform.tfvars.example` の最後のコメントのブロックが、すべてを設定します。

| 設定 | 値 | 既定の構成では |
|---|---|---|
| `node_zones`、`zonal_cluster` | 1 ゾーン、`true` | 2 ゾーンにノードがあるリージョンクラスター |
| `sql_availability_type`、`sql_tier` | `ZONAL`、`db-custom-1-3840`（Postgres で選べる最小の専用コアの階層） | `REGIONAL` |
| `system_machine_type`、`system_node_count` | `e2-standard-2`、1 | 2 つのゾーンそれぞれに e2-standard-2 |
| `workspace_machine_type` | `n2-standard-4` | n2-standard-8 |
| `managed_prometheus` | `false` | オン |

失うもの:

- **コントロールプレーンの高可用性。** ゾーンクラスターの API サーバーは、アップグレードのあいだ使えません。動いているワークスペースは動き続けますが、操作できません
- **データベースの高可用性。** Cloud SQL の `ZONAL` には待機系がありません。ゾーンの障害やメンテナンスの再起動のあいだ、CP は止まります。バックアップとポイントインタイムリカバリはオンのままです
- **ゾーン障害への耐性すべて。** CP、そのディスク、ワークスペースのボリュームとノードが、すべて 1 つのゾーンにあります。ゾーンが落ちると、動いているワークスペースも止まります

`zonal_cluster` を変えるとクラスターが作り直されます。最初の `apply` の前に決めてください。既存の配備をこの構成に移すには、再構築が必要です。

- ワークスペースの Pod の上限は、`n2-standard-4` の約 4 vCPU / 16 GiB に収める必要があります
- システムノードが e2-medium 1 台だと、割り当て可能な CPU が約 0.94 vCPU しかなく、CP が `Pending`（"Insufficient cpu"）のままになり、その claim も `Pending` のままです。そのため、この構成では `e2-standard-2` を使います。動いているクラスターで `system_machine_type` を変えると、ノードプールがその場で更新され、約 6 分かかります
- この構成はゾーンクラスターで一度構築しただけです（最初の `apply` に約 18 分、オーバーレイを適用してから CP が健全になるまで約 14 分）。ほかのゾーン、リージョン版、ほかのリージョンは試していません。共有コアの Cloud SQL の階層（`db-g1-small`）も試していません

費用の見積もりは [一時停止する・費用を見る](pause.ja.md#小さな構成の費用) にあります。

## 3. kubectl をこのクラスターに向ける

Terraform は kubeconfig に触りません。**`kubectl` のコマンドを打つ前に**、このクラスターの認証情報を取り、いまのコンテキストがこのクラスターであることを確かめます。そうしないと、下の Namespace ・RBAC・Secret が、kubeconfig が指していた別のクラスターに入ります。

```bash
eval "$($TF output -raw get_credentials)"     # gcloud container clusters get-credentials …
kubectl config current-context                # gke_<project>_<region>_<prefix>-gke
kubectl get nodes -L agent-fleet.io/pool      # the system and workspace pools
```

運用ページの手順を行う前にも、新しいシェルを開くたびに同じことをします。

## 4. データベースの権限を付ける（最初の 1 回）

CP のデータベースユーザーは、パスワードのない IAM ユーザーで、最初は何の権限もありません。組み込みの `postgres` ユーザーとして、クラスター内の一時的な Pod から、このユーザーにデータベースの所有権を渡します。

- インスタンスは非公開 IP だけで、ワークスペースは VPC の外にあり、`cloud-sql-proxy` も入っていません。そのため、権限付与はクラスターのノード上の Pod（制限されたセキュリティコンテキストで動く、一時的な `postgres:17-alpine`）から実行します
- `postgres` のパスワードは、ここではなくシークレットストアに保管します

### ターミナルがあるとき

```bash
gcloud sql users set-password postgres --instance "$PREFIX-pg" --project "$PROJECT" --prompt-for-password
DBUSER="$($TF output -raw cp_database_user)"
DBHOST="$(gcloud sql instances describe "$PREFIX-pg" --project "$PROJECT" --format='value(ipAddresses[0].ipAddress)')"
# The statements to paste into psql below (the pod knows nothing of $DBUSER):
echo "GRANT \"$DBUSER\" TO postgres; ALTER DATABASE agentfleet OWNER TO \"$DBUSER\"; ALTER SCHEMA public OWNER TO \"$DBUSER\";"
# The CP namespace does not exist yet (step 6 creates it); `default` does.
kubectl -n default run psql-grant -it --rm --restart=Never --image=postgres:17-alpine \
  --override-type=strategic \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":70,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"psql-grant","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' \
  -- psql "host=$DBHOST user=postgres dbname=agentfleet sslmode=require" -W
```

1. `Password:` のプロンプトで `postgres` のパスワードを入力します
2. `psql` のプロンプトに、表示された文を貼り付けます。`GRANT ROLE`、`ALTER DATABASE`、`ALTER SCHEMA` が返れば成功です。`ERROR` が 1 つでも出たら、権限は付いていません
3. `\q` で抜けます

- 対話の `psql` は SQL のエラーで終わらないので、返答を読んでください
- パスワードは Pod のターミナルを通して `psql` に渡るだけで、コマンドライン・Pod の定義・API オブジェクトのどこにも残りません
- Pod は抜けると消えます（`--rm`）。接続が切れて残ったときは `kubectl -n default delete pod psql-grant` で消します

### ターミナルがないとき（スクリプト、CI、エージェントのシェル）

`-it` と `-W` にはターミナルが要るので、上のコマンドは動きません。代わりに、パスワードを一時的な Secret に入れ、そこから `PGPASSWORD` を読む非対話の Pod で `-c` の文を実行し、両方を消します。パスワードは argv にも Pod の定義にも入りません。

上のブロックの最初の行（`gcloud sql users set-password … --prompt-for-password`）もターミナルが要ります。ターミナルがないときは `--password` でパスワードを設定し（その `gcloud` の呼び出しの引数に一瞬だけ載ります）、終わったら新しいランダムな値にします。たとえば、次のように生成したパスワードを先に設定しておくと、下のブロックはプロンプトを出さずにその `PGPW` を使います。

```bash
PGPW="$(openssl rand -base64 24)"                   # kept only in this shell
gcloud sql users set-password postgres --instance "$PREFIX-pg" --project "$PROJECT" --password "$PGPW"
```

権限付与のブロックは `DBUSER` と `DBHOST` を自分で設定し、どちらかが空なら止まります。

```bash
DBUSER="$($TF output -raw cp_database_user)"
DBHOST="$(gcloud sql instances describe "$PREFIX-pg" --project "$PROJECT" --format='value(ipAddresses[0].ipAddress)')"
[ -n "$DBUSER" ] && [ -n "$DBHOST" ] || { echo "DBUSER or DBHOST is empty: stop"; exit 1; }
PWDIR="$(mktemp -d)"; chmod 700 "$PWDIR"
cleanup() {
  rm -rf "$PWDIR"                                   # the local copy first: it needs no API
  kubectl -n default delete pod psql-grant --ignore-not-found --wait=false --request-timeout=30s \
    || echo "could not delete pod psql-grant: delete it by hand"
  kubectl -n default delete secret psql-grant-pw --ignore-not-found --request-timeout=30s \
    || echo "WARNING: secret psql-grant-pw may remain (it holds the postgres password): delete it by hand"
}
trap cleanup EXIT                                   # runs under `set -e` too
[ -n "${PGPW:-}" ] || { read -rs -p 'postgres password: ' PGPW; echo; }   # or read it from your vault
printf '%s' "$PGPW" > "$PWDIR/PGPASSWORD"; unset PGPW   # printf is a builtin: not in any argv
kubectl -n default create secret generic psql-grant-pw --from-file=PGPASSWORD="$PWDIR/PGPASSWORD"
kubectl -n default run psql-grant --restart=Never --image=postgres:17-alpine \
  --override-type=strategic \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":70,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"psql-grant","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}},"env":[{"name":"PGPASSWORD","valueFrom":{"secretKeyRef":{"name":"psql-grant-pw","key":"PGPASSWORD"}}}]}]}}' \
  -- psql -v ON_ERROR_STOP=1 "host=$DBHOST user=postgres dbname=agentfleet sslmode=require" \
  -c "GRANT \"$DBUSER\" TO postgres; ALTER DATABASE agentfleet OWNER TO \"$DBUSER\"; ALTER SCHEMA public OWNER TO \"$DBUSER\";"
kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/psql-grant --timeout=120s \
  || echo "grant pod did not succeed"
kubectl -n default logs psql-grant        # expect GRANT ROLE, ALTER DATABASE, ALTER SCHEMA
```

- `EXIT` の trap は、先にパスワードのファイルを消し、それから削除を 1 つずつ試します。`kubectl delete` が 1 つ失敗しても（ログインの期限切れ、接続なし）残りは飛ばしません。Secret を消せなかったときはそう表示するので、手で消します
- trap は `wait` が時間切れになっても、`set -e` でコマンドが失敗しても動きます。`|| echo` があるので、trap が動く前に `logs` で `ERROR` を読めます
- パスワードのファイルは、実行中だけ手元のモード 700 のディレクトリに置かれます。それが受け入れられないなら、FIFO かシークレットストアのファイルの仕組みを使います
- Pod が `Failed` で終わったら、`logs` に `ERROR` が出ていて、権限は付いていません
- 終わったら `postgres` のパスワードを新しくします（`gcloud sql users set-password … --prompt-for-password`、または保管しないランダムな値）。古い値は、実行中ずっと Secret オブジェクトに入っていたためです

この形のまま実行して確かめたのは、ターミナルがないときの方法だけです（`read` の行を生成したパスワードに置き換えて実行しました）。ターミナルがあるときの `-it … -W` の形は、そのまま実行して確かめてはいません。

## 5. オーバーレイを用意する

```bash
cp -r deploy/kubernetes/overlays/gke "$OVERLAY"
$TF output -raw kustomize_deployment > "$OVERLAY/deployment.yaml"
```

コピーの中で書き換えます。

| ファイル | 書き換えるもの |
|---|---|
| `cp.env` | `PUBLIC_BASE_URL` と `PUBLIC_DOMAIN`（`fqdn`）、`AF_PREVIEW_DOMAIN`（Terraform の `preview_domain`、または空）、サインインの設定、`SUPER_ADMIN_EMAILS`。`AF_TRUSTED_PROXY_HOPS=2` はそのまま（[7. ロードバランサを知る](#7-ロードバランサを知る)） |
| `kustomization.yaml` | `images:` の CP のイメージとリリースのタグ |
| `base/cp-data.yaml` | CP 自身のディスクとして 20 GiB を要求する。内部の git プロバイダーにそれ以上のデータを置くなら、コピーの中でパッチを当てる |

### クローンの外に置く

このコピーは、このリポジトリの作業コピーではなく、自分のリポジトリかシークレットストアに置きます。クローンの外に出すと、`kustomization.yaml` の 2 か所を変える必要があります。

- **パス。** `resources: ../../base`、`components: ../../components/gke` と `../../components/params` は、リポジトリの中の `deploy/kubernetes/overlays/<name>/` からの相対パスで、外では何も指しません。オーバーレイからクローンの `deploy/kubernetes/base` と `deploy/kubernetes/components/…` への**相対**パスに書き換えます。`kubectl kustomize` は絶対パスのディレクトリ参照を拒みます（`new root '…' cannot be absolute`）。適用する前に `kubectl kustomize <your overlay>` で確かめます
- **`OVERLAY`。** シェルごとに、移したコピーのパスにします（`OVERLAY=<your overlay>`）。これらのページでオーバーレイを読む・適用するコマンドはすべて `$OVERLAY` を使うので、クローンの中のコピーには触りません
- **`newName`。** コピーした `images:` には、仮の `newName`（Artifact Registry のパス）が入っています。公開イメージ（`ghcr.io/k-k1/agent-fleet/…`）を使うなら `newName` の行を消して `newTag` を残し、名前を公開のままにします。自分の Artifact Registry を使うなら、`newName` をそのリポジトリにします

### 外向き通信のイベントを保存する（任意）

CP は、生の外向き通信のイベントを消す前に、終わった日（UTC）ごとに Cloud Storage に書き出せます（`AF_EGRESS_ARCHIVE_URL`）。この設定の説明は [外向きの通信を制御する](../secure/egress.ja.md#記録を残す) にあります。既定はオフで、`egress_archive = false` なら plan にバケットも権限も CP の新しい設定も出ません。

| 変数 | 既定 | 意味 |
|---|---|---|
| `egress_archive` | `false` | `true` でオンにする |
| `egress_archive_existing_bucket` | `""` | すでにあるバケットの名前。モジュールは CP の権限だけを管理する。空なら `<name_prefix>-egress-archive-<project_id>` を作る |
| `egress_archive_prefix` | `""` | バケットの中のオブジェクトの接頭辞（CP はすでに `egress/` の下に書く）。`A-Za-z0-9._-` からなるセグメントを 1 つのスラッシュでつなぐ（`team/prod`）。`.` や `..` のセグメント、`#?%`、両端のスラッシュは使えない |
| `egress_archive_location` | `""` | 作るバケットの場所。空なら `region` |
| `egress_archive_retention_days` | `null` | 保存したオブジェクトをこの日数で消す（作ったバケットだけ）。`null` なら消さない。イベントが消されたあとは、保存したものが唯一のコピーになる |

- 作るバケットは、均一なバケットレベルのアクセスと公開アクセスの防止が強制され、`force_destroy = false` です。保存した日のデータが残っているバケットがあると、破棄はそこで止まります
- CP の既存の Google サービスアカウント（Workload Identity のもの。2 つめの ID は作りません）に、**そのバケットだけについて** `roles/storage.objectCreator` と `roles/storage.objectViewer` が付きます
- 閲覧のロールは省けません。次の実行は、すでに書いた日を読み返して確かめます。それがないと読み返しが 403 になり、その日で削除が止まります
- 指定したバケットは、その 2 つの IAM メンバー以外は変更しません。公開アクセスの防止は自分でオンのままにします

`cp.env` は、`terraform output` が成功したときだけ書き換えます。古いキーを消し、出力に行があるときだけ現在の行を足します。出力が空なら保存はオフです。このときキーを残すと、権限のなくなったバケットに CP が書き込み続けます。

```bash
if line=$($TF output -raw egress_archive_env); then
  sed -i '/^AF_EGRESS_ARCHIVE_URL=/d' "$OVERLAY/cp.env"
  [ -z "$line" ] || printf '%s\n' "$line" >> "$OVERLAY/cp.env"
else
  echo "terraform output failed: cp.env left as it was" >&2; false
fi
```

- 読み取りの失敗（バックエンドのエラー、`-chdir` の間違い、出力がまだない）を「オフ」と取り違えないためです。取り違えるとキーが消え、次の適用で保存せずにイベントが消されます。読み取りに失敗するとブロックは 0 以外で終わるので、そのときは適用し直さないでください
- 終わったらオーバーレイを適用し直します。`kustomize` はキーが重なった `cp.env` を拒むので、2 回足さないでください
- 実際のバケットではまだ確かめていません（最初の保存、それを見つけて確かめる 2 回めの実行、閲覧のロールがないときの 403）。オフラインのテストが確かめるのは plan だけです

## 6. オーバーレイを適用し、Secret を作る

CP は Secret `af-cp-secrets` を 1 つ読みます。マニフェストには入っていません。Namespace ができたら作ります。

```bash
kubectl config current-context                # still this cluster (step 3)
kubectl apply -k "$OVERLAY"
kubectl -n "$PREFIX-cp" create secret generic af-cp-secrets \
  --from-literal=AF_MASTER_KEY="$(cat master-key)" \
  --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
  --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
```

- Secret ができるまで、CP の Pod は `CreateContainerConfigError` で待ち、できると自分で起動します
- `AF_MASTER_KEY` は `head -c 32 /dev/urandom | base64` で作ります（compose と同じです）。Kubernetes の Secret や手元のファイルだけでなく、このクラスターより長く残る場所（Secret Manager やシークレットストア）に保管します（P14）
- CP を作り直すとき（新しいクラスター、`af-cp-secrets` の削除）にまた要ります。違うキーでは、古いキーで暗号化したものを読めません
- ほかのサインインのシークレット（`AF_OIDC_<ID>_CLIENT_SECRET`、`GITHUB_OAUTH_CLIENT_SECRET`）も同じ Secret に入れます
- 1 つを入れ替えるときは、`kubectl create secret … --dry-run=client -o yaml | kubectl apply -f -` のあと `kubectl -n "$PREFIX-cp" rollout restart deployment/af-cp` を実行します

キーの要件は [配備の鍵を扱う](../secure/deployment-keys.ja.md) にあります。

## 7. ロードバランサを知る

Gateway `af-cp`（`components/gke/gateway.yaml`）が、GKE に**グローバル外部**アプリケーションロードバランサーを作らせます。従来版のロードバランサーは、使用中の WebSocket でもバックエンドのタイムアウトで閉じるので使いません。ターミナル、ミラー、ブラウザペインは WebSocket なので、2 つの制限が効きます。

| WebSocket | 切れる条件 |
|---|---|
| アイドル（誰も入力しないターミナル、静かなセッション） | バックエンドサービスの `timeoutSec` のあと。既定は 30 秒で、同じファイルの `GCPBackendPolicy` `af-cp` が 3600 にしている |
| 使用中 | `timeoutSec` にかかわらず 24 時間のあと。設定では無効にできないので、Console が再接続する |

- `timeoutSec` を変えるには、そのファイルで値を変え、オーバーレイを `kubectl apply -k` します。GKE は数分でバックエンドサービスを更新します（`gcloud compute backend-services list --project "$PROJECT"` に値が出ます）
- Console は同じソケットで、5 秒ごとの往復の ping と 15 秒ごとのハートビートを送ります。誰も入力していないターミナルが、`timeoutSec` の 3600 秒を過ぎてから 67 分以上つながったままだった例があります。ping によって、接続がアイドルと見なされなかったためです
- 隠れたタブ、凍結したページ、スリープしたマシンではブラウザがタイマーを遅らせたり止めたりすることがあり、そのときどうなるかは確かめていません
- 24 時間の切断は、実際に 24 時間待っては確かめていません。強制的な切断（CP の再起動）は確かめています。ターミナルペインに `[disconnected]` と出て、クリックするとページを再読み込みせずに数秒で再接続し、シェルとその tmux セッションはそのまま残ります。ロードバランサーによる切断もブラウザには同じ不正な切断として届くので、同じように戻ります

### `AF_TRUSTED_PROXY_HOPS` は 2 のまま

ロードバランサーは、受け取った `X-Forwarded-For` に `<client>, <load balancer>` を足し、CP は右から数えます。ロードバランサーの前に何もなければ、2 で実際のクライアントを指します。

- 前に CDN やプロキシを置いて値を上げてよいのは、ロードバランサーにそのプロキシからしか届かず（たとえばプロキシのアドレスだけを通す Cloud Armor のポリシー）、**かつ**プロキシがどのエントリを足すか置き換えるかを確かめたときだけです
- そうでないと、自分で `X-Forwarded-For` を付けてロードバランサーに直接送ったクライアントが、CP から見えるアドレスを自由に選べ、テナントのネットワーク制限をすり抜けられます
- この構成は、ロードバランサーの前のプロキシをサポートしません

## 8. 確かめる

```bash
kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
kubectl get ns "$PREFIX-ws" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'   # restricted
kubectl -n "$PREFIX-cp" get pvc af-cp-data    # Bound
kubectl -n "$PREFIX-cp" get gateway af-cp     # PROGRAMMED True, ADDRESS = lb_address
curl -sS https://<fqdn>/readyz
```

**`PROGRAMMED True` では足りません。`/readyz` そのものを待ちます。** ロールアウトが終わっても、ロードバランサーの準備ができたとは限りません。

- CP の準備ができてから `/readyz` が 200 を返すまで、5〜6 分かかったことがあります。そのあいだ、DNS はすでに新しいアドレスを指していても、`curl` は `000`（接続なし）を返しました
- Gateway が `PROGRAMMED True` になってからも、`/readyz` が約 4 分 `000` を返し続けたことがあります

応答するまでくり返します。

```bash
until [ "$(curl -sS -m 10 -o /dev/null -w '%{http_code}' https://<fqdn>/readyz)" = 200 ]; do sleep 30; done
```

`kubectl -n "$PREFIX-cp" get gateway af-cp -w` で Gateway 自体の状態が見られます。`kubectl wait --for=condition=Programmed gateway/af-cp` は、同じ理由で早く終わりすぎるはずです（試してはいません）。

### CP のアカウントの権限

どの行も、コメントに示した答えを返す必要があります。

```bash
SA=system:serviceaccount:$PREFIX-cp:af-cp
kubectl auth can-i create statefulsets -n "$PREFIX-ws" --as $SA       # yes
kubectl auth can-i delete pods -n "$PREFIX-ws" --as $SA               # yes (the erase pod)
kubectl auth can-i patch namespaces --as $SA                          # no
kubectl auth can-i create networkpolicies -n "$PREFIX-ws" --as $SA    # no
kubectl auth can-i list storageclasses --as $SA                       # no
kubectl auth can-i get "storageclass/$PREFIX-workspace" --as $SA      # yes
kubectl auth can-i create pods -n "$PREFIX-cp" --as $SA               # no
```

### NodeLocal DNSCache がオフ

P11 です。何も表示されなければ正しい状態です。

```bash
kubectl -n kube-system get pods -l k8s-app=node-local-dns
```

### ワークスペースの置き場所と名前解決

サインインしてワークスペースを起動し、その Pod がワークスペースのプールで動き、名前を引けることを確かめます。

```bash
kubectl -n "$PREFIX-ws" get pods -o wide -l agent-fleet.io/workspace
kubectl -n "$PREFIX-ws" exec <pod> -- getent hosts github.com     # an address, not empty
```

### ノードに届かないこと

ワークスペースからは、自分のノードも含め、どのノードにも届かないはずです（P15）。Pod 自身が `open` か `closed` を報告します。

- ノードの行はすべて `closed`、最後の対照の行は `open` になるはずです
- `ERROR` の行が出る、またはノードの行が 1 つもないときは、何も確かめられていません

```bash
nodes=$(kubectl get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}') \
  && [ -n "$nodes" ] || echo "ERROR: could not list the nodes"
probe() {  # <ip> <port>
  kubectl -n "$PREFIX-ws" exec <pod> -- bash -c \
    "if timeout 3 bash -c 'exec 3<>/dev/tcp/$1/$2' 2>/dev/null; then echo '$1:$2 open'; else echo '$1:$2 closed'; fi" \
    || echo "$1:$2 ERROR: exec failed"
}
for ip in $nodes; do for port in 22 10250 10256; do probe "$ip" "$port"; done; done
probe 1.1.1.1 443
```

### ホームディレクトリのオーナー

そのワークスペースのターミナルで確かめます。ホームディレクトリの所有者が `dev` で、ほかの誰も書き込めない状態でないと、`af-gcloud-exec` と `af-aws-exec` はその下に状態を置くのを拒みます。

```bash
stat -c '%U:%G %A' /home/dev    # dev:dev drwxr-sr-x — no w for group or other (the s, from fsGroup, is harmless)
```

- ホームディレクトリは、ホームディレクトリ用の claim の中のディレクトリ `.af-home` です。`dev` として動く init コンテナが、エージェントの起動前にそれを作り、以前のホームディレクトリのファイルをそこへ移します
- claim のルートは、kubelet が残したとおり `root:dev` でグループ書き込みつきのままです

## 困ったとき

| 症状 | 原因と対処 |
|---|---|
| CP の Pod が `CreateContainerConfigError` のまま | `af-cp-secrets` がまだない。[6.](#6-オーバーレイを適用しsecret-を作る) で作ると自分で起動する |
| `curl https://<fqdn>/readyz` が `000` を返す | ロードバランサーの準備がまだ。数分かかるので、[8.](#8-確かめる) のループで待つ |
| 証明書が `ACTIVE` にならない | DNS の承認レコードがまだ引けない。たいてい 1 時間以内 |
| ワークスペースの Pod が `Pending` で、スケールアップが `QUOTA_EXCEEDED` | SSD の割り当て切れ。[ディスクの割り当て（GKE）](overview.ja.md#ディスクの割り当てgke) |
| ワークスペースの Pod が `Init` で止まる | `kubectl logs <pod> -c home-layout` に理由が出る。以前のバージョンに戻したあとなら [ホームの配置より前の版に戻す](upgrade.ja.md#ホームの配置より前の版に戻す) |
| ワークスペースからの名前解決がすべて失敗する | NodeLocal DNSCache がオン（P11） |
| `kubectl kustomize` が `new root '…' cannot be absolute` を返す | オーバーレイのパスが絶対パス。[クローンの外に置く](#クローンの外に置く) |

## 関連

- [Kubernetes 構成の全体像](overview.ja.md)
- [バックアップ・復元する](backup.ja.md)：最初の起動のあとに、CP のディスクにスナップショットの予定を入れます
- [アラートを設定する](alerts.ja.md)
- [サインインの方法を設定する](../signin/overview.ja.md)
