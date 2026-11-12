---
audience: "af-watch-hook のオプション・出力・細かい動きを調べたい人"
source_of_truth: "af-watch-hook --help（ワークスペースの workspace-agent に入っているもの）"
---

# af-watch-hook

[English](af-watch-hook.md) | 日本語

プライベートな GitHub / Bitbucket Cloud のリポジトリに Webhook を足し、そのプルリクエストの見張りに、次のポーリングを待たず合図が届くようにします。
背景は [Webhook で早く知る](../../user/fleet/pr-feedback.ja.md#webhook-で早く知る) にあります。

## 書式

```sh
af-watch-hook add github|bitbucket <owner/name>
af-watch-hook list
af-watch-hook rm <id>
af-watch-hook --help
```

## コマンド

| コマンド | できること |
|---|---|
| `add` | Control Plane にフックを作り、あなた自身の接続でリポジトリに登録する。登録が断られたら Control Plane のフックも消す |
| `list` | あなたのフックを表示する（ID・種別・リポジトリ・作成時刻） |
| `rm` | リポジトリの Webhook を消し、続いて Control Plane のフックを消す |

## `add` が登録するもの

| | GitHub | Bitbucket Cloud |
|---|---|---|
| 本文 | JSON | JSON |
| 署名 | `X-Hub-Signature-256`（フックの秘密による HMAC-SHA256） | `X-Hub-Signature`（同じ） |
| 出来事 | `pull_request`、`pull_request_review`、`pull_request_review_comment`、`pull_request_review_thread`、`issue_comment`、`check_run`、`check_suite`、`workflow_run` | `pullrequest:*`（作成・更新・承認・マージ・却下・コメント・変更依頼） |
| 使う接続 | GitHub の接続 | Bitbucket の接続（OAuth または API トークン） |

## 動き

- **秘密は表示しない。** Control Plane が一度だけ返し、リポジトリの Webhook 設定にだけ渡します
- **リポジトリの管理者権限が要る。** 足りないと `add` は終了コード 1 で終わり、何を付与すればよいかを伝え、Control Plane にフックを残しません
- **何度実行しても同じ。** すでにフックのあるリポジトリへの `add` は何も変えず、既存の ID を表示します
- **`rm` は必ず Control Plane のフックを消す。** リポジトリ側が失敗しても（トークンがない、管理者権限がない）消し、手で消す Webhook の URL を警告で示します。その Webhook はもう検証を通らないので、消すまでの間も害はありません
- 1 人あたり最大 20 件

## 終了コード

| コード | 意味 |
|---|---|
| 0 | 完了（「すでにある」を含む） |
| 1 | 失敗。理由はメッセージに出る |
| 2 | 使いかたの誤り |
