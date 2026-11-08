---
audience: "エージェントに Confluence のページを扱ってもらいたい人"
source_of_truth: "セッションの confluence コマンド（Agent Fleet のラッパー）と、Console の設定 >「課題管理」"
---

# Confluence を使う

[English](confluence.md) | 日本語

Jira を接続しておくと、同じ Atlassian の接続で、エージェントが `confluence` コマンドで Confluence のページを探し、読み、書けます。
Confluence のための設定やログインは要りません。

## 全体像

| あなたがやること | エージェントがやること |
|---|---|
| Jira を接続する | `confluence` コマンドでページを探す・読む・作る・更新する |
| やってほしいことを頼む | あなたの Atlassian アカウントとして書き込む |
| 接続が切れたらつなぎ直す | 接続が使えないときは、そう伝えて止まる |

`confluence` は、どのエージェントのセッションでも使えます。OAuth のトークンは呼び出しのたびに確かめられ、期限が近づくと更新されるので、そのためにセッションを再起動することはありません。

## 始める前に

**⚙設定 →「課題管理」** で Jira を接続します。手順は [課題管理サービスを接続する](issue-tracker.ja.md#jira-を接続する) にあります。

- API トークンで接続していれば、ほかに準備は要りません
- OAuth で接続しているときは、テナントの Atlassian アプリに Confluence のスコープが要ります。足りないと Jira のカードに注意が出ます

| Jira のカードの表示 | 使えないもの |
|---|---|
| Confluence: 利用できません。 | `confluence` のすべてのコマンド |
| Confluence: 検索とコメントは使えますが… | ページの閲覧・作成・更新 |

どちらも、テナント管理者がアプリにスコープを追加したあとで、あなたが Jira をつなぎ直すと使えるようになります。

## エージェントに頼む

ふだんどおりにお願いするだけです。

> Confluence で「リリース手順」のページを探して、要点をまとめて

> この設計メモを DEV スペースに新しいページとして書いて

> ページ 123456 の「既知の問題」の節に、今回の不具合を書き足して

エージェントは次のようなコマンドを使います。

| やること | 例 |
|---|---|
| ページを探す | `confluence search "リリースノート"` |
| ページを読む | `confluence read PAGE_ID --format markdown` |
| スペースを調べる | `confluence space-lookup KEY` |
| ページを作る | `confluence create "タイトル" SPACE --content "…"` |
| ページを更新する | `confluence update PAGE_ID …` |

- **書き込みはあなたの名前で残ります。** できることは、あなたの Atlassian アカウントに許されている範囲までです
- **Markdown で読み書きできます。** 日本語・絵文字・表・リスト、日本語の隣の `code` や **太字** も、そのまま保たれます
- **ほかの人の編集を上書きしません。** 読んだあとでほかの人が同じページを変えていると、更新は断られます。エージェントはページを読み直してからやり直します

## 自分で使う

ターミナルからも同じように使えます。`confluence init` は要りません。

```sh
confluence search "リリースノート"
confluence --help
```

## 困ったとき

### 「Reconnect Jira in Settings」と出る

接続の期限が切れたか、取り消されているか、Confluence のスコープが足りません。
**⚙設定 →「課題管理」** の Jira のカードを確かめ、Jira をつなぎ直してください。

### 「may or may not have been …」と出る

送ったあとで通信が切れ、ページができたかどうかわかりません。書き込みは自動ではやり直しません。
同じタイトルで検索するか、ページを読んで確かめてから、もう一度実行してください。

### 「410」で失敗するコマンドがある

次のコマンドは、Atlassian が廃止した古い API を使っているため、失敗することがあります。

- `move`・`delete`・`children`・`find`・`spaces`・`copy-tree`
- ページの版、プロパティ（front matter）

フォルダは `create --type folder` で作れません。

### 「--jq is not available」と出る

保存済みの接続では `confluence api --jq` を使えません。代わりに `confluence api … | jq '…'` のようにパイプで渡してください。

### Confluence Data Center を使う

設定画面からは接続できません。`confluence init`、`--profile`、`CONFLUENCE_DOMAIN` や `CONFLUENCE_API_TOKEN` などの変数で、自分で設定してください。
そのときの `confluence` は、保存済みの接続を使わず、あなたの設定どおりに動きます。

## 関連

- [課題管理サービスを接続する](issue-tracker.ja.md)：Jira の接続（Confluence も同じ接続を使います）
- [Jira を使う](jira.ja.md)：同じ接続で Jira の課題を扱う
