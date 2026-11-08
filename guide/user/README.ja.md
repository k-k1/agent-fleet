---
audience: "Console からエージェントを動かす人"
source_of_truth: "Console そのもの（画面と食い違ったら画面が正）"
---

# やりたいことから探す

[English](README.md) | 日本語

1 ページに 1 つの「やりたいこと」をまとめています。今のガイドの章を、順にここへ書き直しています。
まだここにないことは [Agent Fleet を使う](../member/README.ja.md) の章にあります。

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

## ワークスペース

| ページ | できること |
|---|---|
| [ワークスペースを作り直す](workspace/recreate.ja.md) | 壊れた環境を、最新のイメージで作り直す |
| [マシンの大きさを変える](workspace/machine.ja.md) | マシンの大きさと、メモリ・ディスクの使い方を確かめ、ディスクの空きを作る |
| [ツールチェーンを選ぶ](workspace/toolchains.ja.md) | Node.js・Java・Go のバージョンとタイムゾーンを選ぶ |
| [データベースを使うテストを動かす](workspace/databases.ja.md) | ワークスペースの中の PostgreSQL と MySQL でテストを動かす |
| [AWS のコードを手元のエミュレーターで試す](workspace/aws-emulator.ja.md) | 本物のアカウントに触らずに、AWS のコードを試す |
| [設定を書き出す・取り込む](workspace/export-import.ja.md) | 自分の設定を、別の環境やアカウントへ持っていく |
| [稼働時間とクラウド費用を見る](workspace/cost.ja.md) | ワークスペースがいつ動いていて、いくらかかっているかを見る |
