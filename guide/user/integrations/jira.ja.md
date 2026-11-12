---
audience: "エージェントに Jira の課題を扱ってもらいたい人"
source_of_truth: "セッションの jira コマンド（Agent Fleet のラッパー）と、Console の設定 >「課題管理」"
---

# Jira を使う

[English](jira.md) | 日本語

Jira を接続しておくと、エージェントが `jira` コマンドで課題を読み、起票し、コメントし、ステータスを進められます。
どのエージェントでも、セッションの中でのログインは要りません。

## 全体像

| あなたがやること | エージェントがやること |
|---|---|
| Jira を接続する | `jira` コマンドで課題を読む・作る・更新する |
| やってほしいことを頼む | あなたの Jira アカウントとして書き込む |
| 接続が切れたらつなぎ直す | 接続が使えないときは、そう伝えて止まる |

`jira` は、接続した Jira アカウントの権限で動きます。エージェントは最初から使い方を知っているので、コマンドを教える必要はありません。
OAuth のトークンは呼び出しのたびに確かめられ、期限が近づくと更新されるので、そのためにセッションを再起動することはありません。

## 始める前に

**⚙設定 →「課題管理」** で Jira を接続します。手順は [課題管理サービスを接続する](issue-tracker.ja.md#jira-を接続する) にあります。

- 接続は OAuth でも API トークンでもかまいません
- Jira Cloud（`….atlassian.net`）だけです。Jira Data Center は[自分で設定します](#jira-data-center-を使う)

## エージェントに頼む

ふだんどおりにお願いするだけです。

> PROJ-123 を読んで、直し方の案をコメントして

> 今の作業内容で PROJ に課題を起票して。親は PROJ-100 で

> PROJ-123 を「In Progress」に進めて、担当を自分にして

エージェントは次のようなことができます。

| やること | 例 |
|---|---|
| 課題を読む・探す | `jira issue view PROJ-1`、`jira issue list -p PROJ` |
| 課題を作る | `jira issue create -p PROJ`（親は `-P PROJ-100`） |
| コメントする | `jira issue comment add PROJ-1 "…"` |
| コメントを直す・消す | `jira issue comment edit PROJ-1 10001 -b "…"`、`jira issue comment delete PROJ-1 10001` |
| ファイルを添付する・添付を一覧・ダウンロード・削除する | `jira issue attachment add PROJ-1 shot.png`、`jira issue attachment list PROJ-1`、`jira issue attachment download PROJ-1 10010`、`jira issue attachment delete PROJ-1 10010` |
| ステータスを進める | `jira issue move PROJ-1 "In Progress"` |
| 担当者を決める | `jira issue assign PROJ-1 <ユーザー>` |

- **書き込みはあなたの名前で残ります。** できることは、あなたの Jira アカウントに許されている範囲までです
- **コメントや説明文は Markdown で書けます。** 日本語の隣の `code` や **太字**、リスト、表も、書いたとおりに Jira に表示されます
- **コメントの ID** は `jira issue view PROJ-1 --raw` の `.fields.comment.comments[].id` でわかります
- **直したり消したりするコメントは決まっています。** エージェントには、そのセッションで自分が書いたコメントか、あなたが指定したコメントだけを
  編集・削除するよう伝えてあります
- **添付はファイルとして付きます。** 画像は課題に添付されるだけで、コメントや説明文の中には入りません。
  エージェントには、そのセッションで自分が付けた添付か、あなたが指定した添付だけを削除するよう伝えてあります
- **作った課題の親は確かめてください。** 親を付けて作ると、Agent Fleet が親を確実に送るように調整しますが、すべての種類のプロジェクトでは確かめられていません

## ボード・スプリント・エピックを使う

ボード・スプリント・エピックを扱うには、プロジェクトとボードを一度設定しておきます。エージェントに頼めば実行してくれます。

```sh
jira init --project PROJ --board "PROJ board"
```

- 正確なボード名は `jira board list -p PROJ` でわかります
- 設定をやり直すときは `--force` を付けます
- 設定しないときは、コマンドごとに `-p PROJ` を付ければ課題は扱えます

2 つ目のボードは、専用の設定ファイルを作って使います。接続が使われるのは `~/.config/agent-fleet/jira/` の中の設定ファイルだけです。

```sh
jira init -c ~/.config/agent-fleet/jira/other-board.yml --project PROJ --board "Other board"
jira -c ~/.config/agent-fleet/jira/other-board.yml sprint list
```

> [!NOTE]
> OAuth で接続しているときは、テナントの Atlassian アプリに Jira Software のスコープが要ります。
> 足りないと、設定の「課題管理」の Jira カードに「**ボード・スプリント・エピック: 利用できません。**」と出て、それらのコマンドと `jira init` が失敗します。
> テナント管理者に追加してもらってから、Jira をつなぎ直してください。

## Atlassian の MCP ツールを使わせたいとき

エージェントは、ふだんは `jira` を使い、MCP ツールは `jira` にできないことにだけ使います。
MCP ツールを優先させたいときは、**⚙設定 →「エージェントへの指示」** に書いておきます。

> Jira は `jira` CLI ではなく Atlassian MCP ツールを使うこと。

コネクタを持つエージェントはこれに従い、持たないエージェントは `jira` を使い続けます。
MCP のコネクタはエージェントごとに用意が要り、ログインが切れるとセッションの再起動が要ります。

## 自分で使う

ターミナルからも同じように使えます。

```sh
jira issue list -p PROJ
jira issue view PROJ-1 --comments 5
```

- `jira init` でトークンを聞かれることはありません
- `--debug` を付けた実行には、接続を使いません（デバッグの出力に認証の情報が出てしまうためです）

## 困ったとき

### 「the Jira connection is unusable」と出る

接続の期限が切れたか、取り消されています。**⚙設定 →「課題管理」** で Jira をつなぎ直してください。

### 課題を作ったら終了コード 3 で終わった

課題はできていて、説明文だけが設定できていません。**もう一度作らないでください。**
説明文は `jira issue edit PROJ-1 --no-input -b "…"` で設定し直します。
`issue edit -b` が終了コード 3 で終わったときも同じで、ほかの変更は反映済みです。

### 書き込みが終了コード 124 で終わった

Jira から応答がなく、打ち切られました。端末のない実行（エージェントの実行など）では、書き込みは約 2 分で打ち切られます。課題やコメントができている可能性があるので、Jira を確かめてから実行し直してください。

- `issue create` のときは、直近 15 分に同じ件名で作った課題が「possible matches」として出ます
- 読み取りのコマンドは、通信が切れたときに自動で最大 2 回やり直します。書き込みはやり直しません

### 「this config has no issue types」と出る

`jira issue create` に要るプロジェクトの課題タイプを、`jira` が取得できていません。一緒に出るヒントのとおりに `jira init` を実行してください。

### 担当者を指定するとプロジェクトを聞かれる

担当者を探すには、プロジェクトが要ります。`-p PROJ` を付けるか、`jira init --project PROJ --board "PROJ board"` を実行してください。
`jira issue assign PROJ-1 <ユーザー>` は、課題キーからプロジェクトを補います。

### ボード・スプリント・エピックが認可のエラーになる

OAuth の接続に Jira Software のスコープがありません。[上の注意](#ボードスプリントエピックを使う)のとおり、テナント管理者に頼んでください。
このときは、`jira` が表示するリンクもサイトではなく `api.atlassian.com` を指します。

### Jira Data Center を使う

設定画面からは接続できません。自分の `--server` を付けた `jira init`、自分で用意した設定ファイル、または `JIRA_API_TOKEN` で設定してください。
そのときの `jira` は、保存済みの接続を使わず、あなたの設定どおりに動きます。

## 関連

- [課題管理サービスを接続する](issue-tracker.ja.md)：Jira の接続と、左ペインの一覧
- [Confluence を使う](confluence.ja.md)：同じ接続で Confluence のページを扱う
