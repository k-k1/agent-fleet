# 0047. テナント管理者が自分のテナントの接続元ネットワークを絞れるようにする（**アプリ層・認証の後**）

[English](0047-tenant-network-restriction.md) | 日本語

- 状態: **採用**（2026-08-17）。検討の記録は [docs/66](../log/66-tenant-network-restriction.md)。
- 関連: [0043-login-idp.md](0043-login-idp.ja.md) 決定 24/25（テナントの外へ届くものは運用者、
  中で閉じるものはテナント管理者）・決定 14（門は画面ではなく解決経路に置く） /
  [0044-workspace-sizing.md](0044-workspace-sizing.ja.md) 決定 3（**既定オフで出して一度も
  発火しなかった**前例） / [docs/64](../log/64-ec2-persistent-workspace.md) §64.25（WAF に
  署名系を採らなかった判断・同じ「効かないものを効くように見せない」線）

## 背景

接続元の制限は `00-network.yaml` の `AlbIngressCidr`（既定 `0.0.0.0/0`）**だけ**で、
デプロイ全体・CFN 再適用・AWS 限定である。テナント管理者は AWS を触らない（触らせない）。
一方で事故として実際に起きるのは「資格情報を持った人が、許されていない場所から入る」であり、
それはテナント単位でしか表現できない。

## 決定 1 — 採る。ただし**ネットワーク防御ではなく「アクセス制限」**として出す

要求は ALB を通り CP に届き、TLS が解け、セッションが検証された**後で**拒否される。
したがって認証前の脆弱性・DoS・帯域・探索には**効かない**。

- **上 2 層（`AlbIngressCidr` / WAF）を置き換えない。**私有デプロイで一番安くて強いのは
  今も SG である。Console の説明文でもそう書く。
- 効くのは「**誰が、どこから、データに触れるか**」だけ。それが本機能の全てである。
- **できないことを画面に書く**——ログイン画面は見えるし、サインインも通る。
  隠すと「IP 制限したのにログインできた＝壊れている」と読まれる。

## 決定 2 — 送信元 IP は **XFF を右から N 番目**で取る。N はデプロイが申告する

CP は今まで `RemoteAddr` すら読んでいない。同定の規則をここで固定する。

- **`AF_TRUSTED_PROXY_HOPS`（既定 0）**。0 = プロキシ無し（`RemoteAddr` が本物）。
  ALB のみ = 1（`30-ingress.yaml` が CP のタスク環境に渡す）。compose + Caddy も 1。
- **クライアント = `XFF[len-N]`**（N=1 なら右端）。プロキシは「受け取った相手」を**追記**する
  ので、信頼できるのは右から数えた分だけ。**左端を読む実装だけが偽装可能**であり、
  それが唯一の間違え方である。
- **ヘッダを読むのは最外周のミドルウェア 1 箇所だけ**にし、結果を context に入れる。
  `authGate` が識別ヘッダを `r.Header.Del` してから自分のものを入れているのと同じ理由——
  下流に生のヘッダを信用させない。
- 起動バナーに `trusted-proxy-hops=N` を出す。

## 決定 3 — 判定は `checkTenantProvider` の隣に置く。**PAT/MCP と git は対象外**

テナントが決まる最初の地点は `selectMembership` の後である。そこに `checkTenantIP` を並べる
（`resolveFull` / `resolveMembership`）。403 `ip_not_allowed`。

⚠️ **`resolveByMembership`（PAT）には入れない。** この経路の送信元は
**本人の Workspace コンテナ**であり、人の所在を一切表さない（`AF_MCP_TOKEN` は起動時に
コンテナへ注入され、git もコンテナの中から CP を叩く）。ここに IP 判定を入れると、
オフィスの CIDR を許可したテナントは**自分のワークスペースの中からの MCP と git を全部塞ぐ**。
止めたい人の道具は既にある——メンバーシップの無効化（PAT はそれで失効する）。

同じ理由で `/internal/*`（Agent からの折り返し）も対象外である。

## 決定 4 — 締め出しの逃げ道は 3 つ。うち 1 つは「表示」ではなく「拒否」

1. **super_admin は対象外。** 運用者はいつでも解除できる。最終手段。
2. **保存時に編集者自身の現在 IP を必ず通す。** 含まれなければ 400 で拒否し、
   **CP から見えている IP をメッセージにそのまま出す**。
3. **誤設定はその場で拒否する。** `hops==0` なのに XFF が来ている（＝プロキシの後ろなのに
   申告が無い）／XFF が短くて取れない、のいずれでも**一切保存させない**。

⚠️ 3 が要る理由: 「あなたの現在 IP」を**表示するだけでは足りない**。誤設定時に見えている
ALB の私有アドレス `10.20.10.5` を「これが私の IP か」と思ってそのまま登録でき、
**絞ったつもりで全員を通す**状態になる。表示ではなく拒否にする。

## 決定 5 — 運用者スイッチを足さない。「オフ」はリストが空であること

`AF_TENANT_IP_RULES=on/off` のような既定オフのゲートは作らない。
[ADR 0044](0044-workspace-sizing.ja.md) 決定 3 は**既定オフで出して一度も発火しなかった**。
機能の有効/無効は**テナントが CIDR を 1 行書いたかどうか**だけで表す。

## 決定 6 — 置き場はテナントのログイン規則の行。持ち主はテナント管理者

- **`tenant.allowed_cidrs`（CSV・1 列）**。要求毎に読むので、既に存在する
  30 秒キャッシュ（`tenantLoginCache`・書き込みで即時 invalidate）に相乗りする。
  `tenantLimits`（JSON）ではない——あれは super_admin が持つ上限であり、持ち主が違う。
- **編集は `PUT /api/admin/tenants/{slug}/network`（`tenantAdminFor`）**。
  `setTenantLogin` は super_admin 専用のまま**触らない**（あの 3 項目はテナントの外へ届く）。
- 表記は prefix と単独 IP の両方・IPv4/IPv6。**ホスト部が残った prefix は丸めて保存し、
  丸めたことを応答に出す**（黙って意味を変えない）。
- 拒否は監査ログに要約して残す。**全要求のアクセスログに送信元 IP は出さない**
  （個人データの扱いが変わる。必要になったら保存期間と併せて別途決める）。

## 影響

- `control-plane/clientip.go`（新設）・`resolver.go`・`tenant_login.go`・`tenants.go`・
  `routes.go`・`main.go`（ミドルウェアとバナー）
- `migrations/`・`migrations-pg/` に `tenant.allowed_cidrs` の 1 列追加
  （`0042_tenant_hidden_providers.sql` と同じ形）
- `console/src/features/settings/` にテナント設定の 1 パネル ＋ 文言（en/ja）
- `deploy/aws/ecs/cfn/30-ingress.yaml` に `AF_TRUSTED_PROXY_HOPS=1`、
  `deploy/compose/.env.example` に Caddy 構成での 1 を案内
- **実機で確かめること**: ALB の後ろで CP が本物のグローバル IP を見ていること
  （これが全ての前提で、机上では確かめられない）
