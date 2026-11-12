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
| `overlays/generic` をコピーして書き換えて適用し、`AF_DATABASE_URL` を入れた Secret を作る | 名前空間、CP、RBAC、NetworkPolicy ができ、CP が起動する |
| 自分の ingress を Service `af-cp` の前に置く | Console に外から届く |
| `af-cp-data` のバックアップと、CP の名前空間の受信ポリシーを足す | GKE の構成と同じ守りになる |

前提条件の一覧は [Kubernetes 構成の全体像](overview.ja.md#前提条件) にあります。

## 手順

シェル変数を設定し、オーバーレイをコピーします。このクローンの外に置くなら、[クローンの外に置く](gke.ja.md#クローンの外に置く) のとおりにパスを直します。

```bash
OVERLAY=<your overlay>     # where your copy of overlays/generic lives
CP_NS=<cpNamespace>        # deployment.yaml's cpNamespace
WS_NS=<wsNamespace>        # deployment.yaml's wsNamespace
cp -r deploy/kubernetes/overlays/generic "$OVERLAY"
```

1. コピーの `deployment.yaml` をクラスターに合わせます
   - 名前空間 `cpNamespace` と `wsNamespace`（この配備だけの接頭辞つき）
   - P3 を満たす StorageClass
   - `workspaceImage` と、`kustomization.yaml` の `images:` の CP のタグ
   - Pod・Service・ノード・コントロールプレーンの範囲が P15 を満たすことも確かめます
2. `cp.env` を書き換えます。`PUBLIC_BASE_URL`、`PUBLIC_DOMAIN`、サインインの設定、`SUPER_ADMIN_EMAILS` と、必要なら次です

| 必要なとき | 設定 |
|---|---|
| ワークスペースを特定のノードで動かす | `cp.env` の `AF_K8S_NODE_SELECTOR` |
| イメージにプルシークレットが要る | `cp.env` の `AF_K8S_IMAGE_PULL_SECRET`。シークレット自体は手順 4 で作る |

3. オーバーレイを適用します。2 つの名前空間、CP、RBAC、NetworkPolicy ができます。CP の Pod は手順 4 まで `CreateContainerConfigError` で待ちます
   ```bash
   kubectl config current-context                # the cluster you mean
   kubectl apply -k "$OVERLAY"
   ```
4. CP の名前空間に CP の Secret を作ります。鍵に加えて、ストアの DSN を `AF_DATABASE_URL` として入れ、ロールアウトを待ちます。使うサインインの秘密（`GOOGLE_OAUTH_CLIENT_SECRET`、`AF_OIDC_<ID>_CLIENT_SECRET`、`GITHUB_OAUTH_CLIENT_SECRET`）も同じ Secret に入れます。鍵の要件は [配備の鍵を扱う](../secure/deployment-keys.ja.md) にあります。イメージにプルシークレットが要るなら、ここで `$WS_NS` に、`AF_K8S_IMAGE_PULL_SECRET` に書いた名前で作ります
   ```bash
   kubectl -n "$CP_NS" create secret generic af-cp-secrets \
     --from-literal=AF_MASTER_KEY="$(cat master-key)" \
     --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
     --from-literal=AF_DATABASE_URL="$(cat database-url)" \
     --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
   kubectl -n "$CP_NS" rollout status deployment/af-cp
   ```
5. クラスターの ingress を Service `af-cp`（ポート 8099）の前に置きます
   - WebSocket を通し、アイドルのタイムアウトを長くします
   - `AF_TRUSTED_PROXY_HOPS` を、`X-Forwarded-For` に足すプロキシの数にします。数えるのは、そのプロキシを通らないと CP に届かないものだけです（理由は [GKE に構築する](gke.ja.md#7-ロードバランサを知る)）。変えたらオーバーレイを適用し直します
6. claim `af-cp-data` にバックアップを用意します。GKE での形は [バックアップ・復元する](backup.ja.md#残すべきもの) にあります
7. CP の名前空間に、`components/gke/cp-networkpolicy.yaml` のような、自分の ingress だけをポート 8099 に通すポリシーを足します。ほかのワークロードも動くクラスターでは、そのポートに届くものは誰にでもなりすませます
8. P1〜P14 を自分で満たします。ほかに確かめるものはありません

マニフェストを変えるときに守ることは [Kubernetes 構成の全体像](overview.ja.md#マニフェストを変えるときに守ること) にあります。

## 関連

- [GKE に構築する](gke.ja.md)：手順 8 の確かめかたはここでも使えます
- [ノードを入れ替える・止まったノードに対処する](nodes.ja.md)
