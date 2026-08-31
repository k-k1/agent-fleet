# 22. エージェント CLI の自己更新（opt-in ＋ 運用者ゲート）

> 🗄 **実装記録**。claude/opencode/codex を、メンバーの opt-in ＋ 運用者の許可で、**コンテナ起動時に in-place で最新へ更新**する。

## 22.1 背景と狙い

3 CLI は Workspace イメージに焼かれ（`/usr/local`、root 所有・版固定）、更新はイメージ再ビルドのみ。
とくに **claude は更新が速く、毎回イメージを出し続けるのは非現実的**（イメージ再ビルド→再配布→コンテナ再作成という反映フローの負荷）。
そこで「使う人が自分で最新に保つ」を opt-in で可能にする。ただし版の一貫性は運用者の裁量に置く。

関連: claude 固有の background auto-updater は root 所有ゆえ書けず "auto-update failed" を出していたので、
別途 `DISABLE_AUTOUPDATER=1` で停止済み（`cf44464a` fix(workspace): claude の background auto-updater を無効化）。本機能の更新は**起動時のみ**で mid-session churn を作らない。

## 22.2 採用した仕組み（/usr/local を起動時に in-place 差し替え）

native `~/.local` install の PATH 芸ではなく、**baked の `/usr/local` を起動時に npm で最新へ差し替える**。

- **成立条件**: `/usr/local/lib/node_modules` と `/usr/local/bin` を**ビルド時に dev(uid 1000) 所有へ chown**
  （Dockerfile）。→ entrypoint（dev 実行）が root 無しで `npm install -g …@latest` を書ける。単一ユーザーコンテナ
  ゆえ dev が自分の global を持つのは権限昇格にならない。**検証済**: 新イメージで node_modules/bin とも書込可。
- **戻せる**: agent-fleet の Stop→Start は `docker rm -f` + `docker run`（毎回 recreate）ゆえ /usr/local は必ず
  イメージ版へ戻る。**OFF にして再起動 = 焼いた版に戻る**が無料で成立。
- **背景 updater は焼き無効のまま**（`DISABLE_AUTOUPDATER=1`）。更新点は entrypoint に一本化。

## 22.3 二層の制御

| 層 | 置き場所 | 誰が | 既定 |
|----|----------|------|------|
| **メンバー opt-in** | `~/.config/agent-fleet/toolchains.json` の `agentUpdate`（Agent `PUT /env/toolchains`、home 永続）| 各メンバー（Console 設定→環境）| OFF |
| **運用者ゲート** | `tenant.limits.allow_agent_self_update`（super_admin が limits で編集、migration 不要）| その社の情シス（super_admin）| OFF（版固定） |

**強制は env ゲート**: CP が tenant policy から `AF_AGENT_SELF_UPDATE_ALLOWED=1` を**コンテナへ注入**し、
entrypoint は `ゲート env=1 かつ toolchains.agentUpdate=true` の時だけ npm 更新。**shell で toolchains.json を
書き換えても env が無ければ無効**＝運用者の意思が最終。Console のメンバートグルは gate 許可時のみ表示（見せ方）。

## 22.4 実装（コア無改修・港越し）

- **Dockerfile**: `chown -R 1000:1000 /usr/local/lib/node_modules /usr/local/bin`（＋既存の `DISABLE_AUTOUPDATER=1`）。
- **entrypoint.sh**: gate 付き更新ブロック（`npm install -g @anthropic-ai/claude-code@latest opencode-ai@latest @openai/codex@latest`）。
- **Agent** `env_toolchains.go`: `toolchains.AgentUpdate` フィールド＋GET 応答 `agentUpdate`（PUT は既存の struct 往復で永続）。
- **CP**: `tenantLimits.AllowAgentSelfUpdate`。`RuntimeFactory.New` に per-workspace `extraEnv []string` を追加（P3-7 の港を拡張）、
  `manager.workspaceExtraEnv` が tenant policy → gate env を注入、`evictTenantCache` で limits 変更時に該当テナントの
  runtime キャッシュを破棄（policy 変更が次の起動へ届く）。`tenants.go`: admin limits PUT/list に gate、`/api/tenants`（メンバー可視）に gate。
- **Console**: `EnvTab` にメンバートグル（gate 許可時のみ、`/api/tenants` の `allow_agent_self_update` で判定）、
  `AdminTab` の tenant limits に運用者ゲートのチェックボックス。

## 22.5 検証

- 新イメージで **dev が /usr/local(node_modules/bin) を書ける**こと、gate の node パース、`DISABLE_AUTOUPDATER=1` を確認。
- env ゲート未設定の起動で更新ブロックが**発火しない**（＝ゲート優先）ことを確認。
- CP `go build/vet/test`（factory 拡張の回帰含む）、Agent build/vet、Console vite build、entrypoint `bash -n` 通過。
- 実 npm 更新（版 bump）込みの E2E は反映後に、運用者が gate 許可＋メンバーが opt-in ＋ Stop→Start で目視。

## 22.6 触れたファイル

`workspace/Dockerfile`・`workspace/entrypoint.sh`・`workspace/agent/env_toolchains.go`／
`control-plane/manager.go`・`runtime.go`・`runtime_ecs.go`・`runtime_test.go`・`tenants.go`／
`console/src/settings/EnvTab.jsx`・`AdminTab.jsx`・`styles.css`。
