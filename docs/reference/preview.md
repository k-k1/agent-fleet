# preview — コンテナ内サービスのプレビュー

ユーザーが Workspace コンテナ内で起動した HTTP サービス（Spring Boot / dev server /
任意の Web アプリ）を、Console から新しいタブで開いて確認するための経路。**追加のホスト
公開ポート（`-p`）もコンテナ再作成も不要**。

## 経路

```
ブラウザ
  https://<host>/agent-fleet/preview/<port>/<path>
    → Caddy (/agent-fleet を strip)
    → CP  GET /preview/<port>/<path>            control-plane/preview.go
          ├ rtFor で認証（他ルートと同じ gateway identity）
          ├ Authorization: Bearer <AGENT_TOKEN> を付与（CP↔Agent）
          └ X-Forwarded-Prefix/Host/Proto を付与
    → Agent  /proxy/<port>/<path>               workspace/agent/preview.go
          ├ Authorization（内部トークン）を除去
          └ httputil.ReverseProxy で転送
    → コンテナ内 http://127.0.0.1:<port>/<path>
```

Agent はコンテナの netns を共有しているので、`127.0.0.1:<port>` でユーザープロセスに
直接届く。各コンテナは専用ネットワーク上で相互不可視のまま（隔離は保たれる）。

## 使い方（Console）

WS バー右側のポート入力に、コンテナ内で起動したサービスのポート（例 `8080`）を入れて
「プレビュー」。新しいタブで `preview/<port>/` が開く。Workspace が running のときだけ有効。

テナント選択中は、新タブ遷移が `X-AF-Tenant` ヘッダを運べないため `?tenant=<slug>` を
URL に付与する（CP の `resolvedFor` がクエリ fallback で解決。terminal WS と同じ方式）。

## アプリ側の設定

| アプリ種別 | 必要な設定 |
|-----------|-----------|
| JSON REST（リダイレクト/絶対URLなし）| なし。そのまま動く |
| Spring Boot（Thymeleaf / リダイレクト / HATEOAS）| `server.forward-headers-strategy=framework`（または `native`）。`X-Forwarded-Prefix` を尊重し、`/agent-fleet/preview/<port>/...` でリンク/リダイレクトを生成する |
| 静的配信 | なし |

`X-Forwarded-Prefix` の値は `PUBLIC_BASE_URL` のパス + `/preview/<port>`（ローカル開発で
未設定なら `/preview/<port>`）。

## 現状の制約（MVP / follow-up）

- **HTTP のみ。WebSocket / SSE 未対応** → Vite/React の HMR ライブリロードは未対応。
  SB の REST/Thymeleaf/actuator は対象。WS ブリッジ（`proxyTerminal` 流用）が次段。
- ポートはユーザーが手入力（コンテナ内の listen ポート自動検出はしていない）。
- サブパス配下に置けない/`forward-headers` 非対応のアプリは絶対パス資産が 404 になりうる。
  その場合はアプリ側で base path を設定する（dev server なら `base` 等）。
- `forward-headers-strategy` を入れていない Spring Boot は `Location: /...` を絶対パスで
  返し、プレビュー外に出てしまう（上表のとおり設定が必要）。
