# 0007. opencode web を pk-opencode-webui 経由で提供する

- 状態: **廃止（撤回, 2026-07）** — `temp/remove-opencode-web-opencode-web-ui` で
  実装（pk-opencode-webui の焼き込み・`opencode serve`＋`bun serve-ui.ts`・`/ocweb`
  プロキシ・Console トグル）を撤去。opencode の利用は tmux 内 TUI（CLI）に一本化。
  以下は当時の設計記録として残す。
- 関連: [reference/preview.md](../dev/05-api-contracts.md)（同じプロキシ機構・WS 制約）/ [HANDOFF §opencode](../HANDOFF.md) / rtk トグル（[エージェント設定タブ](../HANDOFF.md)）の隣に並ぶ機能

## 背景

opencode を現状の tmux 内 TUI（PTY/xterm 橋渡し）だけでなく、**opencode の Web UI**（`opencode web`）でも
使いたいという要望。だが core opencode web は **ルート `/` 前提**で、agent-fleet のサブパス・プレビュー
（`{extPrefix}/preview/{port}/`、`reference/preview.md`）配下では動かない。実機で確認:

- `opencode web` の HTML は `src="/assets/index-*.js"` 等の**絶対ルートパス**を吐く。
- 実行時の API/SSE/WS ベースは `location.hostname.includes("opencode.ai") ? "http://localhost:4096" : location.origin`
  ＝ **`location.origin`（パス無し）**。コード分割チャンクも Vite `base=/` で絶対。
- → サブパス配下に置くと、ブラウザは `/assets/...` や `/event` を **Console オリジンのルート**へ取りに行き衝突＝壊れる。

公式ドキュメントも "the official OpenCode web UI assumes it runs at root path `/`" と明言。

## 検討した選択肢

| 案 | 可否 | 退けた理由 |
|----|------|-----------|
| **A. core web ＋ プロキシで置換** | ❌ | アセットは相対化できても、実行時の `location.origin` ベースの API/SSE/WS と Vite 絶対チャンクは **build 時定数**。プロキシで直すには毎リリース変わる minified バンドルを正規表現手術＝脆く保守不能 |
| **B. ホスト/サブドメイン単位プレビュー** | ✅（core 可）| opencode web を“あるオリジンのルート”に置けば無改修で動く。が agent-fleet に **wildcard DNS + TLS + Host ベースのルーティング**を新設する必要（大きめのインフラ追加） |
| **C. pk-opencode-webui を前段に置く** | ✅ | **採用**（下記）|

## 決定

**C を採用** — [`prokube/pk-opencode-webui`](https://github.com/prokube/pk-opencode-webui)（MIT・Bun/SolidJS 製の
**prefix 対応 Web UI 再実装**）を `opencode serve` の前段に置く。スパイクで核心を確認済み:

- `BASE_PATH=/preview/9999/` で起動した HTML が **相対 URL（`./entry.js`）＋ `<base href="/preview/9999/">`** を
  出力＝最も堅牢な prefix 追従。core が解けなかったベースパス問題を実際に解いている。
- 同梱 `docker/serve-ui.ts`（Bun.serve）が dist 配信＋API プロキシ（`API_URL`）＋**WS upgrade を内部中継**
  （browser ⟷ pk-webui ⟷ `opencode serve`）。

### 付随する決定

1. **専用エンドポイント `/ocweb/`（`/preview/{port}` は流用しない）**。理由: 既存 preview は `/proxy/{port}` を
   **strip してルート転送**する（`reference/preview.md`）が、prefix 対応 UI には**外部パスをそのまま渡す**必要が
   あり意味論が逆。ポートも URL に出さない（ワークスペース 1 つの常駐サービスゆえ、agent が内部ポートへ解決）。
   `BASE_PATH = {extPrefix}/ocweb/`。
2. **ワークスペースに 1 つ**（per-session でなく）。`opencode serve` は複数セッションを抱えるサーバ、pk-webui は
   マルチプロジェクト UI ゆえ自然。既存の tmux opencode スロットとは別系統として共存。
3. **イメージに焼き込む**。bun は **apt 不可**（公式 .deb なし）→ ランタイムは `npm install -g bun`（既存の
   `npm i -g claude/opencode/codex` 行に並べる＝最も一貫）、dist の build は多段 `FROM oven/bun:* AS builder`。
   serve-ui.ts は `Bun.serve` 依存ゆえ node では動かず **bun ランタイム必須**。

## アーキテクチャ（4 層・段階実装）

```
ブラウザ {origin}{extPrefix}/ocweb/<path>
  → CP   /ocweb/<path>            control-plane: 専用ハンドラ（rtFor 認証 + Bearer + X-Forwarded-* + WS 対応）
                                   ※ パスを strip せず /ocweb/<path> をそのまま agent へ
  → Agent /ocweb/<path>           workspace/agent: httputil.ReverseProxy（WS 可）で 127.0.0.1:<pkPort>/ocweb/<path>
  → pk-webui :<pkPort>            BASE_PATH={extPrefix}/ocweb/ · API_URL=http://127.0.0.1:4096
  → opencode serve :4096          headless API（127.0.0.1）
```

| # | 層 | 作業 |
|---|----|------|
| 1 | image | bun（`npm i -g bun`）＋ pk-webui を多段 build → `/opt/opencode-web`（dist + serve-ui.ts + shared）焼込 |
| 2 | agent | opencode web lifecycle（`opencode serve` + `bun serve-ui.ts`）／永続トグル `~/.config/agent-fleet/opencode-web.json`（既定オフ）／`GET/PUT /agents/opencode-web`（状態 + 内部 port）／`/ocweb/<rest>` リバースプロキシ |
| 3 | CP | `/ocweb/<rest>` 専用ハンドラ（**WS 対応**・パス保存・`externalPrefix` を BASE_PATH 算出のため受け渡し）|
| 4 | Console | 「エージェント」タブの opencode セクションに on/off ＋「opencode web を開く」（`/ocweb/` を新タブ）|

## 帰結・リスク

- **CP の WS 対応**は `/ocweb` 専用に入れるが、`reference/preview.md` の「WS/SSE 未対応」制約解消の布石にもなる
  （同じ機構を generic preview へ広げる余地）。
- **第三者 UI の機能ドリフト**: pk-webui は公式 UI の再実装ゆえ機能差・追従コストがある（MIT・維持中 v0.9.2/2026-05）。
  ピンしたコミットで vendor し、更新は明示的に。
- **イメージ肥大**: bun ランタイム ＋ dist 約 30MB（Shiki 言語チャンク）。
- **`opencode serve` は既定 unsecured**（`OPENCODE_SERVER_PASSWORD` 未設定警告）。127.0.0.1 束縛＋認証付き
  `/ocweb` プレビュー経由のみ到達ゆえ露出はしないが、追加ポート公開はしない。
- **extPrefix≠""（Caddy 等がサブパスを strip するデプロイ）は follow-up**。現行デプロイは CP ルート配信
  （`PUBLIC_BASE_URL` にパス無し ⇒ extPrefix=""）ゆえ `BASE_PATH=/ocweb/` で成立する。
