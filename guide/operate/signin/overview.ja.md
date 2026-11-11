---
audience: "配備に IdP をつなぐ配備管理者（ホストの端末と `.env` を扱う人）"
source_of_truth: "`.env` の読み込みは Control Plane のサインイン設定（記述がコードと食い違ったら、このページのバグ）"
---

# サインインの方法を設定する

[English](overview.md) | 日本語

Google・Microsoft Entra ID・GitHub・そのほかの OIDC の IdP を配備につなぎ、サインイン画面にボタンを出せます。
このページは、どの IdP でも共通の準備と、動いたことの確かめ方をまとめたものです。

## 全体像

| あなたがやること | Control Plane がやること |
|---|---|
| IdP 側でアプリ（クライアント）を作り、リダイレクト URI を 1 本登録する | |
| 控えた値を `.env` に書き、Control Plane を起動し直す | 起動時に設定を読み、使える IdP ごとにボタンを組み立てる |
| | 設定に不備のある IdP だけを外し、理由をログに出す |
| テナントが登録した IdP を承認する | 承認された IdP のボタンを、そのテナント専用のサインイン画面に出す |

サインインの流れ（OAuth / OIDC）は Control Plane 自身が実行します。`.env` の `AUTH=oauth` がそのモードで、`AUTH` に既定値はありません。

## はじめる前に

- 有効にする IdP ごとに、client secret を持つ**機密クライアント**が要ります
- **いくつでも同時に有効にできます。** ボタンの順番は、Google が先頭、次に `AF_OIDC_PROVIDERS` に並べた順の OIDC、GitHub が最後です
- **設定に不備のある IdP は、その IdP だけが無効になります。** Control Plane が起動を止めるのは、使える方法が 1 つもないときと、Entra の issuer が危険な指定のとき（[Microsoft Entra ID でサインインする](entra.ja.md)）だけです
- **SAML しかない IdP**（HENNGE One・TrustLogin・CloudGate など）と LDAP は、Control Plane では扱いません。oauth2-proxy や Keycloak を前段に置き、`AUTH=proxy` でそのゲートウェイが付けるメールアドレスを信じさせます。このモードではこの章のページは使いません
- `.env` の変更は、**次に Control Plane を起動したとき**から効きます。テナントが Console から登録する IdP は、承認した時点で効きます

## リダイレクト URI を登録する

どの IdP にも、**承認済みリダイレクト URI**（GitHub では *Authorization callback URL*）として次の 1 本だけを登録します。

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

- `<PUBLIC_BASE_URL>/oauth2/callback` と **1 文字も違わない**ようにします。スキームもホストも同じで、末尾のスラッシュは付けません。`PUBLIC_BASE_URL` にポートがなければ、ポートも付けません
- IdP をいくつ有効にしても、この URI は 1 本のままです。どの IdP から戻ってきたかは署名付きの Cookie で運ぶので、URL には出ません
- テナントが自社の IdP に登録するのも、この同じ URI です。テナントの側の URL ではなく、**この配備の** URL です
- `PUBLIC_BASE_URL` を変えるときは、同じ作業の中ですべての IdP の URI も直します。直さないと、全員が IdP のエラー画面で止まります

構築でいちばん多い失敗がここです。

## どの設定をどこに書くか

サインインの方法には 2 種類あり、同じ設定でも書く場所が違います。

- **配備共通の方法**：あなたが `.env` に書きます。すべてのサインイン画面に出ます
- **テナントが登録する方法**：テナント管理者が Console から登録し、あなたが承認すると動きます。そのテナント専用のサインイン画面にだけ出ます（[テナント独自のサインインを承認する](tenant-idp.ja.md)）

| 設定したいこと | 配備共通（`.env`） | テナントが登録する方法（Console） |
|---|---|---|
| アプリの登録 | あなたが IdP 側で作る | テナントが自社の IdP に作る |
| リダイレクト URI | `https://<PUBLIC_DOMAIN>/oauth2/callback` | 同じ 1 本 |
| どの方法があるか | `AF_OIDC_PROVIDERS`（Google と GitHub は専用のキー） | **サインイン方式** の 1 行が 1 つの方法 |
| client_id・client_secret | `.env` | 入力欄。`AF_MASTER_KEY` で暗号化して保存し、画面には出さない |
| issuer | `AF_OIDC_<ID>_ISSUER` | **issuer（発行者 URL）** |
| メールアドレスを信じる理由 | `AF_OIDC_<ID>_TRUST`（既定値なし） | **email の信頼方法** |
| 誰が入れるか | `AF_OAUTH_ALLOWED_EMAILS`・`_DOMAINS`・`_EMAILS_FILE`、または IdP ごとの `AF_OIDC_<ID>_ALLOWED_*` | **受け入れるメールドメイン**（必須。配備共通の許可リストは使わない） |
| GitHub の組織 | `AF_GITHUB_ALLOWED_ORGS` | **許可する GitHub 組織** |
| Entra の tenant id | `AF_OIDC_<ID>_ALLOWED_TIDS` | **許可する tenant id（Entra の tid・任意）** |
| ボタンの文言 | `AF_OIDC_<ID>_LABEL_JA`・`_LABEL_EN` | **ボタンの文言（日本語）**・**（英語）** |
| 別のアプリ登録をまたいで同じ人と見なす | `AF_OIDC_<ID>_LINK_CLAIM`（任意のクレーム） | **同一アカウントの見分け方**（`oid` だけ） |
| いつ効くか | 次に Control Plane を起動したとき | あなたが承認したとき |
| ボタンが出る場所 | `/login` とすべての `/login/<slug>` | `/login/<slug>` だけ |
| テナントの一覧での見え方 | 「**配備共通**」の印が付き、テナントは編集できない | テナントが作った行で、テナントが編集できる |

次の 3 つは、探しても設定する場所がありません。

- Google のエンドポイントと信頼の規則
- GitHub の `github.com`・`api.github.com`
- Google と GitHub に要求するスコープ（OIDC の IdP は `AF_OIDC_<ID>_SCOPES` で変えられますが、ふだんは要りません）

## IdP ごとに設定する

| IdP | ページ |
|---|---|
| Google | [Google でサインインする](google.ja.md) |
| Microsoft Entra ID | [Microsoft Entra ID でサインインする](entra.ja.md) |
| GitHub の組織 | [GitHub でサインインする](github.ja.md) |
| Okta・Keycloak・Auth0・Cognito・GitLab など | [そのほかの OIDC でサインインする](oidc.ja.md) |
| テナントが自分で登録したもの | [テナント独自のサインインを承認する](tenant-idp.ja.md) |

## 動いたことを確かめる

上から順に確かめます。各段は次の段の前提です。コマンドは compose の例です。ほかの形態では、Control Plane のログを
その形態の runbook（[配備を運用する](../README.ja.md) から開けます）の方法で読みます。

1. **どの方法が組み立てられたか**を見ます。有効な配備共通の IdP の id が、ボタンの順に 1 行で出ます。ここにない id は組み立てられていないので、サインイン画面をいくら見ても理由はわかりません

   ```sh
   docker compose logs cp | grep "login providers:"
   ```

2. **足りない IdP の理由**を見ます。`WARNING: login provider "…" disabled — …` の行に、足りない設定や不正な設定の名前が出ます。いちばん多いのは、既定値のない `AF_OIDC_<ID>_TRUST` です。Google と GitHub は専用の行を出します

   ```sh
   docker compose logs cp | grep -i "login provider"
   ```

3. **Control Plane が起動しない**ときは、理由は 2 つだけです
   - Entra の issuer が複数テナント向けのエンドポイントになっている（[Microsoft Entra ID でサインインする](entra.ja.md#うまくいかないとき)）
   - `AUTH=oauth requires AF_COOKIE_SECRET, PUBLIC_BASE_URL and at least one login provider …`：使える方法が 1 つも設定されていません

4. **`https://<PUBLIC_DOMAIN>/login` を開き**、ボタンの数を数えます。手順 1 の id の数だけ、Google が先頭、GitHub が最後に並びます
   - 「サインイン方法が設定されていません。管理者に連絡してください。」と出るなら、手順 1 で何も組み立てられていません
   - 既定テナント（`default`）で「**ボタンに出す**」を外した方法は、ここにも出ません。このページは既定テナントのページとして描かれるためです。数が合わないときは、まず既定テナントのサインイン方法の一覧を見ます
   - 既定テナントの「**受け入れる**」の絞り込みは、ここには効きません。どのテナントにも属していない人の唯一の入口なので、ボタンが 0 個になる道を作らないためです

5. **`https://<PUBLIC_DOMAIN>/login/<slug>` を開きます**。そのテナントが受け入れる配備共通のボタン（「ボタンに出す」を外したものを除く）のあとに、そのテナント自身の承認済みの方法が並びます。承認済みなのにボタンがないときは、理由がログに出ます

   ```sh
   docker compose logs cp | grep -i "tenant login provider"
   ```

   - すべてのボタンを隠す指定は無視されるので、この画面が空になることはありません
   - 存在しない slug でもエラーにならず、ふつうのサインイン画面が出ます。わざとそうしているので、追いかけなくてかまいません

6. **GitHub のときだけ**、拒否されると結論を出す前に `docker compose logs cp | grep "returned 403"` を見ます（[GitHub でサインインする](github.ja.md#組織の承認が要る)）

7. **入れるはずのアカウントで、通しで 1 回サインインします**
   - 「新しいワークスペースを作成しました」という画面に着いたら、そのメールアドレスはこの配備が初めて見るアドレスです。人が間違った場所で作業を始める前に気づくための画面です
   - メールアドレスの違うアカウント同士を、あとから 1 つにまとめることはできません。両方のアカウントを持っている人は、**設定 → 個人設定 → アカウント** の「**サインイン方法を追加**」で自分で追加できます。追加できるのは同じメールアドレスを名乗る方法だけで、その方法の組織やドメインの条件も満たす必要があります

それでもうまくいかないときは [ログインできない](../diagnose/cannot-login.ja.md) を見てください。

## 関連

- [テナント独自のサインインを承認する](tenant-idp.ja.md)
- [ログインできない](../diagnose/cannot-login.ja.md)
- [サインインの方法を決める](../../admin/access/signin-methods.ja.md)（テナント管理者の手順）
- [ロール — 誰が何をできるか](../../ref/roles.ja.md)
