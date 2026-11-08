---
audience: "エージェントに GitHub や Bitbucket のプルリクエストを扱ってもらいたい人"
source_of_truth: "セッションの gh・bkt コマンド（Agent Fleet のラッパー）と、Console の設定 >「Git ホスティング」"
---

# GitHub・Bitbucket の CLI を使う

[English](gh-bkt.md) | 日本語

GitHub か Bitbucket Cloud を接続しておくと、エージェントが `gh` や `bkt` でプルリクエストを作り、読み、コメントし、
CI の様子を見られます。セッションの中で `gh auth login` や `bkt auth login` をする必要はありません。

## 全体像

| あなたがやること | エージェントがやること |
|---|---|
| GitHub か Bitbucket を接続する | `gh`（GitHub）や `bkt`（Bitbucket Cloud）を、ログインなしで使う |
| やってほしいことを頼む | あなたのアカウントとしてプルリクエストを作る・コメントする |
| 権限が足りないと言われたら、接続を直す | 権限が足りないときは、そう伝えて止まる |

`gh` と `bkt` は、呼び出しのたびに保存済みの接続を読みます。期限のあるトークンは、git と同じように使うときに更新されます。
エージェントは最初から使い方を知っているので、コマンドを教える必要はありません。

## 始める前に

**⚙設定 →「Git ホスティング」** で GitHub か Bitbucket を接続します。git の clone・push と同じ接続です。
手順は [リポジトリと git](../../member/06-code.ja.md#git-プロバイダを接続する) にあります。

| 接続 | 使えるコマンド |
|---|---|
| GitHub（github.com） | `gh` |
| Bitbucket Cloud（bitbucket.org） | `bkt` |

- GitHub Enterprise と Bitbucket Data Center は、設定画面からは接続できません。[自分で設定します](#自分の設定で使う)
- Bitbucket を **「アプリトークンで接続」** でつなぐときは、`bkt` で使う分のスコープもトークンに付けておきます（[Bitbucket のスコープ](#bitbucket-のスコープ)）

## エージェントに頼む

ふだんどおりにお願いするだけです。

> この変更でプルリクエストを作って。説明は日本語で

> PR 123 のレビューコメントを読んで、直して push して

> main のパイプラインが落ちている理由を調べて

エージェントは次のようなことができます。

| やること | GitHub | Bitbucket Cloud |
|---|---|---|
| プルリクエストを作る | `gh pr create` | `bkt pr create` |
| プルリクエストを一覧する・読む | `gh pr list`、`gh pr view 123` | `bkt pr list`、`bkt pr view 123` |
| コメントを読む | `gh pr view 123 --comments` | `bkt pr comments 123` |
| CI・パイプラインを見る | `gh run list`、`gh pr checks 123` | `bkt pipeline` |
| そのほかの API を呼ぶ | `gh api …` | — |

- **書き込みはあなたの名前で残ります。** できることは、あなたのアカウントと接続に許されている範囲までです
- **Bitbucket では、MCP サーバーより `bkt` を使います。** エージェントにはそう伝えてあります。CLI は呼び出しのたびに接続を読むので、
  ログインが切れてセッションを再起動する、ということがありません

## GitHub のプルリクエストの説明を直す

テナントが組み込みの GitHub OAuth アプリで接続しているとき、トークンのスコープは `repo` と `workflow` だけです。
このトークンでは、自分の個人リポジトリでも `gh pr edit` が失敗することがあります。

```text
GraphQL: … The 'login' field requires one of the following scopes: ['read:org']
```

エージェントには、この場合は REST API で直すよう伝えてあります。自分で直すときも同じです。

```sh
gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -F body=@<ファイル>
gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -f title="新しいタイトル"
```

- エラーのリンク先（トークンの設定ページ）は、このトークンには当てはまりません
- GitHub App で接続しているときや、`read:org` を付けたアクセストークンを貼ったときは、このエラーは出ません
- `gh pr edit` は、ほかの理由（チームのレビュアーが見えないなど）で失敗することもあります

## Bitbucket のスコープ

`bkt` は、接続に与えられた権限の範囲でしか動きません。clone と push に要る権限だけでは、プルリクエストやパイプラインは扱えません。

| やること | OAuth で接続したとき | API トークンで接続したとき |
|---|---|---|
| プルリクエスト（作成・コメント） | Pull requests: Read and Write | `read:pullrequest:bitbucket`、`write:pullrequest:bitbucket` |
| パイプラインを見る | Pipelines: Read | `read:pipeline:bitbucket` |
| パイプラインを開始・停止する | Pipelines: Write | `write:pipeline:bitbucket` |

- **OAuth の権限は、テナント管理者が** Bitbucket の OAuth コンシューマーに設定します。足したあとは、Bitbucket をつなぎ直すと新しい権限になります
- **API トークンのスコープは、トークンを作るときに**付けます。write は read を含みません。足りないときは、スコープを付けた新しいトークンを貼り直します
- 同じ権限で、課題管理の詳細から Bitbucket のプルリクエストに「**作業の報告をコメントする**」が使えます。
  このボタンは、接続がプルリクエストの書き込み権限を持つとわかっているときだけ出ます

## 自分の設定で使う

自分で設定したものがあれば、`gh` と `bkt` はそちらを使います。

| こうしているとき | 動き |
|---|---|
| `GH_TOKEN` か `GITHUB_TOKEN` を設定した | `gh` はそのトークンを使い、接続は使いません |
| GitHub Enterprise のホストに `gh auth login` した | そのホストには、あなたのログインが使われます |
| `bkt auth login` をした、または `BKT_HOST`・`BKT_TOKEN` を設定した | `bkt` はあなたの設定どおりに動き、接続は使いません |

- github.com に `gh auth login` していても、GitHub を接続していれば `gh` は接続のトークンを使います
- `bkt` の設定は、Data Center のホストだけのつもりでも bitbucket.org にも効きます。bitbucket.org で接続を使いたいときは、設定ファイル
  （`~/.config/bkt/config.yml`）を消してください

## 困ったとき

### `bkt` が「lacks a scope for this command」と出す

接続に、そのコマンドの権限がありません。メッセージに、足りない権限の名前が出ます。

- **OAuth の接続**：テナント管理者に権限を足してもらい、⚙設定 →「Git ホスティング」で Bitbucket をつなぎ直します
- **API トークンの接続**：スコープを付けた新しいトークンを作って、貼り直します

別のトークンでやり直しても直りません。プルリクエストのスコープがないトークンでは、リポジトリの一覧は取れても `bkt pr list` が `403 Forbidden` になります。

### `gh` や `bkt` がログインしていないと言う

接続がないか、使えなくなっています。⚙設定 →「Git ホスティング」で、カードが「接続済み」かを見てください。

- GitHub のカードに「GitHub の接続を更新できませんでした。…」と出ているときは、切断してからもう一度接続します
- Bitbucket をメールアドレスではないユーザー名（以前のアプリパスワード）で接続しているときは、git は動いても `bkt` は動きません。
  「**アプリトークンで接続**」でつなぎ直してください
- [自分の設定](#自分の設定で使う)が残っていると、接続は使われません

### 「作業の報告をコメントする」が Bitbucket に出ない

接続がプルリクエストの書き込み権限を持つと、まだわかっていません。[Bitbucket のスコープ](#bitbucket-のスコープ)のとおり権限を足し、
Bitbucket をつなぎ直してください。Bitbucket の課題（issue）は一覧に出ないので、コメントできるのはプルリクエストだけです。

## 関連

- [リポジトリと git](../../member/06-code.ja.md)：GitHub・Bitbucket の接続と、clone・push
- [課題管理サービスを接続する](issue-tracker.ja.md)：プルリクエストや課題を左ペインに並べる
- [コマンド](../../ref/cli/README.ja.md#サインイン済みの-cli)：接続を使う CLI の一覧
