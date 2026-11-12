---
audience: "GKE 以外の Kubernetes クラスターに Agent Fleet を建てる人（クラスターの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェスト（食い違ったらそちらが正）"
---

# ほかのクラスターに構築する

[English](other-clusters.md) | 日本語

GKE 以外のクラスターに、汎用のオーバーレイ `deploy/kubernetes/overlays/generic` から Agent Fleet を建てます。
このオーバーレイは、プロバイダ固有のものを何も足さない base です。GKE で Terraform がやることは、すべて自分で用意します。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 前提条件 P1〜P14 を自分で満たし、範囲が P15 を満たすことを確かめる | クラスターがワークスペースを隔離できる状態になる。ほかに確かめるものはない |
| `overlays/generic` をコピーして書き換え、適用する | 名前空間、CP、RBAC、NetworkPolicy ができる |
| 自分の ingress を Service `af-cp` の前に置く | Console に外から届く |
| `af-cp-data` のバックアップと、CP の名前空間の受信ポリシーを足す | GKE の構成と同じ守りになる |

前提条件の一覧は [Kubernetes 構成の全体像](overview.ja.md#前提条件) にあります。

## 手順

`overlays/generic` をコピーし、次をします。

1. コピーの `deployment.yaml` をクラスターに合わせます
   - 名前空間（この配備だけの接頭辞つき）
   - P3 を満たす StorageClass
   - Pod・Service・ノード・コントロールプレーンの範囲が P15 を満たすことも確かめます
2. ストアの DSN を `af-cp-secrets` に `AF_DATABASE_URL` として入れます
3. 必要なら `cp.env` を書き換えます

| 必要なとき | 設定 |
|---|---|
| ワークスペースを特定のノードで動かす | `cp.env` の `AF_K8S_NODE_SELECTOR` |
| イメージにプルシークレットが要る | ワークスペースの名前空間にシークレットを作り、`AF_K8S_IMAGE_PULL_SECRET` を設定する |

4. クラスターの ingress を Service `af-cp`（ポート 8099）の前に置きます
   - WebSocket を通し、アイドルのタイムアウトを長くします
   - `AF_TRUSTED_PROXY_HOPS` を、`X-Forwarded-For` に足すプロキシの数にします。数えるのは、そのプロキシを通らないと CP に届かないものだけです（理由は [GKE に構築する](gke.ja.md#7-ロードバランサを知る)）
5. claim `af-cp-data` にバックアップを用意します。GKE での形は [バックアップ・復元する](backup.ja.md#残すべきもの) にあります
6. CP の名前空間に、`components/gke/cp-networkpolicy.yaml` のような、自分の ingress だけをポート 8099 に通すポリシーを足します。ほかのワークロードも動くクラスターでは、そのポートに届くものは誰にでもなりすませます
7. P1〜P14 を自分で満たします。ほかに確かめるものはありません

マニフェストを変えるときに守ることは [Kubernetes 構成の全体像](overview.ja.md#マニフェストを変えるときに守ること) にあります。

## 関連

- [GKE に構築する](gke.ja.md)：Secret の作り方、確かめるコマンドは同じです
- [ノードを入れ替える・止まったノードに対処する](nodes.ja.md)
