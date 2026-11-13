---
audience: "Kubernetes の配備のノードを保守する人（クラスターとクラウドの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと CP の kubernetes アダプタ（食い違ったらそちらが正）"
---

# ノードを入れ替える・止まったノードに対処する

[English](nodes.md) | 日本語

メンバーのセッションを不意に切らずにノードを入れ替えます。応答しなくなったノードからワークスペースを安全に取り戻す方法と、破棄が残したものを片付ける方法もここにあります。
コマンドは [GKE に構築する](gke.ja.md#シェル変数) のシェル変数と、`kubectl` がこのクラスターを指していることを前提にしています。

## 全体像

| 起きたこと・やること | 起きること |
|---|---|
| ノードを計画どおり入れ替える | cordon し、CP からワークスペースを止め、drain する。メンバーは次に使うときに起動し直す |
| 自動アップグレード・自動修復・プリエンプションが Pod を消す | 動いていたワークスペースは切れ、ほかのノードで自動的に戻る。CP の操作はいらない |
| ノードが応答しなくなる | CP は Pod を置き換えない。VM が止まった証拠を Google Cloud で取ってから、運用する人が Pod を解放する |
| 破棄が「NOT deleted」を監査ログに書く | 残ったものを確かめて手で消す |

CP はノードに対する権限を持っておらず、どの手順でもその権限は要りません。

## ノードを計画どおり入れ替える

drain は停止ではありません。ワークスペースの Pod を消しても `replicas: 1` のままなので、StatefulSet はすぐ別のノードで Pod を作り直します。メンバーのセッションは切れ、ワークスペースは自動的に戻り、その分の容量は課金され続けます。そのため、次の順で進めます。

1. ノードを **cordon** して、新しい Pod が置かれないようにします
   ```bash
   kubectl cordon <node>
   ```
2. **CP からワークスペースを止めます。** 起動中のものも対象です。まず一覧を出します
   ```bash
   kubectl -n "$PREFIX-ws" get pods -l agent-fleet.io/workspace --field-selector spec.nodeName=<node> \
     -o custom-columns=POD:.metadata.name,WORKSPACE:.metadata.labels.agent-fleet\\.io/workspace
   ```
   メンバーごとの詳細で「**ワークスペースを強制停止**」を使います（[ワークスペースを強制停止する](../../admin/resources/idle-stop.ja.md#ワークスペースを強制停止する)）。一覧が空になるまで待ちます。CP の停止操作は、Pod が消えてから完了します
3. **drain** します
   ```bash
   kubectl drain <node> --ignore-daemonsets --delete-emptydir-data
   ```
4. ノードをアップグレードするか置き換え、戻ってきたら **uncordon** します
   ```bash
   kubectl uncordon <node>
   ```

- メンバーは、次に使うときにワークスペースを起動し直します
- ワークスペースの Pod には `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` が付いているので、使用中のセッションが載っているノードをオートスケーラーが外すことはありません。プールはワークスペースが止まるにつれて縮みます

### GKE の自動アップグレードを止める

GKE は、メンテナンスの時間帯（`maintenance_window`）の中で自分でノードをアップグレードします。アップグレードを上の手順だけで行いたいときは、制御したい期間に、範囲が「マイナーとノードのアップグレードなし」のメンテナンスの除外を追加し、ノードのアップグレードを自分で実行します。

```bash
gcloud container clusters update "$PREFIX-gke" --location "$LOCATION" --project "$PROJECT" \
  --add-maintenance-exclusion-name hold-nodes \
  --add-maintenance-exclusion-start <start> --add-maintenance-exclusion-end <end> \
  --add-maintenance-exclusion-scope no_minor_or_node_upgrades
```

## 計画外の drain

自分の管理外の自動アップグレード、ノードの自動修復、プリエンプションは、ノードの Pod を消します。

- 動いていたワークスペースは切れ、ほかのノードで自動的に戻ります（「起動中」を経て「実行中」へ。CP の操作はいりません）
- メンバーからはセッションが終わったように見え、メンバーはつなぎ直します
- ワークスペースが「起動中」のままにならない限り、何もしなくてかまいません。そうなったら、その Pod のイベントを見ます

## 応答しなくなったノード

CP は、応答しなくなったノードの Pod を置き換えません。「1 つのホームにエージェントは多くても 1 つ」を守るための代償です。

- ワークスペースは「起動中」のままになるか、「停止」になっても Pod が残り、その Pod が起動・ホームの掃除・破棄を妨げます
- 停止・作り直し・ホームの掃除はエラーを返します
- 回復は運用する人の仕事です。**最初に、古いプロセスがもう動けないという証拠を取ります**

1. ノードに対応する VM を探し、**Kubernetes ではなく Google Cloud で**、止まっているかなくなっていることを確かめます
   ```bash
   gcloud compute instances describe <node> --zone <zone> --project "$PROJECT" --format='value(status)'
   # TERMINATED, or "not found", is proof. RUNNING, or no answer, is not.
   ```
   VM が動いているのに届かない（ネットワークの分断）、または状態を読めないときは、**何もしないでください**。エージェントがまだホームに書いているかもしれません
2. そのあとで、Node オブジェクトを消す（`kubectl delete node <node>`）か、ボリュームがデタッチされるようにサービス対象外（out-of-service）の taint を付けます
   ```bash
   kubectl taint nodes <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute
   ```
   Node オブジェクトを消しても VM は止まりませんが、taint はボリュームをデタッチします。だから手順 1 が先です
3. StatefulSet が Pod をほかの場所（ボリュームのゾーン）で作り直します。ノードがなくなるか直ったら、taint を外します

### GKE では

ノードプールで自動修復がオンなら（既定です。GKE Standard ではオフにできます）、VM には触らず、自動修復に任せます。

- GKE は、ノードが約 10 分 NotReady を報告すると修復を始めます。ノードを drain し（drain を最大 1 時間待ちます）、同じ名前で VM を作り直します（[ノードの自動修復](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/node-auto-repair)の「Repair criteria」と「Node repair process」）
- Pod は古いノードと一緒に消え、ワークスペースの停止が完了し、メンバーが次に起動すれば普通に動きます
- 急ぐときは、ノードの VM を自分で止めるか消します。止まった VM は古いエージェントを動かせません。マネージドインスタンスグループのメンバーに `instances.stop` を実行すると、グループの修復が VM を作り直します（[インスタンスグループの修復](https://docs.cloud.google.com/compute/docs/instance-groups/about-repair)の「Automatically repair a failed VM」）。そのあと GKE が古い Node オブジェクトとその Pod を消します

自動修復がオフ、または動かないときは、上の手順をそのまま使います。証拠が先で、taint はそのあとです。

GKE 1.35 でそれぞれ 1 回ずつ見たことで、保証ではありません。

| 起きたこと | 見えたこと |
|---|---|
| VM の停止 | 約 110 秒の ACPI シャットダウンで、ノードがなくなる前に Pod を終わらせた |
| ゲストの電源断 | 約 6 秒後にインスタンスグループが作り直した |
| VM を動かしたまま kubelet を止め、運用する人が VM を止めた | グループの修復が数秒のうちに続いた。taint と同じ秒に VM を消したので、どちらが Pod を解放したかはわからない |
| kubelet を止めて放っておいた | NotReady から 11 分 15 秒後にノードの自動修復が始まり、2 分 44 秒後に終わった。切断から 14 分 37 秒後にワークスペースの Pod が消えた |

## 破棄が残したものを片付ける

破棄は、消えたと確かめられなかったものを監査ログに書きます（`workspace.destroy … NOT deleted: …`）。見つけかたは [アラートを設定する](alerts.ja.md#破棄が残したものを見る) にあります。

| 残ったもの | やること |
|---|---|
| `pvc:<namespace>/<name>` | `kubectl -n <namespace> get pvc <name>`。`Terminating` のまま止まっているなら、まだ Pod が使っている（`kubectl -n <namespace> get pods -l agent-fleet.io/workspace=<workspace>`）。その Pod を待つか、そのノードに上の手順を使う |
| `pv:<name>` | `kubectl get pv <name>`。これがあるあいだは、裏のディスクもあるかもしれない。`kubectl get pv <name> -o jsonpath='{.spec.csi.volumeHandle}'` がディスクを示す。ボリュームのオブジェクトが消えたら、`gcloud compute disks list --project "$PROJECT" --filter="name~<pv name>"` でディスクも消えたか確かめる |
| `statefulset:<namespace>/<name>` | 破棄がわざと残した。消えたと確かめられなかった claim とボリュームの一覧を、アノテーションとして持っている。そこにあるものを上のとおり 1 つずつ片付け、**それから**消す: `kubectl -n <namespace> delete statefulset <name>` |

監査ログを誰も読んでいなかった場合に備えて、定期的に点検します。

- どのノードも使っていないディスク: `gcloud compute disks list --project "$PROJECT" --filter="-users:*"`
- `Released` のボリューム: `kubectl get pv | grep Released`
- ワークスペースの名前空間で、メンバーがもういない StatefulSet

## ノードのディスク

ワークスペースのノードのブートディスクが大きい（`workspace_boot_disk_gb`）のは、イメージ、コンテナのログ、各 Pod の `/tmp` とエフェメラルストレージを持つからです。

- コンテナのログには上限があります（50 MiB、3 ファイル）
- kubelet は、空きが 15% を切ると Pod の退避（eviction）を始めます
- Pod のエフェメラルストレージの上限は定期的に確かめられるだけで、書き込みのたびに強制されるわけではありません。書き込みの速い Pod は、退避される前にディスクを埋めることがあります。それに気づくための仕組みが [ディスクのアラート](alerts.ja.md#設定するアラート) です

## 関連

- [アラートを設定する](alerts.ja.md)
- [Kubernetes 構成の全体像](overview.ja.md#マニフェストを変えるときに守ること)：ワークスペースの Pod を強制削除しない理由
- [ワークスペースを強制停止する](../../admin/resources/idle-stop.ja.md#ワークスペースを強制停止する)
