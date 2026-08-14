# docs 索引

ドキュメントは性質（ジャンル）で分ける。**仕様の正は [dev/](dev/README.md)（開発者向け）とコード、
操作の正は [guide/](guide/README.ja.md)（利用者向け）、稼働状態の正は [HANDOFF](HANDOFF.md)。**

| ジャンル | 置き場 | 役割 |
|----------|--------|------|
| 開発者向け設計 | [dev/](dev/README.md) | アーキテクチャ・各コンポーネント・API・データモデル・セキュリティ・連携・デプロイ（**開発者はまず読む**・コードに追従）|
| 利用者向けガイド | [guide/](guide/README.ja.md) | ペルソナ別分冊（member / admin / operator / lite）。操作の正 |
| 引き継ぎ | [HANDOFF.md](HANDOFF.md) | このホストの稼働状態・実行作法・落とし穴・現在地 |
| 時系列ログ | [CHANGELOG-handoff.md](CHANGELOG-handoff.md) | 作業ログ（日付 + 1 行）|
| 前向きの計画 | [roadmap.md](roadmap.md) | フェーズ一覧・マイルストーン + Phase 3 詳細設計 |
| 意思決定（なぜ）| [decisions/](decisions/) | 採否の記録・捨てた選択肢（追記型・不変）|
| 使い終わった計画 | [history/](history/) | 完了済みフェーズの実装プラン・機能設計（記録）|

## 番号付きの機能設計・実装記録

- [20-container-audit-egress.md](20-container-audit-egress.md) — コンテナ内操作の監査ログ & egress 統制（enforce 未了・進行中）
- [23-go-refactor.md](23-go-refactor.md) — Go バックエンド内部リファクタ（CP / Agent、機能不変・ワイヤ互換。残=④契約の型化のみ）
- [24-tts-zundamon.md](24-tts-zundamon.md) — エージェント回答の音声読み上げ（✅ Phase 1〜2 実装済み、実環境検証待ち）
- [25-ops-monitoring.md](25-ops-monitoring.md) — サービス運用（監視・インシデント対応）向け拡張（✅ 4 連携（PagerDuty / Grafana / CloudWatch / AWS）実装済み）
- [26-agent-exit-recording.md](26-agent-exit-recording.md) — エージェントプロセスの終了理由記録（✅ Agent / CP / UI 実装済み、実機目視待ち）
- [27-agent-managed-driver.md](27-agent-managed-driver.md) — エージェント制御の Managed Driver 化（✅ P1〜P3 実装済み。Codex / OpenCode は managed が既定、CLI ルート常設。プロトコル実測を含む実装記録）
- [28-i18n.md](28-i18n.md) — Console 全面 i18n（✅ P0〜P6 実装済み＝エージェント出力言語まで、実機目視待ち）
- [29-keyboard-system.md](29-keyboard-system.md) — Console キーボード操作体系（capture-phase 単一ディスパッチャ＋Leader/パレット＋再割当/端末入力優先）（✅ P0〜P5 実装済み・残＝実機目視）
- [30-session-report.md](30-session-report.md) — フリート・オペレーターへセッション完了・質問・異常終了を自動報告（✅ 実装済み）
- [31-container-browser-pane.md](31-container-browser-pane.md) — Workspace 内 Chromium で localhost を開き描画・入力を Console ペインへ中継（✅ 実装済み・実コンテナで V1 サインオフ完了、[利用契約](31-container-browser-pane-ux-contract.md)）
- [32-agy-agent-kind.md](32-agy-agent-kind.md) — `kind=agy`（Antigravity CLI）を第4種別として実装する並行トラック計画（✅ 実装済み・実測記録を含む。設計は [decisions/0008](decisions/0008-antigravity-cli-agent-kind.md)）
- [33-chat-context-usage.md](33-chat-context-usage.md) — アシスタントチャットのコンテキスト肥大対策（✅ 全4段実装済み）
- [34-native-runtime.md](34-native-runtime.md) — Docker なし WSL 向けコンテナレス Runtime（✅ 実装済み・素の WSL2 実機検証待ち）
- [35-packaging.md](35-packaging.md) — パッケージング & 配布の4ターゲット設計（native / amd64 Linux / EC2-Single / ECS）（✅ dist publish 運用中（0.1.0〜）。リリースノートは `deploy/release/notes/`）
- [36-copilot-agent-kind.md](36-copilot-agent-kind.md) — `kind=copilot`（GitHub Copilot CLI）を第5種別として Terminal+Managed 両対応で実装（✅ 実装済み・実 CLI 契約テスト通過。設計判断は [decisions/0019](decisions/0019-copilot-agent-kind.md)）
- [37-chat-bridge.md](37-chat-bridge.md) — チャットブリッジ（Slack/Discord 連携）: 通知・双方向操縦・AUQ ボタン・承認ゲート（✅ Discord/Slack 実装済み（Slack は Socket Mode 対応）。設計判断は [decisions/0020](decisions/0020-chat-bridge.md)）
- [38-scheduled-execution.md](38-scheduled-execution.md) — 定時実行（オペレーター cron 型）: CP scheduler・wakeFirer・run 履歴・Console UI（✅ 実装済み（v1 コア〜P5.2・アシスタント発火まで）。設計判断は [decisions/0021](decisions/0021-scheduled-execution.md)）
- [39-agent-memory-management.md](39-agent-memory-management.md) — エージェントメモリ管理（git 差分管理・時点/プロジェクト単位ロールバック・環境間 import/export）（✅ P1〜P4 実装済み。設計判断は [decisions/0022](decisions/0022-agent-memory-management.md)）
- [40-cursor-agent-kind.md](40-cursor-agent-kind.md) — `kind=cursor`（Cursor CLI）を第7種別として Terminal+Managed 両対応で実装（✅ Track A/A2/B/C/D-3 実装済み（v1 は login-only）。実 CLI 実測記録を含む。設計判断は [decisions/0023](decisions/0023-cursor-agent-kind.md)）
- [41-svn-checkout.md](41-svn-checkout.md) — SVN チェックアウト対応: URL＋基本認証のフラット作業コピー・認証/ロックの自己修復・自己署名証明書の opt-in 信頼（✅ 実装済み。設計判断は [decisions/0024](decisions/0024-svn-checkout.md)）
- [42-native-auto-update.md](42-native-auto-update.md) — ホスト常駐 `af` の自動更新: stage（取得）と apply（再起動）の分離で systemd の罠を回避（✅ 実装済み・実 systemd 通し検証待ち。設計判断は [decisions/0025](decisions/0025-native-auto-update.md)）
- [43-kiro-agent-kind.md](43-kiro-agent-kind.md) — `kind=kiro`（Kiro、旧 Amazon Q Developer CLI）を第8種別として Terminal+Managed 両対応で実装（✅ Track A/B/C/A2/D 実装済み・実 CLI E2E 通過。設計判断は [decisions/0026](decisions/0026-kiro-agent-kind.md)）
- [44-markdown-code-editor.md](44-markdown-code-editor.md) — Console の File ペイン編集、Markdown 3モード、`/fs/file` 保存API、revision競合、AI提案フォーマット、外部変更の追従（◐ Phase 0〜4 実装済み。Phase 5（hunk 単位＋複数候補）は設計のみ。ADR は [decisions/0027](decisions/0027-markdown-code-editor.md)）
- [44-operator-interaction-graph.md](44-operator-interaction-graph.md) — オペレーター↔セッションのやり取りを会話ごとの UML シーケンス図として可視化（番号は 44 重複。◐ Phase 0（契約凍結）実装済み・P1 以降は後続。ADR は [decisions/0027](decisions/0027-operator-interaction-graph.md)）
- [45-deletion-lock.md](45-deletion-lock.md) — セッション / 作業コピー / アシスタント会話の削除ロック（手動削除も自動 prune も止める。✅ 実装済み・実機目視待ち。設計判断は [decisions/0028](decisions/0028-deletion-lock.md)）
- [46-usage-accounting.md](46-usage-accounting.md) — 使用量アカウンティング（機能別トークン計測とグラフ化）: 補助 LLM 呼び出し（アシスタント/要約/タイトル/サジェスト）とセッション本体を同じ台帳で測る（◐ P0.5〜P4（Console UI 含む）実装済み・残 = P5（MCP）。実測記録を含む。設計判断は [decisions/0029](decisions/0029-usage-accounting.md)）
- [47-turn-abort-auto-resume.md](47-turn-abort-auto-resume.md) — 中断ターンの検知と自動再開: API エラーで切れたターン（Stop フックが鳴らない）を自己修復経路で取りこぼさず通知・報告し、再送で直る中断だけアシスタントが再開させる（✅ 実装済み・実機目視待ち。実測記録を含む。設計判断は [decisions/0030](decisions/0030-turn-abort-auto-resume.md)）
- [48-mcp-registry.md](48-mcp-registry.md) — ユーザー / テナント独自 MCP サーバーの登録: 固定 3 連携をレジストリへ一般化し、アシスタントには起動単位で、対話セッションには各 CLI のネイティブ設定へ書き出して配る（✅ P0〜P5 実装済み。各 CLI の MCP 設定形の実測記録を含む。設計判断は [decisions/0031](decisions/0031-mcp-registry.md)）
- [49-mcp-2026-07-28.md](49-mcp-2026-07-28.md) — MCP 2026-07-28（ステートレス版）対応: initialize/セッション廃止と per-request `_meta` へ、af の MCP サーバー2本と接続テストを両 era 同時対応にする（◐ 実装済み・認可の OAuth 2.1 整合は範囲外。仕様契約と実測記録を含む。設計判断は [decisions/0032](decisions/0032-mcp-2026-07-28.md)）
- [50-mirror-skill-picker.md](50-mirror-skill-picker.md) — ミラーのスキルピッカー: セッションで呼べるスキル/コマンドをセッション単位 API で認識し、コンポーザーのトリガ文字補完＋ボタンでキーボード / マウス / タップいずれでも 1〜2 操作で呼ぶ。クロスエージェント対応（claude/codex/opencode/cursor — codex は `$name` メンション・cursor は ACP 広告リスト。各 kind の実測記録 §7 を含む）（✅ 実装済み・実機目視待ち。設計判断は [decisions/0034](decisions/0034-mirror-skill-picker.md)）
- [51-session-report-v2-ledger.md](51-session-report-v2-ledger.md) — セッション報告 v2: docs/30 の「エッジ駆動＋1bit arm」を指示台帳＋レベル駆動リコンサイラ＋冪等シンクへ置き換える後継設計。既知の穴 A〜G の棚卸し、settled/progressed 証拠テーブル、誤報告の補償 reopen、自己申告ファストパス、3 Phase 移行計画（✅ Phase 1〜3 実装済み。設計判断は [decisions/0035](decisions/0035-session-report-v2-ledger.md)）
- [52-working-sets.md](52-working-sets.md) — 作業グループ（working set）: 名前付きの { 作業コピー, 会話, repo なしセッション } の集合で左ペインの表示を案件ごとに分離。定義は ui-prefs 同期・選択中は端末ローカル・サーバ変更ゼロのフロント完結（✅ 実装済み・実機目視待ち。設計判断は [decisions/0036](decisions/0036-working-sets.md)）
- [53-chromium-attach-view.md](53-chromium-attach-view.md) — 外部プロセスが所有するheadless Chromiumへloopback CDPでattachし、描画・限定入力をConsoleへ中継。ローカルAF MCPが引き渡しを準備し、ユーザーはaction linkを1回クリックして操作ペインを開く（設計確定・未実装。設計判断は [decisions/0038](decisions/0038-chromium-attach-view.md)）
- [54-opencode-console-oauth.md](54-opencode-console-oauth.md) — opencode に API キー以外の認証経路を足す: 共有 `opencode serve` の device flow API（`mode:"auto"`）を Console の接続カードから叩き、opencode.ai アカウントでサインインする（✅ 実装済み。API 実測記録を含む）
- [55-fork-at-message.md](55-fork-at-message.md) — 発言時点からの会話分岐: ミラーの過去のユーザー発言を選び、そこまでの文脈を持つ新セッションを起こす。アンカーは kind 固有の不透明 ID（claude=uuid / codex=turn id / opencode=message id / copilot=event id）で、codex・opencode は公式パラメータ、claude・copilot は転写の切り詰め（各 CLI の実測記録を含む。◐ P1〜P5＝契約＋4 kind（claude/codex/opencode/copilot）＋Console 導線＋「この発言の続きから」まで実装済み・4 種とも実 CLI で通し確認済み。残＝Console からの通し確認。cursor/kiro/agy は対象外と確定。設計判断は [decisions/0039](decisions/0039-fork-at-message.md)）
- [56-project-mcp.md](56-project-mcp.md) — プロジェクトスコープ MCP の管理: 1 つの作業コピーの `.mcp.json` / `opencode.json` / `.codex/config.toml` … を横断で 1 枚に見せ、利用者の明示操作でエージェント間へ反映する（ワンショット・継続同期なし）。プレースホルダ方言（claude=`${VAR}` / opencode=`{env:VAR}` / codex=展開なし）とプロジェクトスコープのゲートの実測記録を含む（設計確定・未実装。設計判断は [decisions/0040](decisions/0040-project-mcp.md)）
- [57-project-tools.md](57-project-tools.md) — プロジェクト単位ツールの共通土台: 作業コピーの中のエージェント設定を扱う道具に共通する「プロジェクトファイル憲章」8 条（自動契機を作らない / 所有マーカーを置かず監査は git / 整形を壊さない編集 / 0600 に頼らない / 他 worktree に触らない / ゲートを代行しない …）と、共通の入口・REST 規約・`internal/projcfg`。1 号機は [56](56-project-mcp.md)（方針確定・未実装）
- [58-cross-session-messaging.md](58-cross-session-messaging.md) — セッション同士のメッセージ: アシスタントを介さず、セッションが `list_peer_sessions` / `send_to_peer_session` で互いへ平文1本を送る。配管は既存（停止中は再開して届け、ターン開始まで配達検証）で、新規は帰属と安全弁 — 指示台帳の arm に触らない・shell/ssm は送受信とも不可・封筒はプロンプト前置・ループ対策とミラー専用行。Claude ネイティブ機能の扱いも決める（設計確定・未実装。**ネイティブ経路は有効化しない** — 有効化に telemetry 復活が要ると実測で判明したため。env 行列・socket 実体・着信ターンの transcript 構造（`origin.kind:"peer"`）まで実機実測した記録を含み、**公開ドキュメントと一致しなかった2点**も記録。設計判断は [decisions/0041](decisions/0041-cross-session-messaging.md)）
- [59-session-sharing.md](59-session-sharing.md) — セッション共有: 同一テナントの別ユーザーへ会話を共有する。共有先は所有者の Workspace やファイル API へ直接到達せず、CP が毎回 ACL を評価して会話 DTO だけを中継する。共有単位は動的（セッション / プロジェクト＝ベース＋配下 worktree / worktree 単位）で、権限は閲覧のみと「提案可」（RW の入力は所有者承認を経てから Agent へ届く）。アーカイブ済みは一覧・直リンク・提案から外す（規則と catalog 行は残るので復元すれば戻る）。実装済み・develop マージ済
- [60-user-instructions.md](60-user-instructions.md) — ユーザー指示: フリート方針（イメージ焼き込み）とプロジェクト指示（コミットされる）の間に「その人の層」を作る。正本は `~/.config/agent-fleet/user-notes.md` 1 本で、**「他人のファイルに書く」より「AF 専用ファイル＋参照」を優先**して配る（claude=user memory を単独所有 / opencode=`instructions` に 1 本追加 / copilot=`instructions/` に AF 専用ファイル / kiro=global steering に AF 専用ファイル / 合成は参照手段の無い codex・agy だけ）。**利用者の追記が毎起動 `cp -f` で消えていた**こと・**agy/copilot/kiro/cursor はフリート方針すら読んでいない**ことを実測で確認。7 種の user スコープ契約を実測した記録を含み（cursor は**ローカルにユーザー層が無く対応不可**と確定・claude は `$CLAUDE_CONFIG_DIR/CLAUDE.md`・codex のバイト予算は **global を含まないと判明＝疑っていた既存バグは無かった**）、課金なしの契約検証手段（`codex debug prompt-input` / 行動カナリア）も型として残した（**P0〜P2 実装済み** — ユーザー指示は 6 kind（claude/codex/opencode/copilot/agy/kiro）＋ Console 設定タブ、フリート方針も同じ配布器で agy/copilot/kiro へ届くようにした。配れないのは cursor だけで理由は構造的。設計判断は [decisions/0042](decisions/0042-user-instructions.md)）
- [61-login-idp.md](61-login-idp.md) — ログイン IdP: L1 の入口を Google 固定から複数化する。**プロバイダ別実装を増やさず汎用 OIDC を 1 本**作り（Entra ID / Okta / Keycloak / Auth0 / Cognito が同時に載る・Google も同実装の 1 インスタンスへ移すが env 名は据え置き）、専用アダプタは OIDC 非対応の **GitHub だけ**（org メンバーシップ判定とセットでのみ）。難所は OAuth ではなく identity で、`user_key = sanitizeUser(email)` が **home ディレクトリ名かつ暗号化 secrets の帰属先**のため、email が IdP 間で違うと workspace が分裂する → `identity_provider` テーブル（`(provider, subject)`）を横に足し、**リンク機構（P1）を GitHub（P2）より先に入れる**。email の信頼根拠は IdP ごとに違う（**Entra は `email_verified` を出さない**・GitHub は `/user/emails` の `primary && verified`）ので provider ごとの `trust` 宣言にし、`common` エンドポイント ＋ `tid` 未指定は fatal。SAML は実装せず `AUTH=proxy` ブリッジで回答。**テナント毎のログイン**（1 デプロイ内の部署分割・P3）も設計に含め、`/login/<slug>`・テナント毎の使用可能 provider・email/ドメイン規則を扱う — 要点は**「入口の門（`authGate`・毎リクエスト）」と「テナントの門（`resolveFull`）」を層として分ける**こと、**URL のテナント指定を認可の根拠にしない**こと（画面のボタンを絞るだけでは `X-AF-Tenant` 差し替えで抜けられる）、**「誰が入れるか」の名簿は membership が持ちテナントに email リストを足さない**こと（招待 API `POST /api/admin/memberships` が未ログインの人の identity も作れる＝器は既にある。テナント側に足すと二重台帳。これで全社共通ドメインの会社でも部署分割が成立し、IdP グループ連携は不要になった）。入口の門の判定に **membership を含める**のが現状で欠けている接続。さらに**テナント定義の認証方式**（P4・グループ各社で Entra テナント自体が違う場合）は、`client_secret` をテナント鍵で封印して DB に持ち tenant_admin が Console で編集する（前例は `mcp_server` の `headers_enc`）が、★ **有効化には super_admin の承認を挟む** — IdP を足せる人は `user_key`＝email がデプロイ全体で 1 つである以上そのデプロイの誰にでもなれるため。**P0（汎用 OIDC）は実装済み**、P1〜P4 は未実装。設計判断は [decisions/0043](decisions/0043-login-idp.md)）
- [62-ecs-start-latency.md](62-ecs-start-latency.md) — ECS の Workspace 起動レイテンシ: Fargate はタスク間でイメージキャッシュを持たないので、Start のたびに ~1GB の workspace イメージをフル pull する（収束まで実測 ~100s・初回は ALB 504）。**SOCI（Seekable OCI）遅延ロードの採否を一次情報で検証**し、前提は現構成が**変更ゼロで満たす**と確認（PV 1.4.0 は既定・ECR private・gzip・task def のフラグ不要）。記憶ベースの前提が一次情報と食い違った 3 点を記録 — **arm64 は対象外ではない**（X86_64/ARM64 両対応）・**「索引を同じ repo へ push」は v1 の話で新規は index manifest v2 のみ**（`soci create`+`push` ではなく **`soci convert`**。新規に v1 を付けても lazy load されずフル pull に落ちる）・**「タスク内の全イメージに索引が必要」は 2023-11 に撤廃済み**（AWS 自身のブログに旧記述が残る）。v2 は digest が変わる＋索引生成に containerd image store が要る（`docker build` は Docker store なので `soci convert --standalone` で回避）ため、置き場は `release-ecr.sh` の `--soci` で「**未変換を一度も push しない**」（`ImageTag` は CP と WS で共有＝別タグにできない）。効果は起動パス解析（entrypoint が読むのは node/npm/git/tmux だけで、chromium・Go・awscli の 6〜7 割は触らない）から**効く側**だが、**~100s の内訳が未計測**なのと lean variant の boot-install（~60s・純ネットワークで SOCI では縮まない）があるため **条件付き採用**＝ `describe-tasks` の `pullStartedAt`/`pullStoppedAt` を先に測るのがゲート。代替は (b) イメージ縮小＝保留（SOCI の効果を削る・EFS と NAT に付け替わるだけ）・(c) UI 吸収＝ほぼ実装済みで、残っていた **`AF_ECS_START_TIMEOUT_SEC` 90s > ALB idle 60s** の 504 は **P0.5 として解消済**（§62.5.1: `Start` は desiredCount 1 まででリターンし healthz 待ちは背景ゴルーチンへ。**ECS では同期待ちが成功する経路が無い**——`running`/`starting` は手前で早期 return するので `waitReady` 到達＝常にタスクをゼロから起動——ため 45s へ切り詰める暫定案は不採用。Console は元から Start 応答の `state` を読まずポーリングで歩くので FE 変更なし。実機未検証）・(d) EC2 起動タイプ＝**却下**（scale-to-zero が消え、その形は `ec2-single` として既にある）。**索引生成のツールチェーンは開発 Workspace で実測済み**で、起票時に書いた「Docker が無いので実検証できない」は**誤りだった** — `crane` + `soci convert --standalone` なら root も Docker も containerd も無しに index manifest v2（`artifactType: application/vnd.amazon.soci.index.v2+json`）まで生成できる（skopeo は apt 依存で入らないので crane を採る／**Docker は setuid を全部剥がしたイメージでは rootless すら原理的に不可**）。計測手順も具体化した（`entryPoint` を潰した probe タスク定義で pull を孤立・`run-task --overrides` は `command` しか上書きできない罠・シナリオは新規ホームと温ホームの 2 つで**判定は後者**（前者は boot-install の分だけ pull の割合が薄まり SOCI を過小評価する）・probe が P2 の A/B ハーネスを兼ねるので workspace 側の変更は不要）。計測に AWS MCP（Agent Toolkit for AWS）を使わない理由も記録（**既定 `--read-only` では `call_aws` 自体が消える**＝読み取りだけのために書き込みトグルを開けることになり過大）。SOCI 本体（P0 計測 / P1 導入）は未着手・ADR は P1 着手時に 0044 として起票

> 完了後も実装契約や実測リファレンスとしてコードから参照する 24・26〜30 は番号付きのまま残す。
> 時系列の実装プランとして役目を終えたものは history/ へ移動: [19 assistant-chat](history/19-assistant-chat.md) /
> [21 memo-queue](history/21-memo-queue.md) / [22 console-rebuild](history/22-console-rebuild.md)。

## reference/ は dev/ へ再編しました（2026-07）

旧 `reference/` の各ファイルは転送スタブになり、内容は下表のとおり dev/ へ移設済み。

| 旧 reference/ | 新しい置き場 |
|---------------|--------------|
| requirements.md | [dev/01](dev/01-architecture.md) + [roadmap](roadmap.md) |
| architecture.md | [dev/01](dev/01-architecture.md)（+ 06/07/08）|
| api-agent.md | [dev/05](dev/05-api-contracts.md)（+ 04/07）|
| portability.md | [dev/09](dev/09-deploy.md) |
| aws.md | [dev/09 §9.5](dev/09-deploy.md) |
| security.md | [dev/07](dev/07-security.md) |
| auth.md | [dev/07 §7.3](dev/07-security.md) + [dev/09 §9.3](dev/09-deploy.md) |
| preview.md | [dev/05 §5.3](dev/05-api-contracts.md)（旧記述は廃止）|
| internal-git-provider.md | [dev/91](dev/91-internal-git.md) |

## decisions/ — 意思決定の記録（ADR）

- [0001-self-host-vs-saas.md](decisions/0001-self-host-vs-saas.md) — SaaS 断念・各社セルフホスト採用
- [0002-claude-auth-onboarding.md](decisions/0002-claude-auth-onboarding.md) — auth と onboarding は別物
- [0003-ssh-to-connections.md](decisions/0003-ssh-to-connections.md) — SSH 鍵 → Connections
- [0004-vanilla-to-react.md](decisions/0004-vanilla-to-react.md) — Console は React + Vite
- [0005-envelope-custodian.md](decisions/0005-envelope-custodian.md) — 封筒暗号 + custodian 抽象
- [0006-mcp-unified.md](decisions/0006-mcp-unified.md) — MCP は管理面+作業面を一体・PAT 認証・E が主目的
- [0007-opencode-web-via-pk-webui.md](decisions/0007-opencode-web-via-pk-webui.md) — opencode web は pk-opencode-webui 経由で提供
- [0008-antigravity-cli-agent-kind.md](decisions/0008-antigravity-cli-agent-kind.md) — Antigravity CLI（`agy`）を第 4 のエージェント種別に
- [0009-transcript-paging.md](decisions/0009-transcript-paging.md) — transcript は末尾ウィンドウ読み込み + 逆方向ページング
- [0010-internal-git-provider.md](decisions/0010-internal-git-provider.md) — テナント内部 git プロバイダ（bare+http-backend を CP 自ホスト）**採用**
- [0011-console-rebuild.md](decisions/0011-console-rebuild.md) — Console リビルド：並行エントリ・zustand・旧側凍結（設計 [22](history/22-console-rebuild.md)）
- [0012-go-internal-refactor.md](decisions/0012-go-internal-refactor.md) — Go バックエンド内部リファクタ：internal 層化・2 バイナリ維持・共有モジュール見送り（設計 [23](23-go-refactor.md)）
- [0013-tts-zundamon.md](decisions/0013-tts-zundamon.md) — 回答の音声読み上げ：CP-native TTS・プロバイダ抽象・ずんだもん主役/Polly 受け皿・ECS オンデマンド（設計 [24](24-tts-zundamon.md)）
- [0014-agent-exit-recording.md](decisions/0014-agent-exit-recording.md) — エージェント終了理由記録：pane ラッパーで exit code 捕捉・自 cgroup で OOM 帰属・意図停止フラグ不要・CP は cgroup 直読み（設計 [26](26-agent-exit-recording.md)）
- [0015-agent-managed-driver.md](decisions/0015-agent-managed-driver.md) — Codex / OpenCode は共有 runtime の managed を既定、CLI ルートを常設（実装記録 [27](27-agent-managed-driver.md)）
- [0016-i18n.md](decisions/0016-i18n.md) — Console i18n は自前軽量層（ja/en・AST 裸和文 lint）（設計 [28](28-i18n.md)）
- [0017-keyboard-system.md](decisions/0017-keyboard-system.md) — Console キーボード操作体系：capture-phase 単一ディスパッチャで xterm を貫く・Leader(⌘K)/パレット/少数アクセラレータ・直接キー＋予約キーのみ再割当・端末入力優先は Leader だけ残す（設計 [29](29-keyboard-system.md)）
- [0018-container-browser-pane.md](decisions/0018-container-browser-pane.md) — Chromium を Workspace 内で動かし、localhost の描画と許可済み入力だけをブラウザペインへ中継（設計 [31](31-container-browser-pane.md)）
- [0019-copilot-agent-kind.md](decisions/0019-copilot-agent-kind.md) — GitHub Copilot CLI（`copilot`）を第 5 のエージェント種別に（設計 [36](36-copilot-agent-kind.md)）
- [0020-chat-bridge.md](decisions/0020-chat-bridge.md) — チャットブリッジ：Slack/Discord から通知受領と双方向操縦（設計 [37](37-chat-bridge.md)）
- [0021-scheduled-execution.md](decisions/0021-scheduled-execution.md) — 定時実行：定義は CP の DB・自前 cron・wakeFirer で停止中も発火（設計 [38](38-scheduled-execution.md)）
- [0022-agent-memory-management.md](decisions/0022-agent-memory-management.md) — エージェントメモリは agent 側 git bare repo で版管理し bundle で環境間移送（設計 [39](39-agent-memory-management.md)）
- [0033-stored-text-locale.md](decisions/0033-stored-text-locale.md) — 「保存データは JA 統一」を撤回：利用者が読む自前生成文（chat の notice）はキー＋引数で保存し Console のカタログで訳す。モデルが読む/書く文字列は据え置き（[0016](decisions/0016-i18n.md) §7 を一部上書き・設計 [28 §2.5/§4](28-i18n.md)）
- [0038-chromium-attach-view.md](decisions/0038-chromium-attach-view.md) — 外部所有Chromiumはloopback CDPへattachし、MCPが返すaction linkをユーザーがクリックしてConsoleペインへ開く（設計 [53](53-chromium-attach-view.md)）
- [0039-fork-at-message.md](decisions/0039-fork-at-message.md) — 会話の分岐点は kind 固有の不透明アンカーで指し（`idx` は使わない）、codex/opencode は公式パラメータ、claude だけ `sessionId` のみ書き換える jsonl 手術を許す（設計 [55](55-fork-at-message.md)・旧判断 [history/fork-from-chat](history/fork-from-chat.md) を差し替え）
- [0040-project-mcp.md](decisions/0040-project-mcp.md) — プロジェクトスコープ MCP は af が所有せず「利用者の代理編集」として明示操作のときだけ書く（自動同期・所有マーカーなし・監査は git／ゲートは代行しない）（設計 [56](56-project-mcp.md)・[0031](decisions/0031-mcp-registry.md) の別軸）
- [0041-cross-session-messaging.md](decisions/0041-cross-session-messaging.md) — セッション同士のメッセージは af の直接送信（P2P）で行う。peer は指示台帳の arm に触らず（AF 版は着信に機械可読な出自を付けられないため回避不能）、shell/ssm は送受信とも不可、封筒はプロンプト前置。Claude ネイティブ経路は**有効化しない**（有効化＝telemetry 復活と実測で判明・技術的障害は無い）（設計 [58](58-cross-session-messaging.md)）
- [0042-user-instructions.md](decisions/0042-user-instructions.md) — ユーザー指示は AF が所有する本文 1 本とし、**AF 専用ファイル＋参照**で各 CLI へ配る（合成は参照手段の無い codex・agy だけ）。配布軸＝自動で書く側・kind 別本文は作らない・claude は `$CLAUDE_CONFIG_DIR/CLAUDE.md`・上限 8 KB の根拠は費用（codex のバイト予算は global を含まないと実測で判明）・cursor は対応不可で確定（設計 [60](60-user-instructions.md)・[0040](decisions/0040-project-mcp.md) の別軸）
- [0043-login-idp.md](decisions/0043-login-idp.md) — ログイン IdP はプロバイダ別実装を増やさず**汎用 OIDC 1 本 ＋ GitHub だけ専用**にし、**同一人物の保証（リンク）を GitHub より先**に入れる。`user_key` は不変で `(provider, subject)` を横に足す（既存デプロイの移行ゼロ）・別 email の結合はサインイン済みからのみ・email の信頼根拠は provider ごとの `trust` 宣言・Entra の `common` ＋ `tid` 未指定は fatal・id_token 署名検証は引き続き不要（TLS 直受け＝Go 依存追加ゼロ）・SAML は `AUTH=proxy` ブリッジで回答。テナント毎のログイン（P3）は**入口の門とテナントの門を別層に置く**・**URL のテナント指定を認可の根拠にしない**（`prov` をテナント解決時に突合して強制）・**名簿は membership が持つ**（招待 API が既に存在・テナントに email リストを足すと二重台帳）・**入口の判定は和で membership を含める**・`allowed_domains` は招待時のガードのみ・`/login/<slug>` の未知 slug は 404 にしない・IdP グループ同期は入れない。テナント定義の認証方式（P4）は**保存と入力は tenant_admin・有効化は super_admin の承認**とし、**テナント定義の provider からは `super_admin` を取れない**・**P1 が前提**（email 一致での identity 結合を無効化しないと email を騙るだけで home ごと乗っ取れる）・秘密はテナント鍵で封印して DB へ・provider id の名前空間を `t:<slug>:<name>` で分ける（設計 [61](61-login-idp.md)）

> 0023〜0032 は対応する設計ドキュメントの行（上記「番号付きの機能設計・実装記録」）に併記。

## history/ — 使い終わった実装プラン（P3-6 は ◐ admin read/write まで完了・dangerous 段のみ残）

- [phase0-poc.md](history/phase0-poc.md) — Phase 0 PoC（`/login` 検証）
- [phase1-plan.md](history/phase1-plan.md) — Phase 1 MVP（§11.10 は今も有効な知見）
- [p3-1-metadatastore.md](history/p3-1-metadatastore.md) — MetadataStore（SQLite）
- [p3-2-identity-tenant.md](history/p3-2-identity-tenant.md) — identity↔tenant 多対多
- [p3-3-envelope-crypto.md](history/p3-3-envelope-crypto.md) — 封筒暗号 + custodian
- [p3-4-quota.md](history/p3-4-quota.md) — リソースバジェット/クォータ
- [p3-5-member-console.md](history/p3-5-member-console.md) — メンバー Console UX
- [console-redesign.md](history/console-redesign.md) — Console UI 刷新ブリーフ
- [console-redesign-backlog.md](history/console-redesign-backlog.md) — Console UX 刷新の残作業バックログ（実装済）
- [p3-6-mcp.md](history/p3-6-mcp.md) — MCP（管理面+作業面を一体・E 駆動）実装プラン（◐ member/drive＋admin read/write 完了・dangerous 段のみ残）
- [p3-7-aws-adapter.md](history/p3-7-aws-adapter.md) — P3-7: AWS デプロイ先アダプタ（ECS）実装プラン
- [p3-9-idle-stop.md](history/p3-9-idle-stop.md) — P3-9: アイドル自動停止（scale-to-zero, 二段構え）
- [p3-9-showback.md](history/p3-9-showback.md) — P3-9: showback（社内使用量の可視化）実装記録
- [p3-10-packaging.md](history/p3-10-packaging.md) — P3-10: パッケージング & 配布 & アップグレード（設計プラン）
- [agent-cli-self-update.md](history/agent-cli-self-update.md) — エージェント CLI の自己更新（opt-in + 運用者ゲート）
- [chat-opencode-codex.md](history/chat-opencode-codex.md) — チャット（MirrorView）を codex / opencode へ汎化
- [fork-from-chat.md](history/fork-from-chat.md) — チャット履歴からの会話 fork（ワンクリック分岐）
