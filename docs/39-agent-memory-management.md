# 39. エージェントメモリ管理 — git 差分管理・時点ロールバック・環境間 import/export

- 状態: **設計中（意思決定前・2026-07-23）**。意思決定は [decisions/0022](decisions/0022-agent-memory-management.md)。
- 要望: 各エージェントの「メモリ」を管理したい。①git での差分管理 ②日時・履歴指定でのロールバック
  ③特定プロジェクトだけのロールバック ④他の agent-fleet 環境への import/export。

## 背景 / 「メモリ」の実態（調査結論 2026-07-23）

まず「各エージェントのメモリ」が実際に何であるかを現物で棚卸しした。

| kind | 置き場 | 実態 | 版管理対象になるか |
|------|--------|------|--------------------|
| claude | `CLAUDE_CONFIG_DIR=/var/lib/af/claude` の `projects/<slug>/memory/*.md`（`MEMORY.md` 索引 + 1事実=1ファイル） | **エージェント自身が書き溜める唯一の永続メモリ**。専用マウント（Docker: `<dataDir>/claude-config`、ECS: EFS AP `/claude-config/<membershipID>`、native: 同型。`runtime_docker.go:107-141` / `runtime_ecs.go:363,387` / `runtime_native.go:362-431`）。recreate / clean-home を生き残る（`workspace-notes.md`） | **○（v1 の対象）** |
| codex | `~/.codex/AGENTS.md` | 毎起動 `cp -f` で image の `workspace-notes.md` に**上書き**（`entrypoint.sh` の refresh。編集は次回起動で消える）＝フリート管理ファイルであり、エージェントが書き溜めるメモリではない | ×（将来: user-owned 領域を設計してから） |
| opencode | `~/.config/opencode/AGENTS.md` | 同上（毎起動 refresh） | × 同上 |
| agy | `~/.gemini/AGENTS.md` + `brain/<uuid>/` | AGENTS.md は指示ファイル、brain は会話履歴でありメモリではない | × |
| copilot | `~/.copilot` | メモリ概念なし（auth+セッションストアのみ） | × |

**結論: 今日「メモリ」と呼べる実体は claude の auto-memory（`projects/*/memory/`）だけ。**
ただし本設計は「メモリルート」を宣言的に持ち、将来 kind を追加するだけで拡張できる形にする。

サイズ実測（本開発環境）: メモリ本体はプロジェクトあたり 32KB〜668KB。一方 `/var/lib/af/claude`
全体は 883MB（大半がセッション transcript の jsonl）。**メモリだけを対象にすれば git 履歴の
コストは無視できる**が、対象選定を誤ると transcript と `.credentials.json` を巻き込む。

もう一つの好条件: claude のメモリは**プロジェクト単位で自己完結**している（`MEMORY.md` 索引も
各 `memory/` ディレクトリ内）。つまり「特定プロジェクトだけロールバック」は、ディレクトリ
1 個のパススコープ操作に落ち、索引の整合性も壊れない。

### 既存機構に無いもの（gap）

- `deploy/compose/backup.sh` は **DATA_DIR 丸ごと tar**（ops 層・全ユーザー一括・時点は取得タイミングのみ）。
- `cleanup_archive.go`（docs/32）は削除セッションの tar.gz 安全網であり、継続的な差分履歴ではない。
- 秘匿接続情報にも export 経路は無く、**個人単位の「履歴・巻き戻し・持ち出し」primitive はゼロ**。
- 環境間移設は roadmap 上も「home+DB を丸ごとバックフィル」型のみで、メモリ粒度の移送は存在しない。

## 決定した既定（2026-07-23 提案）

| 論点 | 既定 | 備考 |
|------|------|------|
| 対象データ | v1 = claude `projects/*/memory/**` のみ。**allowlist 方式** | transcript / `.credentials.json` / `settings.json` は構造的に対象外（★1） |
| 履歴エンジン | **git**（bare repo + 一時 staging コピー） | 差分・時点解決・bundle 移送・パススコープ復元が全部標準機能で載る |
| repo の置き場 | `/var/lib/af/claude/af-memory.git`（claude 専用マウント内） | ファイルブラウザ非公開領域・recreate / clean-home 生存・ECS でも agent から見える。live ツリーには `.git` を置かない |
| 実行主体 | **workspace-agent**（uid=dev、git あり、全 runtime で一様） | CP は REST proxy（dual allowlist）のみ。ECS で CP がファイル直アクセスできない制約を回避 |
| snapshot 契機 | 自動（claude 全セッション idle 遷移 + debounce）＋手動＋（将来 scheduler） | 無変更なら commit しない |
| ロールバック | **履歴は書き換えない**。restore は「新しい snapshot commit」として積む。適用前に pre-restore snapshot 自動取得 | ロールバックのロールバックが常に可能 |
| スコープ | 全体 / プロジェクト単位（`projects/<slug>/`） | 日時指定は「その時刻以前の直近 snapshot」に解決 |
| export | **git bundle（全履歴）を既定**、tar.gz（最新ツリーのみ）も選択可 | bundle は 1 ファイル・履歴込み・`git bundle verify` で検証可能 |
| import | bundle → `refs/imports/<ts>` に fetch / tar → staging 展開。適用は**プロジェクト選択式の置き換え（=新 commit）** | 3-way merge はしない（v1）。ローカル履歴は保全される |
| UI | 設定モーダル「ワークスペース」グループに「エージェントメモリ」タブ | 破壊操作は P4a の統一確認ダイアログ |
| 監査 | `proxy.go auditActionTarget` に restore / import / export をマップ | export DL は本人操作のみ |

## アーキテクチャ

```
live:    /var/lib/af/claude/projects/<slug>/memory/**     ← claude が読み書き（無改変）
               │ ① allowlist copy（idle 契機・数百KB）
               ▼
staging: $TMPDIR/af-memory-staging/claude/projects/<slug>/...
               │ ② git commit（--git-dir=af-memory.git・専用 identity）
               ▼
repo:    /var/lib/af/claude/af-memory.git（bare・agent 管理・マウントと同寿命）
               │ ③ REST（dual allowlist: workspace/agent/routes.go + control-plane/routes.go）
               ▼
Console: 履歴一覧 / 差分 / ロールバック / export DL / import UL
               │ ④（P4）push mirror
               ▼
CP 内部 git（bare+http-backend 流用）→ 停止中閲覧・環境間直接同期
```

### ① メモリルートの宣言（拡張点）

agent 側に宣言テーブルを 1 つ持つ:

```go
type memoryRoot struct {
    Kind    string   // "claude"
    Label   string   // 表示名
    LiveDir string   // claude.ConfigDir()+"/projects"
    Include []string // {"*/memory/**"} — allowlist glob。これ以外は決して読まない
}
```

v1 はエントリ 1 件。codex/opencode に user-owned メモリ領域ができたら（P4 の前提作業）、
ここへ 1 行足すだけで snapshot / rollback / export の全機能が付いてくる。
repo 内レイアウトは `<kind>/projects/<slug>/...` で kind ごとに名前空間を分ける。

### ② snapshot エンジン

- **契機**: 既存の状態検知（working→idle 遷移。false-idle 対策で整備済みの機構）に載せ、
  「claude の全セッションが idle」になってから debounce（既定 5 分）で 1 回。加えて
  15 分 tick の保険と、Console からの手動 snapshot。
- **無変更 skip**: staging へ copy 後 `git status --porcelain` が空なら commit しない
  （空コミットで履歴を汚さない）。
- **コミットメッセージ**: 1 行目 `snapshot: 2026-07-23T12:00+09:00 (2 projects changed)`、
  trailer に `AF-Trigger: auto|manual|pre-restore|restore|import` と変更 slug 一覧。
  一覧 API はここから per-snapshot サマリを組み立てる（`git log --stat` 相当）。
- **整合性**: idle 時に copy するので書きかけファイルをほぼ拾わない。仮に torn file を
  拾っても次回 snapshot で自癒する（メモリは追記型 md であり中間状態も可読）。

### ③ REST API（dual allowlist に両側登録）

| メソッド/パス | 内容 |
|---------------|------|
| `GET  /api/agents/memory/roots` | ルート一覧（kind・プロジェクト数・最終 snapshot 時刻） |
| `GET  /api/agents/memory/snapshots?limit=&before=` | snapshot 一覧（id/ts/契機/変更パスサマリ） |
| `POST /api/agents/memory/snapshots` | 手動 snapshot（`{trigger:"manual"}`） |
| `GET  /api/agents/memory/diff?from=&to=&path=` | unified diff（path 省略で全体、`projects/<slug>` で絞り込み） |
| `POST /api/agents/memory/restore` | `{rev \| at, scope: {all \| projects: [slug...]}}`。手順は ④ |
| `GET  /api/agents/memory/export?format=bundle\|tar` | DL（Content-Disposition。CP proxy は既存 `rest` でストリーム可） |
| `POST /api/agents/memory/import` | multipart 受領 → 検証 → preview `{importId, projects[], snapshots, headTs}` |
| `POST /api/agents/memory/import/apply` | `{importId, projects: [slug...]}` → 選択分を live へ適用（=新 commit） |

`at`（日時指定）は agent 側で `git rev-list -1 --before=<at>` により snapshot に解決する。
CP 側は `control-plane/routes.go` に同パスを `rest` で登録（**登録漏れ=FE 404** の既知罠。
パターンルート `/api/agents/...` の扱いは既存の models ルートに倣う）。

### ④ ロールバックの安全設計

1. `restore` 受領 → まず **pre-restore snapshot** を自動取得（現在の live を必ず保全）。
2. 対象 rev の staging checkout（scope 指定時は `git checkout <rev> -- claude/projects/<slug>/`）。
3. live への適用は **allowlist prefix 限定の rsync --delete**。scope 外・allowlist 外の
   ファイルには一切触れない（`memory/` に無いファイルを消さない・作らない）。
4. 適用結果を **restore commit** として積む（`AF-Trigger: restore`、対象 rev を trailer に記録）。
5. 実行ゲート: claude セッションが実行中（working）の場合は警告を返し、Console 側で
   確認ダイアログを一段強くする（既定は続行可。実行中セッションが後からメモリを
   書けば restore 後の新 commit として現れるだけで、履歴上は追跡可能）。

### ⑤ import / export（環境間移送）

- **export = git bundle**: `git bundle create <file> --all`。1 ファイルに全履歴・全 refs が
  入り、受け側で `git bundle verify` により完全性検証できる。「最新だけ軽く持ち出したい」
  用に tar.gz（HEAD ツリーのみ）も併設。
- **import**:
  - bundle → `git fetch <file> +refs/heads/*:refs/imports/<ts>/*`。ローカル履歴とは独立の
    系譜として保持（graft しない）。preview はこの ref から生成。
  - tar.gz → 検証しつつ staging に展開し、import commit の材料にする。
  - **apply はプロジェクト選択式**: 含まれる slug 一覧を見せ、選んだものだけを
    「置き換え = 新 commit」で live に適用。3-way merge は .md の意味的衝突を機械で
    解決できないためやらない。取り込まなかった側もローカル履歴に残るので後悔がない。
- **slug 互換性**: リポジトリは全環境で `~/repos/<repo>` 規約なので、同名リポジトリなら
  slug（パス由来）が環境間で一致する。worktree suffix 付き slug はメモリを持たない
  （メモリは base slug に付く）ため衝突しない。
- **検証**（★3）: サイズ上限（既定 64MB）/ tar path traversal 防御（`cleanup_archive.go`
  の guard と同型）/ allowlist glob に合致しないエントリの拒否 / import・export とも監査ログ。
- **将来（P4）**: CP 内部 git プロバイダ（ADR 0010 の bare+http-backend）へ agent が
  push mirror する経路を足すと、(a) WS 停止中の履歴閲覧・export、(b) scoped token による
  環境間の**直接** pull 同期、が同じ repo 形式のまま手に入る。v1 のデータ形式（git repo）
  を変えずに積み増しできることが、git を選ぶ決め手の一つ。

### Console UI（P2）

設定モーダル「ワークスペース」グループに「エージェントメモリ」タブを追加
（`SettingsDialog.tsx` の GROUPS へ 1 エントリ + `MemoryTab.tsx`）。

- 上段: ルート/プロジェクト一覧（slug→リポ名整形表示・最終更新・サイズ）と手動 snapshot ボタン。
- 中段: snapshot 履歴（時刻・契機・変更プロジェクト）。行選択で右に diff ビュー
  （SCM コミットペインの diff 描画部品を流用）。
- 操作: 「この時点に戻す」（全体 / プロジェクト選択）→ 統一確認ダイアログ（P4a）。
  日付ピッカーからの「この日時時点へ」も同じ restore に解決。
- 下段: export（bundle / tar 選択 DL）・import（ファイル選択 → プロジェクト選択 preview → 適用）。

## 検討して捨てた選択肢

| 案 | 捨てた理由 |
|----|-----------|
| CP ホスト bare repo を v1 の正にする（内部 git 流用） | WS 停止中も操作できる利点はあるが、ECS では CP がユーザーデータへ直接ファイルアクセスできず、結局 agent push 型が必須。v1 から CP 側に台帳・認証・GC を持つのは可動部過多。P4 の mirror として積む |
| live ツリーへ直接 `git init` | claude 自身がメモリ列挙で `.git` を見る・エージェントの誤操作/破損リスク・allowlist 外ファイル混入。staging コピーはメモリが小さいので実質無料 |
| `/var/lib/af/claude` 全体の版管理 | transcript 883MB と `.credentials.json` を巻き込む。論外 |
| restic / 定期 tar 世代管理 | 差分 UX・時点解決・プロジェクト単位の選択復元・環境間移送のすべてで git に劣る。git はこの4要求の共通基盤 |
| import の 3-way merge | .md の意味的衝突は機械で解決不能。「選択置き換え + 双方の履歴保全」で実用十分 |
| `~/.local/share/agent-fleet` に repo を置く | home は clean-home の対象（homeKeep 追加が要る）。claude-config マウントは仕様として「何をしても消えない」保証が既にあり、メモリと同寿命で自然 |

## 重要な落とし穴（設計で必ず閉じる）

- **★1 巻き込み事故**: 対象は allowlist（`*/memory/**`）で構造的に限定。deny 方式にしない。
  transcript・credentials が repo に入る経路をコード上作らない（テストで担保）。
- **★2 restore と live 書き込みの競合**: pre-restore snapshot の自動取得＋実行中セッション
  警告。restore 自体も commit として積むので、どの順で何が起きたか常に履歴から復元可能。
- **★3 import は外部入力**: サイズ上限・traversal 防御・allowlist 外拒否・監査。bundle は
  `git bundle verify` 通過を必須にする。
- **★4 メモリは個人情報**: export は本人操作のみ・監査ログ必須。UI に「共有前に内容確認」
  の注意書き。運用ポリシー上メモリに秘密を書かない規約はあるが、防御はそれに依存しない。
- **★5 git 実行環境の独立**: repo 専用に `user.name=af-memory` / `user.email` を固定し、
  ユーザーの `~/.gitconfig`（signing 設定等）を継がない（`GIT_CONFIG_GLOBAL=/dev/null`）。
- **★6 slug の表示**: `-home-dev-repos-agent-fleet` をそのまま見せず、リポ名へ整形
  （markdown-ref-linkify で使った slug→表示名対応を流用）。
- **★7 ECS 実測**: agent 側完結なので動く設計だが、EFS 上の staging copy レイテンシは実測
  項目（対象が小さいため実害なしの見込み）。
- **★8 repo 肥大**: 追記型 md ＋テキスト差分なので伸びは遅い。snapshot N 回ごとに
  `git gc --auto`（`git_gc.go` の流儀に倣う）。

## フェーズ

| フェーズ | 内容 | 出口条件 |
|----------|------|----------|
| P1 | snapshot エンジン（roots 宣言・staging・bare repo・自動/手動契機）＋ REST（roots/snapshots/diff）＋ dual allowlist 登録 | 自動 snapshot が実データで積まれ、一覧/差分が API で取れる |
| P2 | restore（全体/プロジェクト・pre-restore・rsync scope 限定）＋ Console タブ（履歴/差分/復元） | UI から日時指定・プロジェクト単位の巻き戻しが往復できる |
| P3 | export/import（bundle/tar・preview・選択適用・検証・監査） | 2 環境間で bundle 持ち回りの移送が実際に通る |
| P4 | 拡張: codex/opencode の user-owned メモリ領域設計（entrypoint refresh との分離が前提）・CP internal git への push mirror（停止中閲覧・直接同期）・operator MCP ツール・scheduler 連携 | — |

## 未決事項（ユーザー判断待ち）

1. 自動 snapshot の既定: ON（idle+5 分 debounce）を推奨。OFF 既定にする理由は見当たらない。
2. export の暗号化: v1 は平文 DL（HTTPS 前提・本人操作のみ）。パスフレーズ付き暗号化
   （age 等）を要件にするか。
3. UI の置き場: 設定モーダル案を推奨（データ管理は設定の「ワークスペース」群と同質）。
   スケジュール同様の左レール常設が良ければ P2 で差し替え可能。
