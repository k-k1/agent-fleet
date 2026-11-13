---
audience: "Docker Compose の配備で、ワークスペースから Control Plane に届かなくなった人"
source_of_truth: "Control Plane の `AF_CP_INTERNAL_LISTEN`・`AF_CP_INTERNAL_URL` の扱い（`control-plane/main.go`・`workspace_listener.go`）と `deploy/compose/.env.example`"
---

# ワークスペースから CP に届かないとき

[English](workspace-listener.md) | 日本語

ホストからは Control Plane に届くのに、ワークスペースからだけ `PUBLIC_BASE_URL` に届かないときに、ホストの外に出ない経路をワークスペースに渡します。
Linux の rootful Docker で動く Docker Compose の配備が対象です。

## 全体像

| あなたがやること | 起きること |
|---|---|
| docker0 のアドレスを調べ、`.env` に 2 つの変数を書く | Control Plane が、そのアドレスにワークスペース専用の 2 つ目のポートを開く |
| `docker compose up -d` で Control Plane を起動し直す | 新しい設定で Control Plane が起動する |
| 各ワークスペースを停止して起動する | ワークスペースの Agent が、公開 URL ではなくこのポートで Control Plane を呼ぶ |

## こんな症状のとき

ワークスペースの Agent は、さまざまな用途で Control Plane を呼び出します。ドキュメントの取得、ブランチ名の規則、テナントの MCP のレジストリ、AWS・Google Cloud のプロファイル、メモ、定期実行、Control Plane 経由の git です。

既定では `PUBLIC_BASE_URL` に向けて呼ぶので、通信は Docker ブリッジの NAT からいったん出て、公開 URL の前段から入り直します。

- この前段（トンネルのデーモンやリバースプロキシ）が Docker ブリッジからの接続を受け付けなくなると、これらが**一斉に**失敗します
- Control Plane 自身は正常で、ホストやブラウザからは普通に届きます
- 例として、トンネルのデーモンの `:443` のソケットがホストの 1 つのインターフェースに bind され、コンテナからだけすぐに RST が返ったことがあります

## 設定する

1. docker0 のゲートウェイのアドレスを調べます（ふつうは `172.17.0.1`）
2. `.env` に 2 つの変数を書きます
3. `docker compose up -d` で Control Plane を起動し直します。Control Plane は 2 つとも `.env` から読みます
4. 各ワークスペースを Console で停止して起動します。設定がワークスペースに届くのは、次の起動からです

```bash
# the docker0 gateway address (normally 172.17.0.1):
docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}'
# .env
AF_CP_INTERNAL_LISTEN=172.17.0.1:8098
AF_CP_INTERNAL_URL=http://host.docker.internal:8098
```

| 変数 | 入れるもの |
|---|---|
| `AF_CP_INTERNAL_LISTEN` | Control Plane が 2 つ目のポートを開くアドレス。docker0 のアドレスとポートです |
| `AF_CP_INTERNAL_URL` | ワークスペースが Control Plane を呼ぶ URL |

> [!WARNING]
> **`0.0.0.0` や `:8098` には bind しないでください。docker0 のアドレスにします。**
> docker0 のアドレスはホストの中だけのものなので、ユーザーごとのどのネットワークからも届き（パケットはブリッジの間を転送されず、ホストに届けられます）、LAN や tailnet のインターフェースには載りません。
> すべてのインターフェースに bind すると、このリスナーが LAN にも開きます。そのときは Control Plane が起動のときにログに `WARNING` を出します。

## このリスナーが応えるもの

- Agent が呼ぶルートだけです。それぞれメンバーシップごとの bearer トークンで守られます
- Console・管理の API・サインイン・egress proxy のルートは 404 を返します
- 本人を示すヘッダーと、すべての転送ヘッダーを捨てます
- 人が開くリンクと `af-curl`・`af-shot` は、これまでどおり `PUBLIC_BASE_URL` を使います

## ホストの側で合わせること

| 項目 | やること |
|---|---|
| `host.docker.internal` | `AF_CP_INTERNAL_URL` がこの名前を使うと、Control Plane は各ワークスペースを `--add-host host.docker.internal:host-gateway` 付きで起動します（Docker Engine 20.10 以降）。`host-gateway` は docker0 のアドレスです。デーモンが `host-gateway-ip` を設定しているときは、そのアドレスに bind します。URL がほかのホスト名なら、DNS に任せます |
| ホストのファイアウォール | INPUT チェインのポリシーが DROP（ufw の既定）なら、Docker のネットワークから、そのアドレスとポートへの TCP を通します |
| egress proxy | `AF_EGRESS_PROXY_ADDR` を設定していると、`AF_CP_INTERNAL_URL` のホストがワークスペースの `NO_PROXY` に入ります。組み込みの egress の許可リストにも入っています |
| LAN からの到達 | Linux は、どのインターフェースに届いたパケットでもそのアドレスあてなら受け取ります。docker0 の範囲をわざわざこのホストへルーティングしている LAN のマシンからは届く可能性があります。Docker ブリッジからだけこのポートを通すファイアウォールで、これも閉じられます |

- 既定ではオフです。アドレスはホストの Docker の設定とファイアウォールで決まり、存在しないアドレスを指定すると Control Plane が止まるからです。設定しなければ 2 つ目のポートはなく、何も変わりません
- Control Plane が起動したときに docker0 がないと、bind に失敗して Control Plane が終了します

## この設定が使えない環境

| 環境 | 理由 |
|---|---|
| Docker Desktop | `host-gateway` が、Control Plane が待ち受けるエンジンではなく、デスクトップのマシンを指します |
| rootless Docker | 確かめていません。docker0 は RootlessKit の名前空間にあり、Docker Engine 29.5 以降の `network_mode: host` は本当のホストの名前空間なので、そこにはそのアドレスがなく、Control Plane は bind に失敗します |

Mac のオーバーレイ（`docker-compose.macos.yml`）は、この仕組みを最初から使います（[macOS（Apple Silicon）で動かす](macos.ja.md)）。

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| 設定したら Control Plane が終了する | `AF_CP_INTERNAL_LISTEN` のアドレスがホストにあるか。docker0 がまだないか、`host-gateway-ip` で別のアドレスになっていないか |
| ログに `AF_CP_INTERNAL_URL is set without AF_CP_INTERNAL_LISTEN` と出る | `AF_CP_INTERNAL_LISTEN` を書き忘れています。ワークスペースは、この Control Plane が応えないアドレスに向かいます |
| 設定したのに、まだ届かない | そのワークスペースを停止して起動したか。ホストのファイアウォールがそのポートを通しているか |

## 関連

- [Docker Compose で構築する](install.ja.md)
- [症状から原因を探す](../diagnose/checklist.ja.md)
- [外向きの通信を制御する](../secure/egress.ja.md)
