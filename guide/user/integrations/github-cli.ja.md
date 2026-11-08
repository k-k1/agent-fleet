---
audience: "エージェントに GitHub のプルリクエストを扱ってもらいたい人"
source_of_truth: "セッションの gh コマンド（Agent Fleet のラッパー）と、Console の設定 >「Git ホスティング」"
---

# GitHub の CLI を使う

[English](github-cli.md) | 日本語

GitHub を接続しておくと、エージェントが `gh`（[GitHub CLI](https://cli.github.com/)）でプルリクエストを作り、読み、コメントし、
CI の様子を見られます。セッションの中で `gh auth login` をする必要はありません。

## 全体像

| あなたがやること | エージェントがやること |
|---|---|
| GitHub を接続する | `gh` を、ログインなしで使う |
| やってほしいことを頼む | あなたのアカウントとしてプルリクエストを作る・コメントする |
| 権限が足りないと言われたら、接続を直す | 権限が足りないときは、そう伝えて止まる |

`gh` は、呼び出しのたびに保存済みの接続を読みます。期限のあるトークンは、git と同じように使うときに更新されます。
エージェントは最初から使い方を知っているので、コマンドを教える必要はありません。

## 始める前に

**⚙設定 →「Git ホスティング」** で GitHub（github.com）を接続します。git の clone・push と同じ接続です。
手順は [リポジトリと git](../../member/06-code.ja.md#git-プロバイダを接続する) にあります。

- GitHub Enterprise は、設定画面からは接続できません。[自分で設定します](#自分の設定で使う)

## エージェントに頼む

ふだんどおりにお願いするだけです。

> この変更でプルリクエストを作って。説明は日本語で

> PR 123 のレビューコメントを読んで、直して push して

> main の CI が落ちている理由を調べて

エージェントは次のようなことができます。

| やること | 例 |
|---|---|
| プルリクエストを作る | `gh pr create` |
| プルリクエストを一覧する・読む | `gh pr list`、`gh pr view 123` |
| コメントを読む | `gh pr view 123 --comments` |
| CI を見る | `gh run list`、`gh pr checks 123` |
| そのほかの API を呼ぶ | `gh api …` |

- **書き込みはあなたの名前で残ります。** できることは、あなたのアカウントと接続に許されている範囲までです

## プルリクエストの説明を直す

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
- `read:org` を付けたアクセストークンを自分で貼っていれば、このエラーは出ません
- `gh pr edit` は、ほかの理由（チームのレビュアーが見えないなど）で失敗することもあります

## 自分の設定で使う

自分で設定したものがあれば、`gh` はそちらを使います。

| こうしているとき | 動き |
|---|---|
| `GH_TOKEN` か `GITHUB_TOKEN` を設定した | `gh` はそのトークンを使い、接続は使いません |
| GitHub Enterprise のホストに `gh auth login` した | そのホストには、あなたが `gh auth login` したアカウントが使われます |

- github.com に `gh auth login` していても、GitHub を接続していれば `gh` は接続のトークンを使います

## 困ったとき

### `gh` がログインしていないと言う

接続がないか、使えなくなっています。⚙設定 →「Git ホスティング」で、GitHub のカードが「接続済み」かを見てください。

- カードに「GitHub の接続を更新できませんでした。…」と出ているときは、切断してからもう一度接続します
- `GH_TOKEN` か `GITHUB_TOKEN` が残っていると、接続は使われません

## 関連

- [Bitbucket の CLI を使う](bitbucket-cli.ja.md)：Bitbucket Cloud で同じことをする
- [リポジトリと git](../../member/06-code.ja.md)：GitHub の接続と、clone・push
- [課題管理サービスを接続する](issue-tracker.ja.md)：プルリクエストや Issue を左ペインに並べる
- [コマンド](../../ref/cli/README.ja.md#サインイン済みの-cli)：接続を使う CLI の一覧
