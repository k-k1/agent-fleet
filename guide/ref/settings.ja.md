---
audience: "全員（ただし「つまみが見つからない人」に向けて書く）"
source_of_truth: "個人/テナント設定は Console、配備の変数は `deploy/compose/.env.example`"
---

# 設定 — どこで何を決めるか

[English](settings.md) | 日本語

挙動を決める層は 3 つあり、この 3 つの取り違えが「変えたのに効かない」のよくある原因です。
狭い順に並べると次のとおりです。

| 層 | 決める人 | どこ | 効くタイミング |
|---|---|---|---|
| 個人 | 自分 | アカウントメニュー → **設定** | 即座、または次のセッションから |
| テナント | テナント管理者 | アカウントメニュー → **テナント設定** | そのテナントのメンバーに |
| 配備 | 配備の運用担当者 | 環境変数（起動前）| 再起動時 |

狭い層が広い層の範囲を超えることはできません。個人設定はテナントの上限を超えられず、テナント設定は
配備が許す範囲を超えられません。設定した値が効いている値と違うときは、1 つ外の層を見てください。

## 個人設定

画面最上部のバーのアカウントメニューにある **⚙設定** で開きます。左レールは 4 つのグループに分かれていて、下の表もその順に並べています。

| グループ | タブ |
|---|---|
| 個人設定 | 表示・アカウント・キー操作・読み上げ・通知・利用枠ボード・アシスタント・AI 補助・エージェントへの指示・スキル・エージェントメモリ・メモリのスナップショット |
| 接続 | エージェント・Git ホスティング・運用・監視・課題管理・チャット連携・MCP サーバー（セッションで使う）・AWS プロファイル/SSM・Google Cloud |
| 外部からのアクセス | MCP トークン・接続アプリ |
| ワークスペース | エージェント使用量・クラウド費用・稼働時間・マシン・ツールチェーン・データベース・プレビュー用サブドメイン・内部リポジトリ・書き出し / 取り込み・危険な操作 |

- 最後に開いたタブが記憶され、次に開くとそのタブから表示されます。
- スマートフォンでは一覧 → 詳細の順に開きます。戻る操作で一覧に戻り、もう一度で閉じます。
- 一部のタブは、設定の実体がワークスペースの中にあるため、ワークスペースが動いている必要があります。停止中はその旨と「ワークスペースを起動」ボタンが出ます。
- どの設定がこのブラウザだけに保存され、どれが別の PC でも同じになるかは [表示とアカウントを変える](../user/settings/display.ja.md#どこに保存されるか) にあります。

| タブ | 決めるもの | 説明のページ |
|---|---|---|
| 表示 | 言語・地域の形式・タイムゾーン・テーマ・情報密度 | [表示とアカウントを変える](../user/settings/display.ja.md) |
| アカウント | 自分のアカウントに紐づくサインイン方法 | [表示とアカウントを変える](../user/settings/display.ja.md#サインイン方法を追加する) |
| キー操作 | ショートカットと再割り当て | [キーボードとコマンドパレットで操作する](../user/start/shortcuts.ja.md#キーを変える) |
| 読み上げ | 読み上げエンジンと声 | [読み上げを使う](../user/settings/read-aloud.ja.md) |
| 通知 | 何を、どう知らせるか | [通知を受け取る](../user/settings/notifications.ja.md) |
| 利用枠ボード | 利用上限に対する自分の状況を、このテナントの誰に・どこまで見せるか（既定は非公開）。メンバーが自分に公開している状況 | [使用量の上限の状況を見せ合う](../user/team/usage-limits.ja.md) |
| アシスタント | アシスタントチャットのエージェントとモデル | [アシスタントに質問する](../user/chat/assistant.ja.md#回答の言語とモデルを決める)・[フリート・オペレーターに任せる](../user/fleet/operator.ja.md#自動応答を調整する) |
| AI 補助 | タイトル・ブランチ名・返信候補・編集提案・計画更新・翻訳の既定エージェントとモデル、機能ごとのオン／オフ・エージェント・モデルの上書き | [翻訳や専用のアシスタントを使う](../user/chat/purpose-assistants.ja.md#ai-補助を設定する) |
| エージェントへの指示 | このワークスペースで起動する全エージェントに足される文 | [作業のしかたを一度だけ書いておく](../user/agents/instructions.ja.md) |
| スキル | 各エージェント（Claude・Codex・OpenCode・Muse）の個人スキル。読み込まれるフォルダ、どれが優先されるか、各ファイルの中身。読み取り専用 | [手元の Claude Code の設定を持ち込む](../user/agents/bring-claude-setup.ja.md#スキルを確かめる) |
| エージェントメモリ | Agent Fleet 自身の共有メモリ。4 つの表示: メモリ（閲覧・検索・作成・編集・ピン留め・忘れさせる・忘却を取り消す・履歴の消去）、変更（変更一覧と元に戻す・忘れさせる）、取り込み・書き出し（カードのハブ：Claude Code・muse・メモリのバンドルからの取り込み、Claude Code への書き戻し、プロジェクトの移動）、設定（セッション用スイッチと同期の状態、外部からの利用） | [エージェントのメモリを見る・直す](../user/memory/overview.ja.md) |
| メモリのスナップショット | エージェント自身のメモリ（各エージェントの種類が持つメモリ）のバージョン管理。3 つの表示: 履歴（この時点に戻すつき。Agent Fleet のメモリの書き込みは頼まない限り隠す）、対象（対象のルートディレクトリ・codex のメモリ・自動スナップショット）、書き出し・取り込み | [スナップショットでメモリを戻す](../user/memory/snapshots.ja.md) |
| エージェント | 各エージェントの接続・既定モデル・除外するモデル・挙動。llama.cpp のオン／オフと自分の llama-server 接続先、Muse Code の導入とログイン。セッショングループ — 停止中セッションのアーカイブまでの日数・セッション間メッセージ・セッションからのセッション起動と子セッションの同時本数・過去セッションの検索（`search_sessions` ツール。コマンドパレットの「会話」はこの設定に関係なく使えます）・画像生成と画像生成の優先順位 | [エージェントを選ぶ](../user/agents/choose.ja.md)・[セッションを止める・再開する](../user/sessions/stop-resume.ja.md#停止したまま置いておくと)・[セッション同士でやりとりさせる](../user/fleet/messages.ja.md)・[セッションから子セッションを起動する](../user/fleet/child-sessions.ja.md)・[セッションに画像を作らせる](../user/images/from-session.ja.md) |
| Git ホスティング | GitHub / Bitbucket の接続 | [Git のサービスを接続する](../user/code/connect-git.ja.md) |
| 運用・監視 | 監視連携 | [監視ツールとつなぐ](../user/integrations/ops-monitoring.ja.md) |
| 課題管理 | Jira ほか作業項目の取得元 | [課題管理サービスを接続する](../user/integrations/issue-tracker.ja.md) |
| チャット連携 | Discord / Slack のブリッジ | [Discord・Slack とつなぐ](../user/integrations/chat-bridge.ja.md) |
| MCP サーバー（セッションで使う） | エージェントが使える連携サーバー | [セッションに MCP サーバーを追加する](../user/integrations/mcp-servers.ja.md) |
| AWS プロファイル/SSM | AWS プロファイルとリモートログイン先 | [AWS SSO を使う](../user/integrations/aws-sso.ja.md)・[SSM で EC2 にログインする](../user/integrations/aws-ssm.ja.md) |
| Google Cloud | `af-gcloud-exec` がコマンドの実行に使う Google Cloud のプロファイル | [Google Cloud を使う](../user/integrations/gcloud.ja.md) |
| MCP トークン・接続アプリ | 外部から自分のセッションを動かすためのトークン。エージェントメモリの設定と合わせれば、外部のエージェントから自分の Agent Fleet のメモリを使うためにも使う | [外部の Claude からワークスペースを動かす](../user/outside/external-claude.ja.md)・[リモートの MCP クライアントをサインインでつなぐ](../user/outside/oauth-client.ja.md) |
| エージェント使用量 | 自分のトークン消費（機能別・エージェント別・モデル別） | [料金と使用量を確かめる](../user/agents/usage.ja.md#何にトークンを使ったかを見る) |
| クラウド費用 | 配備のクラウド費用のうち自分の分 | [稼働時間とクラウド費用を見る](../user/workspace/cost.ja.md#クラウド費用を見る) |
| 稼働時間 | 自分のワークスペースが動いていた時間（24 時間 × 日付のヒートマップ） | [稼働時間とクラウド費用を見る](../user/workspace/cost.ja.md#稼働時間を見る) |
| マシン | 自分のワークスペースが動いているインスタンス（種類・アーキテクチャ・vCPU・メモリ・ディスク）、使用状況、メモリ逼迫（測定のみ） | [マシンと使用状況を見る](../user/workspace/machine.ja.md) |
| ツールチェーン | JDK ほかワークスペースごとのツールチェーン、エージェント CLI の更新、サインイン時の自動起動（テナントでオンのときだけ）、イメージの自動更新 | [ツールチェーンを選ぶ](../user/workspace/toolchains.ja.md)、[サインインしたらワークスペースを起動する](../user/workspace/toolchains.ja.md#サインインしたらワークスペースを起動する)、[新しいイメージへ自動で再起動する](../user/workspace/toolchains.ja.md#新しいイメージへ自動で再起動する) |
| データベース | DB を使うテスト用の PostgreSQL / MySQL（導入・開始・停止・作成・リセット） | [データベースを使うテストを動かす](../user/workspace/databases.ja.md#console-から管理する) |
| プレビュー用サブドメイン | 作っているアプリを外から開く URL（発行する配備のみ） | [プレビュー用のサブドメインを使う](../user/preview/subdomains.ja.md)・[プレビューで Google ログインを試す](../user/preview/dev-signin.ja.md) |
| 内部リポジトリ | 配備自身がホストするリポジトリ | [内蔵の Git を使う](../user/code/internal-git.ja.md) |
| 書き出し / 取り込み | 設定を別の配備へ持って行く | [設定を書き出す・取り込む](../user/workspace/export-import.ja.md) |
| 危険な操作 | 自分のワークスペースへの破壊的操作 | [ワークスペースを作り直す](../user/workspace/recreate.ja.md) |

### いつ反映されるか

これを取り違えると「設定したのに変わらない」ことになります。

| タイミング | 対象 |
|---|---|
| 即時 | 表示・キー操作・読み上げ・通知、接続の追加と削除、停止中セッションのアーカイブ期間（次の一覧更新から）、ツールチェーンのサインイン時の自動起動とイメージの自動更新 |
| 次に起動するセッションから | エージェントの動作設定、エージェントへの指示、セッション間メッセージ、セッションからのセッション起動、過去セッションの検索（オフにすると起動中のセッションの検索もその場で断ります）、Agent Fleet のメモリ（同じく）、画像生成、MCP サーバー |
| 次のチャット送信から | アシスタントの設定、運用・監視の接続（アシスタントで使う場合） |
| ワークスペースの停止 → 起動 | ツールチェーン（タイムゾーン・言語のバージョン）、マシン（管理者が変えたサイズ・種類） |

### やりたいこと → どのタブか

| やりたいこと | タブ |
|---|---|
| 文字が小さい / 配色を変えたい | 表示 |
| 別のサインイン方法でも入れるようにしたい | アカウント |
| Enter で送信したい | キー操作（送信キー） |
| ペインをタブで切り替えたい | 表示（メイン領域の配置） |
| ショートカットを変えたい / ターミナルにキーを渡したい | キー操作 |
| 回答を読み上げてほしい | 読み上げ |
| Slack への通知だけ止めたい | 通知（サービス通知） |
| アシスタントを英語で答えさせたい | アシスタント（回答言語） |
| オペレーターに勝手に進めさせたい / 止めたい | アシスタント（自動進行・自動応答） |
| タイトルや返信候補を AI に作らせたくない | AI 補助（機能ごとのオン／オフ） |
| 補助生成だけ安いモデルで回したい | AI 補助（優先順位・モデル） |
| 毎回同じ前置きをプロンプトに書いている | エージェントへの指示 |
| Claude / Codex にログインしたい | エージェント |
| 課金されるモデルを選ばせたくない | エージェント（使わないモデル） |
| 停止したセッションを一覧に長く残したい・早く片付けたい | エージェント（停止中セッションのアーカイブ） |
| セッション同士で連絡させたい | エージェント（セッション間メッセージ） |
| セッションに過去の会話を検索させたくない | エージェント（過去セッションの検索） |
| エージェントが学んだことを種類をまたいで共有したい | エージェントメモリ（設定） |
| セッションにほかのセッションの状況を見せたい | 設定は不要。どのセッションでもできます（[セッションに全体を見せる](../user/fleet/fleet-visibility.ja.md)） |
| 作業中に気づいたことをメモに残させたい | 設定は不要。どのセッションでもできます（[セッションに全体を見せる](../user/fleet/fleet-visibility.ja.md)） |
| private リポジトリを clone したい | Git ホスティング |
| インシデントの調査を手伝わせたい | 運用・監視 |
| チケットや Issue から作業を始めたい | 課題管理 |
| 席を外しても進捗を受け取りたい | チャット連携 |
| 社内ツールを AI から使わせたい | MCP サーバー（セッションで使う） |
| 手元の Claude から操作したい | 外部からのアクセス → MCP トークン・接続アプリ |
| トークンを何に使ったか知りたい | エージェント使用量 |
| 自分のワークスペースにいくらかかっているか知りたい | クラウド費用 |
| ワークスペースがいつ動いていたか知りたい | 稼働時間 |
| 変なことを覚えさせてしまった | エージェントメモリ（メモリ・変更）、メモリのスナップショット（履歴） |
| Java / Node のバージョンを変えたい | ツールチェーン |
| DB が要るテストを回したい | データベース |
| 作っているアプリを人に見せたい | プレビュー用サブドメイン |
| 別のサーバーに入りたい | AWS プロファイル/SSM |
| 自分の Google Cloud プロジェクトでコマンドを実行したい | Google Cloud |
| 外部に出せないコードを置きたい | 内部リポジトリ |
| 別の環境へ設定を持っていきたい | 書き出し / 取り込み |
| 環境が壊れた | 危険な操作 |

## テナント設定

| タブ | 決めるもの |
|---|---|
| メンバー | 名簿。メンバーごとの資源・セッション・操作 |
| 推論エンジンの利用者 | 配備の自前エンジンを役割（チャット `llm`・画像生成 `image`）ごとに、全メンバーに開くか、チェックしたメンバーだけにするか（配備管理者がテナントに許した範囲の中で）|
| セッション | いまテナント内で動いている全部 |
| 上限・自動停止 | 効いている上限（**読み取り専用**。決めるのは配備管理者）|
| 利用枠ボード | テナントのメンバーが利用上限の状況を公開できるかどうか（既定はオン。オンでも各メンバーは自分で選ぶまで非公開）|
| サインイン方式 | 自社の IdP や GitHub 組織を入口にする（承認が要る）|
| サインイン規則 | 効いている参加方式とドメイン（読み取り専用）|
| 接続元の制限 | メンバーがどこから接続してよいか。同じ画面にある **MCP OAuth** の欄で、外のアプリが OAuth でテナントに接続してよいかも決める（[MCP サーバーを全員に配る](../admin/mcp-distribute.ja.md#外部のアプリからの-oauth-接続を止める)）|
| 連携アプリの OAuth | テナント自身の GitHub / Bitbucket OAuth アプリ |
| MCP 配布 | 全メンバーへ渡す連携サーバー |
| ブランチ命名ルール | 新しいブランチの名前と起点の、テナントとしての既定（リポジトリのパターンごと・JSON の一覧）。メンバーのワークスペースは 5 分以内に取り込む。**助言のみ**。外れた名前は警告になるだけで、拒否はされない |
| ウォッチのソース | メンバーが自分の MCP サーバーをウォッチのソースにできるか。ブロック（既定）だと設定を断り、テナントのソースをすべて止める（[MCP サーバーをウォッチのソースに許可する](../admin/watch-sources.ja.md)） |
| 外部からのメモリ利用 | メンバーが `/mcp`（またはメモリ専用の `/mcp/memory`）経由で Agent Fleet の外のエージェントから自分のメモリを使えるか。許可（既定）ならメンバーが各自で有効にでき、ブロックするとテナント全体からメモリのツールを引き上げる（[外部からのメモリの利用を許可する](../admin/memory-outside.ja.md)） |
| メンバーの既定 | 初回のようこそカードが「エージェントを接続」で指定するエージェント（Claude・Codex・opencode・Cursor・Copilot・Kiro・Muse・指定しない）。メンバーは Console を読み込むたびに読む。**案内のみ**で、ほかのエージェントを接続しても手順は完了になる（[メンバーの既定値を決める](../admin/defaults/members.ja.md)） |
| 監査 | 誰が・いつ・何を変えたか |
| 稼働時間 | メンバーごとのワークスペース稼働時間（書き出し可）＋テナント全体の時間別ヒートマップ |
| クラウド費用 | テナントのクラウド費用をメンバーごとに。AWS で動く配備だけ（[稼働時間とクラウド費用を見る](../admin/usage.ja.md#クラウド費用を見る)）|
| 推論エンジンのモデル | 自前エンジンへモデルを足すモデルカタログを開く（配備管理者がテナントにモデルの取り込みを許した場合だけ。モデルの有効化は配備管理者のまま）|

## 管理モーダル

盾アイコンの**管理**から開きます。見えるのは配備管理者だけです（[roles.ja.md](roles.ja.md)）。左のレールは **テナント**・
**配備全体**・**横断で見る** の 3 グループです。項目によっては、対応する配備にしか出ません。**スロット**は
`ecs-ec2` ターゲット、**推論エンジン**と **API トークン**は Control Plane がエンジンのパネルを提供している配備、
**クラウド費用**は AWS の配備だけです。

| 項目 | 決めるもの | 詳細 |
|---|---|---|
| テナント一覧 | テナントの作成。各テナントの上限とアイドル自動停止、自前エンジンの利用可否とモデル取り込みの許可、メンバー | [インストールする](../operate/install.ja.md#7-メンバーを迎える) |
| サインイン方法の登録簿 | テナントが自分で定義したサインイン方法の承認 | [テナント独自のサインインを承認する](../operate/signin/tenant-idp.ja.md) |
| 通信 | ワークスペースからの外向き通信: 観測（log-only）か enforce か、許可リスト、メンバーの申請 | [外向きの通信を制御する](../operate/secure/egress.ja.md) |
| MCP ログイン許可ホスト | MCP OAuth サーバーが信頼するクライアントのホスト（配備全体）| [MCP のサインインを運用する](../operate/secure/mcp.ja.md#信頼するクライアントのホストを決める) |
| 読み上げ | 音声エンジンのモードと、共通の読み仮名辞書 | [読み上げエンジンを動かす](../operate/engines/read-aloud.ja.md) |
| 見た目 | この配備の色とラベル。ほかの配備と見分けるため | [複数の配備を見分ける](../operate/run/deployment-label.ja.md) |
| 開発用ログイン | プレビュー用サブドメイン上のアプリ向けの、開発用 OIDC IdP のスイッチ | [開発用サインインを使う](../operate/secure/dev-signin.ja.md#開発用-oidc-idp-をオンオフする) |
| 推論エンジン | 自前エンジンのモード・GPU クラス・モデルカタログ・状態と稼働実績 | [自前の GPU で推論エンジンを動かす](../operate/engines/gpu.ja.md) |
| API トークン | モデル取り込み用の Hugging Face と Civitai のトークン、Civitai Red のスイッチ | [推論エンジンにモデルを取り込む](../operate/engines/models.ja.md#トークンを登録する) |
| スロット | `ecs-ec2` のスロットプール: スロットの入れ替え、動かなくなったスロットの強制停止 | [スロットを入れ替える・止める](../operate/aws-ecs/slot-maintenance.ja.md) |
| セッション・稼働時間・クラウド費用・監査・MCP 配布 | テナントの各画面を、全テナント横断で見る | [admin/](../admin/README.ja.md) |

## 配備の変数

Control Plane の起動前に設定します。注釈つきの一覧は
`deploy/compose/.env.example` で、ほとんどの変数を載せていますが全部ではありません。ここには、ほかのすべての形を左右するものだけを挙げます。

| 変数 | 決めるもの |
|---|---|
| `AF_RUNTIME` | 配備形態（[deploy-targets.md](deploy-targets.ja.md)）。不明な値は**起動時に拒否**される |
| `AUTH` | サインインの方式。`oauth`（CP 内蔵）/ `proxy`（前段のゲートウェイ）/ `dev`（サインインなし・単独）。**既定値はなく、未設定だと Control Plane は起動を拒否する**（native は `dev` を使う）。Docker やクラウドで `dev` を使うには `AF_ALLOW_INSECURE_DEV_AUTH=1` も必要で、`PUBLIC_BASE_URL` が https のときは拒否される |
| `DATA_DIR` | 永続する状態がすべてここにあるため、**バックアップすべき対象** |
| `AF_MASTER_KEY` | 保存時の暗号化の大元になるキー。**失うと保存済みの認証情報は復元できない** |
| `AF_KEY_CUSTODIAN` | 保存時のキーを誰が持つか: `local`（既定。`AF_MASTER_KEY` から導く）か `kms`（AWS KMS。`AF_KMS_KEY_ID` と組で。[保存時のキーを AWS KMS にする](../operate/keys/kms.ja.md)）。未知の値や不足は起動時に拒否 |
| `AF_WORKSPACE_DEK` | `derived`（既定）か `random`: メンバーのホームごとに KMS で暗号化した専用のキーを持たせる。移し替えた認証情報ストアは `AF_MASTER_KEY` から導出したキーでは開けなくなり、KMS キーを無効化すると Control Plane はそのキーをアンラップできなくなる（ワークスペースとランタイムにすでに渡した写しでは今も開ける）。`random` は `AF_KEY_CUSTODIAN=kms` が前提で、無効にしても元には戻らない（[ホームごとにキーを分ける](../operate/keys/home-keys.ja.md)） |
| `SUPER_ADMIN_EMAILS` | 誰が配備管理者か |
| `PUBLIC_BASE_URL` | 利用者が到達する URL。OAuth のコールバックもここから組み立てる |
| `WS_MEMORY` | ワークスペースの既定メモリ上限 |
| `AF_PREVIEW_DOMAIN` | プレビュー用サブドメイン（`<ワークスペース>-<ポート>.<ドメイン>`）の親ドメイン。そのためのワイルドカードの DNS 名と証明書が要る。未設定ならプレビュー用サブドメインはなく、Console 自身のプレビューのパスだけになる。`AF_COOKIE_SECRET` と `PUBLIC_BASE_URL` も一緒に設定する。どちらかがないと、プレビューのサインインを信頼できないか、戻る先の Console がないため、Control Plane が起動時に警告する |
| `AF_DEV_IDP` | `0` にすると、開発用ログイン（プレビュー用サブドメイン上のメンバーのアプリが Google の代わりにサインインに使える、Console のオリジン上の OIDC プロバイダー）を、配備管理者のスイッチにかかわらずオフに固定する。未設定ならそのスイッチに従い、スイッチは既定でオン。提供するのは `AF_PREVIEW_DOMAIN` と `PUBLIC_BASE_URL` があるときだけ |
| `AF_MCP_ENABLED` | `true` で `/mcp` エンドポイントとメモリ専用の `/mcp/memory`（Personal Access Token）を提供する。それ以外（`1` を含む）はオフ。テナント単位のメモリのスイッチ（[外部からのメモリの利用を許可する](../admin/memory-outside.ja.md)）は別の、もっと狭いスイッチ |
| `AF_MCP_OAUTH` | `1` で、Control Plane が `/mcp` の OAuth 認可サーバーにもなり、ホスト型のクライアントはメンバーのサインインで接続できる。`AF_MCP_ENABLED=true` と `https` の `PUBLIC_BASE_URL` が要り、どれか欠けると OAuth のパスは 404。各テナントは自分の分をオフにもできる（[MCP サーバーを全員に配る](../admin/mcp-distribute.ja.md#外部のアプリからの-oauth-接続を止める)）。残りは [MCP のサインインを運用する](../operate/secure/mcp.ja.md#リモートの-mcp-クライアントを-oauth-でサインインさせる) |
| `AF_GITHUB_WEBHOOK_SECRET` | この配備の GitHub App の Webhook シークレット。設定すると、Control Plane は App の配信を `/webhooks/github` で受け付ける（App の Webhook URL は `<PUBLIC_BASE_URL>/webhooks/github`）。受けた配信は、実行中のワークスペースに「公開リポジトリのプルリクエストを早めに確認して」と知らせるためだけに使う。未設定ならこのエンドポイントは存在せず、ウォッチはポーリングだけで動く。プライベートリポジトリの配信はこの経路では無視する。配信の中身は何も渡さない。 |
| `AF_MCP_OAUTH_TRUSTED_HOSTS` | MCP OAuth が信頼するクライアントのホスト、または `https://host/path` の URL プレフィックス（カンマ区切り。ホスト名だけなら全パス）。既定は `https://claude.ai/api/mcp/auth_callback`・`claude.com`・`chatgpt.com`、`none` でループバックのクライアントだけ。管理モーダルの **MCP ログイン許可ホスト** で保存した一覧がこの変数に優先する（[MCP のサインインを運用する](../operate/secure/mcp.ja.md#信頼するクライアントのホストを決める)）|
| `AF_EFS_BACKUP` | 手で設定しない。`ecs` / `ecs-ec2` で `update.sh` と `standup.sh` が ingress スタックの `EfsBackup` パラメータ（`present`・`off`・`missing`）から埋め、`missing` なら管理画面に「日次の EFS バックアップがない」バナーが出る（[アップグレードする](../operate/run/upgrade.ja.md#efs-バックアップがないという通知)） |
| `AF_SESSION_IDLE_TIMEOUT`・`AF_WS_IDLE_TIMEOUT`・`AF_BACKGROUND_HOLD_MAX`・`AF_WORKSPACE_START_DEADLINE` | アイドルのセッションとワークスペースを止めるタイミング。既定値は [上限](limits.ja.md#配備が決めるもの)、動きは [アイドル停止と強制停止を使う](../operate/run/idle-stop.ja.md#アイドル自動停止の時間を決める) |

### AWS ECS の場合

ECS のスタックは自由形式の環境変数を受け付けません。Control Plane の変数がタスクに届くのは、`30-ingress.yaml`
のパラメータ経由だけです。下のパラメータはどれも既定が空（または `off`）で、空なら環境変数を足さないので、
Control Plane は自分の既定値のままです。

| 変数 | スタックのパラメータ |
|---|---|
| `AF_SESSION_IDLE_TIMEOUT`・`AF_INTERACTION_IDLE_TIMEOUT`・`AF_WS_IDLE_TIMEOUT`・`AF_PRESENCE_IDLE_TIMEOUT`・`AF_BACKGROUND_HOLD_MAX`・`AF_WORKSPACE_START_DEADLINE`・`AF_USAGE_SAMPLE_INTERVAL` | `SessionIdleTimeout`・`InteractionIdleTimeout`・`WsIdleTimeout`・`PresenceIdleTimeout`・`BackgroundHoldMax`・`WorkspaceStartDeadline`・`UsageSampleInterval`（Go の期間表記。`0` でそのタイマーを止める） |
| `AF_GITHUB_LOGIN_CLIENT_ID`・`AF_GITHUB_LABEL_JA`・`AF_GITHUB_LABEL_EN`・`AF_GITHUB_MEMBERSHIP_TTL`・`AF_GITHUB_MEMBERSHIP_GRACE` | `GithubLoginClientId`・`GithubLabelJa`・`GithubLabelEn`・`GithubMembershipTtl`・`GithubMembershipGrace` |
| `AF_LLM_URL`・`AF_COMFY_URL`・`AF_REMOTE_ENGINE_URL`・`AF_REMOTE_ENGINE_KEYS` | `LlmUrl`・`ComfyUrl`・`RemoteEngineUrl`・`RemoteEngineKeys` |
| `AF_GITHUB_LOGIN_CLIENT_SECRET`・`AF_COMFY_API_KEY`・`AF_REMOTE_ENGINE_TOKEN` | スイッチ `GithubLoginSecret`・`ComfyApiKey`・`RemoteEngineToken`（`on`）。値は SSM SecureString の `<SsmPrefix>/github-login-client-secret`・`/comfy-api-key`・`/remote-engine-token` から読むので、先に作っておきます |

`WS_ENV` には ECS 用のパラメータを意図して用意していません。自由形式の `KEY=VAL` で、シークレットの値も平文の環境変数に載るためです。パラメータは
`30-ingress.yaml` に対する個別の `aws cloudformation deploy` で設定します（`update.sh` が上書きするのは `ImageTag` と、ほかのスタックから読む事実 `EfsBackup`・`WorkspaceSubnets`・`EgressProxyAddr`・`EgressTokenSecretArn`・`EgressArchiveBucket` だけです）。
詳しくは
[`deploy/aws/ecs/cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines)。
LAN のエンジンに ECS から届くのは、VPC にそこへの経路（VPN やピアリング）がある場合だけです。
