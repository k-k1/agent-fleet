# 0022. エージェントメモリは agent 側 git bare repo で版管理し、bundle で環境間を移送する

- 状態: **設計中**（2026-07-23）。実装計画は [docs/39](../39-agent-memory-management.md)。
- 関連: [0010（内部 git プロバイダ）](0010-internal-git-provider.md)・[docs/32 掃除機能](../32-agy-agent-kind.md)・
  `workspace/agent/cleanup_archive.go`・`control-plane/runtime_docker.go` / `runtime_ecs.go` / `runtime_native.go`（claude-config マウント）・
  `workspace/agent/routes.go` + `control-plane/routes.go`（REST dual allowlist）。

## 背景

エージェントが書き溜める永続メモリ（現状 claude の `projects/*/memory/` のみ。codex/opencode の
`AGENTS.md` は毎起動 image 内容へ refresh されるフリート管理ファイルであり対象外）は、
「何をしても消えない」専用マウントに置かれている一方、**履歴が無い**。誤学習・誤った書き換えを
巻き戻す手段も、日時を指定して当時の状態を見る手段も、別の agent-fleet 環境へ持ち出す手段も無い。
既存のバックアップは ops 層の DATA_DIR 丸ごと tar のみで、個人単位・プロジェクト単位の粒度を持たない。

## 決定

1. **履歴エンジンは git**。差分閲覧・日時→時点解決（`rev-list --before`）・パススコープ復元
   （`checkout <rev> -- <dir>`）・単一ファイル移送（`git bundle`）という要求 4 点がすべて
   標準機能で賄え、将来の CP mirror（0010 の bare+http-backend 流用）にも形式無変更で接続できる。
2. **実行主体は workspace-agent、repo は claude 専用マウント内の bare**
   （`/var/lib/af/claude/af-memory.git`）。全 runtime（Docker/ECS/native）で agent からの
   見え方が一様であり、ECS で CP がユーザーデータへ直接ファイルアクセスできない制約を踏まない。
   CP は REST proxy（dual allowlist）だけを担う。
3. **live ツリーに `.git` を置かず、allowlist copy → staging commit 方式**。対象は
   `*/memory/**` の allowlist で構造的に限定し、transcript・credentials を巻き込む経路を作らない。
   claude 自身にはリポジトリの存在を見せない。
4. **ロールバックは履歴の書き換えではなく「restore commit の積み増し」**。適用前に
   pre-restore snapshot を自動取得し、巻き戻しの巻き戻しを常に保証する。スコープは
   全体またはプロジェクト単位（claude メモリはプロジェクト自己完結なので索引も壊れない）。
5. **環境間移送は git bundle（全履歴）を既定**、tar.gz（最新のみ）を併設。import は
   `refs/imports/<ts>` へ独立系譜として取り込み、適用はプロジェクト選択式の
   「置き換え＝新 commit」。.md の 3-way merge は意味的衝突を機械解決できないためやらない。
6. **v1 対象は claude のみだが、メモリルートを宣言テーブル化**して kind 追加を 1 エントリで
   受けられる形にする（codex/opencode は user-owned メモリ領域の設計が前提として先に必要）。

## 結果（見込みと受け入れる制約）

- 得るもの: メモリの全変更履歴（いつ・どのプロジェクトが変わったか）、日時/履歴指定・
  プロジェクト単位のロールバック、bundle 1 ファイルでの環境間移送、監査ログ付きの操作面。
- 受け入れる制約: v1 は WS 起動中のみ操作可能（agent 側実行のため。停止中閲覧は P4 の
  CP mirror で解消）。import は merge しない（選択置き換えのみ）。codex/opencode/copilot の
  メモリは現状実体が無く対象外。
- リスクと手当: import は外部入力（サイズ上限・traversal 防御・bundle verify 必須）。
  export は個人情報を含みうる（本人操作限定・監査・UI 注意書き）。repo 肥大は追記型 md の
  性質上緩慢で、定期 `git gc --auto` で足りる見込み。
