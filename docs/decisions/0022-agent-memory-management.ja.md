# 0022. エージェントメモリは agent 側 git bare repo で版管理し、bundle で環境間を移送する

[English](0022-agent-memory-management.md) | 日本語

- 状態: **採択**（2026-07-27。設計 2026-07-23、未決 4 点も既定値のまま決着）。
  実装計画は [docs/39](../log/39-agent-memory-management.md)。
- 関連: [0010（内部 git プロバイダ）](0010-internal-git-provider.ja.md)・
  `workspace/agent/cleanup_archive.go`（掃除の gz 安全網 — 専用の設計文書は無い）・`control-plane/runtime_docker.go` / `runtime_ecs.go` / `runtime_native.go`（claude-config マウント）・
  `workspace/agent/routes.go` + `control-plane/routes.go`（REST dual allowlist）。

## 背景

エージェントが書き溜める永続メモリのローカル実体は 2 つある: claude の auto-memory
（`projects/<slug>/memory/*.md`・既定 ON）と、codex の memories ワークスペース
（`~/.codex/memories/` の md 群＋派生状態 sqlite。feature flag は stable だが**既定 OFF**＝
本フリートでは未使用なだけで機構は存在する）。全 8 種別を調査した結果、他はローカル実体を
持たない: opencode はネイティブ実装なし（上流 issue open）、agy CLI は一級メモリ未確認、
copilot（Copilot Memory）と cursor（旧 Memories は削除済・現存は Automations 用）は
サーバー側管理、kiro は自動メモリなし（steering md＋派生状態の knowledge 索引のみ。
global steering `~/.kiro/steering/*.md` は将来ルート候補）。
これらメモリは消えない置き場にある一方、**履歴が無い**。誤学習・誤った書き換えを巻き戻す手段も、
日時を指定して当時の状態を見る手段も、別の agent-fleet 環境へ持ち出す手段も無い。
既存のバックアップは ops 層の DATA_DIR 丸ごと tar のみで、個人単位・プロジェクト単位の粒度を持たない。

## 決定

1. **履歴エンジンは git**。差分閲覧・日時→時点解決（`rev-list --before`）・パススコープ復元
   （`checkout <rev> -- <dir>`）・単一ファイル移送（`git bundle`）という要求 4 点がすべて
   標準機能で賄え、将来の CP mirror（0010 の bare+http-backend 流用）にも形式無変更で接続できる。
2. **実行主体は workspace-agent、repo は claude 専用マウント内の bare**
   （`/var/lib/af/claude/af-memory.git`）。全 runtime（Docker/ECS/native）で agent からの
   見え方が一様であり、ECS で CP がユーザーデータへ直接ファイルアクセスできない制約を踏まない。
   CP は REST proxy（dual allowlist）だけを担う。
3. **live ツリーに `.git` を置かず、allowlist copy → staging commit 方式**。対象は roots の
   allowlist（claude: `*/memory/**`、codex: `memories/**` から `.git` 等を除外）で構造的に
   限定し、transcript・credentials・派生状態 sqlite を巻き込む経路を作らない。
   エージェント自身にはリポジトリの存在を見せない。codex は統合パイプラインが自前の
   `~/.codex/memories/.git` を差分ベースラインに使うため、staging 方式は干渉回避の必須条件でもある。
4. **ロールバックは履歴の書き換えではなく「restore commit の積み増し」**。適用前に
   pre-restore snapshot を自動取得し、巻き戻しの巻き戻しを常に保証する。スコープは
   claude が全体/プロジェクト単位（メモリがプロジェクト自己完結なので索引も壊れない）、
   codex はワークスペース全体のみ（プロジェクト区分がファイル内エントリのため）。
   codex の派生状態（sqlite・自前 `.git`）は復元せず、diff 駆動の統合パイプラインに
   外部変更として再消化させる。
5. **環境間移送は git bundle（全履歴）を既定**、tar.gz（最新のみ）を併設。import は
   `refs/imports/<ts>` へ独立系譜として取り込み、適用はプロジェクト選択式の
   「置き換え＝新 commit」。.md の 3-way merge は意味的衝突を機械解決できないためやらない。
   **5-b（2026-08-25 追加）: 適用に「移設」を足す**。既定（置き換え）は最新ツリーしか使わず、
   bundle が運んできた過去は `refs/imports` に埋もれたまま刈られる——「前の環境の履歴ごと
   引っ越す」という一番自然な期待に応える手段が無かった。移設は main をその系譜へ**付け替える**
   （マージでも書き換えでもない）ので、決定 4 の「履歴を書き換えない」は保たれ、一覧・差分・
   巻き戻しの既存機能がそのまま相手の履歴に効く。入れ替えた元の main は `refs/premigrate/<ts>`
   へ退避して消さない。範囲は**全体固定**——一部だけ入れ替えると履歴（相手の系譜）と live
   （混在）が食い違い、以後の巻き戻しの意味を説明できなくなるため。
6. **メモリルートは宣言テーブル化し、v1 から claude と codex の 2 件を宣言**。codex は
   `~/.codex/memories/` の存在検知で自動有効になる。フリートとして memories 機能を ON に
   するかは別判断で、**P4 でその配線（`features.memories` の Console トグル＋コストを抑える
   `[memories]` seed）を入れた**が、既定は codex 自身と同じ OFF のまま——有効化は
   バックグラウンドのトークン消費を伴うので、利用者が選ぶ。kiro global steering は第 3 ルート候補（watch）、opencode/agy は
   上流実装待ちの watch、copilot/cursor はサーバー側管理でローカル実体が無いため対象外。
   上流で codex が Claude Code のメモリレイアウトを直接 import する機能を開発中である
   （`external_agent_memory_import`）こと、Gemini CLI v0.40 が完全ローカル md の階層メモリへ
   刷新したことは、「md ディレクトリを正とする汎用ルート設計」の妥当性を裏付ける。

## 結果（見込みと受け入れる制約）

- 得るもの: メモリの全変更履歴（いつ・どのプロジェクトが変わったか）、日時/履歴指定・
  プロジェクト単位のロールバック、bundle 1 ファイルでの環境間移送、監査ログ付きの操作面。
- 受け入れる制約: **WS 起動中のみ操作可能**（agent 側実行のため）。当初これは「P4 の
  CP mirror で解消」する前提だったが、P4 の調査で内部 git プロバイダの認可が
  **テナント単位で per-user ACL を持たない**ことが判明した（`git_http.go` の
  `authorizeGitRepo`：read は当該テナントのアクティブメンバー全員に開く）。個人メモリを
  そのまま mirror すると同僚から clone でき、本 ADR が「メモリは個人情報・export は本人
  操作のみ」として置いた前提と衝突するため、**mirror は実装せず前提条件つきの将来トラック
  へ送った**（内部 git に所有者限定 repo の概念を入れるか、台帳外の per-user ミラー領域と
  専用 API を新設するか。いずれも別 ADR 相当）。よって**停止中閲覧は当面できない**。
  import は merge しない（選択置き換えのみ）。codex のロールバック
  粒度は全体のみ。opencode/agy はネイティブメモリが無く対象外、copilot はサーバー側で対象外。
- リスクと手当: import は外部入力（サイズ上限・traversal 防御・bundle verify 必須）。
  export は個人情報を含みうる（本人操作限定・監査・UI 注意書き）——加えて **v1 は平文 DL を
  選ぶ代わりに、export 経路の secret スキャンを必須要件とする**（検出時は既定でブロックし、
  内容を確認した本人の明示 ack でだけ通す。bundle は全履歴を運ぶので走査も全履歴を見る）。
  repo 肥大は追記型 md の性質上緩慢で、定期 `git gc --auto` で足りる見込み。
