---
audience: "Console からエージェントを動かす人"
source_of_truth: "Console そのもの（画面と食い違ったら画面が正）"
---

# やりたいことから探す

[English](README.md) | 日本語

1 ページに 1 つの「やりたいこと」をまとめています。今のガイドの章を、順にここへ書き直しています。
まだここにないことは [Agent Fleet を使う](../member/README.ja.md) の章にあります。

## セッション

| ページ | できること |
|---|---|
| [セッションに返事をする](sessions/reply.ja.md) | 会話を読み、質問・プラン・許可のカードに答え、続きの指示を送る |
| [送る内容を待たせる・ターンを止める](sessions/queue.ja.md) | ターン中の指示を溜めて順に送り、キューがあるときにターンを止める |
| [プランにコメントする・レビューさせる](sessions/plans.ja.md) | プランの箇所にコメントを付けて返し、別のエージェントにレビューさせる |
| [スキルやコマンドを呼び出す](sessions/skills.ja.md) | セッションで呼べるスキルやコマンドを、一覧から選んで入力欄に入れる |
| [過去のメッセージから分岐する](sessions/branch-message.ja.md) | 自分の過去の発言から、そこまでの会話を持った新しいセッションを始める |
| [セッションが変えた内容を見る](sessions/changes.ja.md) | セッションが実際に触ったファイルを一覧で見る |
| [会話を Markdown で保存する](sessions/export.ja.md) | 会話全体を Markdown のファイルとしてダウンロードする |
| [回答を自分の言語で読む](sessions/translate.ja.md) | 自分の読まない言語の回答を、ターンを使わずに訳して読む |

## 連携

| ページ | できること |
|---|---|
| [AWS SSO を使う](integrations/aws-sso.ja.md) | エージェントに、あなたの権限で AWS のコマンドを実行してもらう |
| [SSM で EC2 にログインする](integrations/aws-ssm.ja.md) | EC2 に、ブラウザの端末からログインする |
| [Google Cloud を使う](integrations/gcloud.ja.md) | エージェントに、あなたの権限で Google Cloud のコマンドを実行してもらう |
| [セッションに MCP サーバーを追加する](integrations/mcp-servers.ja.md) | 社内のツールや外部のサービスを、MCP サーバーとしてエージェントに使わせる |
| [課題管理サービスを接続する](integrations/issue-tracker.ja.md) | Jira の課題や GitHub の Issue を左ペインに並べ、そこからセッションを始める |
| [Jira を使う](integrations/jira.ja.md) | エージェントに、Jira の課題を読んだり起票したりしてもらう |
| [Confluence を使う](integrations/confluence.ja.md) | エージェントに、Confluence のページを探したり書いたりしてもらう |
| [GitHub の CLI を使う](integrations/github-cli.ja.md) | エージェントに、`gh` で GitHub のプルリクエストを作ったり読んだりしてもらう |
| [Bitbucket の CLI を使う](integrations/bitbucket-cli.ja.md) | エージェントに、`bkt` で Bitbucket のプルリクエストを作ったり、パイプラインを見たりしてもらう |
| [Discord・Slack とつなぐ](integrations/chat-bridge.ja.md) | セッションの通知を Discord や Slack で受け、返信やボタンで答える |
| [監視ツールとつなぐ](integrations/ops-monitoring.ja.md) | PagerDuty・Grafana・CloudWatch・AWS を見ながら、アシスタントにインシデントの整理を手伝ってもらう |

## 画像

| ページ | できること |
|---|---|
| [画像ギャラリーを使う](images/gallery.ja.md) | フォルダの画像をカードで並べて見比べる |
| [画像を生成する](images/generate.ja.md) | エージェントを使わずに、配備のエンジンで絵をまとめて作る |
| [スタジオで画像を仕上げる](images/studio.ja.md) | エージェントと話しながら、画像生成の下書きを直す |
| [セッションに画像を作らせる](images/from-session.ja.md) | 会話の中で、エージェントに画像を作ってもらう |

## ワークスペース

| ページ | できること |
|---|---|
| [ワークスペースを作り直す](workspace/recreate.ja.md) | 壊れた環境を、最新のイメージで作り直す |
| [マシンと使用状況を見る](workspace/machine.ja.md) | マシンの大きさと、メモリ・ディスクの使い方を確かめ、ディスクの空きを作る |
| [ツールチェーンを選ぶ](workspace/toolchains.ja.md) | Node.js・Java・Go のバージョンとタイムゾーンを選ぶ |
| [データベースを使うテストを動かす](workspace/databases.ja.md) | ワークスペースの中の PostgreSQL と MySQL でテストを動かす |
| [AWS のコードを手元のエミュレーターで試す](workspace/aws-emulator.ja.md) | 本物のアカウントに触らずに、AWS のコードを試す |
| [設定を書き出す・取り込む](workspace/export-import.ja.md) | 自分の設定を、別の環境やアカウントへ持っていく |
| [稼働時間とクラウド費用を見る](workspace/cost.ja.md) | ワークスペースがいつ動いていて、いくらかかっているかを見る |
