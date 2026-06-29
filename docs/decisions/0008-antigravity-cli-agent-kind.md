# 0008. Antigravity CLI（`agy`）を第4のエージェント種別として取り込む

- 状態: 調査完了・**設計提案**（未実装）
- 関連: [session.go](../../workspace/agent/session.go)（kind 分岐 / `startSessionTmux`）/ [codex_auth.go](../../workspace/agent/codex_auth.go)（device-auth 流用元）/ [0006-mcp-unified](0006-mcp-unified.md) / [HANDOFF §エージェント種別](../HANDOFF.md)
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

## 未解決（RDRAND 有効ホストで再確認）

- `agy` のヘッドレス/SSH 認可 URL を **コンテナ内で**完走できるか（keyring 不在環境の挙動）。
- **GCP プロジェクト経路の per-user ログイン**手順（`gcloud` 連携要否、env で渡す資格の形）。
- ログイン状態照会・logout の正確なサブコマンド名（codex の `login status` 相当）。
- resume の単位（codex/opencode はスロット sid で resume。`agy` の persistent history との対応）。
- keyring を使う場合のコンテナ永続化先（home 配下に落ちるか＝denylist 追加要否）。
- イメージ同梱は `--dir /usr/local/bin` で root 設置（claude/codex と同列）か、home 設置で
  自己更新を許すか（root 設置だと dev ユーザーの background self-update が効かない）。

## 決定（提案）

**`kind=agy` を第4のエージェント種別として追加する**。ToS ゲートは GCP プロジェクト経路で通過済み。
実装は codex 追加の轍（launch 分岐＋`agy_auth.go` device-auth 流用＋CP プロキシ＋Console パネル）に
乗り、構造差分はイメージ導入が npm でなく `install.sh` の 1 点のみ。**PoC でインストールは確認済
だが、agy の FIPS ビルドが RDRAND を必須とし本開発ホストでは起動不可**（上記）。**次アクション=
RDRAND 有効ホストで対話/認証/resume を再 PoC → 段階実装**。配備ドキュメントに RDRAND 要件を明記。

[Issue #78]: https://github.com/google-antigravity/antigravity-cli/issues/78
