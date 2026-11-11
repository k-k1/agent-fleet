---
audience: "Console からエージェントを動かす人"
source_of_truth: "Console そのもの（画面と食い違ったら画面が正）"
---

# やりたいことから探す

[English](README.md) | 日本語

1 ページに 1 つの「やりたいこと」をまとめています。今のガイドの章を、順にここへ書き直しています。
まだここにないことは [Agent Fleet を使う](../member/README.ja.md) の章にあります。

## はじめに

| ページ | できること |
|---|---|
| [Agent Fleet でできること](start/overview.ja.md) | 何ができて、どのページから読めばいいかを知る |
| [最初のセッションを始める](start/quickstart.ja.md) | サインインしてから、最初の返事を受け取るまでを進める |
| [用語と全体像](start/concepts.ja.md) | テナント・ワークスペース・セッション・エージェントの関係を知る |
| [画面の見かた](start/console-tour.ja.md) | 上部バー・ワークスペース操作バー・左ペイン・メイン領域のどこに何があるかを知る |
| [キーボードとコマンドパレットで操作する](start/shortcuts.ja.md) | ショートカットとコマンドパレットで、マウスを使わずに動かす |
| [画面を分けて並べる](start/panes.ja.md) | セッションやファイルをペインに並べ、配置を選ぶ |
| [スマートフォンで使う](start/phone.ja.md) | スマートフォンから進み具合を見て、返事をする |
| [一日の終わりにワークスペースを止める](start/end-of-day.ja.md) | 作業を残したままワークスペースを止め、「要再起動」に対応する |
| [ターミナルを使わない人のための入口](start/without-terminal.ja.md) | チャット・閲覧・進み具合の確認だけで使う |

## セッション

| ページ | できること |
|---|---|
| [セッションを始める](sessions/start.ja.md) | 「はじめる」から、エージェント・モデル・最初のプロンプトを決めてセッションを起動する |
| [ワークツリーで並行して作業する](sessions/worktrees.ja.md) | セッションごとに専用の作業コピーを作り、同じリポジトリで並行して作業する |
| [Issue やチケットから始める](sessions/from-issues.ja.md) | 課題管理の一覧の課題やプルリクエストから、セッションを始める |
| [セッションの状態を読む](sessions/status.ja.md) | 行の印と通知で、どのセッションが返事を待っているかを知る |
| [セッションに返事をする](sessions/reply.ja.md) | 会話を読み、質問・プラン・許可のカードに答え、続きの指示を送る |
| [送る内容を待たせる・ターンを止める](sessions/queue.ja.md) | ターン中の指示を溜めて順に送り、キューがあるときにターンを止める |
| [プランにコメントする・レビューさせる](sessions/plans.ja.md) | プランの箇所にコメントを付けて返し、別のエージェントにレビューさせる |
| [スキルやコマンドを呼び出す](sessions/skills.ja.md) | セッションで呼べるスキルやコマンドを、一覧から選んで入力欄に入れる |
| [過去のメッセージから分岐する](sessions/branch-message.ja.md) | 自分の過去の発言から、そこまでの会話を持った新しいセッションを始める |
| [セッションが変えた内容を見る](sessions/changes.ja.md) | セッションが実際に触ったファイルを一覧で見る |
| [会話を Markdown で保存する](sessions/export.ja.md) | 会話全体を Markdown のファイルとしてダウンロードする |
| [回答を自分の言語で読む](sessions/translate.ja.md) | 自分の読まない言語の回答を、ターンを使わずに訳して読む |
| [セッションを止める・再開する](sessions/stop-resume.ja.md) | セッションをいったん止めて、あとで止めたところから続ける |
| [終わったら止める・起こしたままにする](sessions/auto-stop.ja.md) | 作業が終わったら止まるようにする。長い作業を自動停止させない |
| [終わったセッションを片付ける](sessions/cleanup.ja.md) | セッションをアーカイブし、要らない作業コピーやブランチを消す。消したものはごみ箱から戻す |
| [使いすぎたセッションを予算で止める](sessions/budget.ja.md) | 推定の費用が予算に達したら、セッションを止める |
| [別のセッションやエージェントに引き継ぐ](sessions/handoff.ja.md) | 会話を新しいセッションや別の種類のエージェントで続ける |

## コード

| ページ | できること |
|---|---|
| [Git のサービスを接続する](code/connect-git.ja.md) | GitHub や Bitbucket を接続して、private リポジトリをクローン・push する |
| [リポジトリをクローンする](code/clone.ja.md) | リポジトリをワークスペースに持ってくる。何もないところから始める |
| [内蔵の Git を使う](code/internal-git.ja.md) | 外部のサービスを使わずに、テナントの中にリポジトリを置く |
| [リポジトリからセッションを起動する](code/launch-from-repo.ja.md) | リポジトリの行からセッションを起動し、行のバッジで状態を読む |
| [Git Flow を初期化する](code/git-flow.ja.md) | 課題から作るブランチを、git-flow の流儀にそろえる |
| [コミットグラフでコミットする](code/commit.ja.md) | 履歴と差分を見て、端末を開かずにコミットする |
| [Subversion のリポジトリを使う](code/svn.ja.md) | Subversion のリポジトリをチェックアウトして、セッションで作業する |

## ファイルとターミナル

| ページ | できること |
|---|---|
| [ファイルを見る・ダウンロードする](files/browse.ja.md) | ワークスペースのファイルをたどって開き、ファイルやフォルダをダウンロードする |
| [ドキュメントや図を表示する](files/view.ja.md) | Markdown・スライド・`.drawio` の図・PDF・Office の文書を Console で読む |
| [ファイルを編集する](files/edit.ja.md) | 開いたファイルをその場で直して保存する。AI に書き換えを提案させる |
| [ファイルをセッションに渡す](files/send.ja.md) | 選んだ範囲を、指示を添えてセッションやアシスタントに送る |
| [ターミナルを使う](files/terminal.ja.md) | ブラウザの端末でコピー・貼り付けをし、打鍵の遅れを見て、フォントを変える |

## プレビュー

| ページ | できること |
|---|---|
| [起動した Web アプリを見る](preview/browser-pane.ja.md) | ワークスペースで起動した Web アプリを、ブラウザペインや軽量プレビューで開く |
| [プレビュー用のサブドメインを使う](preview/subdomains.ja.md) | アプリをルート直下の URL で開き、同僚やテナントの外の人に見せる |
| [プレビューで Google ログインを試す](preview/dev-signin.ja.md) | プレビューの URL や自分の PC のアプリで、Google ログインを試す |
| [エージェントが開いたブラウザを操作する](preview/agent-browser.ja.md) | エージェントのブラウザで、ログインなど人にしかできない操作を引き受ける |

## エージェント

| ページ | できること |
|---|---|
| [エージェントを選ぶ](agents/choose.ja.md) | 契約に合うエージェントを選んで接続し、既定モデルや使わないモデルを決める |
| [Claude を接続する](agents/claude.ja.md) | Claude Code をセッションで動かす。リモートコントロールを使う |
| [Codex を接続する](agents/codex.ja.md) | ChatGPT のサブスクか API キーで Codex を動かす |
| [OpenCode を接続する](agents/opencode.ja.md) | いくつものプロバイダのモデルを、OpenCode で切り替えて使う |
| [GitHub Copilot を接続する](agents/copilot.ja.md) | GitHub の接続だけで Copilot CLI を動かす |
| [Cursor を接続する](agents/cursor.ja.md) | Cursor のアカウントで Cursor CLI を動かす |
| [Kiro を接続する](agents/kiro.ja.md) | Kiro を導入し、Builder ID などで動かす |
| [Antigravity を接続する](agents/agy.ja.md) | Google アカウントで Antigravity CLI を動かす（実験枠） |
| [Muse Code を接続する](agents/muse.ja.md) | Muse Code を導入し、Meta のアカウントか API キーで動かす |
| [lcpp を使う](agents/lcpp.ja.md) | 配備の llama.cpp エンジンや自分の llama-server でセッションを動かす |
| [料金と使用量を確かめる](agents/usage.ja.md) | モデルの定価、利用枠の残り、トークンを何に使ったかを見る。RTK で節約する |
| [作業のしかたを一度だけ書いておく](agents/instructions.ja.md) | 毎回の頼みごとを、すべてのエージェントに伝わるように書いておく |
| [手元の Claude Code の設定を持ち込む](agents/bring-claude-setup.ja.md) | 自分のスキル・サブエージェント・フックをワークスペースでも使う |

## フリート

| ページ | できること |
|---|---|
| [動いているセッションを一覧する](fleet/overview.ja.md) | 動いているセッションをカードで並べ、返事を待っているものを見つける |
| [セッションのつながりを時系列で見る](fleet/graph.ja.md) | どのセッションがどれを起動し、何が行き来したかを時間軸の図で見る |
| [作業グループで絞り込む](fleet/working-sets.ja.md) | 案件ごとにリポジトリや会話をまとめ、左ペインの表示を切り替える |
| [セッションから子セッションを起動する](fleet/child-sessions.ja.md) | セッションに別のセッションを起動させ、仕事を分けて渡させる |
| [セッション同士でやりとりさせる](fleet/messages.ja.md) | 並行して動くセッションの間で、短いメッセージを送らせる |
| [PR のレビューをセッションに伝える](fleet/pr-feedback.ja.md) | PR の CI の失敗やレビューの指摘を、PR を出したセッションに知らせる |
| [セッションに全体を見せる](fleet/fleet-visibility.ja.md) | セッションに、ほかのセッションの状態を見せ、気づいたことをメモに残させる |
| [フリート・オペレーターに任せる](fleet/operator.ja.md) | チャットから、セッションの起動・指示・報告の受け取りを任せる |
| [フリート・オペレーターの使いかたの例](fleet/operator-patterns.ja.md) | 並列の実装やクロスレビューなど、よく効く頼み方の型を知る |
| [決まった時刻に実行する](fleet/schedules.ja.md) | 決まった時刻や間隔で、エージェントに作業させる |

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

## チーム

| ページ | できること |
|---|---|
| [セッションをメンバーに渡す](team/hand-over.ja.md) | 共有しているメンバーに、セッションの続きを引き継いでもらう |
| [受信箱でチームメイトに頼む](team/inbox.ja.md) | メンバーにメッセージや依頼を送り、引き受けた人がセッションを始める |
| [会話を共有する](team/share.ja.md) | 会話を読み取り専用で見せ、必要なら提案を受け付ける |
| [使用量の上限の状況を見せ合う](team/usage-limits.ja.md) | 利用上限にどれだけ近いかを、選んだメンバーに見せる |

## チャット

| ページ | できること |
|---|---|
| [アシスタントに質問する](chat/assistant.ja.md) | リポジトリなしで質問する。長い会話を圧縮し、作業計画を残す |
| [翻訳や専用のアシスタントを使う](chat/purpose-assistants.ja.md) | ファイルを翻訳・要約する。自分用のアシスタントを作り、AI 補助を設定する |
| [メモをためて渡す](chat/memos.ja.md) | 思いついた指示をためて整理し、まとめてセッションに送る |

## メモリ

| ページ | できること |
|---|---|
| [エージェントのメモリを見る・直す](memory/overview.ja.md) | エージェントが覚えたことを読み、書き直し、ピン留めし、忘れさせる |
| [メモリの変更を確かめる](memory/changes.ja.md) | 誰が・いつ・何を変えたかを確かめ、まずい変更を元に戻す |
| [メモリの使いかたを設定する](memory/settings.ja.md) | セッションにメモリを使わせる。Claude Code のメモリを引き継ぐ |
| [古くなったメモリを見つける](memory/lint.ja.md) | なくなったパスや廃止した用語を使うメモリを点検し、印を付ける |
| [メモリの自動整理を使う](memory/tidy-up.ja.md) | メモリを直す提案をモデルに作らせ、1 件ずつ承認する |
| [メモリを取り込む・書き出す](../member/18-agent-memory.ja.md#取り込み書き出し) | Claude Code などのメモリを取り込み、Claude Code へ書き戻す |
| [スナップショットでメモリを戻す](../member/18-agent-memory.ja.md#メモリのスナップショット) | エージェント自前のメモリの履歴を見て、以前の時点に戻す |
| [外部のエージェントからメモリを使う](../member/18-agent-memory.ja.md#外部のエージェントから-agent-fleet-のメモリを使う) | 手元の Claude Code や claude.ai から Agent Fleet のメモリを使う |

## 外からつなぐ

| ページ | できること |
|---|---|
| [外部の Claude からワークスペースを動かす](outside/external-claude.ja.md) | 手元の Claude Code や Claude Desktop から、トークンでセッションを動かす |
| [リモートの MCP クライアントをサインインでつなぐ](outside/oauth-client.ja.md) | claude.ai・ChatGPT・Claude Code・Codex を、トークンなしでつなぐ |
| [手元の Claude Code の作業をワークスペースに移す](outside/move-work-in.ja.md) | 手元で始めた作業を push して、ワークスペースのセッションで続ける |

## 設定

| ページ | できること |
|---|---|
| [表示とアカウントを変える](settings/display.ja.md) | 言語・タイムゾーン・テーマ・フォントを変え、サインイン方法を追加する |
| [通知を受け取る](settings/notifications.ja.md) | 通知の種類ごとに、未読ドット・OS の通知・読み上げを選ぶ |
| [読み上げを使う](settings/read-aloud.ja.md) | 回答を声で読み上げる。声と読み方を選ぶ |

## セキュリティ

| ページ | できること |
|---|---|
| [資格情報を安全に扱う](security/credentials.ja.md) | トークンや鍵をどこに置き、どこに書かないかを知る |

## 困ったとき

| ページ | できること |
|---|---|
| [ワークスペースが起動しない・止まった](troubleshooting/workspace.ja.md) | 電源ボタンの横のバッジや、起動・ディスクのメッセージから直し方を探す |
| [セッションが再開できない・見当たらない](troubleshooting/sessions.ja.md) | 消えた・再開できない・途中で止まったセッション、共有や定時実行の不調を直す |
| [エージェントにログインできない・モデルが出ない](troubleshooting/agents.ja.md) | ログインの期限切れ、選べないモデル、止まったアシスタントを直す |
| [クローンや認証に失敗する](troubleshooting/git.ja.md) | クローン、private リポジトリの認証、submodule の失敗を直す |
| [プレビューやブラウザが表示されない](troubleshooting/preview.ja.md) | ブラウザペインやプレビューの URL に出るエラーから直し方を探す |
| [MCP・クラウド・Jira が動かない](troubleshooting/integrations.ja.md) | MCP サーバー、AWS・Google Cloud のコマンド、`jira`、外から接続するアプリの不調を直す |
| [ターミナルやスマートフォンで操作できない](troubleshooting/terminal-phone.ja.md) | キー操作、コピー＆ペースト、スマートフォンでの操作、出ないメニューを直す |
| [よくある質問](troubleshooting/faq.ja.md) | 最初に試すことと、よく聞かれる質問の答えを読む |
| [不具合を報告する・要望を出す](troubleshooting/report.ja.md) | Agent Fleet そのものの不具合や、ほしい機能を Issue で伝える |
