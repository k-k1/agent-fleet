---
audience: "外のネットワークに出られないホストに配備を入れる人"
source_of_truth: "`deploy/compose/release.sh`・`load-images.sh` と `control-plane drawio-preseed`（食い違ったらスクリプトとコードが正）"
---

# 閉じたネットワークに入れる

[English](air-gapped.md) | 日本語

インターネットに出られないホストにも、Agent Fleet を入れられます。イメージ・TLS・エージェントの CLI・図のアイコンを、外に頼らずに用意します。

## 全体像

| あなたがやること | 起きること |
|---|---|
| イメージを社内のレジストリにミラーするか、手で持ち込む | ホストがレジストリに届かなくても、配備が起動する |
| TLS 証明書を自己署名か社内 CA のものにする | Let's Encrypt に届かなくても、Console が HTTPS で開く |
| エージェントの CLI を組み込んでビルドしたワークスペースのイメージを使う | ワークスペースが起動時に CLI を取りに行かない |
| 図のアイコンをキャッシュに入れておく | `.drawio` の図が、アイコン付きで描かれる |

> [!IMPORTANT]
> **「閉じたネットワークで動く」は、配備が起動するという意味です。** エージェントは、モデルのエンドポイントに届かなければ何もできません。
> この手順は、制限された社内ネットワークのホスト向けです。完全に切り離されたネットワーク向けではありません。

## イメージを用意する

イメージは GHCR で配っていて、リリースにイメージの tar は付いていません。レジストリに届かないホストでは、次のどちらかにします。

| 方法 | 向いているとき |
|---|---|
| `ghcr.io/k-k1/agent-fleet/*` を社内のレジストリにミラーし、`REGISTRY` をそこに向ける | 社内にレジストリがある。`docker compose pull` がそのまま使え、保守が軽い |
| ネットワークのあるマシンで `release.sh --save` を実行してイメージを書き出し、対象のホストで `load-images.sh` で読み込む | レジストリがない。アップグレードのたびにビルド・コピー・読み込みを繰り返す |

手で持ち込むときは、ネットワークのあるマシンでイメージを書き出します。

```bash
VERSION=<version> deploy/compose/release.sh --save
#   -> deploy/compose/dist/agent-fleet-images-<version>.tar.gz ができる（docker save を gzip したもの）
```

その tar とバンドルを対象のホストにコピーし、そこで読み込みます。

```bash
deploy/compose/load-images.sh agent-fleet-images-<version>.tar.gz
docker compose up -d
```

手で持ち込んだイメージは `agent-fleet/{control-plane,workspace}:<version>` という名前になります（`REGISTRY=` で変えられます）。対象のホストの `.env` では `REGISTRY=agent-fleet` にして、名前を合わせます。

## TLS を用意する

閉じたネットワークからは Let's Encrypt を使えません。[テナントとプロビジョニングの方式を決める](../install-decisions.ja.md#tls-internal-を使うか) の `tls internal`（自己署名）に切り替えるか、社内 CA の証明書を使います。

## エージェントの CLI を組み込んでビルドする

既定のワークスペースのイメージは軽量版で、エージェントの CLI を含まず、コンテナの起動時に、イメージの `versions.json` で固定されたバージョンを入れます。外に出られないホストでは、これが失敗します。

1. CLI を組み込んだイメージをビルドします（`BAKE_AGENT_CLIS=1`）
2. `WS_ENV` で `CLAUDE_INSTALL=0` を渡し、起動時に Claude を取りに行かないようにします

## 図のアイコンを入れておく

同梱の drawio ビューアはオフラインでも図を描けますが、ベンダーのアイコン（AWS・GCP・Azure・Kubernetes・ラック機器など）は同梱していません。
ふだんは、初めて使うときに Control Plane が取得してキャッシュしますが、外に出られないと取得に失敗し、**図は警告なしに、枠と色とラベルだけに劣化します**。

避けるには、持ち込んだディレクトリからキャッシュを埋めておきます。

1. 外に出られるマシンで、drawio のリポジトリを 1 回クローンします（`v31.1.8`）
2. `drawio/src/main/webapp/stencils` を持ち込みます
3. Control Plane のホストで `control-plane drawio-preseed --from <ディレクトリ>` を実行します

```sh
control-plane drawio-preseed --from /path/to/stencils        # 既定のセット：49 件、17.0 MB
control-plane drawio-preseed --from /path/to/stencils --all  # 全部：203 件、40.8 MB
```

- 既定のセットは、クラウドとインフラの図、一般の作図に使うものです。何が入り何が入らないかは、`--list` で先に確かめられます
- 1 件ずつ、同梱のマニフェストにある SHA-256 と照合してから置くので、持ち込みの経路が信用できなくても中身は保証されます
- キャッシュは内容からファイル名が決まる方式（コンテンツアドレス）で、インデックスファイルはありません。埋めたディレクトリを tar にして別のホストに運んでも同じです

## 関連

- [アップグレードする](upgrade.ja.md)
- [ネイティブパッケージを更新する](../native/update.ja.md#手で持ち込む)：ネイティブパッケージを外に出られないホストに入れる
- [インストールする](../install.ja.md)
