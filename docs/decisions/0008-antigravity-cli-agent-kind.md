# 0008. Antigravity CLI（`agy`）を第4のエージェント種別として取り込む

- 状態: **採用決定**（2026-07-20。Starter=実験枠で実装開始、GCP 経路で常用化を目指す。実装計画は [32](../32-agy-agent-kind.md)）
- 関連: [session.go](../../workspace/agent/session.go)（セッション統合）/ [Codex auth](../../workspace/agent/internal/agents/codex/auth.go)（device-auth の現行実装）/ [0006-mcp-unified](0006-mcp-unified.md) / [HANDOFF §エージェント種別](../HANDOFF.md)
- 出自: ユーザー依頼「antigravity cli を Agent-Fleet に組み込めないか検討」（2026-06-29〜30 調査）

## 背景

Agent-Fleet は既に **`claude | opencode | codex | shell`** の複数エージェント種別を
`kind` 一つで切り替える構造（`session.go` の `startSessionTmux` 分岐、種別ごとの
`*_auth.go`、イメージ同梱、Console セッション種別セレクタ）を持つ。ここに Google の
**Antigravity CLI（バイナリ名 `agy`）** を `kind=agy` として加えられるかを調べた。

`agy` は Google が 2026-05 に出した **Go 製ターミナルエージェント**（旧 Gemini CLI の後継、
Gemini CLI は 2026-06-18 廃止）。SSH/キーボード駆動を明示的に想定した TUI で、構造的に
**claude/codex と同型＝PTY で動く対話エージェント**。よって既存の PTY→tmux→xterm 橋渡しを
そのまま再利用できる。

- モデル: Gemini 系が主、**オプションで Claude・OSS バックエンドも指定可**。
- 認証: システムキーリング →（無ければ）Google Sign-In。**SSH セッション検知時は認可 URL を
  表示してローカルで完了**（codex の device-auth・claude の sign-in URL ボタンと同型）。
  CI/ヘッドレスは `ANTIGRAVITY_TOKEN`。素の API key 認証は要望中（[Issue #78]）。
- 非対話: `agy -p "<prompt>" --output-format json`、`--headless` + `--approve <policy>`。
- 設定流儀: ルートの `AGENTS.md`（全プロンプト前置）、`.agents/skills/*.md`（スラッシュコマンド）。
  → **既存の `WS_NOTES`→`AGENTS.md` シード機構（entrypoint）がそのまま効く**。
- MCP 対応あり（[0006](0006-mcp-unified.md) の統合 MCP 方針と整合）。

## ToS 判定（最大のゲート）

Anthropic ToS を [0001](0001-self-host-vs-saas.md) で慎重に詰めた以上、同等の検証を行った。
**結論: claude よりむしろクリーンな道がある。**

認証は**全階層とも同一の Google Sign-In**（device-auth/認可 URL）で、`agy` のコードは
**ログイン階層に非依存**。よって階層選択は**実装差ではなく運用/ToS ポリシー**の問題。

| 経路（BYO ログイン階層） | 学習利用 | クォータ | セルフホスト適合 | 判定 |
|------|---------|---------|------------------------------|------|
| **会社 Workspace（Gemini for Business / AI Ultra for Business）** | **収集しない**（明示） | 企業枠 | 会社所有シート＝[overview](../HANDOFF.md) の方針と一致 | ✅ **推奨** |
| **GCP プロジェクト** | **されない**（私的環境外に保存せず） | 消費ベース課金 | 各ユーザーが自分の GCP 資格→**GCP ToS** | ✅ **推奨** |
| 個人 **AI Pro（$20）/ Ultra（$249.99）** | **既定で学習**（「Gemini Apps Activity」オフでオプトアウト） | Pro=5h ごとリフレッシュだが `agy` の重い compute effort で**2h で 5h ロック**の報告 | 技術的には BYO 可だが細い | ⚠ **個人検証どまり**（claude の「個人 Pro/Max 避ける」と同型） |
| 消費者/無料 | 学習（同上） | **1 日 20 req/アカウント**（desktop/CLI/SDK 共有） | 本番不向き | ⚠ 動作確認のみ |
| Claude モデルを `agy` 経由 | — | — | 追加で **Anthropic 商用規約**にも拘束 | 併用時注意 |

**会社 Workspace または GCP プロジェクト経路は Agent-Fleet の「1 社=1 デプロイ・自社
セルフホスト・BYO」（[overview](../HANDOFF.md)）とそのまま一致**し、SaaS を断念させた ToS
グレーを踏まない。個人 AI Pro は技術的には同じ device-auth で通るが、**学習利用（オプト
アウト頼み）＋クォータ枯渇**の 2 点で claude の個人プラン同様に会社運用では避ける。
→ **ゲート通過。会社 Workspace / GCP 経路を推奨前提とし、実装自体は階層非依存。**

## 既存パターンへの接地（変更箇所）

codex/opencode 追加と同じ轍。触る範囲は限定的:

1. **イメージ同梱** — `workspace/Dockerfile:86` は今 `npm install -g … @openai/codex …`。
   `agy` は npm ではなく `curl -fsSL https://antigravity.google/cli/install.sh | bash` の
   **Go バイナリ**なので、claude（`claude.ai/install.sh`）と同型の install 行を 1 本追加し、
   `&& agy --version` を検証行に足す。**ここが唯一の構造差分。**
2. **launch 分岐** — `session.go:210` の `switch m.Kind` に `case "agy":` を追加。
   作業ディレクトリで `agy`（必要なら resume/model フラグ付き）を起動する `buildAgyProgram` を
   `buildCodexProgram` に倣って新設。`session.go:431` の許可リストにも `"agy"` を追加。
3. **認証 `agy_auth.go`** — `codex_auth.go` の **device-auth/PTY スクレイプ機構をほぼ流用**。
   `agy` の SSH 認可 URL を `claudeFlow` で掴んで Console に出し、ポーリングで完了検知。
   状態表示は `agy` のログイン状態照会コマンドで（codex の `login status` 相当を確認要）。
   資格は `agy` 自身が keyring/home に持つ＝**claude/codex と同じく暗号ストア外＋denylist**。
4. **CP ルート** — `control-plane/main.go` の codex と同型に
   `/api/connections/agy/...`（device start/poll・disconnect）を `proxyAgentREST` で足すだけ。
5. **Console** — セッション種別セレクタに `agy` を追加、Connections タブに認証パネルを 1 枚。
   バックエンド API が codex と同形なら UI も複製で済む。
6. **AGENTS.md シード** — entrypoint の `WS_NOTES`→`AGENTS.md` コピー先に `agy` の
   参照パス（プロジェクト root の `AGENTS.md` を既に読む）を含める。rtk ブロック追記も同様。

## PoC 結果（2026-06-30、使い捨てコンテナ `agent-fleet/workspace:dev`）

ビルドせず既存イメージの使い捨てコンテナで実施（[ホスト OOM リスク](../HANDOFF.md)回避）。

- ✅ **インストール成功**: `curl -fsSL https://antigravity.google/cli/install.sh | bash` は
  **非対話・冪等・sha512 検証つき**で `$HOME/.local/bin/agy` に設置（Cloud Run の manifest→
  flat native build を取得）。`--dir` で設置先指定可、既存なら skip。Debian12/curl/x86_64 でOK。
- ❌ **起動不可（本開発ホストのみ）**: `agy` が起動直後に `CRNGT failed` → SIGABRT。スタックは
  `crypto/internal/boring._goboringcrypto_RAND_bytes`。**agy は Go BoringCrypto(FIPS) ビルド**で、
  x86 の FIPS 乱数モジュールが **RDRAND 命令を必須**とする。本ホスト（AMD Ryzen Embedded
  R2514・ベアメタル・`detect-virt: none`）は **`/proc/cpuinfo` に rdrand 非提示**（カーネル
  マスク/BIOS 無効の疑い）→ 自己テスト abort。`seccomp=unconfined` でも変わらず、プリビルド
  ゆえ FIPS 無効化スイッチもなく**ユーザー空間からは回避不可**。

→ **新たな配備要件: agy を動かすホストは RDRAND 有効が必須**（一般的なクラウド VM・現行 CPU の
多くは満たすが、この開発ホストは満たさない）。これは agy/Agent-Fleet 固有の欠陥ではなく FIPS
ビルドの性質。**この開発ホストでは対話/認証/resume の実機確認まで到達できない**ため、以下は
RDRAND 有効ホストで再 PoC する。

## 再 PoC 結果（2026-07-20、RDRAND 有効ホスト＝WSL2 / Ryzen 7 PRO 8840HS の Workspace コンテナ内）

前回の次アクション「RDRAND 有効ホストで再 PoC」を実施。`/proc/cpuinfo` に `rdrand`/`rdseed`
提示ありのホストで、**起動〜認証〜非対話実行まで全て完走**した（v1.1.4）。

- ✅ **起動成功**: `install.sh` → `~/.local/bin/agy`、`agy --version` 正常。`CRNGT failed` は
  再現せず。**RDRAND 要件の裏取り完了**（有効ホストなら問題なし。0008 の配備要件は妥当）。
- ✅ **コンテナ内認証フロー完走**（keyring 不在環境）: TUI 起動で「1. Google OAuth /
  2. Use a Google Cloud project」の 2 択 → OAuth 選択で**認可 URL＋認可コード貼り付け
  （PKCE、redirect_uri=`antigravity.google/oauth-callback`）が端末内に表示**される。
  tmux `send-keys` でコード投入し完了 = **codex device-auth 同型の PTY スクレイプで
  Console 統合が成立する**。GCP プロジェクト経路も同セレクタの選択肢 2 として存在。
- **初回オンボーディング**（`agy_auth.go` がスクレイプで踏む画面列）: カラースキーム選択 →
  ToS + **Interactions データ収集オプトイン（既定オン、TUI 上でトグルしてオプトアウト可**、
  今回オフで完走）→ ワークスペースの trust プロンプト（Yes/No）→ メイン画面。
- **資格の永続化先**: keyring 不在時は **`~/.gemini/antigravity-cli/antigravity-oauth-token`
  （平文、home 配下）** → claude/codex と同じく**暗号ストア外＝denylist 追加要**。
- **ログイン状態照会**: 専用サブコマンドは無い。未認証時に `agy models` が
  「Please sign in」エラーを返すため、**これが `login status` 相当の判定に使える**。
- ✅ **非対話実行**: `agy -p "<prompt>"` が正常応答（既定 Gemini 3.5 Flash (Medium)）。
- **モデル一覧**（Starter Quota の個人 Google アカウント）: Gemini 3.5 Flash (Low/Medium/High)、
  Gemini 3.1 Pro (Low/High)、**Claude Sonnet 4.6 / Opus 4.6 (Thinking)**、GPT-OSS 120B。

## Starter Quota 実測と採用判定（2026-07-20 追記）

個人 Google アカウント（表示名 **Antigravity Starter Quota** = consumer 無償枠）のまま
第4種別として採用できるかを検討。TUI `/usage` の実測で**クォータ制度が本 ADR 初版の
調査時（1日20req・5h リフレッシュ）から変わっている**ことを確認した。

- **現行は週次・モデルグループ制**: 「Gemini 系（Flash/Pro 共有）」と「Claude/GPT 系
  （Opus 4.6 / Sonnet 4.6 / GPT-OSS 120B 共有）」の 2 プールが**それぞれ週次上限**を持ち、
  **トークンコスト比例**で消費される（`/usage` の説明文言より）。実測: 極小の `-p` 1 回で
  Gemini プールの約 1% を消費 → **Starter の週次プールは極小プロンプト換算で約 100 回分**。
  実際のエージェントタスク（リポジトリ文脈込み）は 1 タスクで数%〜を消費すると見込む。
- **クォータは同一アカウントの Google エージェント面と共有**（Antigravity IDE・Jules・
  Code Assist 等の unified wallet。2026-04 の quota 制度変更報道と整合）。CLI 単独の枠ではない。
- **`/usage` は PTY スクレイプ可能** → Console の Connections パネルに残量%を出せる。
- 判明した運用面: **`/logout` あり**（logout 手段解決）。**resume 単位 = 会話 UUID**。
  `--continue` は cwd の最終会話（`cache/last_conversations.json` が **cwd→会話ID の
  マップ**）、`--conversation <ID>` で明示 resume、一覧は `conversation_summaries.db`
  （SQLite、平文で読める）か TUI `/resume`。**スロット sid との対応付けは「スロット毎に
  作業 dir が分かれていれば `--continue` で自動」「または ID を CP 側で保存」のどちらも可**。
- **構造化出力は現版に無い**: v1.1.4 の flags に `--output-format` は存在せず（初版調査の
  記載は旧版/未実装機能とみられる）。`-p` はプレーンテキストのみ。
  → **実行方式は Terminal (CLI)（tmux/PTY）一択**。codex/opencode のような Managed
  （イベントストリーム駆動）は現状組めない。claude と同列の扱いになる。

**2026-07-20 追記（個人有償サブスク 3 プランの再評価）**: 初版で「個人 AI Pro=検証どまり」と
一括判定した根拠のうち**クォータ面は現行プランでは解消し得る**。現行は AI Plus $4.99 /
AI Pro $19.99 / AI Ultra $100（Pro 比 5 倍）/ AI Ultra $200（同 20 倍）で、有償プランは
compute ベース・**5 時間リフレッシュ＋週次上限**、Pro 以上は**クレジット追い足し可**＝枯渇が
運用停止に直結しない。一方 **ToS 面は初版判定のまま**: 全消費者プランが個人アカウント限定
（Workspace 加入不可）で、学習除外は有償でもオプトアウト頼み。
→ **個人利用デプロイの常用は AI Pro で成立し得る（要実測）。会社デプロイの常用は引き続き
会社 Workspace / GCP プロジェクト経路のみ**。詳細比較表は [32 §AI サブスク経路の比較](../32-agy-agent-kind.md)。

**同日実測で確定**: AI Pro 実機計測により、同一実タスクの週次消費が Starter 6.01% → **Pro 0.22%
（プール ≈27 倍、週 ≈455 実タスク分）**、Claude 系も週 ≈81 実タスク分、`/usage` に 5h 枠バーが
追加されることを確認。**個人利用デプロイの常用は AI Pro で成立（実測確定）**。数値は
[32 §Track D-4 実測結果](../32-agy-agent-kind.md)。会社デプロイの判定は不変。

**判定: `kind=agy` の採用価値はあるが、Starter Quota では「claude/codex/opencode と並ぶ
常用ドライバ」にはならない。**

| 観点 | 評価 |
|------|------|
| 技術統合（auth スクレイプ・tmux・resume・AGENTS.md・MCP） | ✅ 全て成立（Terminal 方式限定） |
| Starter の量 | ❌ 週次プール極小＋IDE/Jules と共有。常用は数タスクで枯渇 |
| Starter の ToS | ⚠ 初版判定どおり個人検証どまり（学習はオプトアウト済でも会社運用不適） |
| 位置づけ | **補助枠**: Claude/GPT プールが別枠なので「Gemini/Claude second-opinion を週数回」用途、および統合実装の検証用 |

→ **個人利用デプロイ（WSL 即起動導線の路線）には Starter のまま「実験的・補助エージェント」
として採用可**。会社デプロイでの常用採用は **Workspace / GCP 経路が前提**（初版判定を維持）。

## M1 統合結果（2026-07-20 追記）

Track A/B/C をマージし、**M1 の完了条件（Console 契約の API 実機駆動: 作成・会話・resume・
logout・認証フロー・`/usage` 4 バー・RDRAND 非露出）を実機で通した** — 詳細は
[32 §統合と M1 E2E 結果](../32-agy-agent-kind.md)。E2E で 1 件の統合バグを発見・修正:
**v1.1.4 TUI は resume 単位（cwd→会話マップ）を graceful exit 時にしか flush しない**
（「初回プロンプトで書く」という Track D 観測は `-p` のプロセス即終了による見え方）。
対応は WireLive の dead 側 capture ＋ halt の `agents.GracefulStopper`（`/exit` 送出→猶予→kill）。
この知見は本文「resume 単位 = 会話 UUID」の運用条件として上書きする。

## 未解決（残り）

- **GCP プロジェクト経路の per-user ログイン**手順（`gcloud` 連携要否、env で渡す資格の形。
  TUI セレクタ選択肢 2 の中身は未走行）。
- ~~イメージ同梱は root 設置か home 設置か~~ → **Track B で root 設置に確定**
  （`--dir /usr/local/bin`＋`AGY_CLI_DISABLE_AUTO_UPDATE=true` で自己更新封殺。
  ⚠️ 値は `true` のみ有効 — `1` は無視される。docs/32 §（自己更新）／docs/70 §70.14.9）。
- Managed 実行方式の可否は `agy` 側の構造化出力（`--output-format` 相当）の将来提供待ち。
- CP・ブラウザ込みの L2 E2E（`e2e/`）は docker のあるホストで別途（本コンテナは docker 無し）。

## 決定（提案）

**`kind=agy` を第4のエージェント種別として追加する**。ToS ゲートは GCP プロジェクト経路で通過済み。
実装は codex 追加の轍（launch 分岐＋`agy_auth.go` device-auth 流用＋CP プロキシ＋Console パネル）に
乗り、構造差分はイメージ導入が npm でなく `install.sh` の 1 点のみ。**PoC でインストールは確認済
だが、agy の FIPS ビルドが RDRAND を必須とし本開発ホストでは起動不可**（上記）。配備ドキュメント
に RDRAND 要件を明記。**2026-07-20 追記: RDRAND 有効ホストでの再 PoC 完了**（上記「再 PoC 結果」
— 起動・OAuth 認証・`-p` 非対話まで実機確認済）。**次アクション=残り未解決（GCP 経路・logout・
resume 単位）を潰しつつ段階実装**。

[Issue #78]: https://github.com/google-antigravity/antigravity-cli/issues/78
