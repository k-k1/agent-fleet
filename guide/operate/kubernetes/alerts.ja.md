---
audience: "Kubernetes（GKE）の配備を見張る人（Google Cloud の監視を設定できる SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェストと CP の監査ログ（食い違ったらそちらが正）"
---

# アラートを設定する

[English](alerts.md) | 日本語

GKE の配備で、CP が応答しない、ワークスペースが起動できない、ノードのディスクが足りないといった問題に、メンバーより先に気づけるようにします。
ログは Cloud Logging に送られます。アラートは Cloud Monitoring に設定します。

## 全体像

| あなたがやること | 起きること |
|---|---|
| Cloud Monitoring に下の表のアラートを作る | 問題が起きると通知が届く |
| メンバーを外したあと、監査ログを SQL で見る | 破棄で消しきれなかったものがわかる。これだけは Cloud Logging にない |

## 設定するアラート

| アラート | 方法 |
|---|---|
| CP の準備ができていない（データベースも含む） | `https://<fqdn>/readyz` の稼働時間チェック |
| 証明書の期限が近い | 同じ稼働時間チェックで、SSL 証明書の検証と有効期限のアラートをオンにする |
| ワークスペースが `Pending` のまま | ワークスペースの名前空間で起きる `FailedScheduling` イベントからログベースの指標を作り、10 分続いたら通知する。起動のダイアログにも同じ理由が出る |
| クォータが起動を拒んだ | ワークスペースの名前空間で、メッセージに `exceeded quota` を含むイベントから作るログベースの指標 |
| ワークスペースのノードのディスクが足りない | ワークスペース用プールのノードについて、ノード状態 `DiskPressure`、または `kubernetes.io/node/ephemeral_storage/allocatable_bytes` と使用量の比較 |

## 破棄が残したものを見る

破棄は、削除を確認できなかったものを監査ログに書きます。監査ログはデータベースにあり、Cloud Logging にはありません。メンバーを外したあとに、次のクエリで確かめます。

```sql
SELECT at, target, detail FROM audit_log WHERE action = 'workspace.destroy' AND detail LIKE '%NOT deleted:%'
```

見つかったものの片付けかたは [ノードを入れ替える・止まったノードに対処する](nodes.ja.md#破棄が残したものを片付ける) にあります。

## 関連

- [ノードを入れ替える・止まったノードに対処する](nodes.ja.md)：ノードのディスクが足りなくなる理由
- [Kubernetes 構成の全体像](overview.ja.md#ディスクの割り当てgke)：GKE のディスクの割り当て
