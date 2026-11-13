---
audience: "配備に IdP をつなぐ配備管理者（ホストの端末と `.env` を扱う人）"
source_of_truth: "`.env` の読み込みは Control Plane のサインイン設定（記述がコードと食い違ったら、このページのバグ）"
---

# そのほかの OIDC でサインインする

[English](oidc.md) | 日本語

Okta・Keycloak・Auth0・Cognito・GitLab など、OIDC に対応した IdP でこの配備にサインインできるようにします。
どれも同じしくみで扱うので、IdP にアプリを登録して `.env` に 5 行書くだけです。

## 全体像

| あなたがやること | Control Plane がやること |
|---|---|
| IdP に機密クライアントの Web アプリを登録する | |
| issuer・クライアント ID・シークレット・信頼の規則を `.env` に書き、起動し直す | 設定がそろった IdP ごとにボタンを出す |
| | 最初にボタンが押されたとき、issuer の discovery ドキュメントを読み、名乗る issuer が一致するか確かめる |

## IdP にアプリを登録する

IdP の管理画面で、**機密クライアント**（client secret を持つ Web アプリ）を登録します。

- リダイレクト URI は `https://<PUBLIC_DOMAIN>/oauth2/callback` です（[リダイレクト URI を登録する](overview.ja.md#リダイレクト-uri-を登録する)）
- client ID と client secret を控えます

issuer は、IdP ごとに次の形をしています。管理画面で探すときの目安にしてください。

| IdP | issuer の形 |
|---|---|
| Okta | `https://<org>.okta.com`。カスタム認可サーバーなら `https://<org>.okta.com/oauth2/<authorization-server-id>` |
| Keycloak | `https://kc.example.com/realms/<realm>` |
| Auth0 | `https://<tenant>.<region>.auth0.com/` |
| Cognito | `https://cognito-idp.<region>.amazonaws.com/<user-pool-id>` |
| GitLab | `https://gitlab.com`、または自前の GitLab のベース URL |

## .env に書く

```sh
AF_OIDC_PROVIDERS=okta
AF_OIDC_OKTA_ISSUER=https://<your-org>.okta.com
AF_OIDC_OKTA_CLIENT_ID=<client-id>
AF_OIDC_OKTA_CLIENT_SECRET=<client-secret>
AF_OIDC_OKTA_TRUST=email_verified
```

- **issuer は、ブラウザのアドレス欄ではなく discovery ドキュメントからコピーします。** Control Plane は issuer に `/.well-known/openid-configuration` を足して読み、その中の `issuer` が同じ値かを確かめます。末尾のスラッシュの有無は違いとみなしません
- **`_TRUST`** はメールアドレスを信じる理由で、既定値はありません
  - その IdP が `email_verified` を出すなら `email_verified`（Okta・Keycloak・Auth0 はたいてい出します）
  - 出さないが、issuer が 1 つのディレクトリに固定されているなら `issuer`
  - 迷ったら `email_verified` から始めます。合っていなくても、拒否されて理由がログに残るだけで、通ってはいけない人が通ることはありません
- issuer は `https` にします。`http` は、ローカルの Keycloak や Dex で開発するときのために `localhost`・`127.0.0.1`・`::1` にだけ許され、そのとき Control Plane は警告を出します
- id を並べれば、ボタンも増えます（`AF_OIDC_PROVIDERS=entra,okta` など）
- id の決まり、ふだんは要らない設定（スコープ・PKCE・IdP ごとの許可リスト）、別のアプリ登録をまたいで同じ人と見なす設定は、Entra と同じです（[Microsoft Entra ID でサインインする](entra.ja.md)）

書いたら Control Plane を起動し直し、[動いたことを確かめる](overview.ja.md#動いたことを確かめる) の手順で確認します。

## うまくいかないとき

### ボタンが出ない

ログの `WARNING: login provider "…" disabled — …` に、足りない設定の名前が出ます。多いのは `_TRUST` の書き忘れと、`https` でない issuer です。

### ログに discovery …: issuer mismatch と出る

設定した issuer が、discovery ドキュメントの `issuer` と違います。ドキュメントの値をそのままコピーします。

### `_TRUST=email_verified` にしたら全員が拒否される

その IdP が `email_verified` を出していないので、どのアドレスも未確認として扱われます。issuer が 1 つのディレクトリに固定されているなら `_TRUST=issuer` にします。

## 関連

- [サインインの方法を設定する](overview.ja.md)
- [Microsoft Entra ID でサインインする](entra.ja.md)
- [ログインできない](../diagnose/cannot-login.ja.md)
