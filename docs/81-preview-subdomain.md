# docs/81 — プレビュー用サブドメイン（Workspace 起動ごとに発行）

決定は [ADR 0062](decisions/0062-preview-subdomain.md)。ecs / ecs-ec2 のデプロイで、
**Workspace を起動するたびにランダムなサブドメインを 1 つ発行**し、利用者が許可した
ポート（既定 3000 / 8080）をそのサブドメイン配下の**ルート直下**で開けるようにする。

## 0. きっかけと、今のプレビューの限界

今の簡易プレビューは**パス方式**である。

```
ブラウザ → CP  /preview/8080/{rest}      (control-plane/preview.go)
        → Agent /proxy/8080/{rest}       (workspace/agent/preview.go)
        → コンテナ内 127.0.0.1:8080
```

要望は「React が 3000、Spring Boot が 8080 という**ごく普通の 2 ポート構成**を、そのまま
見たい」である。パス方式はこの形に 3 か所で噛み合わない。

1. **ルート直下で配れない。** アプリが `/static/js/main.js` や `/api/users` のような
   **絶対パス**を吐いた瞬間に、`/preview/3000` の外へ出て 404 になる。CP は
   `X-Forwarded-Prefix` を渡しているが、これを読むのは Spring Boot のようにフレームワーク
   側が対応している一部だけで、ブラウザで動く JS のバンドルには効かない。
2. **2 ポートが 1 オリジンに同居する。** `/preview/3000` と `/preview/8080` は同じ
   オリジンなので、cookie も localStorage も Service Worker のスコープも混ざる。
   「React とその API サーバ」という関係を、ローカル PC での姿（別オリジン、または
   dev server の proxy で同一オリジン）のどちらにも寄せられない。
3. **WebSocket が通らない。** CP 側は素の `http.Client` で往復しているだけで Upgrade を
   扱わない（`preview.go` の冒頭に「WebSocket/HMR is a follow-up」と書いてある）。
   Vite / CRA の HMR も Spring の STOMP も動かない。

ホスト方式にすると 1 と 2 は**構造として消える**（アプリはルート直下に居て、ポートごとに
別オリジンになる）。3 は別途 CP を Upgrade 対応にする作業で、ホスト方式の前提でもある。

## 1. URL の形

```
https://{slug}-{port}.{PreviewDomain}/
```

- `slug` … Workspace の**起動ごと**に発行するランダム文字列（§4）。小文字英数のみ、
  `-` を含まない（ポートとの区切りに使うため）。
- `port` … 利用者が許可したポート（§5）。
- `PreviewDomain` … デプロイ全体で 1 つ（例 `pv.example.com`）。

```
起動時に発行: slug = k7f2q9x1w3ub5nzt

  https://k7f2q9x1w3ub5nzt-3000.pv.example.com/   → React  (127.0.0.1:3000)
  https://k7f2q9x1w3ub5nzt-8080.pv.example.com/   → Spring (127.0.0.1:8080)
```

**ラベルを 1 段にするのは ACM の制約が理由である。** ワイルドカード証明書は
`*.pv.example.com` のように**ラベル 1 つ分**しか受け持たない。`3000.{slug}.pv.example.com`
という形（ポートを別ラベルにする形）は `*.*.pv.example.com` を要求するので、ACM では
発行できない。Workspace ごとに証明書を発行する道は、起動のたびに ACM 発行と DNS 検証を
待つことになり、**起動が数分伸びるうえ ACM のクォータに当たる**ので採らない。

⚠️ この選択には代償がある。`{slug}-3000` と `{slug}-8080` は**兄弟**であって親子ではない
ので、「この Workspace のプレビュー全体で共有する cookie」を作る場所が無い（共通の親は
`pv.example.com` ＝**全 Workspace の共通祖先**なので、そこに置いたら他人の Workspace へ
漏れる）。認証 cookie はホストごとに 1 枚になる（§6）。

却下した形:

| 形 | 却下の理由 |
|----|-----------|
| `{port}.{slug}.pv.example.com` | ACM のワイルドカードが 1 ラベルしか効かない（上記） |
| ポートごとに無関係な slug を発行 | 片方の漏洩が他方に波及しない利点はあるが、人が無関係な文字列を 2 つ扱うことになり、Console の表示も「どっちがどっち」を毎回説明する羽目になる。漏洩の粒度は Workspace で十分 |
| `{slug}.pv.example.com` ＋ パスでポート | §0 の 1 と 2 がそのまま残る。ホスト方式にする意味が無い |

## 2. 3000 + 8080 の 2 ポート構成で、本当に動くのか

要望の核心はここなので、先に結論を置く。

> **React（3000）を dev server の proxy 経由で Spring Boot（8080）に繋ぐ書き方なら、
> AF 専用のコードを 1 行も足さずに、ローカル PC と同じ設定のまま動く。**
> **React が `http://localhost:8080` を直書きしている書き方は、プレビューでは動かない**
> ——ブラウザから見た `localhost` は「その画面を見ている人の PC」だからである。

### 2.1 ブラウザから見た構図

```
ローカル PC                            プレビュー
─────────────                          ─────────────
localhost:3000  ← 画面                 {slug}-3000.pv.example.com  ← 画面
      │ /api を proxy                        │ /api を proxy（dev server が中で）
      ▼                                      ▼
localhost:8080  ← API                  コンテナ内 127.0.0.1:8080
```

dev server の proxy を使う限り、**ブラウザが知っているオリジンは 1 つだけ**である。
CORS も cookie の SameSite も登場しない。ローカルとプレビューの違いは
「dev server が listen しているのがどのマシンか」だけになり、これは AF が面倒を見る層に
収まる。8080 のサブドメインは「API を直接叩いて確かめたい」ときのために出しておく。

### 2.2 アプリの書き方ごとの適合表

| アプリの書き方 | ローカル PC | プレビュー | 判定 |
|---|---|---|---|
| React が `/api/...` を**相対**で呼び、dev server の `server.proxy` で 8080 へ流す | ✅ | ✅ | ★ 推奨。AF 専用のコードは 0 |
| React が `http://localhost:8080/...` を**直書き** | ✅ | ❌ | 不可。ブラウザの `localhost` は見ている人の PC |
| React が `import.meta.env.VITE_API_BASE`（既定 `http://localhost:8080`）を読む | ✅ | ✅ | 可。プレビューでは AF が注入する兄弟オリジンで上書き（§8）。CORS と cookie の話が付いて回る |
| 同一オリジンで配る（Spring が React のビルド成果物も返す） | ✅ | ✅ | 可。ポートは 1 つで済む |

### 2.3 Vite / CRA 側で必要になる設定

```js
// vite.config.js — ローカルでもプレビューでも同じファイルで動く形
export default defineConfig({
  server: {
    proxy: { "/api": { target: "http://127.0.0.1:8080" } },
  },
});
```

⚠️ **dev server のホスト検査**（Vite の `server.allowedHosts` による DNS リバインド対策）に
引っかからないよう、CP と Agent は上流へ `Host: 127.0.0.1:{port}` を送り続ける（§3・決定 8）。
これで `allowedHosts` の設定は不要になる。

⚠️ **HMR（Vite の WebSocket）だけは、設定が要る可能性が高い。** HMR クライアントが
どのホスト / ポートへ繋ぎに行くかは Vite の版で挙動が違う（ページの `location` を使う版と、
dev server のポートを埋め込む版がある）。**実機で 1 回確かめてから案内を書く**。必要なら
`server.hmr.clientPort: 443` を環境で分岐させる 1 行になる。HMR が繋がらなくても画面は
出る（更新が自動で反映されないだけ）ので、P0 の受け入れ条件からは外す。

### 2.4 別オリジンで直接呼びたい場合（§2.2 の 3 行目）

`{slug}-3000` から `{slug}-8080` を `fetch` すると**クロスオリジン**になる。ローカル PC で
`localhost:3000` → `localhost:8080` をやっているときと**同じ問題**（Spring 側の CORS 設定が
要る）が起きるが、プレビューではもう 1 つ、**AF の認証 cookie が cross-site では飛ばない**
という問題が乗る（§6 で cookie は `SameSite=Lax`）。

この構成を選ぶ人のために、Workspace 設定の opt-in を P3 に置く（決定 10）:

- 認証 cookie を `SameSite=None; Secure` にする
- **同じ slug の兄弟オリジンに限って** CP が CORS ヘッダ（`Access-Control-Allow-Origin` は
  そのオリジンを名指し、`Allow-Credentials: true`）を補い、preflight の `OPTIONS` は CP が
  答える（アプリに届かせない）

既定は OFF。**「クロスオリジンを既定で通す」は、URL を知っている第三者のページから
利用者のブラウザ経由でプレビューを叩ける状態を既定にすること**なので、選んだ人にだけ
渡す。

## 3. 上流へ送るヘッダ

Agent の `handlePreview` は今も `Host: 127.0.0.1:{port}` に書き換えて渡している。
**ホスト方式でもこれを変えない。**

- dev server のホスト検査（§2.3）を素通りできる
- アプリが Host を信じて絶対 URL を作っても、それは内部アドレスなので**外に漏れない**

公開名は `X-Forwarded-Host` / `X-Forwarded-Proto` で伝える。Spring Boot は
`server.forward-headers-strategy=framework` で拾う。**`X-Forwarded-Prefix` は送らない**
——ホスト方式ではアプリはルート直下に居るので、prefix は「間違った前置き」になる。

## 4. slug の発行と失効

- **Workspace の Start でその都度、新しい slug を発行する**（要望どおり）。前回の URL は
  その時点で 404 になる。長く生きる URL を作らないので、共有した URL が意図せず生き続ける
  事故が起きない。
- **Stop / Destroy で失効**（DB から消す）。停止中の Workspace の slug は解決しない。
- **DNS も証明書も、発行のたびには触らない。** ワイルドカードの A エイリアス 1 本と
  ワイルドカード証明書 1 枚が、全 Workspace・全ポートを受け持つ。★ Route53 に
  Workspace ごとのレコードを書く設計にすると、起動のたびに API 呼び出しと伝播待ちが
  増え、レコード数のクォータにも当たる。**発行コストを 0 にできるのがホスト方式の
  数少ない「ただの得」なので、ここは絶対に崩さない。**
- 文字集合は `[a-z0-9]`、長さ 20（≒100 bit）。`-` を含めない。DB に一意制約を張り、
  衝突したら引き直す。
- ★ **slug にテナント名・メンバー名・Workspace id を混ぜない。** URL は Slack にもチケットにも
  貼られる。推測できないことと同じくらい、**そこから誰の何かが読めないこと**が要る。

## 5. 許可ポート

Workspace 設定（`ws_settings`、CP DB 側・停止中でも編集できる層）に**公開ポートの列挙**を
足す。既定は `3000, 8080`。

- 列挙に**無い**ポートのサブドメインは **404**（「そのポートは許可されていません」ではなく、
  存在も答えない。許可ポートの有無を外から探れないようにする）
- Agent 側の既存ガード（自分自身のポートへの転送禁止）はそのまま効かせる
- 上限は 8 個程度で足りる。増やしたくなる圧力は「全部開ける」に向かうが、
  **意図せず立っているサービス（DB 管理画面、デバッガ、MCP サーバ）まで露出させない**の
  がこの列挙の目的なので、既定で全開放にはしない

## 6. 認証（既定は必須）

**Console のセッション cookie は、プレビューのオリジンへ絶対に渡さない。**
プレビューで動くのは利用者が書いた任意のコードで、そこに CP のセッションを渡すことは、
そのコードに利用者としての API 呼び出し権限を渡すことである。パス方式では同一オリジン
だったのでヘッダを剥がして凌いでいたが（`sanitizedHeader`）、ホスト方式では**そもそも
別オリジンなのでブラウザが送らない**。これはホスト方式の副次的な、しかし大きな利点である。

代わりに、プレビューのホストごとに専用の cookie をハンドシェイクで発行する。

```
1. GET https://{slug}-3000.pv.example.com/foo          （cookie 無し）
2. 302 → https://af.example.com/preview-auth?slug=…&port=3000&next=/foo
         ここは Console のオリジン＝ authGate の中。未ログインなら通常のログインへ
3. CP: slug → Workspace を引き、呼び手がその Workspace を使える人か確認
        → 30 秒で切れる署名付きワンタイム token を発行
   302 → https://{slug}-3000.pv.example.com/__af/preview-auth?t=…
4. token 検証 → Set-Cookie af_pv=<署名付き>  HttpOnly / Secure / SameSite=Lax /
                Path=/ / ホスト限定 / 有効期限はセッションの残り
   302 → /foo
```

- cookie の中身は **slug ＋ membership ＋ 期限**を署名したもの。★ **slug を含めるので、
  Workspace を再起動して slug が変われば cookie は自動的に無効になる。**
- ポートごとに 1 枚（§1 の代償）。2 ポート開くと最初の 1 回だけ往復が 2 度走る。
- **テナントの CIDR 制限（docs/66）はプレビューにも効かせる。** テナントが自分の
  ネットワークを絞っているなら、プレビューも絞られている側にある（公開モードでも同じ。
  §7）。

### 6.1 公開モード（Workspace 単位で切替）

外部の人に見せたいときのために、Workspace 単位で「認証なしで開ける」に切り替えられる。

- **既定は OFF。停止 / 再起動で必ず OFF に戻る**（slug も変わるので URL ごと失効する）。
  ★ fail-closed にする理由は単純で、**「公開のままにしていたことを忘れる」以外の事故が
  ほぼ無い**からである。
- 切替は監査ログに残す（誰が・どの Workspace を・いつ）。
- 公開中は `X-Robots-Tag: noindex` を付ける。
- Console の表示は「公開中」であることを常時見せる（畳まれた状態で忘れられるのが最悪）。

## 7. CP のリクエスト経路

```
withClientIP( previewHostDispatch( logRequests( gzip( etag( authGate( mux ))))))
                    │
                    └─ Host が {slug}-{port}.{PreviewDomain} に一致したときだけ
                       プレビュー専用ハンドラへ。それ以外は素通し
```

- **`authGate` の外側**に置く。プレビューのホストは authGate とは別の認証（§6）を持つので、
  中に入れるとログイン画面へ跳ばされる。
- **`gzip` / `etag` の外側**に置く。プレビューの中身は CP の JSON ではないので、
  二重圧縮や ETag の付け直しをさせない。
- ★ **プレビューのホストでは、CP の API も Console も一切出さない（すべて 404）。**
  同じプロセスが両方を持っているので、ここを緩めると「プレビューのオリジンから CP API を
  叩ける」という、§6 で閉じたはずの穴が裏口から開く。プレビューホストで応答するのは
  §6 のハンドシェイク用 `/__af/preview-auth` と、プロキシ本体だけ。
- Host が一致しない（ALB のヘルスチェックなど）ときの挙動は今と同じ。

### 7.1 WebSocket / ストリーミング

CP のプロキシを `httputil.ReverseProxy` に置き換える（Agent 側は既に ReverseProxy なので
Upgrade を通す）。`FlushInterval` を設定して SSE とストリーミング応答も詰まらせない。
既存のパス方式もこの実装に相乗りさせる（同じ弱点を 2 つ抱えないため）。

## 8. コンテナ内へ渡す情報

Workspace の env に発行結果を入れる（`workspaceExtraEnv` 経由・起動時に確定するので自然に載る）。

```
AF_PREVIEW_DOMAIN=pv.example.com
AF_PREVIEW_SLUG=k7f2q9x1w3ub5nzt
AF_PREVIEW_PORTS=3000,8080
AF_PREVIEW_URL_3000=https://k7f2q9x1w3ub5nzt-3000.pv.example.com
AF_PREVIEW_URL_8080=https://k7f2q9x1w3ub5nzt-8080.pv.example.com
```

- アプリの設定（`VITE_API_BASE`、Spring の `app.cors.allowed-origins` など）が**既に env を
  読む形になっているなら、これで足りる**。読んでいないアプリに AF 専用のコードを足させない。
- セッションの中のエージェントも、この env で「今このアプリはどの URL で見えているか」を
  答えられる。

## 9. アイドル自動停止との噛み合い（docs/75）

- プレビューの**リクエスト**は活動に数える（今も `touchWorkspace` を呼んでいる）。人が見て
  いる画面が勝手に落ちるのは事故である。
- ⚠️ **開きっぱなしの WebSocket そのものは数えない。** HMR のソケットは誰も見ていなくても
  張られたままになるので、接続の存在を活動と見なすと**永久に止まらない Workspace**が
  できる。数えるのは「新しいリクエスト」と「WS のメッセージ」であって、接続の生存では
  ない。

## 10. デプロイ（ecs / ecs-ec2）

`deploy/aws/ecs/cfn/30-ingress.yaml` に足すもの:

| 変更 | 中身 |
|---|---|
| パラメータ `PreviewDomain` | 例 `pv.example.com`。**空ならホスト方式は無効**（パス方式だけが残る） |
| `PreviewCert`（新規） | `*.{PreviewDomain}` の**ワイルドカード証明書**。DNS 検証（`HostedZoneId`） |
| `ListenerCertificate`（新規） | `PreviewCert` を Listener443 の**追加証明書**として貼る |
| Route53 | `*.{PreviewDomain}` の A エイリアス（ALB へ）1 本 |
| CP の env | `AF_PREVIEW_DOMAIN` |
| ALB / `Cert` / `TargetGroup` | **変更なし**。デフォルトアクションが CP の TG なので、ホストが増えても素通しで届く |

### 10.1 ACM の証明書はワイルドカード 1 枚を「2 枚目」として足す

**`*.{PreviewDomain}` のワイルドカード証明書を 1 枚**、DNS 検証で作る。ラベル 1 段しか
受け持たないので `{slug}-{port}` の形（§1）と対になっている——**この証明書の形が、URL の
形を決めている**。

★ **既存の `Cert`（Console の FQDN 用）に SAN として足すのではなく、別リソースとして作り、
`AWS::ElasticLoadBalancingV2::ListenerCertificate` で Listener443 に貼る。**
ALB のリスナーは既定証明書 1 枚に加えて追加証明書を持て、SNI で選ばれる。

- SAN を足す形は **既存証明書の置換**になる（新しい ACM 証明書が発行され、Listener の
  既定証明書が差し替わる）。**Console が今出せている TLS を、プレビューを足すという理由で
  作り直す必要はどこにも無い。**
- 2 枚に分けておくと、**プレビューをやめるときは `ListenerCertificate` を外すだけ**で
  Console 側の証明書は 1 度も動かない。
- `PreviewDomain` が空のデプロイでは、この 2 リソースを丸ごと作らない（`Condition`）。

⚠️ **ワイルドカード証明書の DNS 検証レコードは `_xxx.{PreviewDomain}`** に付く
（`*.` は取れる）。同じゾーンに `{PreviewDomain}` が無くても検証はできるが、
`HostedZoneId` が **`{PreviewDomain}` を含むゾーン**を指していないと CloudFormation は
検証待ちのまま止まる（ここは失敗ではなく「進まない」形で出るので、気付きにくい）。

⚠️ **ワイルドカードは 1 段だけ。** `*.pv.example.com` は `k7f2-3000.pv.example.com` に
一致するが、`a.b.pv.example.com` には一致しない。§1 の形を将来「ポートを別ラベルに」と
変えたくなったら、**証明書の側が先に破綻する**ことを覚えておく。

⚠️ **`PreviewDomain` を Console の FQDN の子（`*.af.example.com`）にするか、兄弟
（`pv.example.com`）にするか**は、cookie の書き込み範囲の話である。子にすると、プレビューで
動くアプリが `.af.example.com` のドメイン cookie を書けてしまい、Console の cookie を
上書き / 固定できる余地が残る（現行コードは応答の `Set-Cookie` から CP と認証ゲートウェイの
名前を剥がしているので実害は抑えられているが、**構造として持たない方が良い**）。
**兄弟を既定として案内する。** 完全に分離したいなら**別の登録ドメイン**を使うほかない
（同じ登録ドメインである限り `.example.com` の cookie は書ける）。

**ローカル / docker / native では `PreviewDomain` は空で運用する** ——ワイルドカード DNS も
証明書も無い環境で、ホスト方式は成立しない。**パス方式は消さない**（決定 1）。

## 11. Console の UI

`WsBar` のプレビューのポップオーバー（今は「ポート」「パス」「ペインで開く」「軽量
プレビュー」）に足す:

- 許可ポートごとの行（`3000` / `8080`）と、**発行済み URL のコピー**
- 「新しいタブで開く」（＝ホスト方式の軽量プレビュー）
- 停止中は URL を出さない（**発行されていない**ものを見せない）
- 公開モードのトグルと、公開中であることの常時表示（§6.1）
- 許可ポートの編集は Workspace 設定へ（§5）

「ペインで開く」（コンテナ内 Chromium）は、コンテナの中から見るので**今までどおり
`127.0.0.1:{port}` を指す**。プレビュー用サブドメインは**外から見るための道**であって、
ペインの経路とは別物である。

## 12. 段階

| | 中身 |
|---|---|
| **P0-a 入口** | CFN（`PreviewDomain` / SAN / Route53）と CP の env。実機で名前が解決し TLS が張れるところまで |
| **P0-b CP** | Host 振り分け・slug の発行 / 失効（マイグレーション: sqlite と Postgres の両方）・認証必須のハンドシェイク・許可ポート（既定 3000,8080）・ReverseProxy 化（WebSocket / SSE） |
| **P1 Console** | プレビューのポップオーバーと Workspace 設定の許可ポート編集 |
| **P2 公開モード** | Workspace 単位の切替・fail-closed・監査ログ・`noindex` |
| **P3 兄弟オリジン** | `SameSite=None` ＋ CP が補う CORS の opt-in（§2.4）・env 注入（§8） |
| **P4 実機と案内** | 3000 + 8080 の実アプリで通し確認（特に HMR §2.3）・`docs/guide` を二言語で更新 |

## 13. 実機でしか確かめられないこと

- ACM の SAN 追加による証明書置換が、稼働中の Listener を無停止で差し替えられるか
- Vite の HMR がプレビュー越しにどのホスト / ポートへ繋ぎに行くか（§2.3）
- ALB のアイドルタイムアウト 60 秒と、張りっぱなしの HMR ソケットの相性
  （切れたときに dev server 側が再接続するか）
- WAF のレートリミット（`WafRateLimitPer5Min`）が、プレビューの静的アセットの束を
  1 IP から取りに行く動きで誤爆しないか
