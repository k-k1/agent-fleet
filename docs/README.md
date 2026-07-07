# docs 索引

ドキュメントは性質（ジャンル）で分ける。**現状の正は [HANDOFF](HANDOFF.md)**。設計ドキュメントは
「不変の契約と設計意図」に純化し、運用の現在地は HANDOFF を指す。

| ジャンル | 置き場 | 役割 |
|----------|--------|------|
| 現状リファレンス | [HANDOFF.md](HANDOFF.md) | いま動いているもの・実行作法・落とし穴・テーマ別機能リファレンス（**まず読む**）|
| 時系列ログ | [CHANGELOG-handoff.md](CHANGELOG-handoff.md) | HANDOFF へ至った作業ログ（日付 + 1 行）|
| 前向きの計画 | [roadmap.md](roadmap.md) | フェーズ一覧・マイルストーン + Phase 3 詳細設計 |
| 不変の設計・契約 | [reference/](reference/) | コードに追従させる設計（下記）|
| 意思決定（なぜ）| [decisions/](decisions/) | 採否の記録・捨てた選択肢（追記型・不変）|
| 使い終わった計画 | [history/](history/) | 完了済みフェーズの実装プラン（記録）|

## reference/ — 不変の設計・契約

- [requirements.md](reference/requirements.md) — 用語、機能要件、非機能要件、確定/未決
- [architecture.md](reference/architecture.md) — 全体構成、コンポーネント、データモデル（テナント/identity）、主要フロー
- [api-agent.md](reference/api-agent.md) — API 表面の地図 + Workspace Agent 設計（契約はコードが正）
- [portability.md](reference/portability.md) — ポート&アダプタ（local/aws 両対応）
- [security.md](reference/security.md) — 脅威モデル、隔離境界、シークレット管理（封筒暗号）
- [preview.md](reference/preview.md) — コンテナ内サービスのプレビュー（/preview/{port} 経路）
- [aws.md](reference/aws.md) — AWS 構成、ネットワーク、コスト試算
- [internal-git-provider.md](reference/internal-git-provider.md) — テナント内部 git プロバイダ（bare + smart-HTTP）**P1実装済み**

## decisions/ — 意思決定の記録（ADR）

- [0001-self-host-vs-saas.md](decisions/0001-self-host-vs-saas.md) — SaaS 断念・各社セルフホスト採用
- [0002-claude-auth-onboarding.md](decisions/0002-claude-auth-onboarding.md) — auth と onboarding は別物
- [0003-ssh-to-connections.md](decisions/0003-ssh-to-connections.md) — SSH 鍵 → Connections
- [0004-vanilla-to-react.md](decisions/0004-vanilla-to-react.md) — Console は React + Vite
- [0005-envelope-custodian.md](decisions/0005-envelope-custodian.md) — 封筒暗号 + custodian 抽象
- [0006-mcp-unified.md](decisions/0006-mcp-unified.md) — MCP は管理面+作業面を一体・PAT 認証・E が主目的
- [0010-internal-git-provider.md](decisions/0010-internal-git-provider.md) — テナント内部 git プロバイダ（bare+http-backend を CP 自ホスト）**採用**

## history/ — 使い終わった実装プラン（P3-6 は ◐ 段1 完了・admin 残）

- [phase0-poc.md](history/phase0-poc.md) — Phase 0 PoC（`/login` 検証）
- [phase1-plan.md](history/phase1-plan.md) — Phase 1 MVP（§11.10 は今も有効な知見）
- [p3-1-metadatastore.md](history/p3-1-metadatastore.md) — MetadataStore（SQLite）
- [p3-2-identity-tenant.md](history/p3-2-identity-tenant.md) — identity↔tenant 多対多
- [p3-3-envelope-crypto.md](history/p3-3-envelope-crypto.md) — 封筒暗号 + custodian
- [p3-4-quota.md](history/p3-4-quota.md) — リソースバジェット/クォータ
- [p3-5-member-console.md](history/p3-5-member-console.md) — メンバー Console UX
- [console-redesign.md](history/console-redesign.md) — Console UI 刷新ブリーフ
- [p3-6-mcp.md](history/p3-6-mcp.md) — MCP（管理面+作業面を一体・E 駆動）実装プラン（◐ 段1=member/drive 完了・ライブ / admin ツール残）
