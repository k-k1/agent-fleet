# 0002. Claude 認証 — auth と onboarding は別物

- 状態: 確定（続10→11→12 の訂正連鎖の到達点）
- 関連: [HANDOFF §6.10.3](../HANDOFF.md) / [dev/08 §8.5 Claude 認証・オンボーディング](../build/08-integrations.md)（旧 architecture §2.6） / [history/phase0-poc](../log/phase0-poc.md)

## 背景

ヘッドレスコンテナで各ユーザーの Claude を本人として動かす（BYO `/login`）。Phase 0 で `/login` 自体は
**localhost コールバック非依存**（`redirect_uri=platform.claude.com/oauth/code/callback`）と確定し、最大の
リスクは消えた。だが Console から接続させる実装で、何度も「認証済みのはずがログイン画面が出る」に嵌った。

## 訂正の連鎖（教訓として圧縮）

1. **誤**: `setup-token` を env `CLAUDE_CODE_OAUTH_TOKEN` で注入 → これは `claude -p`（headless）専用で
   対話 TUI は読まない。
2. **誤**: 合成 `.credentials.json`（refreshToken 空）→ headless は通るが対話 TUI は refresh 不可で拒否。
3. `ANTHROPIC_AUTH_TOKEN` は対話を認証できるが「API Usage Billing」扱いになりサブスク機能（RC 等）を殺す恐れ → 不採用。
4. `tmux new-session -e VAR=val` はセッション環境にしか入らずペインのプロセスに伝播しない（env はコマンド前置で渡す）。

## 決定

- **認証本体**は `claude auth login --claudeai`（本物のサブスク OAuth）。claude 自身が refreshToken 付きの
  `.credentials.json` を書く＝対話 TUI が認証され RC 等も維持。URL は PTY 駆動で抽出 → Console 表示 → コード貼付。
- **ログイン画面が出る真因は認証情報ではなく `.claude.json` の `hasCompletedOnboarding` 未設定**。`auth status`
  が `loggedIn:true` でも、対話 TUI はオンボード・ウィザード（先頭がログイン方式選択）を再実行する。
  → セッション起動毎に `.claude.json` へ `hasCompletedOnboarding=true` ＋ `projects[dir].hasTrustDialogAccepted=true`
  を seed する。`--dangerously-skip-permissions` では trust もオンボードも飛ばせないため明示 seed が必須。
- `CLAUDE_CONFIG_DIR` 設定下では claude は `.claude.json` を CCD 配下で読む（P3-5 の機微状態退避と整合）。

## 帰結

- **auth と onboarding は別物**。認証可否は `claude auth status` でも起動バナーでも判定できず、`send-keys` で
  実プロンプト→応答でのみ確証する。
- Claude 接続状態の表示は `claude auth status`（JSON `loggedIn`）の実行時プローブで足りる（DB テーブル不要）。
