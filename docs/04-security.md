# 04. セキュリティ / 脅威モデル

## 4.1 前提となるリスク

各 Workspace では Claude が**任意コードを実行**する（`--dangerously-skip-permissions` 運用を含む）。
よって「ユーザーのセッションが untrusted コードを動かす」ことを前提に境界を設計する。
守るべきは「他ユーザーのデータ」「Control Plane / AWS 基盤」「シークレット」。

## 4.2 信頼境界

```
信頼度 低 ┌──────────────────────────────┐
          │ Workspace コンテナ内部        │  ← 任意コード実行を許容する領域
          │ (claude + ユーザーの操作)     │
          └──────────────┬───────────────┘
                         │ ここが主要な隔離境界
信頼度 高 ┌──────────────▼───────────────┐
          │ Control Plane / AWS 基盤      │  ← Workspace から侵害されてはならない
          └──────────────────────────────┘
```

## 4.3 隔離コントロール

| 対象 | コントロール |
|------|-------------|
| ユーザー間ファイル | EFS アクセスポイントで root ディレクトリと uid/gid をユーザー毎に固定。他ユーザー領域へ到達不可。 |
| プロセス / メモリ | コンテナ境界（別タスク）。1 ユーザー 1 コンテナ。 |
| コンテナ → ホスト | 非特権コンテナ。`privileged` 禁止、capability 最小化、可能な範囲で read-only root fs。Fargate ならホスト共有なし。 |
| コンテナ → AWS 基盤 | Task Role は最小権限。Workspace タスクに ECS 制御・他ユーザー EFS・Secrets への権限を与えない。 |
| Control Plane 権限 | ECS/EFS 制御権限は Control Plane のロールにのみ付与。Workspace とは別ロール。 |
| ネットワーク | Workspace は外部公開しない。Egress は Bitbucket / Anthropic / claude.ai に限定（許可リスト型）。 |
| メタデータ盗用 | IMDS へのアクセスを Workspace から遮断（hop limit / 無効化）。Task Role 認証情報の濫用を防ぐ。 |

## 4.4 シークレット管理

| シークレット | 保管 | 露出範囲 |
|-------------|------|----------|
| Google OAuth クライアントシークレット | Secrets Manager | Control Plane / ALB のみ |
| Bitbucket SSH 秘密鍵 | 各 Workspace の `~/.ssh`（EFS, 600）| 当該ユーザーのみ |
| Claude 認証情報 `.credentials.json` | 各 Workspace の `~/.claude`（EFS）| 当該ユーザーのみ |
| Bitbucket API トークン（任意, B5）| Secrets Manager（ユーザー毎エントリ）| Control Plane が代理利用 |

- 秘密鍵・認証情報は EFS のユーザー領域に閉じ、他ユーザー・Control Plane から読めない設計にする。
- ログに秘密を出さない。ターミナルストリームの保存可否は方針決定する。

## 4.5 認証・認可

- **L1**: Google `hd` クレームで会社ドメイン強制。許可リスト（メール）併用。
- **セッション**: 短命トークン + リフレッシュ。ALB OIDC のセッションタイムアウト設定。
- **認可**: ユーザーは自分の Workspace/Repository/Session のみ操作可。管理者ロールは別。
- **L2**: Claude 認証は各ユーザー本人。Workspace を跨いだ認証情報の共有を禁止。

## 4.6 監査・可観測性

- AuditLog にユーザー操作（clone / セッション起動・停止 / 設定変更 / ログイン）を記録。
- CloudWatch でコンテナのリソース・異常終了を監視。
- 不正な egress 試行・権限エラーをアラート対象に。

## 4.7 リスクと残課題

1. **`--dangerously-skip-permissions` の既定運用** — 利便性と引き換えにコンテナ内の被害は前提化。
   コンテナ境界が唯一の砦になるため 4.3 を厳格に。
2. **EFS のクロスユーザー設定ミス** — アクセスポイントの uid/gid と root を取り違えると漏洩。
   IaC でテンプレート化し手作業を排除。
3. **`/login` 認証情報の持続** — 長期保持する認証情報の失効・ローテーション方針が要る。
4. **サプライチェーン** — Workspace イメージに入れるツールの出所管理、定期更新。
