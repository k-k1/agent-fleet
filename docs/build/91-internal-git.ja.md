---
audience: "内部 git プロバイダに触れる人"
source_of_truth: "コード"
updated: "2026-07"
---

# 91. テナント内部 git プロバイダ（bare + smart-HTTP）

[English](91-internal-git.md) | 日本語

- 状態: **P1 実装済み**（MVP）。契約はコードが正（`control-plane/git_http.go`・`internal_git.go` ほか）。
- 関連: [01 アーキテクチャ](01-architecture.ja.md) / [07 §7.6](07-security.ja.md#76-シークレット管理と封筒暗号) /
  [05 API 契約](05-api.ja.md) / ADR [0010](../decisions/0010-internal-git-provider.ja.md)（採否）/
  [0003](../decisions/0003-ssh-to-connections.ja.md)（git 認証＝Connections）

## 1. 目的とスコープ

テナント内でリポジトリを**フリート内に閉じて**持てるようにする。外部 GitHub/Bitbucket
アカウントを介さず、Control Plane（CP）がテナント毎の bare リポジトリを smart-HTTP で配信し、
既存のプロバイダ抽象（Connections / RepoPicker / cred helper / clone・SCM 閲覧）にそのまま載せる。

狙い（A–C）:

- **A. チーム内共有** — 同一テナントのメンバー間でリポジトリ／エージェント成果（ブランチ）を共有。
- **B. private scratch/seed** — エージェント用の内部リポジトリ（外部に出さない作業場）。
- **C. コードを外に出さない** — コンプラ/隔離。資格情報も外部プロバイダを持たない。

### 非目標（このフェーズでは作らない）

- PR / コードレビュー / CI（GitHub 相当。将来 Gitea/Forgejo へ載せ替える「②」領域）。
- 組織/チームの細粒度権限マトリクス（当面は membership role で read/write の2段）。
  ※ LFS 本体（Batch API＋basic 転送＋容量クォータ＋孤児 GC＋**ロック API**）は **P3 で実装済み**（§9）。

## 2. なぜこの形か（要約。詳細は ADR 0010）

- **CP に置く**: テナントを知る唯一の共有コンポーネントが CP。per-user コンテナは
  各ワークスペースに閉じており横断共有できない。
- **bare + `git http-backend`**: 最小コードで clone/fetch/**push** まで smart-HTTP で成立。
  閲覧は既存の SCM（コミットグラフ）を clone 後にそのまま使える。
- **AWS CodeCommit は不採用**: 2024 年に新規顧客受付終了。かつ IAM 認証がトークン注入型の
  統一 cred helper と噛み合わない。
- clone/閲覧/コミットの機構は**既にホスト非依存**（`git.go` / `git_remote.go` / `fs.go` /
  `SourceControlView`）。追加は「CP 側 git サーバ＋トークン注入＋プロバイダ登録」の 3 ブロックのみ。

## 3. 全体構成

```
  Console ──(X-AF-Tenant)──▶ Control Plane ───proxy /api──▶ per-user Agent (per membership)
     │                          │  ▲                              │  git clone / fetch / push
     │ provider tab = internal  │  │ CP native (Agent 経由でない)  ▼
     └── api/internal-git/* ─────┘  │                 https://<base>/git/<slug>/<repo>.git
        （repo 一覧 / 作成 / 削除）  │                              ▲
                                     └── smart-HTTP (git-http-backend) │ Basic: pw = tenant git token
        bare repos:  ${DATA_DIR}/git/<slug>/<repo>.git                │ cred helper が自動注入
        （既存の永続ボリューム。deploy compose の ${DATA_DIR} bind）  ─┘
```

- 内部プロバイダの **repo 一覧/作成は CP ネイティブ**（CP がリポの所有者なので Agent を経由しない。
  既存の「全プロバイダは Agent 経由」から意図的に分岐）。
- **clone/push はワークスペースのコンテナ内 git** が `https://<base>/git/<slug>/<repo>.git` へ。
  コンテナは専用 docker ネットワーク＋NAT egress なので、到達は**共有コンテナ網ではなく
  デプロイのベース URL（Caddy TLS 終端）** を用いる。

## 4. ストレージ

- 配置: `${DATA_DIR}/git/<tenant-slug>/<repo>.git`（bare）。
  `WS_DATA`（既定 `/tmp/af-data`）配下で、既に永続化＋`${DATA_DIR}:${DATA_DIR}` bind 済み
  （`deploy/compose/docker-compose.yml`）。
- 既定テナント/その他テナントの slug 規則は既存の `manager.workspaceNames` に倣う
  （既定 = flat、その他 = `<slug>/`）。git は別ツリー `git/<slug>/` に分ける。
- メタデータ: SQLite に **`git_repo` テーブル**（新 migration）。一覧・作成者・作成時刻・
  （将来）クォータ/監査の台帳。ディレクトリ走査でなく DB を正にして FS レースを避ける。
- **LFS オブジェクト**（P3）: content-addressed で `<repo>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>`
  （oid=sha256）。repo の `.git` ツリー内に置くので delete/rename で一緒に移動/削除される。
  容量クォータ用の会計台帳として **`lfs_object` テーブル**（tenant, repo, oid, size）を持ち、
  テナント合計バイトを O(1) の SUM で得る（FS 走査を避ける）。

## 5. 認証・認可・トークンモデル

2 つの認証面がある。

### 5.1 Console/API 面（repo 管理・プロバイダタブ）

既存の CP identity＋tenant 解決（`X-AF-Tenant` → `resolvedFor`）をそのまま使う。
`GET/POST/DELETE /api/internal-git/repos` は**解決済みテナントにスコープ**。追加の資格情報は不要。

### 5.2 git smart-HTTP 面（clone/fetch/push）

- membership（identity × tenant）毎に**決定的な HMAC トークン**を用いる（**token 用の DB は持たない**）。
  形式 `afg_<b64url(membershipID)>.<HMAC-tag>`。署名鍵はデプロイ master key（AF_MASTER_KEY）から
  派生（`git_http.go` `gitSignKey`）。CP は同じ関数でトークンを**再生成**できるので、注入は冪等で
  平文の保存も復元問題も無い（PAT 表流用は却下 → ADR 0010）。
- ワークスペース起動時（`manager.go` `workspaceExtraEnv`）に、CP が env
  `AF_INTERNAL_GIT_HOST` / `AF_INTERNAL_GIT_TOKEN`（= `mintGitToken(membershipID)`）を注入。
  Agent は起動時（`cred_helper.go` `seedInternalGit`）にこれを暗号ストアへ
  `s.Git[<host>] = { User: "x-access-token", Token: <token> }` として seed する。
  → 統一 cred helper（`cred_helper.go` `runCredHelper`）は**任意ホストを既に配信する**ので、
  これだけで clone/push の Basic 認証が透過的に通る。
- CP の smart-HTTP ハンドラは Basic の **password を token として検証**（tag 照合）→ 埋め込まれた
  membership を**ライブ参照**して (tenant, role) を解決し、以下を**毎リクエスト強制**:
  - URL の `<slug>` == token のテナント（他テナントのリポに到達不可）。
  - repo が `git_repo` 台帳に存在（未登録は 404）。
  - `git-upload-pack`（read）= 有効な membership。
  - `git-receive-pack`（push/write）= role で可否（`canPush`: member / tenant_admin。将来の viewer は read-only）。
- **失効**: membership を無効化すると `GetMembershipByID`（`status='active'` フィルタ）が外れ、同じ
  決定的トークンが即座に通らなくなる（token 表が無くてもライブで失効）。全体ローテーションが要る段は
  membership に epoch 列を足して HMAC 入力に混ぜる（P2）。

## 6. データフロー

- **リポ作成**: Console →（CP）`POST /api/internal-git/repos {name}` → `git_repo` 行 + `git init --bare`
  `${DATA_DIR}/git/<slug>/<name>.git`（既定ブランチ設定）→ `clone_url` を返す。
- **一覧**: Console（provider タブ=internal）→ `GET /api/internal-git/repos` → `git_repo` から
  テナント分を返す（RepoPicker が Agent 経由でなくこの CP エンドポイントへ分岐）。
- **ブランチ一覧**: `GET /api/internal-git/repos/{name}/branches`（bare を `git for-each-ref` で読む）。
- **clone/起動**: 既存の clone-then-start（`ensureRepo` / `handleCloneRepo`）に `clone_url` を渡すだけ。
  cred helper が token 注入。
- **push（共有）**: エージェント/ユーザーがブランチを push → 他メンバーが同 URL から clone/fetch。
- **閲覧・コミット**: clone 後は既存の `repos/{name}/graph|status|checkout|…` と `fs/*` がそのまま動く
  （プロバイダ非依存）。

## 7. 統合点（変更箇所の地図）

| # | 箇所 | 変更 |
|---|------|------|
| 1 | `control-plane/main.go`（mux, 〜419） | ルート追加: `/git/{slug}/{repo...}`（smart-HTTP）、`/api/internal-git/*`（管理 API） |
| 2 | `control-plane/git_http.go`（新規） | HMAC トークン発行/検証 + `git http-backend`(CGI) ラッパ + slug 封じ込め + 台帳存在確認 + role 認可 |
| 3 | `control-plane/internal_git.go`（新規） | repo 一覧/作成/削除、branches、`clone_url` 生成 |
| 4 | `control-plane/migrations/0014_git_repo.sql`（新規） | `git_repo` テーブルのみ（**token 表は作らない** — 決定的 HMAC） |
| 5 | `control-plane/store{,_sqlite}.go` | `GitRepo` 型 + CRUD、`GetMembershipByID`（token→tenant/role 解決用） |
| 6 | `control-plane/main.go` | ルート登録（`/api/internal-git/*`、`/git/{slug}/{repo...}`）＋ `/git/` を authGate 免除、`internalGitHost` 設定 |
| 7 | `control-plane/manager.go`（env 注入） | `workspaceExtraEnv` で `AF_INTERNAL_GIT_HOST` / `AF_INTERNAL_GIT_TOKEN` を注入 |
| 8 | `control-plane/Dockerfile` | runtime に `git`（`git-http-backend`）を追加 |
| 9 | `workspace/agent/cred_helper.go` | `seedInternalGit`（起動時に env→`s.Git[host]` へ seed）＋ `internalGitHost` |
| 10 | `workspace/agent/connections.go` | `handleConnectionsGet` に `internal` 状態（`internalGitStatus`） |
| 11 | `workspace/agent/git.go` `gitProviderHost` | 内部ホスト（env で動的一致）を `internal` slug にバッジ |
| 12 | `console/src/components/RepoPicker.tsx` | `PROVIDERS` に `internal` タブ、internal 時は repo/branch を **`api/internal-git/*`**（CP 直）へ分岐 |
| 13 | `console/src/settings/GitTab.tsx` | 「内部リポジトリ」カード（一覧/作成/削除、OAuth 不要、WS 停止中も可） |
| 14 | `docs/README.md` / 本書 / ADR 0010 | 索引・設計・決定の更新 |

`workspace/agent/git_remote.go` の switch は触らない（内部一覧は **CP 直**。Agent→CP 認証が要るため Agent
経由は非推奨）。clone/閲覧/コミットは既存のまま無改造。**`gitHosts`（connections.go）も触らない** —
内部ホストは実行時 env で動的、cred helper は `s.Git[host]` にある任意ホストを配信するため登録不要。

## 8. 隔離・セキュリティ

- **テナント越境の遮断**: token の tenant と URL の `<slug>` 一致を smart-HTTP の**全リクエスト**
  （info/refs・upload-pack・receive-pack）で検証。
- **パス封じ込め**: slug/repo 名は正規表現で検証、`..` 拒否、`${DATA_DIR}/git/<slug>/` 配下に限定。
- **権限**: read=member 以上、write(push)=role。将来は repo 単位の ACL を `git_repo` に拡張可能。
- **秘密の非漏洩**: token は暗号ストア（`secrets.enc`, AES-256-GCM）に注入し平文化しない
  （[0003](../decisions/0003-ssh-to-connections.ja.md) / [0005](../decisions/0005-envelope-custodian.ja.md) 準拠）。
- CP に **git 実行面が増える**点は新たな攻撃面。入力（refspec/パス）検証を厳格化する。
- **LFS**（P3）: smart-HTTP と同じ `authorizeGitRepo`（テナント越境遮断・台帳存在）を全操作で適用。
  oid は sha256 hex のみ許可＝転送パスのパス封じ込めも兼ねる。アップロードは sha256 を検証し oid 不一致を
  拒否（汚染防止）。容量は batch/PUT 双方でクォータ強制。大容量はメモリに載せずストリーム（共有ホスト配慮）。

## 9. フェーズ

- **P1（MVP・実装済み）**: `git_http.go` + `internal_git.go` + token 注入 + 作成/一覧/削除 API + provider タブ。
  → 内部リポを clone/push でき、閲覧は既存 SCM。A/B/C を満たす。
- **P2（実装済み）**: 以下を追加。
  - **リネーム**: `POST /api/internal-git/repos/{name}/rename {new_name}`（bare 移動＋台帳更新、
    既存 clone は origin URL の更新が必要）。
  - **クォータ**: `tenantLimits.max_git_repos`（0=無制限）を作成時に強制（`enforceGitRepoQuota` →
    超過は 409 `quota_exceeded`）。admin limits API / AdminTab に露出。
  - **`git gc` cron**: `git_gc.go`（全 bare を `git gc --auto` で逐次 repack。`AF_GIT_GC_INTERVAL`
    既定 24h、0 で無効。メモリ配慮で逐次・`--auto`）。
  - **監査ログ**: 作成/削除/リネームを既存 audit 台帳へ（`internal_git.repo.create|delete|rename`、
    `auditGit`）。admin 監査ビューに出る。
  - **空リポ/既定ブランチ UX**: 新規作成した空リポ（コミット無し＝ブランチ無し）でも RepoPicker が
    `default_branch` をプレースホルダとして選択・clone 可能に。
- **P3（実装済み）**: **Git LFS**。
  - `git_lfs.go`: Batch API（`POST .../info/lfs/objects/batch`）＋ basic 転送
    （`PUT/GET .../info/lfs/objects/{oid}`）。認証・封じ込めは smart-HTTP と共通の
    `authorizeGitRepo`（Basic トークン→membership→slug 一致→台帳存在）を再利用。
  - **アップロードは sha256 検証**（oid 不一致は 422）、temp→fsync→rename で原子的公開、dedup。
  - **容量クォータ**: `tenantLimits.max_lfs_bytes`（0=無制限）を **batch 時（507 error entry）と PUT 時**
    の両方で強制。`lfs_object` 台帳で O(1) 集計。admin limits API / AdminTab（MB 入力）に露出。
  - **ロック API**（`git_lfs_locks.go`）: create / list / verify / unlock を実装（`info/lfs/locks`）。
    認証は `authorizeGitRepo` 共用、create/unlock は write（`canPush`）・list/verify は read。path は
    (tenant, repo) 毎に一意（二重ロックは 409＋既存ロック）。verify は所有者で ours/theirs に分割
    （push 前に他人のロックを検知）。unlock は所有者のみ、`force` は tenant_admin に限り他人のロックも解除。
    ロックは `lfs_lock` テーブルに保存し repo の delete/rename に追従。実 `git lfs lock/locks/unlock` の E2E あり。
  - ワークスペースは git-lfs 同梱・cred helper 連携済みでクライアント無改造。実 `git lfs push`/clone の E2E あり。
  - **孤児オブジェクト GC**: 既存の `git gc` cron（`git_gc.go`）に統合。どの reachable なポインタからも
    参照されない LFS blob を削除して容量を戻す。参照 oid の列挙は **pure-git**（CP に git-lfs 不要）で
    `git cat-file --batch-all-objects` から全 blob を走査しポインタを抽出。**grace 期間**
    （`AF_LFS_GC_GRACE` 既定 14 日）で mtime が新しいオブジェクトは残し、「upload→ref push」途中の
    誤削除を防ぐ。列挙失敗時は**何も消さない**（conservative）。削除で `lfs_object` 台帳も減り quota が戻る。
- **clone なしツリー閲覧**（実装済み）: `internal_git_browse.go`。CP が bare を直接読む read-only の
  tree/blob/commit API（`GET .../repos/{name}/tree|blob|commits`、CP ネイティブ・テナントスコープ・read）。
  - `git ls-tree`（dir 一覧、tree 優先ソート）／`cat-file`（blob。1 MiB 超は too_large、バイナリ・LFS
    ポインタはフラグのみ返す）／`log`（コミット）を薄くラップ。ref/path は正規表現で検証
    （`..`・先頭 `-`・絶対パス・制御文字を拒否＝arg 誤認/traversal 対策）。空リポは空一覧。
  - Console: `InternalRepoBrowser`（GitTab の「参照」ボタン）でブランチ選択＋パンくず＋ツリー＋テキスト
    プレビュー（binary/too_large/LFS は注記）。
- **見送り（将来）**: PR/レビュー/CI が要れば ② へ載せ替え。
- **将来（②）**: PR/レビュー/CI が要るなら Gitea/Forgejo を内包して載せ替え。

## 10. 確定事項（P1 実装で確定）

1. **CP イメージの `git` バイナリ**: → **追加**（`control-plane/Dockerfile` runtime に `git`）。
   `git-http-backend` は `/usr/lib/git-core/git-http-backend`。`GIT_HTTP_BACKEND` env で上書き可。
2. **token モデル**: → **membership 毎の決定的 HMAC トークン（token 用 DB なし）**。PAT 表流用は却下
   （平文復元不可で注入が非冪等・ユーザー一覧を汚す）。§5.2 / ADR 0010 参照。
3. **clone URL ホスト**: → **`PUBLIC_BASE_URL`**（Caddy TLS 終端。コンテナはヘアピン NAT で到達）。
   未設定なら内部 git は無効（作成 API は 503 `not_configured`、注入もスキップ）。
4. **内部ホスト id**: → provider id / UI ラベル = **`internal`（「内部」）**。実 URL ホストは
   `PUBLIC_BASE_URL` のホストで、実行時に `AF_INTERNAL_GIT_HOST` として Agent へ注入（コンパイル時固定不可）。
