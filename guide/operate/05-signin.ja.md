---
audience: "IdP を配備につなぐ人"
source_of_truth: "`deploy/` 配下のスクリプト（記述がスクリプトと食い違ったら、このページのバグ）"
---

# 05. サインイン方式の設定 — IdP 側の手順まで通しで

[English](05-signin.md) | 日本語

この章の内容は、やりたいことごとのページに移りました。

| やりたいこと | ページ |
|---|---|
| どの IdP でも共通の準備、リダイレクト URI、設定の置き場所、動いたことの確かめ方 | [サインインの方法を設定する](signin/overview.ja.md) |
| Google のクライアントを作って `.env` に書く | [Google でサインインする](signin/google.ja.md) |
| Entra のアプリ登録、issuer の固定、2 つのアプリ登録をまたぐ設定 | [Microsoft Entra ID でサインインする](signin/entra.ja.md) |
| GitHub の OAuth App、組織の承認、再起動後の再サインイン | [GitHub でサインインする](signin/github.ja.md) |
| Okta・Keycloak・Auth0・Cognito・GitLab など | [そのほかの OIDC でサインインする](signin/oidc.ja.md) |
| テナントが登録した IdP を確かめて承認する | [テナント独自のサインインを承認する](signin/tenant-idp.ja.md) |
| それでも動かないとき | [ログインできない](diagnose/cannot-login.ja.md) |
