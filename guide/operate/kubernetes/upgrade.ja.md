---
audience: "Kubernetes の配備の Control Plane を新しいバージョンに上げる人（クラスターとクラウドの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと CP のホームの配置のコード（食い違ったらそちらが正）"
---

# CP をアップグレードする

[English](upgrade.md) | 日本語

Kubernetes の配備で、データベースと CP のディスクのバックアップを取ってから CP を新しいバージョンに上げます。うまくいかなかったときに、両方のバックアップと前のイメージで戻す方法もここにあります。
コマンドは [GKE に構築する](gke.ja.md#シェル変数) のシェル変数と、`kubectl` がこのクラスターを指していることを前提にしています。

## 全体像

| あなたがやること | 起きること |
|---|---|
| CP を止め、Pod が消えるのを待つ | データベースに書き込むものがなくなる |
| データベースのバックアップと CP のディスクのスナップショットを取る | 戻せる状態になる |
| オーバーレイのタグを変えて適用する | CP が起動時にデータベースを移行する。**ダウングレードはできない** |
| ワークスペースはメンバーに任せる | 動いているワークスペースは起動したときのイメージのまま。次の起動で新しいイメージになる |

- Deployment の戦略は `Recreate` なので、2 つの CP が 1 つのデータベースに対して動くことはありません
- 一般的な注意（リリースノート、バックアップ）は [アップグレードする](../run/upgrade.ja.md) にあります

## 上げる

1. CP を止め、**Pod が消えるまで待ちます**（スケールは非同期です）
   ```bash
   kubectl -n "$PREFIX-cp" scale deployment/af-cp --replicas=0
   kubectl -n "$PREFIX-cp" wait --for=delete pod -l app.kubernetes.io/name=af-cp --timeout=300s
   ```
2. 両方のバックアップを取り、それぞれ終わるのを待ちます。`DISK` と `ZONE` は [CP のディスクを探す](backup.ja.md#cp-のディスクを探す) の方法で取得します
   ```bash
   gcloud sql backups create --instance "$PREFIX-pg" --project "$PROJECT" --description "before <version>"
   gcloud compute snapshots create "$PREFIX-cp-data-before-<version>" --project "$PROJECT" \
     --source-disk "$DISK" --source-disk-zone "$ZONE"
   ```
3. オーバーレイの `images:` に新しいタグを設定して適用します。`kubectl apply` がレプリカを 1 に戻します
   ```bash
   kubectl apply -k "$OVERLAY"
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   ```

ワークスペースのイメージは、`deployment.yaml` の `workspaceImage` に従います。

- 動いているワークスペースは、起動したときのイメージのままです
- 次の起動で、そのタグの今のダイジェストに固定されます
- 古いイメージのままのワークスペースには、Console が古いことを示します

## 戻す

戻すとは、**上げるときの手順 2 の両方のバックアップと、前のイメージを一緒に戻すこと**です。移行済みのデータベースで前のイメージを動かしても、ロールバックにはなりません。

1. CP を止め、Pod が消えるのを待ちます（上げるときの手順 1 と同じ）。Pod が消える前に復元を始めないでください。CP が動いていると、置き換え中のデータベースに書き込んでしまいます
2. データベースを戻します
   ```bash
   gcloud sql backups list --instance "$PREFIX-pg" --project "$PROJECT"
   gcloud sql backups restore <backup-id> --restore-instance "$PREFIX-pg" --project "$PROJECT"
   ```
3. ディスクを戻すのは、スナップショットのあとに内部の git プロバイダーを使ったときだけです。今のディスクは残したまま、スナップショットから新しいディスクを作り、それから claim をそちらに向けます。新しいディスクを作れなかった場合は、まだ何も変わっていません。使う手順は [スナップショットからディスクとボリュームを作る](backup.ja.md#スナップショットからディスクとボリュームを作る) にあります。内部の git プロバイダーを使っていなければ、この手順は飛ばします。claim `af-cp-data` は今のディスクにバインドされたままです。それで何が変わるかは手順 4 と 5 にあります
   ```bash
   OLD_PV="$PV" OLD_DISK="$DISK"                      # 「CP のディスクを探す」から
   NAME="$PREFIX-cp-data-restore-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data SNAP="$PREFIX-cp-data-before-<version>"
   # ……「スナップショットからディスクとボリュームを作る」のディスクの部分……
   kubectl patch pv "$OLD_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'   # 古いディスクを残す
   kubectl -n "$PREFIX-cp" delete pvc af-cp-data
   # ……「スナップショットからディスクとボリュームを作る」のボリュームの部分……
   ```
   手順 4 の前にやめるときは、新しいボリュームを消し、古いボリュームにバインドされた claim を作り直します（`kubectl patch pv "$OLD_PV" -p '{"spec":{"claimRef":null}}'` のあと、`volumeName: $OLD_PV` の claim `af-cp-data`）
4. オーバーレイに前のタグを設定し、`kubectl apply -k` します。手順 3 を実行したなら、claim が作り直されて戻したボリュームにバインドされます。飛ばしたなら claim を消していないので、元のボリュームのままです。これを確かめ、あわせて CP がディスクのゾーンで動いていることも確かめます
   ```bash
   kubectl -n "$PREFIX-cp" get pvc af-cp-data          # Bound。VOLUME は手順 3 を実行したなら $NAME、飛ばしたなら元のボリューム
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   kubectl -n "$PREFIX-cp" get pods -o wide -l app.kubernetes.io/name=af-cp   # $ZONE のノード
   ```
5. **手順 3 を実行したときだけです。** 飛ばしたなら、ディスクもスナップショットの予定も元のままなので、することはありません。実行したなら、戻した配備が動くことを確かめて（サインインし、リポジトリをクローンする）から、ディスクを整理します。古いディスクを消し、戻したボリュームを StorageClass の `Delete` に戻し（破棄や撤去でほかと同じく消えるように）、スナップショットの予定を新しいディスクに移します
   ```bash
   echo "old: $OLD_PV $OLD_DISK  new: $NAME"          # 3 つとも、このシェルの手順 3 で設定したもの。空なら止める
   kubectl delete pv "$OLD_PV"
   gcloud compute disks delete "$OLD_DISK" --zone "$ZONE" --project "$PROJECT"
   kubectl patch pv "$NAME" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
   # 「CP のディスクを探す」のブロックで PV・HANDLE・ZONE・DISK を読み直してから:
   gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
     --resource-policies "$PREFIX-cp-data"
   ```

## ホームディレクトリの配置より前のバージョンに戻す

今のバージョンでは、ワークスペースのホームディレクトリはホームディレクトリ用の claim の中のディレクトリ `.af-home` です。この配置での最初の起動が以前のホームディレクトリをそこへ移し、そのことを状態の claim に記録します。

**それより前の CP はこの配置を知りません。この配置で起動したことのあるワークスペースがあるなら、そのバージョンまで戻すのは安全ではありません。**

| 前の CP でやること | 起きること |
|---|---|
| ワークスペースを起動する | Pod が claim のルートをホームディレクトリとしてマウントする。メンバーにはほとんど空のホームディレクトリが見え、ファイルは `.af-home` に隠れる |
| 「ホームを掃除する」・管理者の「home を掃除」 | `.af-home` ごと消す。移したホームディレクトリも、ログインも消える |
| 「作り直す」 | ルートの `repos` だけを消す。移した `~/repos` は隠れたまま残る |

CP をそのバージョンより前に戻すときは、次のようにします。

1. 先にすべてのワークスペースを止めます。ホームディレクトリを戻すまで、前の CP でどれも起動・作り直し・掃除しないでください
2. ワークスペースごとに 1 つずつ戻します。ホームディレクトリのディスクのスナップショットを取り、ワークスペースを止めたまま、ワークスペースのイメージから Pod を動かします。ワークスペースの Pod と同じセキュリティコンテキスト（`dev` として）で、ホームディレクトリ用の claim を `/v`、状態の claim を `/s` にマウントし、その中で次をスクリプトとして（`sh -s`）実行します
   ```bash
   V=/v R=/s/wipe/layout
   set -eu
   cd "$V"
   # 最後の移動のあとで止まっていた: 記録を消すだけ
   if [ "$(cat -- "$R" 2>/dev/null)" = reverting ] && [ ! -e .af-home ] && [ ! -L .af-home ]; then rm -f -- "$R"; exit 0; fi
   [ -d .af-home ] && [ ! -L .af-home ] || { echo ".af-home is missing or not a directory"; exit 1; }
   [ -f "$R" ] && [ ! -L "$R" ] || { echo "no layout record at $R"; exit 1; }
   st=$(cat -- "$R")
   case "$st" in
     done)
       for e in .[!.]* ..?* *; do
         [ -e "$e" ] || [ -L "$e" ] || continue
         case "$e" in .af-home|lost+found) ;; *) echo "the root holds $e besides the home: see step 3"; exit 1;; esac
       done ;;
     reverting) ;;  # 途中で止まった実行: そのまま続ける
     *) echo "the layout record is not done or reverting"; exit 1 ;;
   esac
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}
     case "$n" in .af-home|lost+found) echo "the home holds $n, a name the root keeps"; exit 1;; esac
     if [ -e "$n" ] || [ -L "$n" ]; then echo "$n is both in the home and on the root"; exit 1; fi
   done
   # 確認がすべて通ってから、移動を記録する。ファイルは丸ごと置き換え、途中で切らない
   if [ "$st" = done ]; then
     if [ -e "$R.tmp" ] || [ -L "$R.tmp" ]; then echo "$R.tmp is in the way"; exit 1; fi
     { echo reverting > "$R.tmp" && mv -fT -- "$R.tmp" "$R"; } || { echo "cannot mark $R"; exit 1; }
   fi
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}; ro=
     # ディレクトリを移すと ".." が書き換わるので、そのディレクトリへの書き込み権限が要る
     if [ -d "$e" ] && [ ! -L "$e" ] && [ ! -w "$e" ]; then chmod u+w -- "$e"; ro=1; fi
     mv -T -- "$e" "$n"
     [ -z "$ro" ] || chmod u-w -- "$n"
   done
   rmdir .af-home
   rm -f -- "$R"
   ```
   - スクリプトは、記録や移動をする前にすべてを確かめます。既存の名前を上書きする移動はしません
   - 途中で止まると、記録は `reverting` になります。表示されたことを直してもう一度実行すると、続きから進みます
   - 終わるまで、そのワークスペースはどちらの CP でも起動しないでください。今のバージョンは `reverting` の記録を拒み、前のバージョンは半分移したルートをマウントします
   - 移す途中で止まった読み取り専用のディレクトリは、書き込める状態で残ることがあります
   - 記録が消えたあと、今のバージョンでの次の起動がホームディレクトリをもう一度移します
3. それでも前の CP でワークスペースが動いてしまったら、今のバージョンはそれを起動し直すのを拒みます。`kubectl logs <pod> -c home-layout` に、claim のルートにホームディレクトリ以外のものがあると出ます
   - ルートには前の CP のセッションが書いたもの、`.af-home` には移したホームディレクトリがあります
   - ワークスペースを止めてディスクのスナップショットを取り、同じような Pod の中で、項目ごとにどちらのコピーを残すか決めます。ルートに `.af-home` と `lost+found` だけが残るようにします。または手順 2 のとおりにホームディレクトリを戻します

## 関連

- [アップグレードする](../run/upgrade.ja.md)
- [バックアップ・復元する](backup.ja.md)
- [Kubernetes 構成の全体像](overview.ja.md)
