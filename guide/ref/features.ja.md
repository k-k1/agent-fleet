---
audience: "全員（他の表がぶら下がる索引）"
source_of_truth: "「在るか・誰が使えるか」はこの表、「どうやるか」はリンク先"
updated: "2026-10"
---

# 機能カタログ

[English](features.md) | 日本語

製品ができることを 1 行 1 つ、読者が出会う場所で束ねています。**誰が**はそれを使える
最小のロール（[roles.md](roles.ja.md)）、**どこ**は Console がその画面を呼ぶ名前です。

**機能を出したのにここに行が無ければ、その機能は完了していません**
（`docs/CONVENTIONS.ja.md` §8）。

## セッションを扱う

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| セッションの起動（エージェント・モデル・effort・開始モード）| メンバー | ワークスペース操作バー → Start | [03 セッションを始める](../member/03-starting-sessions.ja.md) |
| 新しい git worktree で動かす | メンバー | Start ダイアログ | [06 リポジトリと git](../member/06-code.ja.md) |
| チャットミラー — 実行中のエージェントを追う・操縦する | メンバー | メイン領域 | [04 会話](../member/04-conversation.ja.md) |
| 質問・プラン・権限確認に答える | メンバー | ミラー | [04 会話](../member/04-conversation.ja.md)。どのエージェントが尋ねるかは [agents.md](agents.ja.md) |
| 停止したセッションに残った質問とプランに答える | メンバー | ミラーの持ち越しカード | [04 会話](../member/04-conversation.ja.md#停止したセッションに残った質問とプラン) |
| ターン中の追撃を溜める（送信待ち）| メンバー | ミラーの入力欄 → 送信 ▾ → 送信待ちに追加、または Alt+Enter | [04 会話](../member/04-conversation.ja.md#送る前に追撃を溜める送信待ち) |
| プランを別のセッションにレビューさせる | メンバー | プランのカード | [04 会話](../member/04-conversation.ja.md#プランを別のセッションにレビューさせる) |
| スキル / コマンドのピッカー | メンバー | ミラーの入力欄 | [04 会話](../member/04-conversation.ja.md)。対応するエージェントは [agents.md](agents.ja.md) |
| セッションに繋がった実端末 | メンバー | メイン領域 | [08 ターミナル](../member/08-terminal.ja.md) |
| 端末の往復時間（打鍵の遅れ）| メンバー | ターミナルの上部 | [08 ターミナル](../member/08-terminal.ja.md) |
| 端末での Ctrl+C / Ctrl+V によるコピー・貼り付け | メンバー | ターミナル | [08 ターミナル](../member/08-terminal.ja.md#コピーペースト) |
| 停止したセッションの再開 | メンバー | 左ペイン → そのセッション | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md) |
| ホーム — 返事待ちのもの、続きの作業、初回のセットアップ | メンバー | 空のメイン領域、操作バー → ホーム、またはリーダー `g h` | [01 初日](../member/01-first-day.ja.md) |
| セッション一覧 — 稼働中のセッションをカードで一望 | メンバー | 操作バー → セッション一覧、またはリーダー `g s` | [11 たくさんのセッションを動かす](../member/11-many-sessions.ja.md) |
| フリート俯瞰図 — 1 セッション 1 レーン、横軸が時間 | メンバー | セッション一覧 → 俯瞰図、またはリーダー `g f` | [11 たくさんのセッションを動かす](../member/11-many-sessions.ja.md#セッション同士の関係を時間で見るフリート俯瞰図) |
| 作業コピー配下のセッションをまとめて停止 | メンバー | リポジトリ／worktree の右クリック → 配下のセッションを停止 | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md#セッションを止める片付ける) |
| 会話を新しいセッションへ引き継ぐ | メンバー | セッションの ⋯ メニュー | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md#別のセッションで続ける-引き継ぎ分岐移動) |
| セッションを別のエージェントへ移す | メンバー | セッションの ⋯ メニュー → 別のエージェントへ移す…、またはチャットの利用上限の帯 | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md#別のエージェントへ移すmove) |
| 会話を Markdown で書き出す | メンバー | セッションの ⋯ メニュー → Markdown で書き出す | [04 会話](../member/04-conversation.ja.md#会話を-markdown-で保存する) |
| 過去の発言からの分岐 | メンバー | ミラーの過去の発言 | [04 会話](../member/04-conversation.ja.md)。どのエージェントで可能かは [agents.md](agents.ja.md) |
| 別メンバーへセッションを引き継ぐ | メンバー | チャットの引き継ぎ提案カード → メンバーへ引き継ぐ | [12 他の人と扱う](../member/12-collaboration.ja.md#別のメンバーにセッションを引き継ぐ) |
| セッションの読み取り共有 | メンバー | 共有セッション | [12 他の人と扱う](../member/12-collaboration.ja.md) |
| マーカー | メンバー | ミラーの選択範囲 | [04 会話](../member/04-conversation.ja.md) |
| 変更ファイル | メンバー | ミラーのヘッダ下の帯 | [04 会話](../member/04-conversation.ja.md) |
| コンテキスト使用量ゲージ | メンバー | セッションのヘッダ | [09 エージェント](../member/09-agents.ja.md) |
| 回答を自分の言語で読む（翻訳・ターンを使わない）| メンバー | ミラーの回答の下 | [04 会話](../member/04-conversation.ja.md#回答を自分の言語で読む翻訳) |
| セッションに画像を作らせる（`generate_image`）| メンバー | スタジオに付けたセッション以外で、頼むだけ | [13 画像](../member/13-images.ja.md#セッションに画像を作らせる) |
| セッションから別のセッションを起こす（子セッション）— 別のエージェントに仕事を渡し、起こしたものの面倒を見る | メンバー | 設定 → エージェント → セッション、あとはどのセッションでも頼むだけ | [11 たくさんのセッションを動かす](../member/11-many-sessions.ja.md#セッションから別のセッションを起こす子セッション) |
| 中断の検知と自動再開 | メンバー | 自動 | [04 会話](../member/04-conversation.ja.md) |

## コードを扱う

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| リポジトリの取り込み | メンバー | リポジトリ → Clone | [06 リポジトリと git](../member/06-code.ja.md) |
| 空のフォルダから始める | メンバー | リポジトリ → 新規フォルダ | [06 リポジトリと git](../member/06-code.ja.md) |
| コミットグラフ・差分・ステージ・コミット | メンバー | コミットグラフ | [06 リポジトリと git](../member/06-code.ja.md) |
| worktree | メンバー | 各リポジトリの下 | [06 リポジトリと git](../member/06-code.ja.md) |
| サインイン済みの `gh` と `bkt` をセッションで使う | メンバー | どのセッションでも | [15 外部サービス](../member/15-external-services.ja.md#セッションで-gh-と-bkt-を使う)。サインイン済みの CLI の一覧は [15](../member/15-external-services.ja.md#サインイン済みの-cli) |
| サインイン済みの `jira` をセッションで使う | メンバー | どのセッションでも | [15 外部サービス](../member/15-external-services.ja.md#セッションで-jira-を使う) |
| Jira のコメントを編集・削除する（`jira issue comment edit` / `delete`）| メンバー | どのセッションでも | [15 外部サービス](../member/15-external-services.ja.md#セッションで-jira-を使う) |
| Subversion の作業コピー — 更新、ログを表示、ローカルの変更 | メンバー | リポジトリ → SVN の行 | [06 リポジトリと git](../member/06-code.ja.md#subversionsvnリポジトリ) |
| ファイルツリーとビューア | メンバー | ファイル | [07 ファイル](../member/07-files.ja.md) |
| フォルダを zip でダウンロードする | メンバー | ファイルツリーまたはギャラリー → 右クリック → zip でダウンロード | [07 ファイル](../member/07-files.ja.md#フォルダを-zip-でダウンロードする) |
| フォルダからシェルを開く | メンバー | ファイルツリー → フォルダを右クリック → ここでシェルを開く | [07 ファイル](../member/07-files.ja.md#フォルダからシェルを開く) |
| 表示した Markdown の中のリンク — 課題番号、ファイルのパス、ワークスペースのプレビューを開くループバック URL | メンバー | ミラー、チャット、ファイルとドキュメントのビュアー | [07 ファイル](../member/07-files.ja.md#表示した-markdown-の中のリンク) |
| 返信やドキュメントの中のファイル・フォルダのリンクのメニュー | メンバー | リンクを右クリック | [アイコン・バッジ・メニュー](../member/badges-and-menus.ja.md#返信やドキュメントの中のファイルとフォルダのリンク) |
| Markdown / コードの編集 | メンバー | ファイル → 各ファイル | [07 ファイル](../member/07-files.ja.md) |
| `.drawio` の図 | メンバー | ファイル → `.drawio` | [07 ファイル](../member/07-files.ja.md) |
| PDF・Office 文書のプレビュー | メンバー | ファイル → その文書 | [07 ファイル](../member/07-files.ja.md) |
| 画像生成 — エージェントを介さずに絵をまとめて作る | メンバー | 操作バー → 画像生成、またはリーダー `g i` | [13 画像](../member/13-images.ja.md#画像生成) |
| 編集と部分描き直し — 参照画像・マスク画像・指示編集のモデル | メンバー | 画像生成 → 上級 → 操作 | [13 画像](../member/13-images.ja.md#画像生成) |
| LoRA と、選ぶ前に見えるトリガー語 | メンバー | 画像生成 → LoRA | [13 画像](../member/13-images.ja.md#画像生成) |
| 画像生成のスタジオ — エージェントと会話で下書きを直し、生成は人が押す（編集履歴・版・絵の履歴）| メンバー | 画像生成 → エージェントを付ける | [13 画像](../member/13-images.ja.md#スタジオ-エージェントと会話で下書きを直す) |
| 画像生成の知識のメモ — モデル・系統ごとに 4 節、`~/imagegen-knowledge/` | メンバー | 画像生成 → メモ、またはファイルツリー | [13 画像](../member/13-images.ja.md#スタジオ-エージェントと会話で下書きを直す) |
| 画像ギャラリー — フォルダの画像をカードで | メンバー | ファイルツリー →「ギャラリーで開く」、またはセッションの「生成した画像 (N)」| [13 画像](../member/13-images.ja.md#画像ギャラリー) |
| ギャラリーのカードのメニュー — コピー・名前の変更・削除・生成したセッションへ | メンバー | ギャラリーのカード → 右クリック | [アイコン・バッジ・メニュー](../member/badges-and-menus.ja.md#画像ギャラリーのカード) |
| ローカル Web アプリのブラウザペイン | メンバー | ワークスペース操作バー → プレビュー | [14 プレビューとブラウザ](../member/14-preview-browser.ja.md)。契約は [browser-pane.md](browser-pane.ja.md) |
| プレビュー用サブドメイン（起動ごとに発行）| メンバー | ワークスペース操作バー → プレビュー | [14 プレビューとブラウザ](../member/14-preview-browser.ja.md) |
| プレビュー URL で Google サインインを使う（開発用 OIDC IdP）| メンバー | 設定 → プレビュー用サブドメイン | [14 プレビューとブラウザ](../member/14-preview-browser.ja.md#プレビュー-url-で-google-サインインを使う開発用-oidc-idp) |
| エージェントが持つ Chromium への接続 | メンバー | エージェントが渡すリンク | [14 プレビューとブラウザ](../member/14-preview-browser.ja.md) |

## 仕事を並べる

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| 作業グループ | メンバー | 左ペインの最上部 | [11 たくさんのセッションを動かす](../member/11-many-sessions.ja.md) |
| メモキュー | メンバー | メモキュー | [10 チャットとメモ](../member/10-chat-memo.ja.md) |
| 作業項目の受信箱（Issue / チケット / プルリクエスト）| メンバー | 課題管理 | [03 セッションを始める](../member/03-starting-sessions.ja.md)。どのプロバイダが何を出すかは [repos.md](repos.ja.md) |
| 定時実行（無人実行）| メンバー | スケジュール | [11 たくさんのセッションを動かす](../member/11-many-sessions.ja.md) |
| 通知センター | メンバー | 画面最上部のバー | [20 設定](../member/20-settings.ja.md) |
| 受信箱 — メンバー間のメッセージと依頼（引き受け・起動・PR の記録） | メンバー | 画面最上部のバー → 受信箱 | [12 他の人と扱う](../member/12-collaboration.ja.md#メンバーに頼む-受信箱) |
| 受信箱のレシピ — 勧めるエージェントやリポジトリ付きのプロンプトを送り、メモに取り込む | メンバー | 受信箱 → 新規 / カード | [12 他の人と扱う](../member/12-collaboration.ja.md#メンバーに頼む-受信箱) |
| 受信箱のメッセージにセッションを添付し、宛先に共有する | メンバー | 受信箱 → 新規 | [12 他の人と扱う](../member/12-collaboration.ja.md#メンバーに頼む-受信箱) |
| 未読の通知があるセッションの赤いドット | メンバー | 左ペイン・タブ | [03 セッションを始める](../member/03-starting-sessions.ja.md#状態を読む-バッジと通知) |
| エンジンのピル — チャット / 画像のエンジンが起きているか | メンバー | 画面最上部のバー | [アイコン・バッジ・メニュー](../member/badges-and-menus.ja.md#画面最上部のエンジンのピル) |
| アシスタントチャット | メンバー | アシスタント | [10 チャットとメモ](../member/10-chat-memo.ja.md) |
| チャットブリッジ（Discord / Slack）| メンバー | 設定 → チャット連携 | [15 外部サービス](../member/15-external-services.ja.md) |
| 返信候補 | メンバー | ミラーの入力欄 | [04 会話](../member/04-conversation.ja.md) |
| 削除ロック | メンバー | セッションの ⋯ メニュー | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md) |
| 掃除とごみ箱 | メンバー | 掃除 | [05 止める・片付ける](../member/05-stopping-and-tidying.ja.md) |
| キーボード操作体系 | メンバー | 設定 → キー操作 | [02 Console の基本](../member/02-console-basics.ja.md) |
| 音声読み上げ | メンバー | 設定 → 読み上げ | [20 設定](../member/20-settings.ja.md) |

## 自分の設定

いずれも**設定**の中で、タブが「どこ」です。タブの一覧は
[settings.md](settings.ja.md)、意味は [20 設定](../member/20-settings.ja.md)。

| 機能 | 誰が | どこ |
|---|---|---|
| エージェントの接続 | メンバー | エージェント |
| エージェントへの指示 | メンバー | エージェントへの指示 |
| Agent Fleet 共有メモリの管理 | メンバー | エージェントメモリ（[18](../member/18-agent-memory.ja.md#エージェントメモリ)） |
| エージェント自前のメモリの版管理 | メンバー | メモリのスナップショット（[18](../member/18-agent-memory.ja.md#メモリのスナップショット)） |
| メモリの提案 — 終わったセッションがメモリを提案し、あなたが承認する（既定はオフ）| メンバー | エージェントメモリ → 設定（[18](../member/18-agent-memory.ja.md#設定)） |
| Agent Fleet の外のエージェントから Agent Fleet のメモリを使う（既定はオフ）| メンバー | エージェントメモリ → 設定（[18](../member/18-agent-memory.ja.md#外部のエージェントから-agent-fleet-のメモリを使う)） |
| Muse Code の contributor モード（既定はオフ）| メンバー | エージェント → Muse Code → 動作（[09](../member/09-agents.ja.md#muse-code)）
| Git ホスティングの接続 | メンバー | Git ホスティング |
| 内部リポジトリ | メンバー | 内部リポジトリ |
| AWS プロファイルと SSM | メンバー | AWS プロファイル/SSM（[16](../member/16-cloud-accounts.ja.md#社内の別ホストに入るssm)） |
| Google Cloud のプロファイル | メンバー | Google Cloud（[16](../member/16-cloud-accounts.ja.md#google-cloud-で自分としてコマンドを実行するaf-gcloud-exec)） |
| 連携サーバーとトークン | メンバー | MCP サーバー / MCP トークン |
| サインインでリモート MCP クライアントを接続する（OAuth。配備が有効にしているとき）— 接続済みアプリ、最近の呼び出し | メンバー | MCP トークン（[19](../member/19-outside-access.ja.md#サインインでリモート-mcp-クライアントを接続するoauth)）
| `/mcp` から送った入力の「MCP 経由」バッジ | メンバー | ミラーの入力の吹き出し（[19](../member/19-outside-access.ja.md#外部の-claude-から自分のワークスペースを操作するmcp)）
| OAuth が要る登録済み MCP サーバーにサインインする（サインイン・もう一度サインイン・追加の権限を許可する） | メンバー | MCP サーバー → 認証 → OAuth（[20](../member/20-settings.ja.md#mcp-サーバー)）
| 課題管理の接続 | メンバー | 課題管理 |
| 使用量 | メンバー | エージェント使用量 |
| 利用枠ボード: 利用上限に対する自分の状況を公開し、メンバーが自分に公開している状況を見る | メンバー | 利用枠ボード |
| クラウド費用 | メンバー | クラウド費用 |
| AI補助（機能ごとのオン・オフ／エージェント／モデル） | メンバー | AI 補助 |
| 表示・言語・キー操作 | メンバー | 表示 / キー操作 |
| 設定の書き出し / 取り込み | メンバー | 書き出し / 取り込み |
| ツールチェーン | メンバー | ツールチェーン |
| データベース（Postgres / MySQL — 導入・起動・停止・リセット） | メンバー | ツールチェーン |
| プレビュー用サブドメインの設定 | メンバー | プレビュー用サブドメイン（発行する配備のみ）|
| 危険な操作 | メンバー | 危険な操作（作り直し・ホームの掃除。配備が実行できる場合だけ——[deploy-targets.md](deploy-targets.ja.md)） |

## テナントの管理

いずれも**テナント設定**の中。セクションは [admin/](../admin/README.ja.md)、
どれが読み取り専用かは [roles.md](roles.ja.md)。

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| メンバー | テナント管理者 | メンバー | [admin 01](../admin/01-members.ja.md) |
| メンバーのホームのバックアップの削除（配備がバックアップを取る場合） | テナント管理者 | メンバー → その人 | [admin 01](../admin/01-members.ja.md) |
| テナント全体のセッション | テナント管理者 | セッション | [admin 02](../admin/02-limits.ja.md) |
| 上限とアイドル自動停止 | テナント管理者（読み取り）| 上限・自動停止 | [admin 02](../admin/02-limits.ja.md) |
| テナントで利用枠ボードを使うかどうか | テナント管理者 | 利用枠ボード | [admin 02](../admin/02-limits.ja.md) |
| このテナントが llm / image エンジンを使ってよいか、テナント管理者がモデルを取り込んでよいか | 配備管理者（super_admin）| テナント設定 → 上限 | [admin 02](../admin/02-limits.ja.md) |
| テナントのどのメンバーが llm / image エンジンを使えるか | テナント管理者 | 推論エンジンの利用者 | [admin 02](../admin/02-limits.ja.md#推論エンジンを使えるメンバーあなたが設定できる) |
| 許可が出ているテナントでの、エンジンのモデルの取り込み | テナント管理者 | 推論エンジンのモデル | [admin 02](../admin/02-limits.ja.md#エンジンのモデルを取り込むテナントに許可が出ている場合だけ) |
| ワークスペースのサイズ | 配備管理者 | — | [deploy-targets.md](deploy-targets.ja.md) |
| サインイン方式とサインイン規則 | テナント管理者 | サインイン方式 / サインイン規則 | [admin 05](../admin/05-access.ja.md#サインイン方式とサインイン規則) |
| 接続元の制限 | テナント管理者 | 接続元の制限 | [admin 05](../admin/05-access.ja.md#接続元の制限) |
| 外のアプリが MCP OAuth でテナントに接続してよいか | テナント管理者 | 接続元の制限 → MCP OAuth | [admin 04](../admin/04-mcp-outside.ja.md#mcp-oauth) |
| 連携アプリの OAuth | テナント管理者 | 連携アプリの OAuth | [admin 05](../admin/05-access.ja.md) |
| ブランチ命名ルール — チームの既定のブランチ名と起点 | テナント管理者 | ブランチ命名ルール | [admin 06](../admin/06-team-defaults.ja.md#ブランチ命名ルール) |
| メンバーの既定 — 新しいメンバーがつなぐエージェント | テナント管理者 | メンバーの既定 | [admin 06](../admin/06-team-defaults.ja.md#メンバーの既定) |
| メンバーが外部から Agent Fleet のメモリを使ってよいか | テナント管理者 | 外部からのメモリ利用 | [admin 04](../admin/04-mcp-outside.ja.md#agent-fleet-の外からのメモリ利用) |
| 連携サーバーの配布 | テナント管理者 | MCP 配布 | [admin 04](../admin/04-mcp-outside.ja.md) |
| 監査 | テナント管理者 | 監査 | [admin 03](../admin/03-audit-usage.ja.md) |
| 稼働時間 | テナント管理者 | 稼働時間 | [admin 03](../admin/03-audit-usage.ja.md#稼働時間稼働時間) |
| クラウド費用をメンバーごとに（AWS のみ）| テナント管理者 | クラウド費用 | [admin 03](../admin/03-audit-usage.ja.md#クラウド費用クラウド費用) |

## 配備を運用する

| 機能 | 誰が | どこ | 詳細 |
|---|---|---|---|
| 配備形態 | 配備管理者 | 起動前 | [deploy-targets.md](deploy-targets.ja.md) |
| 導入・更新・バックアップ・復旧 | 配備管理者 | 端末 | [operator 02](../operate/02-install.ja.md) / [03](../operate/03-run.ja.md) |
| 入口・TLS・サインイン方式 | 配備管理者 | 端末 | [operator 04](../operate/04-secure.ja.md) |
| 監査ログと egress 統制 | 配備管理者 | 管理 | [operator 04](../operate/04-secure.ja.md#egress-統制の運用) |
| MCP OAuth が信頼するクライアントのホスト | 配備管理者（super_admin）| 管理 → MCP ログイン許可ホスト | [operator 04](../operate/04-secure.ja.md#信頼するクライアントのホスト配備全体-superadmin-のみ) |
| プレビュー URL 用の開発用 OIDC IdP | 配備管理者（super_admin）| 管理 → 開発用ログイン | [operator 04](../operate/04-secure.ja.md#開発用ログイン開発用-oidc-idp-superadmin-のみ) |
| AWS KMS による保存時の鍵と、ホームごとの専用鍵 | 配備管理者 | スタックのパラメータ、それから端末 | [operator 12](../operate/12-keys.ja.md) |
| この配備の色とラベル（見た目）| 配備管理者（super_admin）| 管理 → 見た目 | [operator 03](../operate/03-run.ja.md#2-つの配備を見分ける色とラベル) |
| 使わない間の配備の一時停止 | 配備管理者 | 端末 | [operator 03](../operate/03-run.ja.md#使わない間の配備を一時停止する) |
| 監視連携 | 配備管理者 | 設定 → 運用・監視 | [member 15](../member/15-external-services.ja.md) |
| スロットのプールとインスタンス種別 | 配備管理者 | 管理 | [deploy-targets.md](deploy-targets.ja.md) |
| 起動テンプレート変更後のスロットの入れ替え | 配備管理者（super_admin）| 管理 → スロット | [operator 11](../operate/11-slot-pool.ja.md#ecs-ec2-起動テンプレートを変えたあとのスロットの入れ替え) |
| 動かなくなったスロットの強制停止 | 配備管理者（super_admin）| 管理 → スロット → 停止（強制）| [operator 11](../operate/11-slot-pool.ja.md#ecs-ec2-動かなくなったスロットの停止) |
| 推論エンジンの GPU クラス | 配備管理者（super_admin）| 管理 → 推論エンジン | [operator 07](../operate/07-engines.ja.md#gpu-のインスタンスクラス) |
| モデルの取り込み — 検索・計画カード・1 押し | 配備管理者（super_admin）、許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| S3 Bucket — 配備が実際に持っているものの台帳 | 配備管理者（super_admin）| 管理 → 推論エンジン → モデルカタログ → バケット | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| 目録のモデル名・作例・ファミリー分類 | 配備管理者（super_admin）、または許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ → 登録済み | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| 配布元の量子化一覧と、GPU クラスに収まるかの判定 | 配備管理者（super_admin）、または許可されたテナント管理者 | 管理 → 推論エンジン → モデルカタログ → 登録済み | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| Hugging Face / Civitai のトークン（ログインが要る取り込み）| 配備管理者（super_admin）| 管理 → API トークン | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| 検索の出所としての Civitai Red（NSFW の姉妹ドメイン）| 配備管理者（super_admin）、配備がそれを与えられている場合 | 管理 → API トークン | [operator 07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| 音声エンジンと共通の読み仮名辞書 | 配備管理者（super_admin）| 管理 → 読み上げ | [operator 07](../operate/07-engines.ja.md#読み上げ音声エンジンと共通の読み仮名辞書) |
| 自前の ComfyUI での画像生成 | 配備管理者（super_admin）| 端末、それから 管理 → 推論エンジン | [operator 08](../operate/08-image-engine.ja.md) |
| 別の配備の llm / image エンジンを借りる | 配備管理者（super_admin）| 端末と、向こうの配備のメンバーシップ 1 つ | [operator 09](../operate/09-borrowed-engine.ja.md) |
| 自前の llama.cpp でのチャット推論 | 配備管理者（super_admin）| 端末、それから 管理 → 推論エンジン | [operator 10](../operate/10-llm-lan.ja.md) |
| コンテナ内のユーザーガイド | — | 自動 | [roles.md](roles.ja.md) |
