# Tailscale Funnel + Google OAuth（CP ネイティブ）構成

## 概要

外部からのアクセスを Tailscale Funnel で受け、**agent-fleet の Control Plane (CP) 自身が
Google OAuth 認証**を行う。認証済みユーザーのみが Console / API にアクセスできる。
oauth2-proxy と Caddy は廃止し、CP に集約した。

```
Internet (HTTPS)
  ↓
Tailscale Funnel (443) — https://af.example.ts.net/
  ↓ (ローカルへ素通し)
Control Plane (127.0.0.1:8099, AUTH=oauth)
  ├─ /login                 … ログインページ（未認証で到達可）
  ├─ /oauth2/login|callback  … Google OAuth フロー（CP が所有）
  ├─ /healthz, /brand/*      … 認証除外
  ├─ /mcp                    … Bearer PAT 認証（Google セッション非依存）
  └─ それ以外すべて          … 署名付きセッション cookie を検証（無ければ /login へ）
        ↓ 認証済み email を X-Forwarded-Email として後段へ
      Console 配信 / API / Agent への proxy / per-user Workspace 解決
```

旧構成（oauth2-proxy → Caddy → CP、OpenCode catch-all）は廃止。OpenCode-web は
Console の kind=opencode セッションに置き換わったため不要。

## 認証の仕組み

- **エッジが CP になった**: 以前は oauth2-proxy が前段で認証境界を担っていた。今は CP が
  Funnel 直下のエッジ。`authGate` ミドルウェアが全リクエストを検査する。
- **なりすまし防止**: Funnel はクライアントのヘッダを素通しするため、`authGate` は受信した
  `X-Forwarded-Email`（識別ヘッダ）を**必ず削除**してから、検証済みセッションの email を
  自分でセットする。CP は loopback 束縛なので Funnel 以外からは到達できない。
- **セッション**: ログイン成功で `af_session` cookie（HMAC-SHA256 署名・HttpOnly・Secure・
  SameSite=Lax、既定 TTL 168h）を発行。後段の `resolveIdentity` は AUTH=proxy と同じ経路で
  この email を読むため、tenant 解決・Bitbucket・per-user Workspace は無改修で動く。
- **許可リスト**: oauth2-proxy の emails.txt の後継。3系統を併用可、すべて空なら全拒否
  （fail closed）:
  - `AF_OAUTH_ALLOWED_EMAILS`（CSV、完全一致のメール）
  - `AF_OAUTH_ALLOWED_DOMAINS`（CSV、ドメイン単位。例 `example.com,foo.co.jp`）
  - `AF_OAUTH_ALLOWED_EMAILS_FILE`（1行＝メール or `@example.com`（ドメイン）、# コメント可）
  ファイルはログイン毎に読むので**追加は再起動不要**（env の2つは CP 再起動が必要）。
- **MCP**: `/mcp` は `authGate` の除外パス。Bearer PAT で CP が直接検証する（従来どおり）。

## アクセス URL

| サービス | URL |
|---|---|
| Console | https://af.example.ts.net/ |
| ログイン | https://af.example.ts.net/login |
| MCP | https://af.example.ts.net/mcp |

## 設定（環境変数）

CP は `deploy/local/run-dev.sh` が `deploy/local/oauth.env` を読み込んで起動する
（`oauth.env.example` 参照）。OAuth に必要な値:

```ini
AUTH=oauth
CP_ADDR=127.0.0.1:8099
PUBLIC_BASE_URL=https://af.example.ts.net

# Google OAuth 2.0 Client（Web application）。oauth2-proxy 用の既存クライアントを流用可。
GOOGLE_OAUTH_CLIENT_ID=<client-id>.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=<client-secret>

# セッション cookie の HMAC キー:  head -c 32 /dev/urandom | base64
AF_COOKIE_SECRET=<base64-32-bytes>
AF_SESSION_TTL=168h            # 任意（既定 168h）

# 許可リスト（3系統を併用可。すべて空なら全拒否）
AF_OAUTH_ALLOWED_EMAILS=k1.kami@gmail.com          # 完全一致のメール（CSV）
AF_OAUTH_ALLOWED_DOMAINS=example.com               # ドメイン単位（CSV）
AF_OAUTH_ALLOWED_EMAILS_FILE=deploy/local/allowed-emails.txt
```

## Google Cloud Console 設定

- **承認済みリダイレクト URI**: `https://af.example.ts.net/oauth2/callback`
  - oauth2-proxy 時代と**同一**。既存クライアントをそのまま流用でき、登録変更は不要。

## ログインページ

`GET /login` は CP が自前で配信する独立 HTML（`control-plane/oauth_google.go`）。
ブランドバナー `console/public/brand/agent-fleet-banner.png`（→ ビルドで `dist/brand/` に出力、
`/brand/*` は認証除外）をヒーローに表示し、「Google でサインイン」ボタンが `/oauth2/login` へ。
許可リスト外のアカウントは `/login?error=forbidden` に戻り、その旨を表示する。
バナーが無い場合はテキストのワードマークにフォールバックする。

## 旧構成からの切替手順（cutover）

1. **Funnel を CP に付け替え**
   ```bash
   tailscale funnel reset
   tailscale funnel --bg 8099
   tailscale funnel status   # / proxy http://127.0.0.1:8099 を確認
   ```
2. **oauth2-proxy / Caddy を停止・無効化**
   ```bash
   systemctl --user disable --now oauth2-proxy caddy
   rm -f ~/.config/systemd/user/oauth2-proxy.service ~/.config/systemd/user/caddy.service
   systemctl --user daemon-reload
   ```
3. **oauth.env を AUTH=oauth に更新**（上記「設定」）。`PUBLIC_BASE_URL` から `/agent-fleet` を外す。
4. **Bitbucket OAuth consumer のコールバック URL を変更**:
   `https://af.example.ts.net/api/oauth/bitbucket/callback`（`/agent-fleet` を除去）。
5. **Google Cloud Console: 変更不要**（`/oauth2/callback` は登録済み・クライアント流用）。
6. **CP を再起動**して反映:
   ```bash
   cd ~/workspace-private/agent-fleet && deploy/local/run-dev.sh
   ```
7. 動作確認: `https://af.example.ts.net/` を開く → `/login` → Google → Console。

## サービス管理

```bash
# Funnel
tailscale funnel status

# CP ログ（run-dev.sh をフォアグラウンド実行 or tmux 運用）
# 健全性
curl -fsS https://af.example.ts.net/healthz   # => ok
```

## 許可ユーザーの追加・削除

`AF_OAUTH_ALLOWED_EMAILS_FILE`（既定 `deploy/local/allowed-emails.txt`）を編集するだけで
**即時反映**（ログイン毎に再読込、再起動不要）。env の `AF_OAUTH_ALLOWED_EMAILS` /
`AF_OAUTH_ALLOWED_DOMAINS` を使う場合のみ CP 再起動が必要。

```
k1.kami@gmail.com      # 完全一致のメール
newuser@example.com    # ← 追加はここに1行
@example.com           # ← @ 始まりはドメイン全体を許可
```

## MCP の認証（agent-fleet P3-6）

手元の Claude（Claude Code / Desktop）から **Bearer PAT** で認証する。`/mcp` は `authGate` の
除外パスなので Google セッション無しで到達でき、CP が PAT を検証する。

- **公開 URL**: `https://af.example.ts.net/mcp`（Streamable HTTP）
- **認証**: `Authorization: Bearer <PAT>`。PAT は Console（設定 → MCP タブ）で発行。無効なら 401。
- **CP 側**: `AF_MCP_ENABLED=true` のときだけ `/mcp` が有効。

```bash
curl -X POST https://af.example.ts.net/mcp \
  -H "Authorization: Bearer af_pat_xxxxx" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
# => 正常: serverInfo / トークン無し: 401（Google ログインへリダイレクトされない＝除外が効いている）
```

クライアント設定例（`.mcp.json` / `claude_desktop_config.json`）:

```json
{
  "mcpServers": {
    "agent-fleet": {
      "type": "http",
      "url": "https://af.example.ts.net/mcp",
      "headers": { "Authorization": "Bearer af_pat_xxxxx" }
    }
  }
}
```

## 注意事項

- CP は必ず loopback（`127.0.0.1:8099`）束縛で、Funnel 経由のみ公開する。直接外部に晒さない。
- `AF_COOKIE_SECRET` は WS_DATA の外で管理する（スナップショットに含めない）。
- `AUTH=oauth` は Funnel（HTTPS）配下専用。ローカル素の HTTP では Secure cookie が保存されない
  ため、純粋なローカル開発は従来どおり `AUTH=dev` を使う。
- セッション失効・別アカウントへの切替は `/oauth2/logout`（cookie 破棄 → /login）。
