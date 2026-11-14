---
audience: "GKE 以外の Kubernetes クラスターに Agent Fleet を構築する人（クラスターの管理権限を持つ SRE）"
source_of_truth: "`deploy/kubernetes/` のマニフェスト（食い違ったらそちらが正）"
---

# ほかのクラスターに構築する

[English](other-clusters.md) | 日本語

GKE 以外のクラスターに、汎用のオーバーレイ `deploy/kubernetes/overlays/generic` から Agent Fleet を建てます。
このオーバーレイは、プロバイダー固有のものを何も足さない base そのものです。GKE なら Terraform が用意するものは、すべて自分で用意します。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 前提条件 P1〜P14 を自分で満たし、範囲が P15 を満たすことを確かめる | クラスターがワークスペースを隔離できる状態になる。これらを代わりに確かめる仕組みはない |
| `overlays/generic` をコピーして書き換えて適用し、`AF_DATABASE_URL` を入れた Secret を作る | Namespace、CP、RBAC、NetworkPolicy ができ、CP が起動する |
| 自分の ingress を Service `af-cp` の前に置く | Console に外から届く |
| 最初の適用の前に CP の Namespace の受信ポリシーを足し、`af-cp-data` のバックアップを用意する | CP の最初の起動から、GKE の構成と同じ保護が効く |

前提条件の一覧は [Kubernetes 構成の全体像](overview.ja.md#前提条件) にあります。

## 手順

シェル変数を設定し、オーバーレイをコピーします。このクローンの外に置くなら、[クローンの外に置く](gke.ja.md#クローンの外に置く) のとおりにパスを直します。

```bash
OVERLAY=<your overlay>     # overlays/generic のコピーを置く場所
CP_NS=<cpNamespace>        # deployment.yaml の cpNamespace
WS_NS=<wsNamespace>        # deployment.yaml の wsNamespace
cp -r deploy/kubernetes/overlays/generic "$OVERLAY"
```

1. コピーの `deployment.yaml` をクラスターに合わせます
   - Namespace `cpNamespace` と `wsNamespace`（この配備だけの接頭辞つき）
   - P3 を満たす StorageClass
   - `workspaceImage` と、`kustomization.yaml` の `images:` の CP のタグ
   - Pod・Service・ノード・コントロールプレーンのアドレス範囲が P15 を満たすことも確かめます
2. `cp.env` を書き換えます。`PUBLIC_BASE_URL`、`PUBLIC_DOMAIN`、サインインの設定、`SUPER_ADMIN_EMAILS` と、必要なら次です

| 必要なとき | 設定 |
|---|---|
| ワークスペースを特定のノードで動かす | `cp.env` の `AF_K8S_NODE_SELECTOR` |
| イメージにプルシークレットが要る | `cp.env` の `AF_K8S_IMAGE_PULL_SECRET`。シークレット自体は手順 5 で作る |

3. **何かが起動する前に、CP への受信を絞ります。** base には CP の Namespace の受信ポリシーがないため、ポート 8099 に届く通信は、どのユーザーにでもなりすませます。`components/gke/cp-networkpolicy.yaml` をオーバーレイにコピーして書き換えます
   - 8099 の規則の Google の `ipBlock` 2 つを、自分の ingress の送信元（コントローラーの Pod の Namespace とラベル、またはアドレス）に置き換えます
   - 8098 の規則の `af-placeholder-ws` を、自分の `wsNamespace` にします（このラベルはオーバーレイが書き換えません。ポリシー自体の Namespace は書き換えます）
   ```bash
   cp deploy/kubernetes/components/gke/cp-networkpolicy.yaml "$OVERLAY/cp-networkpolicy.yaml"
   # 上のとおりに書き換え、kustomization.yaml の resources: に `- cp-networkpolicy.yaml` を足してから:
   kubectl kustomize "$OVERLAY" | grep -A3 'name: cp-ingress'   # namespace: が自分の cpNamespace になる
   ```
4. オーバーレイを適用します。2 つの Namespace、CP、RBAC と、CP の受信ポリシーを含む NetworkPolicy ができます。CP の Pod は手順 5 まで `CreateContainerConfigError` のまま待ちます
   ```bash
   kubectl config current-context                # 使うつもりのクラスター
   kubectl apply -k "$OVERLAY"
   ```
5. CP の Namespace に CP の Secret を作ります。キーに加えてストアの DSN を `AF_DATABASE_URL` として入れ、ロールアウトを待ちます。使うサインインのシークレット（`GOOGLE_OAUTH_CLIENT_SECRET`、`AF_OIDC_<ID>_CLIENT_SECRET`、`GITHUB_OAUTH_CLIENT_SECRET`）も同じ Secret に入れます。キーの要件は [配備のキーを扱う](../secure/deployment-keys.ja.md) にあります。イメージにプルシークレットが要るなら、ここで `$WS_NS` に、`AF_K8S_IMAGE_PULL_SECRET` に書いた名前で作ります
   ```bash
   kubectl -n "$CP_NS" create secret generic af-cp-secrets \
     --from-literal=AF_MASTER_KEY="$(cat master-key)" \
     --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
     --from-literal=AF_DATABASE_URL="$(cat database-url)" \
     --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
   kubectl -n "$CP_NS" rollout status deployment/af-cp
   ```
6. クラスターの ingress を Service `af-cp`（ポート 8099）の前に置きます。ingress は、手順 3 のポリシーが許可する送信元にします
   - WebSocket を通し、アイドルタイムアウトを長くします
   - `AF_TRUSTED_PROXY_HOPS` を、`X-Forwarded-For` に追記するプロキシの数にします。数えるのは、そのプロキシを通らないと CP に届かないものだけです（理由は [GKE に構築する](gke.ja.md#7-ロードバランサーを知る)）。変えたらオーバーレイを適用し直します
7. claim `af-cp-data` のバックアップを用意します。GKE での例は [バックアップ・復元する](backup.ja.md#残すべきもの) にあります
8. P1〜P14 を自分で満たします。これらを代わりに確かめる仕組みはありません

マニフェストを変えるときに守ることは [Kubernetes 構成の全体像](overview.ja.md#マニフェストを変えるときに守ること) にあります。

## 関連

- [GKE に構築する](gke.ja.md)：その手順 8 の確認方法は、ここでも使えます
- [ノードを入れ替える・止まったノードに対処する](nodes.ja.md)
