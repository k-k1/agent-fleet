---
audience: "全員（他の表がぶら下がる索引）"
source_of_truth: "「在るか・誰が使えるか」はこの表、「どうやるか」はリンク先"
---

# 機能カタログ

[English](features.md) | 日本語

製品の機能を 1 行に 1 つずつ、読者がその機能に出会う場所ごとにまとめています。**誰が**はそれを使える
最小のロール（[roles.md](roles.ja.md)）、**どこ**は Console がその画面を呼ぶ名前です。

**機能を出したのにここに行がなければ、その機能は完了していません**
（`docs/CONVENTIONS.ja.md` §8）。

## セッションを扱う

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| セッションの起動（エージェント・モデル・effort・開始モード）| メンバー | ワークスペース操作バー → Start | [セッションを始める](../user/sessions/start.ja.md) |
| 新しい git worktree で動かす | メンバー | Start ダイアログ | [ワークツリーで並行して作業する](../user/sessions/worktrees.ja.md) |
| チャットミラー — 実行中のエージェントを追う・操縦する | メンバー | メイン領域 | [セッションに返事をする](../user/sessions/reply.ja.md) |
| 質問・プラン・権限確認に答える | メンバー | ミラー | [セッションに返事をする](../user/sessions/reply.ja.md)。どのエージェントが尋ねるかは [agents.md](agents.ja.md) |
| 停止したセッションに残った質問とプランに答える | メンバー | ミラーの持ち越しカード | [セッションに返事をする](../user/sessions/reply.ja.md#止まったセッションに残ったカードに答える) |
| ターン中の追撃を溜める（送信待ち）| メンバー | ミラーの入力欄 → 送信 ▾ → 送信待ちに追加、または Alt+Enter | [送る内容を待たせる・ターンを止める](../user/sessions/queue.ja.md#送信待ちに溜める) |
| プランを別のセッションにレビューさせる | メンバー | プランのカード | [プランにコメントする・レビューさせる](../user/sessions/plans.ja.md#別のセッションにレビューさせる) |
| スキル / コマンドのピッカー | メンバー | ミラーの入力欄 | [スキルやコマンドを呼び出す](../user/sessions/skills.ja.md)。対応するエージェントは [agents.md](agents.ja.md) |
| セッションに繋がった実端末 | メンバー | メイン領域 | [ターミナルを使う](../user/files/terminal.ja.md) |
| 端末の往復時間（打鍵の遅れ）| メンバー | ターミナルの上部 | [ターミナルを使う](../user/files/terminal.ja.md) |
| 端末での Ctrl+C / Ctrl+V によるコピー・貼り付け | メンバー | ターミナル | [ターミナルを使う](../user/files/terminal.ja.md#コピーと貼り付け) |
| 停止したセッションの再開 | メンバー | 左ペイン → そのセッション | [セッションを止める・再開する](../user/sessions/stop-resume.ja.md) |
| ホーム — 返事待ちのもの、続きの作業、ひと目で、初回のセットアップ | メンバー | 空のメイン領域、操作バー → ホーム、またはリーダー `g h` | [画面の見かた](../user/start/console-tour.ja.md#ホーム) |
| セッション一覧 — 稼働中のセッションをカードで一望 | メンバー | 操作バー → セッション一覧、またはリーダー `g s` | [動いているセッションを一覧する](../user/fleet/overview.ja.md) |
| フリート俯瞰図 — 1 セッション 1 レーン、横軸が時間 | メンバー | セッション一覧 → 俯瞰図、またはリーダー `g f` | [セッションのつながりを時系列で見る](../user/fleet/graph.ja.md) |
| 作業コピー配下のセッションをまとめて停止 | メンバー | リポジトリ／worktree の右クリック → 配下のセッションを停止 | [セッションを止める・再開する](../user/sessions/stop-resume.ja.md#1-つの作業コピーで動いているセッションをまとめて止める) |
| 会話を新しいセッションへ引き継ぐ | メンバー | セッションの ⋯ メニュー | [別のセッションやエージェントに引き継ぐ](../user/sessions/handoff.ja.md#どれを使うか) |
| セッションを別のエージェントへ移す | メンバー | セッションの ⋯ メニュー → 別のエージェントへ移す…、またはチャットの利用上限の帯 | [別のセッションやエージェントに引き継ぐ](../user/sessions/handoff.ja.md#別のエージェントへ移す) |
| 会話を Markdown で書き出す | メンバー | セッションの ⋯ メニュー → Markdown で書き出す | [会話を Markdown で保存する](../user/sessions/export.ja.md) |
| 過去の発言からの分岐 | メンバー | ミラーの過去の発言 | [過去のメッセージから分岐する](../user/sessions/branch-message.ja.md)。どのエージェントで可能かは [agents.md](agents.ja.md) |
| 別メンバーへセッションを引き継ぐ | メンバー | セッションの ⋯ メニュー、またはチャットの引き継ぎ提案カード → メンバーへ引き継ぐ | [セッションをメンバーに渡す](../user/team/hand-over.ja.md) |
| セッションの読み取り共有 | メンバー | 共有セッション | [会話を共有する](../user/team/share.ja.md) |
| マーカー | メンバー | ミラーの選択範囲 | [セッションに返事をする](../user/sessions/reply.ja.md#会話の一文にマーカーを引く) |
| 変更ファイル | メンバー | ミラーのヘッダ下の帯 | [セッションが変えた内容を見る](../user/sessions/changes.ja.md) |
| コンテキスト使用量ゲージ | メンバー | セッションのヘッダ | [料金と使用量を確かめる](../user/agents/usage.ja.md#会話の長さを見る) |
| 回答を自分の言語で読む（翻訳・ターンを使わない）| メンバー | ミラーの回答の下 | [回答を自分の言語で読む](../user/sessions/translate.ja.md) |
| セッションに画像を作らせる（`generate_image`）| メンバー | スタジオに付けたセッション以外で、頼むだけ | [セッションに画像を作らせる](../user/images/from-session.ja.md) |
| セッションから別のセッションを起こす（子セッション）— 別のエージェントに仕事を渡し、起こしたものの面倒を見る | メンバー | 設定 → エージェント → セッション、あとはどのセッションでも頼むだけ | [セッションから子セッションを起動する](../user/fleet/child-sessions.ja.md) |
| 中断の検知と自動再開 | メンバー | 自動 | [ターンが途中で切れた](../user/troubleshooting/sessions.ja.md#ターンが途中で切れた) |

## コードを扱う

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| リポジトリの取り込み | メンバー | リポジトリ → Clone | [リポジトリをクローンする](../user/code/clone.ja.md) |
| 空のフォルダから始める | メンバー | リポジトリ → 新規フォルダ | [リポジトリをクローンする](../user/code/clone.ja.md#何もないところから始める) |
| コミットグラフ・差分・ステージ・コミット | メンバー | コミットグラフ | [コミットグラフでコミットする](../user/code/commit.ja.md) |
| worktree | メンバー | 各リポジトリの下 | [リポジトリからセッションを起動する](../user/code/launch-from-repo.ja.md) |
| サインイン済みの `gh` と `bkt` をセッションで使う | メンバー | どのセッションでも | [GitHub の CLI を使う](../user/integrations/github-cli.ja.md)、[Bitbucket の CLI を使う](../user/integrations/bitbucket-cli.ja.md)。サインイン済みの CLI の一覧は [コマンド](cli/README.ja.md#サインイン済みの-cli) |
| サインイン済みの `jira` をセッションで使う | メンバー | どのセッションでも | [Jira を使う](../user/integrations/jira.ja.md) |
| サインイン済みの `confluence`（Jira の接続）をセッションで使う | メンバー | どのセッションでも | [Confluence を使う](../user/integrations/confluence.ja.md) |
| Jira のコメントを編集・削除する（`jira issue comment edit` / `delete`）| メンバー | どのセッションでも | [Jira を使う](../user/integrations/jira.ja.md#エージェントに頼む) |
| Jira の課題にファイルを添付する・添付を一覧・ダウンロード・削除する（`jira issue attachment`）| メンバー | どのセッションでも | [Jira を使う](../user/integrations/jira.ja.md#エージェントに頼む) |
| Subversion の作業コピー — 更新、ログを表示、ローカルの変更 | メンバー | リポジトリ → SVN の行 | [Subversion のリポジトリを使う](../user/code/svn.ja.md) |
| ファイルツリーとビューア | メンバー | ファイル | [ファイルを見る・ダウンロードする](../user/files/browse.ja.md) |
| フォルダを zip でダウンロードする | メンバー | ファイルツリーまたはギャラリー → 右クリック → zip でダウンロード | [ファイルを見る・ダウンロードする](../user/files/browse.ja.md#フォルダを-zip-でダウンロードする) |
| フォルダからシェルを開く | メンバー | ファイルツリー → フォルダを右クリック → ここでシェルを開く | [ファイルを見る・ダウンロードする](../user/files/browse.ja.md#フォルダからシェルを開く) |
| 表示した Markdown の中のリンク — 課題番号、ファイルのパス、ワークスペースのプレビューを開くループバック URL | メンバー | ミラー、チャット、ファイルとドキュメントのビュアー | [ドキュメントや図を表示する](../user/files/view.ja.md#文書の中のリンク) |
| 返信やドキュメントの中のファイル・フォルダのリンクのメニュー | メンバー | リンクを右クリック | [ドキュメントや図を表示する](../user/files/view.ja.md#文書の中のリンク) |
| Markdown / コードの編集 | メンバー | ファイル → 各ファイル | [ファイルを編集する](../user/files/edit.ja.md) |
| `.drawio` の図 | メンバー | ファイル → `.drawio` | [ドキュメントや図を表示する](../user/files/view.ja.md) |
| PDF・Office 文書のプレビュー | メンバー | ファイル → その文書 | [ドキュメントや図を表示する](../user/files/view.ja.md) |
| 画像生成 — エージェントを介さずに絵をまとめて作る | メンバー | 操作バー → 画像生成、またはリーダー `g i` | [画像を生成する](../user/images/generate.ja.md) |
| 編集と部分描き直し — 参照画像・マスク画像・指示編集のモデル | メンバー | 画像生成 → 上級 → 操作 | [画像を生成する](../user/images/generate.ja.md#今ある絵を編集する) |
| LoRA と、選ぶ前に見えるトリガー語 | メンバー | 画像生成 → LoRA | [画像を生成する](../user/images/generate.ja.md#プロンプトを書く) |
| 画像生成のスタジオ — エージェントと会話で下書きを直し、生成は人が押す（編集履歴・版・絵の履歴）| メンバー | 画像生成 → エージェントを付ける | [スタジオで画像を仕上げる](../user/images/studio.ja.md) |
| 画像生成の知識のメモ — モデル・系統ごとに 4 節、`~/imagegen-knowledge/` | メンバー | 画像生成 → メモ、またはファイルツリー | [スタジオで画像を仕上げる](../user/images/studio.ja.md#知識のメモを育てる) |
| 画像ギャラリー — フォルダの画像をカードで | メンバー | ファイルツリー →「ギャラリーで開く」、またはセッションの「生成した画像 (N)」| [画像ギャラリーを使う](../user/images/gallery.ja.md) |
| ギャラリーのカードのメニュー — コピー・名前の変更・削除・生成したセッションへ | メンバー | ギャラリーのカード → 右クリック | [画像ギャラリーを使う](../user/images/gallery.ja.md#カードを操作する) |
| ローカル Web アプリのブラウザペイン | メンバー | ワークスペース操作バー → プレビュー | [起動した Web アプリを見る](../user/preview/browser-pane.ja.md)。契約は [browser-pane.md](browser-pane.ja.md) |
| プレビュー用サブドメイン（起動ごとに発行）| メンバー | ワークスペース操作バー → プレビュー | [プレビュー用のサブドメインを使う](../user/preview/subdomains.ja.md) |
| プレビュー URL で Google サインインを使う（開発用 OIDC IdP）| メンバー | 設定 → プレビュー用サブドメイン | [プレビューで Google ログインを試す](../user/preview/dev-signin.ja.md) |
| エージェントが持つ Chromium への接続 | メンバー | エージェントが渡すリンク | [エージェントが開いたブラウザを操作する](../user/preview/agent-browser.ja.md) |

## 仕事を並べる

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| 作業グループ | メンバー | 左ペインの最上部 | [作業グループで絞り込む](../user/fleet/working-sets.ja.md) |
| メモキュー | メンバー | メモキュー | [メモをためて渡す](../user/chat/memos.ja.md) |
| 作業項目の受信箱（Issue / チケット / プルリクエスト）| メンバー | 課題管理 | [Issue やチケットから始める](../user/sessions/from-issues.ja.md)。どのプロバイダが何を出すかは [repos.md](repos.ja.md) |
| 定時実行（無人実行）| メンバー | スケジュール | [決まった時刻に実行する](../user/fleet/schedules.ja.md) |
| 通知センター | メンバー | 画面最上部のバー | [通知を受け取る](../user/settings/notifications.ja.md#通知センターで読む) |
| 受信箱 — メンバー間のメッセージと依頼（引き受け・起動・PR の記録） | メンバー | 画面最上部のバー → 受信箱 | [受信箱でチームメイトに頼む](../user/team/inbox.ja.md) |
| 受信箱のレシピ — 勧めるエージェントやリポジトリ付きのプロンプトを送り、メモに取り込む | メンバー | 受信箱 → 新規 / カード | [受信箱でチームメイトに頼む](../user/team/inbox.ja.md) |
| 受信箱のメッセージにセッションを添付し、宛先に共有する | メンバー | 受信箱 → 新規 | [受信箱でチームメイトに頼む](../user/team/inbox.ja.md) |
| 未読の通知があるセッションの赤いドット | メンバー | 左ペイン・タブ | [セッションの状態を読む](../user/sessions/status.ja.md#赤いドット) |
| セッション行の作業項目リンク — ブランチのプルリクエストと紐づいたチケット | メンバー | 左ペイン、セッション行 | [Issue やチケットから始める](../user/sessions/from-issues.ja.md#セッションとチケットの紐づき)。どのプロバイダが対象かは [repos.md](repos.ja.md) |
| プルリクエストの通知 — マージ可能・マージ・クローズ | メンバー | 設定 → 通知 | [20 設定](../user/settings/notifications.ja.md) |
| セッションがプルリクエストを見張る — チェックの完了・コンフリクト・マージを知らせる | メンバー | （エージェントのツール `watch`） | [PR のレビューをセッションに伝える](../user/fleet/pr-feedback.ja.md#セッションが自分で-pr-を見張る) |
| 応答なしバッジ — ワークスペースは起動しているが Agent が応答しない | メンバー | ワークスペースの電源ボタンの横 | [ワークスペースが起動しない・止まった](../user/troubleshooting/workspace.ja.md#電源ボタンの横に応答なしと出る) |
| エンジンのピル — チャット / 画像のエンジンが起きているか | メンバー | 画面最上部のバー | [lcpp を使う](../user/agents/lcpp.ja.md) |
| アシスタントチャット | メンバー | アシスタント | [アシスタントに質問する](../user/chat/assistant.ja.md) |
| チャットブリッジ（Discord / Slack）| メンバー | 設定 → チャット連携 | [Discord・Slack とつなぐ](../user/integrations/chat-bridge.ja.md) |
| 返信候補 | メンバー | ミラーの入力欄 | [セッションに返事をする](../user/sessions/reply.ja.md#返信サジェストを使う) |
| 削除ロック | メンバー | セッションの ⋯ メニュー | [終わったセッションを片付ける](../user/sessions/cleanup.ja.md#削除ロックをかける) |
| 掃除とごみ箱 | メンバー | 掃除 | [終わったセッションを片付ける](../user/sessions/cleanup.ja.md#まとめて片付ける掃除) |
| キーボード操作体系 | メンバー | 設定 → キー操作 | [キーボードとコマンドパレットで操作する](../user/start/shortcuts.ja.md) |
| 音声読み上げ | メンバー | 設定 → 読み上げ | [読み上げを使う](../user/settings/read-aloud.ja.md) |
| スキルの表示 — 各エージェントのスキルを読み取り専用で（個人は設定、リポジトリのものはプロジェクトツリー → スキル…） | メンバー | 設定 → スキル、リポジトリ行のメニュー → スキル… | [手元の Claude Code の設定を持ち込む](../user/agents/bring-claude-setup.ja.md#スキルを確かめる) |

## 自分の設定

いずれも**設定**の中で、タブが「どこ」です。タブの一覧と、それぞれを説明するページは
[settings.md](settings.ja.md) にあります。

| 機能 | 誰が | どこ |
|---|---|---|
| エージェントの接続 | メンバー | エージェント |
| エージェントへの指示 | メンバー | エージェントへの指示 |
| Agent Fleet 共有メモリの管理 | メンバー | エージェントメモリ（[エージェントのメモリを見る・直す](../user/memory/overview.ja.md)） |
| エージェント自身のメモリの版管理 | メンバー | メモリのスナップショット（[スナップショットでメモリを戻す](../user/memory/snapshots.ja.md)） |
| Agent Fleet の外のエージェントから Agent Fleet のメモリを使う（既定はオフ）| メンバー | エージェントメモリ → 設定（[外部のエージェントからメモリを使う](../user/memory/external.ja.md)） |
| Muse Code の contributor モード（既定はオフ）| メンバー | エージェント → Muse Code → 動作（[Muse Code を接続する](../user/agents/muse.ja.md#動作設定)）
| Git ホスティングの接続 | メンバー | Git ホスティング |
| 内部リポジトリ | メンバー | 内部リポジトリ |
| AWS プロファイルと SSM | メンバー | AWS プロファイル/SSM（[AWS SSO を使う](../user/integrations/aws-sso.ja.md)、[SSM で EC2 にログインする](../user/integrations/aws-ssm.ja.md)） |
| Google Cloud のプロファイル | メンバー | Google Cloud（[Google Cloud を使う](../user/integrations/gcloud.ja.md)） |
| 連携サーバーとトークン | メンバー | MCP サーバー（セッションで使う） / 外部からのアクセス → MCP トークン・接続アプリ |
| サインインでリモート MCP クライアントを接続する（OAuth。配備が有効にしているとき）— 接続済みアプリ、最近の呼び出し | メンバー | 外部からのアクセス → MCP トークン・接続アプリ（[リモートの MCP クライアントをサインインでつなぐ](../user/outside/oauth-client.ja.md)）
| `/mcp` から送った入力の「MCP 経由」バッジ | メンバー | ミラーの入力の吹き出し（[外部の Claude からワークスペースを動かす](../user/outside/external-claude.ja.md#セッションに届く指示の見え方)）
| OAuth が要る登録済み MCP サーバーにサインインする（サインイン・もう一度サインイン・追加の権限を許可する） | メンバー | MCP サーバー（セッションで使う） → 認証 → OAuth（[セッションに MCP サーバーを追加する](../user/integrations/mcp-servers.ja.md#oauth-のサーバーにサインインする)）
| 課題管理の接続 | メンバー | 課題管理 |
| 使用量 | メンバー | エージェント使用量 |
| 利用枠ボード: 利用上限に対する自分の状況を公開し、メンバーが自分に公開している状況を見る | メンバー | 利用枠ボード |
| クラウド費用 | メンバー | クラウド費用 |
| AI補助（機能ごとのオン・オフ／エージェント／モデル） | メンバー | AI 補助 |
| 表示・言語・キー操作 | メンバー | 表示 / キー操作 |
| 設定の書き出し / 取り込み | メンバー | 書き出し / 取り込み |
| ツールチェーン | メンバー | ツールチェーン |
| データベース（Postgres / MySQL — 導入・起動・停止・リセット） | メンバー | データベース |
| プレビュー用サブドメインの設定 | メンバー | プレビュー用サブドメイン（発行する配備のみ）|
| 危険な操作 | メンバー | 危険な操作（作り直し・ホームの掃除。配備が実行できる場合だけ。[deploy-targets.md](deploy-targets.ja.md)） |

## テナントの管理

いずれも**テナント設定**の中にあります。セクションは [admin/](../admin/README.ja.md)、
どれが読み取り専用かは [roles.md](roles.ja.md) にあります。

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| メンバー | テナント管理者 | メンバー | [メンバーの状況を見る](../admin/members/roster.ja.md) |
| メンバーのホームのバックアップの削除（配備がバックアップを取る場合） | テナント管理者 | メンバー → その人 | [メンバーを外す](../admin/members/remove.ja.md#4-バックアップを削除する) |
| テナント全体のセッション | テナント管理者 | セッション | [リソースの使われかたを見る](../admin/resources/overview.ja.md#いま動いているセッションを見る) |
| 上限とアイドル自動停止 | テナント管理者（読み取り）| 上限・自動停止 | [セッション数の上限を決める](../admin/resources/session-limits.ja.md#テナント全体の上限を確かめる)・[放置されたワークスペースを止める](../admin/resources/idle-stop.ja.md) |
| テナントで利用枠ボードを使うかどうか | テナント管理者 | 利用枠ボード | [セッション数の上限を決める](../admin/resources/session-limits.ja.md#利用枠ボードを使うか決める) |
| このテナントが llm / image エンジンを使ってよいか、テナント管理者がモデルを取り込んでよいか | 配備管理者（super_admin）| テナント設定 → 上限 | [推論エンジンを使える人とモデルを決める](../admin/resources/engines.ja.md) |
| テナントのどのメンバーが llm / image エンジンを使えるか | テナント管理者 | 推論エンジンの利用者 | [推論エンジンを使える人とモデルを決める](../admin/resources/engines.ja.md#使えるメンバーを決める) |
| 許可が出ているテナントでの、エンジンのモデルの取り込み | テナント管理者 | 推論エンジンのモデル | [推論エンジンを使える人とモデルを決める](../admin/resources/engines.ja.md#モデルを取り込む) |
| ワークスペースのサイズ | 配備管理者 | — | [deploy-targets.md](deploy-targets.ja.md) |
| サインイン方式とサインイン規則 | テナント管理者 | サインイン方式 / サインイン規則 | [サインインの方法を決める](../admin/access/signin-methods.ja.md) |
| 接続元の制限 | テナント管理者 | 接続元の制限 | [サインインできるネットワークを絞る](../admin/access/networks.ja.md) |
| 外のアプリが MCP OAuth でテナントに接続してよいか | テナント管理者 | 接続元の制限 → MCP OAuth | [MCP サーバーを全員に配る](../admin/mcp-distribute.ja.md#外のアプリからの-oauth-接続を止める) |
| 連携アプリの OAuth | テナント管理者 | 連携アプリの OAuth | [連携に使う OAuth アプリを選ぶ](../admin/access/oauth-apps.ja.md) |
| ブランチ命名ルール — チームの既定のブランチ名と起点 | テナント管理者 | ブランチ命名ルール | [ブランチ名の規則を決める](../admin/defaults/branch-rules.ja.md) |
| メンバーの既定 — 新しいメンバーがつなぐエージェント | テナント管理者 | メンバーの既定 | [メンバーの既定値を決める](../admin/defaults/members.ja.md) |
| メンバーが外部から Agent Fleet のメモリを使ってよいか | テナント管理者 | 外部からのメモリ利用 | [外部からのメモリの利用を許可する](../admin/memory-outside.ja.md) |
| 連携サーバーの配布 | テナント管理者 | MCP 配布 | [MCP サーバーを全員に配る](../admin/mcp-distribute.ja.md) |
| 監査 | テナント管理者 | 監査 | [監査ログを見る](../admin/audit.ja.md) |
| 稼働時間 | テナント管理者 | 稼働時間 | [稼働時間とクラウド費用を見る](../admin/usage.ja.md#稼働時間を見る) |
| クラウド費用をメンバーごとに（AWS のみ）| テナント管理者 | クラウド費用 | [稼働時間とクラウド費用を見る](../admin/usage.ja.md#クラウド費用を見る) |

## 配備を運用する

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| 配備形態 | 配備管理者 | 起動前 | [deploy-targets.md](deploy-targets.ja.md) |
| 導入・更新・バックアップ・復旧 | 配備管理者 | シェル | [インストールする](../operate/install.ja.md) / [アップグレードする](../operate/run/upgrade.ja.md) / [バックアップして復元する](../operate/run/backup-restore.ja.md) |
| 入口・TLS・サインイン方式 | 配備管理者 | シェル | [サインイン](../operate/signin/overview.ja.md) |
| 監査ログと egress 統制 | 配備管理者 | 管理 | [外向きの通信を制御する](../operate/secure/egress.ja.md) |
| MCP OAuth が信頼するクライアントのホスト | 配備管理者（super_admin）| 管理 → MCP ログイン許可ホスト | [MCP のサインインを運用する](../operate/secure/mcp.ja.md#信頼するクライアントのホストを決める) |
| プレビュー URL 用の開発用 OIDC IdP | 配備管理者（super_admin）| 管理 → 開発用ログイン | [開発用サインインを使う](../operate/secure/dev-signin.ja.md#開発用-oidc-idp-をオンオフする) |
| AWS KMS による保存時の鍵と、ホームごとの専用鍵 | 配備管理者 | スタックのパラメータ、それからシェル | [保存時の鍵を AWS KMS にする](../operate/keys/kms.ja.md)・[ホームごとに鍵を分ける](../operate/keys/home-keys.ja.md) |
| この配備の色とラベル（見た目）| 配備管理者（super_admin）| 管理 → 見た目 | [複数の配備を見分ける](../operate/run/deployment-label.ja.md) |
| 使わない間の配備の一時停止 | 配備管理者 | シェル | [アイドル停止と強制停止を使う](../operate/run/idle-stop.ja.md#使わないあいだ配備を一時停止する) |
| 監視連携 | 配備管理者 | 設定 → 運用・監視 | [監視ツールとつなぐ](../user/integrations/ops-monitoring.ja.md) |
| スロットのプールとインスタンス種別 | 配備管理者 | 管理 | [deploy-targets.md](deploy-targets.ja.md) |
| 起動テンプレート変更後のスロットの入れ替え | 配備管理者（super_admin）| 管理 → スロット | [スロットを入れ替える・止める](../operate/aws-ecs/slot-maintenance.ja.md#古いスロット) |
| 動かなくなったスロットの強制停止 | 配備管理者（super_admin）| 管理 → スロット → 停止（強制）| [スロットを入れ替える・止める](../operate/aws-ecs/slot-maintenance.ja.md#応答しないスロットを止める) |
| 推論エンジンの GPU クラス | 配備管理者（super_admin）| 管理 → 推論エンジン | [自前の GPU で推論エンジンを動かす](../operate/engines/gpu.ja.md#gpu-のインスタンスクラスを選ぶ) |
| モデルの取り込み — 検索・計画カード・ワンクリックでの取り込み | 配備管理者（super_admin）、許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#計画を読んで取り込む) |
| S3 Bucket — 配備が実際に持っているものの台帳 | 配備管理者（super_admin）| 管理 → 推論エンジン → モデルカタログ → バケット | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#s3-bucket-を見る) |
| カタログのモデル名・作例・ファミリー分類 | 配備管理者（super_admin）、または許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ → 登録済み | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#登録済みの行を整える) |
| 配布元の量子化一覧と、GPU クラスに収まるかの判定 | 配備管理者（super_admin）、または許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ → 登録済み | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#別のサイズやバージョンに替える) |
| Hugging Face / Civitai のトークン（ログインが要る取り込み）| 配備管理者（super_admin）| 管理 → API トークン | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#トークンを登録する) |
| 検索先としての Civitai Red（NSFW の姉妹ドメイン）| 配備管理者（super_admin）、配備がそれを与えられている場合 | 管理 → API トークン | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#トークンを登録する) |
| 音声エンジンと共通の読み仮名辞書 | 配備管理者（super_admin）| 管理 → 読み上げ | [読み上げエンジンを動かす](../operate/engines/read-aloud.ja.md) |
| 自前の ComfyUI での画像生成 | 配備管理者（super_admin）| シェル、それから 管理 → 推論エンジン | [自前の ComfyUI で画像を生成する](../operate/engines/comfyui.ja.md) |
| 別の配備の llm / image エンジンを借りる | 配備管理者（super_admin）| シェルと、向こうの配備のメンバーシップ 1 つ | [別の配備のエンジンを借りる](../operate/engines/borrow.ja.md) |
| 自前の llama.cpp でのチャット推論 | 配備管理者（super_admin）| シェル、それから 管理 → 推論エンジン | [LAN の llama.cpp でチャットする](../operate/engines/llama-cpp.ja.md) |
| コンテナ内のユーザーガイド | — | 自動 | [roles.md](roles.ja.md) |
