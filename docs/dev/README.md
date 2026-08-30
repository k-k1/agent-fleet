# dev/ — 開発者向けドキュメント（索引と執筆規範）

agent-fleet の**開発者向け**体系ドキュメント。読者は「新規参画する開発者」と「将来の自分 / AI セッション」。
**仕様の正は dev/（とコード）、稼働状態の正は [HANDOFF](../HANDOFF.md)**。利用者向けの操作説明は
[guide/](../use/README.ja.md)（ペルソナ別分冊）にあり、dev/ には書かない。

## 読む順

- **新規参画**: [01 アーキテクチャ](01-architecture.md) → [05 API 契約](05-api-contracts.md) →
  [06 データモデル](06-data-model.md) → [10 開発作法](10-development.md)
- **特定コンポーネント担当**: 01 → 該当各論（[02 Console](02-console.md) / [03 Control Plane](03-control-plane.md) /
  [04 Workspace Agent](04-workspace-agent.md)）
- **セキュリティレビュー**: [07 セキュリティ](07-security.md) → [08 外部連携](08-integrations.md) → 01
- **デプロイ・運用設計**: [09 デプロイ](09-deploy.md) →（実手順は `deploy/*/README.md`）

| ファイル | 内容 |
|----------|------|
| [01-architecture.md](01-architecture.md) | 提供モデル・用語・3プロセス構成・認証2層・主要フロー・アダプタ一覧 |
| [02-console.md](02-console.md) | Console（React+Vite+zustand）の設計 |
| [03-control-plane.md](03-control-plane.md) | Control Plane の責務地図・リクエストの一生・バックグラウンドジョブ |
| [04-workspace-agent.md](04-workspace-agent.md) | Agent のセッションモデル・エージェント kind 統合パターン・Workspace イメージ |
| [05-api-contracts.md](05-api-contracts.md) | 2境界の API 地図・中継4経路・横断規約・監査点 |
| [06-data-model.md](06-data-model.md) | エンティティ・マイグレーション作法 |
| [07-security.md](07-security.md) | 脅威モデル・隔離・L1/L2 認証・封筒暗号・egress 統制 |
| [08-integrations.md](08-integrations.md) | 外部システム連携（Google/GitHub/Bitbucket/Claude/codex/opencode/MCP/AWS）|
| [09-deploy.md](09-deploy.md) | デプロイ3形態・ポート&アダプタ・env 索引 |
| [10-development.md](10-development.md) | ビルド反映早見表・テスト・規約・ドキュメント更新責務 |
| [90-code-map.md](90-code-map.md) | コードマップ（**ファイルパス列挙を許す唯一のファイル**・陳腐化前提）|
| [91-internal-git.md](91-internal-git.md) | 内部 git プロバイダ（bare + smart-HTTP + LFS）|
| [92-tui-modal-driving.md](92-tui-modal-driving.md) | TUI モーダル駆動（AUQ ほか）の実測検証プレイブックと挙動記録 |
| [93-worktree-dependencies.md](93-worktree-dependencies.md) | worktree の依存・ビルドキャッシュの言語別作法（実測。運用ガイドの根拠）|

## 執筆規範（dev/ 全ファイル共通）

1. **ワイヤ契約と責務で書く。** 「どのプロセスのどの責務か・外部から見た契約は何か」を書き、
   「どのファイルの何行目か」は書かない。エンドポイントパス・env 変数名・DB カラム名・
   tmux/コンテナ命名規則は内部リファクタ（docs/23 Go リファクタ＝別ブランチ進行中・ワイヤ完全互換がハード制約）でも
   不変なので自由に書いてよい。**行番号参照は全面禁止**。実装位置を示したいときは
   [90-code-map](90-code-map.md) への参照か、grep 可能なアンカー（エンドポイントパス・env 名・エラーコード文字列）で示す。
2. **単記の原則。** 機能仕様の置き場は dev/ ただ一つ。HANDOFF は「稼働状態・ホスト固有作法・落とし穴・
   進捗現在地」＋ dev/ へのリンクのみ（かつての §6.10 の肥大を規範で防ぐ）。利用者向け操作説明は guide/ へ。
   運用 runbook（コマンド手順）は `deploy/*/README.md` が正で、dev/ は複製せずリンクする。
3. **現状 vs 計画の凡例**（本文の大半にバッジは付けない。境界事例にだけ付ける）:
   - 無印 / ✅ — 実装済み・運用中
   - 🚧 — 実装済み・未実運用（または部分実装）。例: AWS(ECS) アダプタ、egress enforce
   - 📋 — 設計・決定のみでコード無し。例: KMS/Vault custodian
   - 📋 の内容は decisions/ か roadmap へのリンク付きで **3 行以内**。設計本文は decisions/ 側に置く
   （dev/ を「動いているものの説明」に保つ）。
4. **各ファイル冒頭に共通ヘッダ**を置く: `正: コード（本書は地図と設計意図）/ 主な更新トリガ: <例> / 最終確認: YYYY-MM`。

## 更新責務の早見表（何を変えたらどれを更新するか）

| コード変更 | 更新する dev/ ファイル |
|-----------|----------------------|
| API グループの追加・パス変更 | 05（地図の行）+ 該当各論（02/03/04）|
| migration 追加 | 06 |
| 認証・暗号・隔離・監査に触る変更 | 07 |
| 外部プロバイダ（OAuth・CLI エージェント）の追加/変更 | 08（+ 04 の kind パターン）|
| デプロイ形態・env 変数・アダプタの追加 | 09 |
| ビルド/反映手順・テスト方式の変更 | 10 |
| **ファイル/パッケージの移動（リファクタ）** | 90 のみ（他は影響なしが正常）|
| 機能の追加そのもの | 該当各論 + 必要なら guide/ 側（利用者に見える場合）|
