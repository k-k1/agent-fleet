---
audience: "Kubernetes の配備の Control Plane を新しい版に上げる人（クラスターとクラウドの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと CP のホームの配置のコード（食い違ったらそちらが正）"
---

# CP をアップグレードする

[English](upgrade.md) | 日本語

Kubernetes の配備で、データベースと CP のディスクのバックアップを取ってから CP を新しい版に上げます。うまくいかなかったときに、両方のバックアップと前のイメージで戻す方法もここにあります。
コマンドは [GKE に構築する](gke.ja.md#シェル変数) のシェル変数と、`kubectl` がこのクラスターを指していることを前提にしています。

## 全体像

| あなたがやること | 起きること |
|---|---|
| CP を止め、Pod が消えるのを待つ | データベースに書くものがなくなる |
| データベースのバックアップと CP のディスクのスナップショットを取る | 戻る道ができる |
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
2. 両方のバックアップを取り、それぞれ終わるのを待ちます。`DISK` と `ZONE` は [CP のディスクを探す](backup.ja.md#cp-のディスクを探す) で読みます
   ```bash
   gcloud sql backups create --instance "$PREFIX-pg" --project "$PROJECT" --description "before <version>"
   gcloud compute snapshots create "$PREFIX-cp-data-before-<version>" --project "$PROJECT" \
     --source-disk "$DISK" --source-disk-zone "$ZONE"
   ```
3. オーバーレイの `images:` に新しいタグを設定して適用します。`kubectl apply` がレプリカを 1 に戻します
   ```bash
   kubectl apply -k "deploy/kubernetes/overlays/$PREFIX"
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   ```

ワークスペースのイメージは、`deployment.yaml` の `workspaceImage` に従います。

- 動いているワークスペースは、起動したときのイメージのままです
- 次の起動で、そのタグの今のダイジェストに固定されます
- 古いイメージのままのワークスペースには、Console が古いことを示します

## 戻す

戻すとは、**上げるときの手順 2 の両方のバックアップと、前のイメージを一緒に戻すこと**です。移行したデータベースに前のイメージを載せても、戻したことにはなりません。

1. CP を止め、Pod が消えるのを待ちます（上げるときの手順 1 と同じ）。その前に復元を始めないでください。動いている CP は、置き換えるデータベースに書き込みます
2. データベースを戻します
   ```bash
   gcloud sql backups list --instance "$PREFIX-pg" --project "$PROJECT"
   gcloud sql backups restore <backup-id> --restore-instance "$PREFIX-pg" --project "$PROJECT"
   ```
3. ディスクは、スナップショットのあとに内部の git プロバイダを使ったときだけ戻します。今のディスクは残し、スナップショットから新しいディスクを作り、それから claim をそちらに向けます。新しいディスクを作れなければ、まだ何も変わっていません。手順の部品は [スナップショットからディスクとボリュームを作る](backup.ja.md#スナップショットからディスクとボリュームを作る) にあります
   ```bash
   OLD_PV="$PV" OLD_DISK="$DISK"                      # from "Find the CP's disk"
   NAME="$PREFIX-cp-data-restore-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data SNAP="$PREFIX-cp-data-before-<version>"
   # ... the disk part of "Make a disk and a volume from a snapshot" ...
   kubectl patch pv "$OLD_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'   # keep the old disk
   kubectl -n "$PREFIX-cp" delete pvc af-cp-data
   # ... the volume part of "Make a disk and a volume from a snapshot" ...
   ```
   手順 4 の前にやめるときは、新しいボリュームを消し、古いボリュームに結び付いた claim を作り直します（`kubectl patch pv "$OLD_PV" -p '{"spec":{"claimRef":null}}'` のあと、`volumeName: $OLD_PV` の claim `af-cp-data`）
4. オーバーレイに前のタグを設定し、`kubectl apply -k` します。claim が作り直されて、戻したボリュームに結び付きます。それと、CP がディスクのゾーンで動いていることを確かめます
   ```bash
   kubectl -n "$PREFIX-cp" get pvc af-cp-data          # Bound, VOLUME = $NAME
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   kubectl -n "$PREFIX-cp" get pods -o wide -l app.kubernetes.io/name=af-cp   # a node in $ZONE
   ```
5. 戻した配備を確かめたら（サインインし、リポジトリをクローンする）、ディスクを片付けます。古いディスクを消し、戻したボリュームを StorageClass の `Delete` に戻し（破棄や撤去でほかと同じく消えるように）、スナップショットの予定を新しいディスクに移します
   ```bash
   kubectl delete pv "$OLD_PV"
   gcloud compute disks delete "$OLD_DISK" --zone "$ZONE" --project "$PROJECT"
   kubectl patch pv "$NAME" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
   # re-read PV, HANDLE, ZONE and DISK with the block of "Find the CP's disk", then:
   gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
     --resource-policies "$PREFIX-cp-data"
   ```

## ホームの配置より前の版に戻す

今の版では、ワークスペースのホームはホームの claim の中のディレクトリ `.af-home` です。この配置での最初の起動が以前のホームをそこへ移し、そのことを状態の claim に記録します。

**それより前の CP はこのことを知らないので、その後に起動したワークスペースについて、そこまで戻すのは安全ではありません。**

| 前の CP でやること | 起きること |
|---|---|
| ワークスペースを起動する | Pod が claim のルートをホームとしてマウントする。メンバーにはほとんど空のホームが見え、ファイルは `.af-home` に隠れる |
| 「ホームを掃除する」・管理者の「home を掃除」 | `.af-home` ごと消す。移したホームも、ログインも消える |
| 「作り直す」 | ルートの `repos` だけを消す。移した `~/repos` は隠れたまま残る |

CP をその版より前に戻すときは、次のようにします。

1. 先にすべてのワークスペースを止めます。ホームを戻すまで、前の CP でどれも起動・作り直し・掃除しないでください
2. 1 つずつ戻します。ホームのディスクのスナップショットを取り、ワークスペースを止めたまま、ワークスペースのイメージから Pod を動かします。ワークスペースの Pod と同じセキュリティコンテキスト（`dev` として）で、ホームの claim を `/v`、状態の claim を `/s` にマウントし、その中で次をスクリプトとして（`sh -s`）実行します
   ```bash
   V=/v R=/s/wipe/layout
   set -eu
   cd "$V"
   # Stopped after the last move: only the record is left to remove.
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
     reverting) ;;  # a run stopped halfway: carry on
     *) echo "the layout record is not done or reverting"; exit 1 ;;
   esac
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}
     case "$n" in .af-home|lost+found) echo "the home holds $n, a name the root keeps"; exit 1;; esac
     if [ -e "$n" ] || [ -L "$n" ]; then echo "$n is both in the home and on the root"; exit 1; fi
   done
   # Only now, with every check passed, mark the move; replaced whole, never truncated.
   if [ "$st" = done ]; then
     if [ -e "$R.tmp" ] || [ -L "$R.tmp" ]; then echo "$R.tmp is in the way"; exit 1; fi
     { echo reverting > "$R.tmp" && mv -fT -- "$R.tmp" "$R"; } || { echo "cannot mark $R"; exit 1; }
   fi
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}; ro=
     # Moving a directory rewrites its "..", which needs write permission on it.
     if [ -d "$e" ] && [ ! -L "$e" ] && [ ! -w "$e" ]; then chmod u+w -- "$e"; ro=1; fi
     mv -T -- "$e" "$n"
     [ -z "$ro" ] || chmod u-w -- "$n"
   done
   rmdir .af-home
   rm -f -- "$R"
   ```
   - 何かを記録したり移したりする前に、すべてを確かめます。既存の名前の上には何も移しません
   - 途中で止まると、記録は `reverting` になります。表示されたことを直してもう一度実行すると、続きから進みます
   - 終わるまで、そのワークスペースはどちらの CP でも起動しないでください。今の版は `reverting` の記録を拒み、前の版は半分移したルートをマウントします
   - 移す途中で止まった読み取り専用のディレクトリは、書き込める状態で残ることがあります
   - 記録が消えたあと、今の版での次の起動がホームをもう一度移します
3. それでも前の CP でワークスペースが動いてしまったら、今の版はそれを起動し直すのを拒みます。`kubectl logs <pod> -c home-layout` に、claim のルートにホーム以外のものがあると出ます
   - ルートには前の CP のセッションが書いたもの、`.af-home` には移したホームがあります
   - ワークスペースを止めてディスクのスナップショットを取り、同じような Pod の中で、それぞれどちらの写しを残すか決めます。ルートに `.af-home` と `lost+found` だけが残るようにします。または手順 2 のとおりにホームを戻します

## 関連

- [アップグレードする](../run/upgrade.ja.md)
- [バックアップ・復元する](backup.ja.md)
- [Kubernetes 構成の全体像](overview.ja.md)
