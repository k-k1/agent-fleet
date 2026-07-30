# 17. P3-5 実装プラン — メンバー Console UX（git/ファイル可視化）

> 🗄 **歴史的記録（完了）** — 現状は [HANDOFF §6.10](../HANDOFF.md)、設計は [ロードマップ P3-5](../roadmap.md#p3-5-管理コンソール--管理-api)。**§17.1（機微状態を browse 範囲外へ退避する前提）の設計理由は今も中核**。以下は当時の実装プラン。

[12 Phase 3](../roadmap.md) の P3-5 を**メンバー（開発者）向け Console 体験**として再定義する
（当初の「管理 UI」は別途小さく切り出す）。開発者が日々触る面: ログイン → テナント選択 → Clone →
セッション（claude / 通常 shell）→ **git/ファイルの可視化（IDE 的なソース管理＋エクスプローラ）**。

login / テナント選択 / Clone / claude セッションは既存（P3-2 + Phase 1/2）。本書は**新規分**を設計する。

## 17.0 確定スコープ（2026-06-27 の意思決定）

| # | 内容 |
|---|------|
| A | **shell セッション**（claude でなく bash）。session に `kind` 追加 |
| B | **git 閲覧**: changes / diff / log / tree / file（read-only・traversal 防御・サイズ上限）|
| C | **軽い git 操作**: stage / unstage / discard / commit（POST、明示確認）|
| D | **機微状態の browse 範囲外退避** → コンテナ全体ブラウズを安全化 |
| E | **Console**: ソース管理パネル ＋ エクスプローラ（両方）|

## 17.1 D が前提（先にやる）— 機微状態を browse 範囲外へ

ファイルブラウザを広く（home〜コンテナ全体）開けるようにするため、**機微状態を browse ルート外の別永続領域へ退避**する。

- **`CLAUDE_CONFIG_DIR` を home 外へ**: 例 `/var/lib/af/claude`。**別の永続マウント**（`<dataDir>/secure` → `/var/lib/af`）に置き、browse ルート（`/home/node`）の外に出す。Claude Code は `CLAUDE_CONFIG_DIR` を尊重（§2.7/§2.9）。
- **agent secrets を同様に退避**: `secrets.enc`（P3-3）も `/var/lib/af/secrets` へ。`secretsPath()`（`workspace/agent/secrets.go`）を env（例 `AF_STATE_DIR`）で差し替え。
- **平文クレデンシャルを作らない**: Connections トークンは暗号ストア＋セッション env 注入（`CLAUDE_CODE_OAUTH_TOKEN`）の既存経路。手動 `/login` を促さない運用。
- **browse denylist**（二重の安全網）: ブラウザは `~/.claude` `~/.config/agent-fleet` `~/.ssh` `~/.git-credentials` `/var/lib/af` を**常に拒否**。
- **原理的限界（明記）**: 同一 uid の対話 shell から本人の env トークンを完全に隠すことはできない（本人の BYO トークンゆえ他者漏洩ではない）。完全 shell 不可視には claude を別 uid 実行が要るが、**本書では「ブラウザ不可視＋at-rest 暗号＋env 注入（平文ファイルなし）」を採用**（別 uid は将来オプション）。
- **移行**: 既存運用者の `~/.claude` / `~/.config/agent-fleet` を新領域へ one-shot 移動（コンテナ entrypoint で、無ければ移動）。home は永続ゆえ一度きり。

## 17.2 A. shell セッション

- `POST /sessions` に `kind`（`claude`(既定) | `shell`）を追加（`workspace/agent/session.go`）。`shell` は tmux で `bash -l` を起動（claude env 注入は不要）。
- Console の New session フォームに kind トグル。`Session` 表示に kind バッジ。

## 17.3 B. git 閲覧エンドポイント（Agent, read-only）

`workspace/agent/git.go` に追加（CP は既存 `proxyAgentREST` でそのまま委譲）。すべて **repo 配下に解決・`..` 拒否・サイズ上限**。

| エンドポイント | 内容 | 実装 |
|---|---|---|
| `GET /repos/{name}/changes` | 変更ファイル一覧 `[{path, index, worktree, kind(staged/unstaged/untracked)}]` | `git status --porcelain=v2` を file 単位に解析（件数版の拡張）|
| `GET /repos/{name}/diff?path=&staged=` | unified diff テキスト | `git diff [--staged] -- <path>`（path 省略で全体・上限）|
| `GET /repos/{name}/log?limit=&ref=` | コミット履歴 `[{hash, short, author, date, subject}]` | `git log --max-count=N --pretty=format:...` |
| `GET /repos/{name}/tree?path=` | ディレクトリ一覧 `[{name, type(dir/file), size}]` | `os.ReadDir`（repo 内に解決）|
| `GET /repos/{name}/file?path=` | ファイル内容（text・サイズ上限・バイナリ判定）| `os.ReadFile`＋上限＋UTF-8 検査 |

## 17.4 C. 軽い git 操作エンドポイント（Agent, write）

| エンドポイント | 内容 |
|---|------|
| `POST /repos/{name}/stage` `{paths:[]}` | `git add -- <paths>` |
| `POST /repos/{name}/unstage` `{paths:[]}` | `git restore --staged -- <paths>` |
| `POST /repos/{name}/discard` `{paths:[]}` | `git restore -- <paths>`（**破壊的**→ Console で確認）|
| `POST /repos/{name}/commit` `{message, all?}` | `git commit -m`（`all` で `-a`）。user.name/email は Connections 設定を使用 |

すべて paths を repo 内に解決・`..` 拒否。commit は空メッセージ拒否。

## 17.5 E. Console（ソース管理 ＋ エクスプローラ）

リポジトリを選ぶと右に IDE 的ビュー（既存 vanilla JS を拡張、xterm と同居）:

- **ソース管理**: 変更一覧（staged/unstaged/untracked を区分）→ クリックで**色付き unified diff**。各行・各ファイルに stage/unstage/discard、下部に commit メッセージ＋Commit。
- **エクスプローラ**: ファイルツリー（`tree` を遅延展開）→ クリックで**ファイル内容ビューア**（read-only、シンタックスは最小）。
- **ブランチ**: 現在ブランチ・ahead/behind chip（既存 status）＋ 切替（既存 branches/checkout）。
- **fetch** ⤓・dirty ● は既存を流用。破壊的操作（discard）は確認ダイアログ。

## 17.6 CP / セキュリティ

- CP: 新 `GET/POST /api/repos/{name}/{changes,diff,log,tree,file,stage,unstage,discard,commit}` を `proxyAgentREST` で委譲（多くは既存パターン）。テナント解決（X-AF-Tenant）も既存どおり。
- Agent: traversal 防御（`filepath.Clean`＋repo ルート prefix 検査）、`file`/`diff` はサイズ上限（例 1–2 MiB）、バイナリは内容を返さずメタのみ。browse denylist（17.1）。
- 監査: write 系（stage/commit/discard）は将来 AuditLog に（P3-9）。

## 17.7 推奨シーケンス（OOM 注意 — ホストのメモリ枯渇は稼働中フリート全体を巻き込む）

1. **D（機微退避）**: image entrypoint + `secrets.go` の state dir env 化 + 移行。**先に入れて browse を安全に**。
2. **B（閲覧エンドポイント）** + CP proxy + 単体（traversal/上限）。
3. **A（shell セッション）**。
4. **E ソース管理パネル**（changes/diff/log/branch）→ **E エクスプローラ**（tree/file）。
5. **C（write 操作）** + Console アクション（確認つき）。
6. ライブ検証（運用者 repo で changes/diff/log/tree/file、shell セッション、stage/commit、機微が browse/denylist で出ないこと）。

## 17.8 スコープ外（後続）

- 管理者 UI（テナント/ユーザー/使用量/監査）= 別 increment（旧 P3-5 admin）。
- claude を別 uid 実行（完全 shell 不可視）。
- 全文検索・シンタックスハイライト強化・インライン編集（claude/端末で代替）。
