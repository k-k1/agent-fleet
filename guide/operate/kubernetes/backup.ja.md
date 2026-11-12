---
audience: "Kubernetes（GKE）の配備のデータを守る人（クラスターとクラウドの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと `deploy/gcp/gke/` の Terraform（食い違ったらそちらが正）"
---

# バックアップ・復元する

[English](backup.md) | 日本語

GKE の配備で、失うと困るものを知り、CP のディスクにスナップショットの予定を入れ、データベースとディスクの復元を、本番に触らずに予行します。
コマンドは [GKE に構築する](gke.ja.md#シェル変数) のシェル変数と、`kubectl` がこのクラスターを指していることを前提にしています。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 何もしない | Terraform がデータベースの自動バックアップとポイントインタイムリカバリをオンにしている（`sql_backup_retention_days`、既定は 7 日） |
| 最初の起動のあとに、CP のディスクにスナップショットの予定を入れる | 毎日スナップショットが取られ、14 日で消える |
| データベースを複製して中を確かめ、消す | データベースを戻せることと、かかる時間がわかる |
| スナップショットからディスクを作り、Pod で中を読み、消す | ディスクを戻せることと、かかる時間がわかる |

## 残すべきもの

配備の状態を持つものは 2 つで、組になっています。

| もの | 中身 |
|---|---|
| データベース（Cloud SQL） | すべての行 |
| CP 自身のディスク（claim `af-cp-data`、`WS_DATA` の `/var/lib/af-cp`） | 内部の git プロバイダのベアリポジトリと LFS オブジェクト、git トークンの鍵。これがないとデータベースのリポジトリの一覧は役に立たず、新しい鍵は配ったすべての git トークンを無効にする |

- 3 つめは `AF_MASTER_KEY` で、金庫にあります（P14）
- ホームはこの版ではバックアップされません。ディスクを失うと、ホームも失います
- **CP を止めて**取ったバックアップは、2 つのあいだで整合します。予定で取るものは整合しません。ディスクのスナップショットとデータベースのバックアップは違う時刻に動くので、そのあいだにプッシュしたリポジトリは片方にしかないことがあります

### CP のディスクを探す

復元するとディスクが置き換わるので、**復元のあとにもう一度**探します。

```bash
PV="$(kubectl -n "$PREFIX-cp" get pvc af-cp-data -o jsonpath='{.spec.volumeName}')"
HANDLE="$(kubectl get pv "$PV" -o jsonpath='{.spec.csi.volumeHandle}')"   # projects/<p>/zones/<z>/disks/<name>
ZONE="$(echo "$HANDLE" | cut -d/ -f4)"; DISK="${HANDLE##*/}"
```

## スナップショットの予定を入れる

最初の起動のあとに一度、そして復元でディスクが置き換わるたびに入れます（予定は claim ではなくディスクに付きます）。

```bash
gcloud compute resource-policies create snapshot-schedule "$PREFIX-cp-data" --project "$PROJECT" \
  --region "$REGION" --daily-schedule --start-time 02:30 --max-retention-days 14   # once
gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
  --resource-policies "$PREFIX-cp-data"
```

予定で取ったスナップショットは自分で期限切れになります。下の手順やアップグレードで手で取ったものは、消すまで残ります（`gcloud compute snapshots delete <name> --project "$PROJECT"`）。

## スナップショットからディスクとボリュームを作る

ディスクの予行と、[CP をアップグレードする](upgrade.ja.md#戻す) の戻す手順は、どちらもスナップショットからディスクを作り、事前に作ったボリュームとして Kubernetes に渡します。

- このボリュームは、StorageClass が作るものと違って自分のゾーンを持たないので、PersistentVolume にゾーンを書きます
- `nodeAffinity` がないと、Pod がシステムプールの別のゾーンに置かれることがあり、そこではディスクを付けられず、いつまでも起動しません

変数を設定します。`ZONE` は [CP のディスクを探す](#cp-のディスクを探す) のものです。

| 変数 | 中身 |
|---|---|
| `NAME` | 新しいディスクとボリュームの名前。**復元ごとに違う名前**にする。決まった名前だと、前の復元で使い始めたディスクとぶつかる |
| `CLAIM` | そのボリュームを使う claim |
| `SNAP` | スナップショット |

2 つの部分に分かれているので、ほかを変える前にディスクだけを作れます。

ディスク:

```bash
gcloud compute disks create "$NAME" --zone "$ZONE" --project "$PROJECT" \
  --source-snapshot "$SNAP" --type pd-balanced
SIZE="$(gcloud compute disks describe "$NAME" --zone "$ZONE" --project "$PROJECT" --format='value(sizeGb)')"
```

claim `$CLAIM` 用のボリューム:

```bash
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $NAME
spec:
  capacity: { storage: ${SIZE}Gi }
  accessModes: ["ReadWriteOnce"]
  persistentVolumeReclaimPolicy: Retain      # until it is confirmed; see each procedure
  storageClassName: $PREFIX-workspace
  claimRef: { namespace: $PREFIX-cp, name: $CLAIM }
  csi:
    driver: pd.csi.storage.gke.io
    volumeHandle: projects/$PROJECT/zones/$ZONE/disks/$NAME
    fsType: ext4
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - { key: topology.gke.io/zone, operator: In, values: ["$ZONE"] }
YAML
```

## データベースの復元を予行する

立ち上げたあとに一度、そしてデータベースの設定を変えるたびに、本番のインスタンスに触らずに予行します。

```bash
gcloud sql instances clone "$PREFIX-pg" "$PREFIX-pg-rehearsal" --project "$PROJECT" \
  --point-in-time "$(date -u -d '-15 min' +%Y-%m-%dT%H:%M:%SZ)"
# gcloud may stop waiting after about 10 minutes ("taking longer than expected") while the
# clone goes on. Wait for the operation it named, or until the clone's state is RUNNABLE:
#   gcloud beta sql operations wait --project "$PROJECT" --timeout unlimited <operation>
#   gcloud sql instances describe "$PREFIX-pg-rehearsal" --project "$PROJECT" --format='value(state)'
# connect to the clone as in "4. Grant the database (once)" and check that the data is there:
#   SELECT count(*) FROM identity;  SELECT max(at) FROM audit_log;
gcloud sql instances patch "$PREFIX-pg-rehearsal" --project "$PROJECT" --no-deletion-protection
gcloud sql instances delete "$PREFIX-pg-rehearsal" --project "$PROJECT"
```

- 複製への接続は [GKE に構築する](gke.ja.md#4-データベースの権限を付ける最初の-1-回) の psql の Pod と同じ方法です
- 複製は本番のインスタンスから削除保護を受け継ぐので、削除だけでは "The instance is protected" で失敗します
- 削除保護を外すのは**複製だけ**（`$PREFIX-pg-rehearsal`）にしてください。`$PREFIX-pg` では外しません。本番のデータベースを誤って消さないように、Terraform がそこに設定しています

## ディスクの復元を予行する

スナップショットを本番のディスクの横に戻し、**中を読んで**、消します。ディスクを作れただけでは、中身について何もわかりません。

スナップショットを取る前に:

1. テスト用のリポジトリに、内部の git プロバイダを通して目印のコミットをプッシュし、コミット ID を控えます
2. LFS を使うなら、そのコミットで 1 つのファイルを LFS で追跡し、oid と大きさを控えます（`git lfs ls-files -l` が oid、`wc -c` が大きさを示します）

あるだけの LFS オブジェクトを確かめるだけでは足りません。LFS オブジェクトが 1 つもないスナップショットでも通ってしまいます。

本番の claim には触らずに、次を実行します。

```bash
NAME="$PREFIX-cp-data-rehearsal-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data-rehearsal SNAP=<snapshot>
SENT_REPO="git/<tenant slug>/<test repo>.git" SENT_REF=refs/heads/<branch> SENT_COMMIT=<commit id>
LFS_OID=<oid, or empty without LFS> LFS_SIZE=<bytes, or empty>
LFS_PATH=""; [ -n "$LFS_OID" ] && LFS_PATH="$SENT_REPO/lfs/objects/${LFS_OID:0:2}/${LFS_OID:2:2}/$LFS_OID"
# ... both parts of "Make a disk and a volume from a snapshot" ...
IMAGE="$(kubectl -n "$PREFIX-cp" get deployment af-cp -o jsonpath='{.spec.template.spec.containers[?(@.name=="cp")].image}')"
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $CLAIM, namespace: $PREFIX-cp }
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: $PREFIX-workspace
  volumeName: $NAME
  resources: { requests: { storage: ${SIZE}Gi } }
---
apiVersion: v1
kind: Pod
metadata: { name: af-cp-data-rehearsal, namespace: $PREFIX-cp }
spec:
  restartPolicy: Never
  nodeSelector: { agent-fleet.io/pool: system }
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    seccompProfile: { type: RuntimeDefault }
  containers:
    - name: check
      image: $IMAGE
      command: ["sh", "-c"]
      args:
        - |
          set -e; cd /data; ls -la
          test -d git || { echo "FAIL: no git/"; exit 1; }
          for r in git/*/*.git; do git --git-dir="\$r" fsck --no-dangling >/dev/null; echo "fsck ok \$r"; done
          test "\$(git --git-dir="$SENT_REPO" rev-parse "$SENT_REF")" = "$SENT_COMMIT" \
            || { echo "FAIL: sentinel commit"; exit 1; }
          echo "sentinel commit ok"
          if [ -n "$LFS_PATH" ]; then
            test -f "$LFS_PATH" || { echo "FAIL: sentinel LFS object missing"; exit 1; }
            test "\$(wc -c < "$LFS_PATH")" -eq "$LFS_SIZE" || { echo "FAIL: sentinel LFS size"; exit 1; }
            test "\$(sha256sum "$LFS_PATH" | cut -c1-64)" = "$LFS_OID" || { echo "FAIL: sentinel LFS hash"; exit 1; }
            echo "sentinel lfs ok"
          fi
          find git -path '*/lfs/objects/*' -type f > /tmp/lfs; n=0
          while read -r f; do
            test "\$(sha256sum "\$f" | cut -c1-64)" = "\$(basename "\$f")" || { echo "FAIL: \$f"; exit 1; }
            n=\$((n + 1))
          done < /tmp/lfs
          echo "lfs objects present and intact: \$n"
      env: [{ name: HOME, value: /tmp }]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: { drop: ["ALL"] }
      volumeMounts:
        - { name: data, mountPath: /data, readOnly: true }
        - { name: tmp, mountPath: /tmp }
  volumes:
    - name: data
      persistentVolumeClaim: { claimName: $CLAIM, readOnly: true }
    - name: tmp
      emptyDir: {}
YAML
kubectl -n "$PREFIX-cp" get pvc "$CLAIM"                 # Bound, to $NAME
kubectl -n "$PREFIX-cp" wait --for=jsonpath='{.status.phase}'=Succeeded pod/af-cp-data-rehearsal --timeout=600s
kubectl -n "$PREFIX-cp" logs af-cp-data-rehearsal       # "fsck ok" per repo, "sentinel commit ok", "sentinel lfs ok"
kubectl -n "$PREFIX-cp" get pod af-cp-data-rehearsal -o wide   # its node is in $ZONE
```

次のすべてを満たしたときだけ合格です。

- claim が結び付いた
- Pod がディスクのゾーンで、uid 1000 として動き、`Succeeded` で終わった
- どのリポジトリも `fsck` を通った
- 目印のコミットが控えたものと同じ
- LFS を使うなら、目印のオブジェクトがそのパスにあり、控えた大きさとハッシュと一致し、ほかのオブジェクトもそれぞれ名前と一致した

最初の 1 回は、確かめが失敗しうることも確かめます。Pod を消して適用し直すことを、`SENT_COMMIT` を 1 文字変えて、`LFS_OID` を 1 文字変えて（オブジェクトがないことになる）、`LFS_SIZE` を違う値にして、それぞれ行います。どれも `FAIL:` の行を出して `Failed` で終わるはずです。

終わったらすべて消します。ボリュームは `Retain` なので、消すまでディスクが残ります。

```bash
kubectl -n "$PREFIX-cp" delete pod af-cp-data-rehearsal
kubectl -n "$PREFIX-cp" delete pvc "$CLAIM"
kubectl delete pv "$NAME"
gcloud compute disks delete "$NAME" --zone "$ZONE" --project "$PROJECT"
```

## 復元にかかる時間を記録する

予行の日付と、それぞれの復元にかかった時間を記録します。それが復旧にかかる時間です。

ほとんど空のデータでの一例です。目安にすぎず、実際のデータではどれも長くなります。

| 作業 | かかった時間 |
|---|---|
| データベースの複製 | 約 10 分 |
| ディスクのスナップショット | 61 秒 |
| スナップショットからのディスク | 26 秒 |
| 確かめの Pod | 48 秒 |

## 関連

- [CP をアップグレードする](upgrade.ja.md)：上げる前のバックアップと、戻す手順
- [バックアップして復元する](../run/backup-restore.ja.md)：ほかの形態
- [撤去する](teardown.ja.md)：撤去のあとに残るスナップショット
