# 0022. エージェントメモリは agent 側 git bare repo で版管理し、bundle で環境間を移送する

- 状態: **設計中**（2026-07-23）。実装計画は [docs/39](../39-agent-memory-management.md)。
- 関連: [0010（内部 git プロバイダ）](0010-internal-git-provider.md)・[docs/32 掃除機能](../32-agy-agent-kind.md)・
  `workspace/agent/cleanup_archive.go`・`control-plane/runtime_docker.go` / `runtime_ecs.go` / `runtime_native.go`（claude-config マウント）・
  `workspace/agent/routes.go` + `control-plane/routes.go`（REST dual allowlist）。

## 背景

エージェントが書き溜める永続メモリのローカル実体は 2 つある: claude の auto-memory
（`projects/<slug>/memory/*.md`・既定 ON）と、codex の memories ワークスペース
（`~/.codex/memories/` の md 群＋派生状態 sqlite。feature flag は stable だが**既定 OFF**＝
本フリートでは未使用なだけで機構は存在する）。opencode はネイティブ実装なし（上流 issue open）、
agy CLI は一級メモリ未確認、copilot のメモリは GitHub サーバー側でローカル実体が無い。
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
6. **メモリルートは宣言テーブル化し、v1 から claude と codex の 2 件を宣言**。codex は
   `~/.codex/memories/` の存在検知で自動有効になる（フリートとして memories 機能を ON に
   するかは別判断・別配線）。opencode/agy は上流実装待ちの watch、copilot はローカル実体が
   無いため対象外。上流で codex が Claude Code のメモリレイアウトを直接 import する機能を
   開発中である（`external_agent_memory_import`）ことは、この汎用ルート設計の妥当性を裏付ける。

## 結果（見込みと受け入れる制約）

- 得るもの: メモリの全変更履歴（いつ・どのプロジェクトが変わったか）、日時/履歴指定・
  プロジェクト単位のロールバック、bundle 1 ファイルでの環境間移送、監査ログ付きの操作面。
- 受け入れる制約: v1 は WS 起動中のみ操作可能（agent 側実行のため。停止中閲覧は P4 の
  CP mirror で解消）。import は merge しない（選択置き換えのみ）。codex のロールバック
  粒度は全体のみ。opencode/agy はネイティブメモリが無く対象外、copilot はサーバー側で対象外。
- リスクと手当: import は外部入力（サイズ上限・traversal 防御・bundle verify 必須）。
  export は個人情報を含みうる（本人操作限定・監査・UI 注意書き）。repo 肥大は追記型 md の
  性質上緩慢で、定期 `git gc --auto` で足りる見込み。
