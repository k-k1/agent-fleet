# 18. Console UI 刷新 — ハンドオフ・ブリーフ

> ✅ **実施済み（このブリーフの刷新は完了）**。決定: **React + Vite** を採用（18.5 の要決定）。現状・新機能・新エンドポイント・保留事項は **[HANDOFF §6.10](../HANDOFF.md)** を参照。以下は当時の設計ブリーフ（記録）。

現 Console は Phase 1 MVP の最小 vanilla JS が機能追加で肥大化し、**情報設計が破綻している**。
本書は UI 刷新を別セッション/担当が拾えるよう、現状診断・全機能インベントリ・利用可能 API・制約・
推奨 IA・要決定事項を自己完結でまとめる。**バックエンド（CP/Agent API）は完成・安定**しており、本タスクは
**フロントの作り直しに限定**できる。

## 18.1 現状の問題（診断）

`console/`（`index.html` / `app.js` 617+行 / `style.css`）。Phase 1 から継ぎ足しで:

- **ナビゲーションが無い**。端末ペインの上に **3 つのオーバーレイ（ソース管理 SCM / Files / Admin）**が
  バラバラのボタンから開く。トップレベルの行き来が体系化されていない。
- **暗号アイコンの羅列**（●⤓⎇▶✕⏹≤🗂⧉）。ラベルが無く意味不明。
- **詰め込みフォーム**（`flex-wrap` のインライン input 群）。clone / new-session が窮屈。
- **IA がちぐはぐ**: 一部はサイドバー（Connections/Repos/Sessions）、一部はオーバーレイ（SCM/Files/Admin）。
- **ヘッダ過密**（テナント picker・⚙admin・ws-state・Start/Stop・🗂files・⧉sign-in URL が混在）。
- 視覚階層・余白・一貫性が無く、全体に密度が高すぎる。

## 18.2 機能インベントリ（刷新後も必ず保持）

| 機能 | 現在の置き場 | 使う API |
|------|--------------|----------|
| ログイン状態 / 誰として | （ヘッダ暗黙）| `GET /api/whoami` |
| テナント選択 | ヘッダ picker（所属1は自動・非表示）| `GET /api/tenants`（`super_admin` flag 同梱）。全 API に `X-AF-Tenant` 付与（端末 WS は `?tenant=`）|
| Workspace 起動/停止/状態 | ヘッダ | `GET /api/workspace`・`POST /api/workspace/start\|stop` |
| ターミナル（xterm）| 中央ペイン | `GET /ws/terminal?session=&tenant=` |
| `/login` URL コピー | ヘッダ ⧉ | （端末バッファから復元、§2.6/§11.10）|
| Claude セッション開始/一覧/停止/attach | サイドバー Sessions | `GET/POST /api/sessions`・`POST /api/sessions/{name}/stop`。kind=claude\|shell |
| Connections（Claude/GitHub/Bitbucket）接続/切断/状態 | サイドバー Connections | `GET /api/connections`・各 `PUT/DELETE/oauth …`（[06]/[HANDOFF §6.6]）|
| リポジトリ clone/一覧/削除/branch切替/fetch | サイドバー Repos | `/api/repos`・`/api/repos/{name}/status\|branches\|checkout\|fetch` |
| ソース管理（変更/diff/log/stage/unstage/discard/commit）| SCM オーバーレイ | `/api/repos/{name}/changes\|diff\|log\|stage\|unstage\|discard\|commit` |
| ファイルブラウザ（tree/viewer, read-only）| Files オーバーレイ | `/api/fs/tree?path=`・`/api/fs/file?path=`（denylist 済）|
| 管理（テナント/メンバー/クォータ/使用量・強制停止）| Admin オーバーレイ（super_admin のみ）| `/api/admin/tenants`・`/api/admin/tenants/{slug}/members`・`POST /api/admin/{tenants,memberships,stop-workspace}`・`PUT /api/admin/{tenants/{slug}/limits,user-limits}` |

## 18.3 制約（刷新で守ること）

- **配信**: CP が `console/` をディスクから `Cache-Control: no-store` で配信（リロードで即反映・再ビルド不要）。`control-plane/main.go` の static handler。
- **base-path 相対**: Tailscale Funnel→Caddy が `/agent-fleet` を strip するため、**全 URL は `document.baseURI` 相対**（現 `rel()` ヘルパ）。絶対パス禁止。
- **テナントヘッダ**: 全リクエストに `X-AF-Tenant`（現状 `window.fetch` ラップで注入）。端末 WS は header 不可ゆえ `?tenant=`。
- **xterm.js**: CDN から fit/web-links/unicode11/webgl アドオン（端末描画は現状維持が無難。§11.10 の知見=unicode11+WebGL+JetBrainsMono）。
- **可視性制御**: テナック picker は所属1で自動非表示、⚙admin は `super_admin` のみ。
- **セキュリティ**: 秘密は CP/Console に出さない（既存どおり Agent 内処理）。ファイルブラウザの denylist は Agent 側で担保済（UI 変更で緩めない）。

## 18.4 推奨 IA（たたき台・要確認）

機能が「端末 / Repos / ソース管理 / Files / Connections / Admin」と増えたので、**左アクティビティ・レール（VSCode 風）**が素直。
オーバーレイを廃し、各機能を**1 つのメイン領域に切替表示**する。

```
┌────┬──────────────────────────────────────────────┐
│ ▭  │  top bar: user@tenant ▾   workspace: ● running  Start/Stop │
│ ⌂  ├──────────────────────────────────────────────┤
│ ⎇  │                                              │
│ 🗂 │        main view (選択中の1つ)               │
│ 🔌 │   端末 / Repos / Source Control / Files /      │
│ ⚙  │   Connections / Admin                         │
└────┴──────────────────────────────────────────────┘
左レール=アイコン+ラベルでビュー切替／中央=単一メイン／上=ID・テナント・WS制御
```

- **レール項目**: Terminal（既定）/ Repos / Source Control / Files / Connections /（super_admin のみ）Admin。
- **オーバーレイ廃止** → 各々をメインの「ビュー」に。端末は1ビューとして常駐（セッション attach 状態を保持）。
- **暗号アイコン → アイコン+ラベル**。破壊的操作（delete/discard/force-stop）は確認＋色で区別。
- **フォーム整形**: clone / new-session を縦積み・ラベル付き・適切な余白に。
- 代替案: 上タブ式 / 現サイドバー維持で整形のみ（軽いが拡張性低い）。

## 18.5 ⚠️ 最大の要決定事項：vanilla JS 維持 vs フレームワーク導入

- 確定スタック（[01]/[README]）は **Next.js(React)+xterm.js** だが、実装は Phase 1 で**最小 vanilla JS**にした。
- 刷新は **React 採用の好機**だが、**ビルド工程**が増え、P3-10 パッケージング（自社セルフホスト・配布）と
  「ディスク配信・no-store・即反映」の手軽さとトレードオフ。
- **選択肢**:
  1. **vanilla JS 維持**（コンポーネント化＋CSS 整理）。ビルド無し・配信単純。小規模に十分。**推奨（まず IA を直す）**。
  2. **軽量フレームワーク**（Preact/Alpine 等を CDN、ビルド無し）。中庸。
  3. **Next.js/React 本格採用**（ビルド導入、確定スタックに整合）。将来の作り込みに強いが配布が重くなる。
- **この判断を最初に確定**してから着手すること。推奨は 1 か 2（no-build を維持）。

## 18.6 進め方（提案）

1. 18.5 の方針決定（vanilla 維持 推奨）。
2. レイアウト骨格（レール＋トップバー＋単一メイン）と CSS の土台を作り直す。
3. 既存機能を**ビューへ移植**（端末→Repos→SCM→Files→Connections→Admin の順）。API は不変なので JS ロジックは大半再利用可。
4. 破壊的操作の確認・アイコン+ラベル・フォーム整形・空状態/エラー表示を通す。
5. ブラウザで実機確認（headless 不可ゆえ**人間の目視前提**）。レスポンシブは社内 PC 幅で十分。

## 18.7 着手メモ

- 触るのは `console/` のみ（バックエンド不変）。`deploy/local/run-dev.sh` 起動中ならリロードで反映（no-store）。
- 現 `app.js` の API 呼び出し（`api()`/`rel()`/`window.fetch` ラップ/`attach()` 端末/各 refresh 関数）は**動作する資産**。捨てずにビュー単位へ再編。
- 現状の全機能はバックエンド検証済（[HANDOFF](../HANDOFF.md) の P3-1〜P3-5＋管理 UI）。**振る舞いの正解は現 Console の挙動**を参照。
