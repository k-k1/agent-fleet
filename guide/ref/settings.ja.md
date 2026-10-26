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
| 個人 | 自分 | アカウントメニュー →**設定** | 即座、または次のセッションから |
| テナント | テナント管理者 | アカウントメニュー →**テナント設定** | そのテナントのメンバーに |
| 配備 | 配備を運用する人 | 環境変数（起動前）| 再起動時 |

狭い層は広い層を広げられません。個人設定はテナントの上限を超えられず、テナント設定は
配備が許す範囲を超えられません。設定した値が効いている値と違うときは、1 つ外の層を見てください。

## 個人設定

| タブ | 決めるもの |
|---|---|
| 表示 | 言語・テーマ・情報密度 |
| キー操作 | ショートカットと再割り当て |
| エージェント | 各エージェントの接続・既定モデル・除外するモデル・挙動。llama.cpp のオン／オフと自分の llama-server 接続先、Muse Code の導入とログイン。セッショングループ — セッション間メッセージ・セッションからのセッション起動と子セッションの同時本数・過去セッションの検索・画像生成 |
| エージェントへの指示 | このワークスペースで起こす全エージェントに足される文 |
| エージェントメモリ | Agent Fleet 自身の共有メモリ。4 つの表示: メモリ（閲覧・検索・作成・編集・ピン留め・忘れさせる・忘却を取り消す・履歴の消去）、変更（変更一覧と元に戻す・忘れさせる、セッションが提案したメモリを承認・却下する **提案**）、取り込み・書き出し（カードのハブ：Claude Code・muse・メモリのバンドルからの取り込み、Claude Code への書き戻し、プロジェクトの移動）、設定（セッション用スイッチと同期の状態、外部からの利用、**終わったセッションからメモリを提案させる** と **自動でも提案させる**）。[member/18](../member/18-agent-memory.ja.md) を参照 |
| メモリのスナップショット | 各エージェント種別が自前で持つメモリの版管理。3 つの表示: 履歴（この時点に戻すつき。Agent Fleet のメモリの書き込みは頼まない限り隠す）、対象（対象のルート・codex のメモリ・自動スナップショット）、書き出し・取り込み |
| アシスタント | アシスタントチャットのエージェントとモデル |
| AI 補助 | タイトル・ブランチ名・返信候補・編集提案・計画更新・翻訳の既定エージェントとモデル、機能ごとのオン・オフ／エージェント／モデルの上書き |
| エージェント使用量 | 自分のトークン消費（機能別・エージェント別・モデル別）|
| クラウド費用 | 配備のクラウド費用のうち自分の分 |
| 稼働時間 | 自分のワークスペースが動いていた時間（24 時間 × 日付のヒートマップ）|
| Git ホスティング | GitHub / Bitbucket の接続 |
| 内部リポジトリ | 配備自身がホストするリポジトリ |
| AWS プロファイル/SSM | AWS プロファイルとリモートログイン先 |
| Google Cloud | `af-gcloud-exec` がコマンドの実行に使う Google Cloud のプロファイル |
| 課題管理 | Jira ほか作業項目の取得元 |
| チャット連携 | Discord / Slack のブリッジ |
| MCP サーバー（セッションで使う） | エージェントが使える連携サーバー |
| MCP トークン・接続アプリ | 外部から自分のセッションを動かすためのトークン。エージェントメモリの設定と合わせれば、外部のエージェントから自分の Agent Fleet のメモリを使うためにも使う |
| 通知 | 何を、どう知らせるか |
| 利用枠ボード | 利用上限に対する自分の状況を、このテナントの誰に・どこまで見せるか（既定は非公開）。メンバーが自分に公開している状況 |
| 読み上げ | 読み上げエンジンと声 |
| マシン | 自分のワークスペースが動いているインスタンス（種類・アーキテクチャ・vCPU・メモリ・ディスク）|
| ツールチェーン | JDK ほかワークスペース毎のツールチェーン |
| データベース | DB を使うテスト用の PostgreSQL / MySQL（導入・開始・停止・作成・リセット）|
| プレビュー用サブドメイン | 作っているアプリを外から開く URL（発行する配備のみ）|
| 運用・監視 | 監視連携 |
| 書き出し / 取り込み | 設定を別の配備へ持って行く |
| アカウント | 自分のアカウントに紐づくサインイン方式 |
| 危険な操作 | 自分のワークスペースへの破壊的操作 |

## テナント設定

| タブ | 決めるもの |
|---|---|
| メンバー | 名簿。メンバー毎の資源・セッション・操作 |
| 推論エンジンの利用者 | 配備の自前エンジンを役割（チャット `llm`・画像生成 `image`）ごとに、全メンバーに開くか、チェックしたメンバーだけにするか（配備管理者がテナントに許した範囲の中で）|
| セッション | いまテナント内で動いている全部 |
| 上限・自動停止 | 効いている上限（**読み取り専用**。決めるのは配備管理者）|
| 利用枠ボード | テナントのメンバーが利用上限の状況を公開できるかどうか（既定はオン。オンでも各メンバーは自分で選ぶまで非公開）|
| サインイン方式 | 自社の IdP や GitHub 組織を入口にする（承認が要る）|
| サインイン規則 | 効いている参加方式とドメイン（読み取り専用）|
| 接続元の制限 | メンバーがどこから接続してよいか。同じ画面にある **MCP OAuth** の欄で、外のアプリが OAuth でテナントに接続してよいかも決める（[admin 04](../admin/04-mcp-outside.ja.md#mcp-oauth)）|
| 連携アプリの OAuth | テナント自身の GitHub / Bitbucket OAuth アプリ |
| MCP 配布 | 全メンバーへ渡す連携サーバー |
| ブランチ命名ルール | 新しいブランチの名前と起点の、テナントとしての既定（リポジトリのパターンごと・JSON の一覧）。メンバーのワークスペースは 5 分以内に取り込む。**助言のみ**——外れた名前は警告になるだけで拒否されない |
| 外部からのメモリ利用 | メンバーが `/mcp`（またはメモリ専用の `/mcp/memory`）経由で Agent Fleet の外のエージェントから自分のメモリを使えるか。許可（既定）ならメンバーが各自で有効にでき、ブロックするとテナント全体からメモリのツールを引き上げる（[管理 04](../admin/04-mcp-outside.ja.md#agent-fleet-の外からのメモリ利用)） |
| メンバーの既定 | 初回のようこそカードが「エージェントを接続」で名指しするエージェント（Claude・Codex・opencode・Cursor・Copilot・Kiro・Muse・指定しない）。メンバーは Console を読み込むたびに読む。**案内のみ**——ほかのエージェントで接続しても手順は完了になる（[管理 06](../admin/06-team-defaults.ja.md#メンバーの既定)） |
| 監査 | 誰が・いつ・何を変えたか |
| 稼働時間 | メンバー毎のワークスペース稼働時間（書き出し可）＋テナント全体の時間別ヒートマップ |
| クラウド費用 | テナントのクラウド費用をメンバーごとに。AWS で動く配備だけ（[admin 03](../admin/03-audit-usage.ja.md#クラウド費用クラウド費用)）|
| 推論エンジンのモデル | 自前エンジンへモデルを足すモデルカタログを開く（配備管理者がテナントにモデルの取り込みを許した場合だけ。モデルの有効化は配備管理者のまま）|

## 管理モーダル

盾アイコンの**管理**で、見えるのは配備管理者だけです（[roles.ja.md](roles.ja.md)）。左のレールは **テナント**・
**配備全体**・**横断で見る** の 3 グループです。項目によっては、対応する配備にしか出ません: **スロット**は
`ecs-ec2` ターゲット、**推論エンジン**と **API トークン**は Control Plane がエンジンのパネルを提供している配備、
**クラウド費用**は AWS です。

| 項目 | 決めるもの | 詳細 |
|---|---|---|
| テナント一覧 | テナントの作成。各テナントの上限とアイドル自動停止、自前エンジンの利用可否とモデル取り込みの許可、メンバー | [operate/02](../operate/02-install.ja.md#7-最初のテナントとメンバー) |
| サインイン方法の登録簿 | テナントが自分で定義したサインイン方式の承認 | [operate/05](../operate/05-signin.ja.md#7-テナントが自分で定義するサインイン方式) |
| 通信 | ワークスペースからの外向き通信: 観測（log-only）か enforce か、許可リスト、メンバーの申請 | [operate/04](../operate/04-secure.ja.md#egress-統制の運用) |
| MCP ログイン許可ホスト | MCP OAuth サーバーが信頼するクライアントのホスト（配備全体）| [operate/04](../operate/04-secure.ja.md#信頼するクライアントのホスト配備全体-superadmin-のみ) |
| 読み上げ | 音声エンジンのモードと、共通の読み仮名辞書 | [operate/07](../operate/07-engines.ja.md#読み上げ音声エンジンと共通の読み仮名辞書) |
| 見た目 | この配備の色とラベル。ほかの配備と見分けるため | [operate/03](../operate/03-run.ja.md#2-つの配備を見分ける色とラベル) |
| 開発用ログイン | プレビュー用サブドメイン上のアプリ向けの、開発用 OIDC IdP のスイッチ | [operate/04](../operate/04-secure.ja.md#開発用ログイン開発用-oidc-idp-superadmin-のみ) |
| 推論エンジン | 自前エンジンのモード・GPU クラス・モデルカタログ・状態と稼働実績 | [operate/07](../operate/07-engines.ja.md) |
| API トークン | モデル取り込み用の Hugging Face と Civitai のトークン、Civitai Red のスイッチ | [operate/07](../operate/07-engines.ja.md#モデルの取り込みとテナントとの関係) |
| スロット | `ecs-ec2` のスロットプール: スロットの入れ替え、動かなくなったスロットの強制停止 | [operate/11](../operate/11-slot-pool.ja.md) |
| セッション・稼働時間・クラウド費用・監査・MCP 配布 | テナントの各画面を、全テナント横断で見る | [admin/](../admin/README.ja.md) |

## 配備の変数

Control Plane の起動前に設定します。注釈つきの一覧は
`deploy/compose/.env.example` で、ほとんどの変数を載せていますが全部ではありません。ここには「他の全部の形を決めてしまう」ものだけを挙げます。

| 変数 | 決めるもの |
|---|---|
| `AF_RUNTIME` | 配備形態（[deploy-targets.md](deploy-targets.ja.md)）。不明な値は**起動時に拒否**される |
| `AUTH` | サインインの方式。`dev`（単独）/ `oauth`（CP 内蔵）/ `proxy`（前段のゲートウェイ）|
| `DATA_DIR` | 永続する状態がすべてここにあるため、**バックアップすべき対象** |
| `AF_MASTER_KEY` | at-rest 暗号の根。**失うと保存済みの資格情報は復元できない** |
| `AF_KEY_CUSTODIAN` | 保存時の鍵を誰が持つか: `local`（既定。`AF_MASTER_KEY` から導く）か `kms`（AWS KMS。`AF_KMS_KEY_ID` と組で。[operate/12](../operate/12-keys.ja.md#aws-kms-で保存時の鍵を守る)）。未知の値や不足は起動時に拒否 |
| `AF_WORKSPACE_DEK` | `derived`（既定）か `random`: メンバーのホームごとに KMS で封じた専用の鍵を持たせる。移し替えた資格情報ストアは `AF_MASTER_KEY` から導いた鍵では開けなくなり、KMS 鍵を無効化すると Control Plane は鍵を開けなくなる（ワークスペースとランタイムにすでに渡した写しでは今も開ける）。`random` は `AF_KEY_CUSTODIAN=kms` が前提で、無効にしても元には戻らない（[operate/12](../operate/12-keys.ja.md#ホームごとに専用の鍵を持たせる)） |
| `SUPER_ADMIN_EMAILS` | 誰が配備管理者か |
| `PUBLIC_BASE_URL` | 利用者が到達する URL。OAuth のコールバックもここから組み立てる |
| `WS_MEMORY` | ワークスペースの既定メモリ上限 |
| `AF_PREVIEW_DOMAIN` | プレビュー用サブドメイン（`<ワークスペース>-<ポート>.<ドメイン>`）の親ドメイン。そのためのワイルドカードの DNS 名と証明書が要る。未設定ならプレビュー用サブドメインは無く、Console 自身のプレビューのパスだけになる。`AF_COOKIE_SECRET` と `PUBLIC_BASE_URL` も一緒に設定する。どちらかが無いと、プレビューのサインインを信頼できないか戻る先の Console が無いため、Control Plane が起動時に警告する |
| `AF_DEV_IDP` | `0` にすると、開発用ログイン（プレビュー用サブドメイン上のメンバーのアプリが Google の代わりにサインインに使える、Console のオリジン上の OIDC プロバイダ）を、配備管理者のスイッチにかかわらずオフに固定する。未設定ならそのスイッチに従い、スイッチは既定でオン。提供するのは `AF_PREVIEW_DOMAIN` と `PUBLIC_BASE_URL` があるときだけ |
| `AF_MCP_ENABLED` | `true` で `/mcp` エンドポイントとメモリ専用の `/mcp/memory`（Personal Access Token）を提供する。それ以外（`1` を含む）はオフ。テナント単位のメモリのスイッチ（[admin 04](../admin/04-mcp-outside.ja.md#agent-fleet-の外からのメモリ利用)）は別の、もっと狭いスイッチ |
| `AF_MCP_OAUTH` | `1` で、Control Plane が `/mcp` の OAuth 認可サーバーにもなり、ホスト型のクライアントはメンバーのサインインで接続できる。`AF_MCP_ENABLED=true` と `https` の `PUBLIC_BASE_URL` が要り、どれか欠けると OAuth のパスは 404。各テナントは自分の分をオフにもできる（[admin 04](../admin/04-mcp-outside.ja.md#mcp-oauth)）。残りは [operate/04](../operate/04-secure.ja.md#リモート-mcp-クライアント向けのサインインoauth) |
| `AF_MCP_OAUTH_TRUSTED_HOSTS` | MCP OAuth が信頼するクライアントのホスト（カンマ区切り）。既定は `claude.ai`・`claude.com`・`chatgpt.com`、`none` でループバックのクライアントだけ。管理モーダルの **MCP ログイン許可ホスト** で保存した一覧がこの変数に優先する（[operate/04](../operate/04-secure.ja.md#信頼するクライアントのホスト配備全体-superadmin-のみ)）|
| `AF_EFS_BACKUP` | 手で設定しない。`ecs` / `ecs-ec2` で `update.sh` と `standup.sh` が ingress スタックの `EfsBackup` パラメータ（`present`・`off`・`missing`）から埋め、`missing` なら管理画面に「日次の EFS バックアップがない」バナーが出る（[operate/03](../operate/03-run.ja.md#アップグレード)） |
| `AF_SESSION_IDLE_TIMEOUT`・`AF_WS_IDLE_TIMEOUT`・`AF_BACKGROUND_HOLD_MAX`・`AF_WORKSPACE_START_DEADLINE` | アイドルのセッションと Workspace を止めるタイミング。既定値は [上限](limits.ja.md#配備が決めるもの)、動きは [operate/03](../operate/03-run.ja.md#アイドル停止と-force-stop) |

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

`WS_ENV` には ECS 用のパラメータを意図して用意していません。自由形式の `KEY=VAL` で、秘密の値も平文の環境変数に載るためです。パラメータは
`30-ingress.yaml` に対する個別の `aws cloudformation deploy` で設定します（`update.sh` が上書きするのは `ImageTag` と、ほかのスタックから読む事実 `EfsBackup`・`WorkspaceSubnets`・`EgressProxyAddr`・`EgressTokenSecretArn`・`EgressArchiveBucket` だけです）。
詳しくは
[`deploy/aws/ecs/cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines)。
LAN のエンジンに ECS から届くのは、VPC にそこへの経路（VPN やピアリング）がある場合だけです。
