# 10. Phase 0 PoC 手順書（ローカル Docker / `/login` 検証）

ロードマップ [Phase 0](../roadmap.md) の実行手順。
最小スキャフォールド（`phase0/`）で検証した（検証完了後に scaffold は削除済み）。本書は「何を確かめ、何を記録し、どこで合格とするか」を記録として残す。

> **状態: 検証完了（2026-06-26 / claude v2.1.193）。** 最大リスク（ヘッドレスでの `/login`）は解消。
> サブスク認証は `redirect_uri=platform.claude.com/oauth/code/callback` で **localhost コールバック非依存**、
> ヘッドレス/リモートで無条件に成立する（H1〜H3 達成）。`.credentials.json` は永続ホームで再起動後も有効。
> 実機の確定事項は [02 §2.6 検証結果](../dev/08-integrations.md#85-claude-認証オンボーディングl2-の本丸) と
> [11 §11.10](../log/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。以下は当初の検証計画（記録として残す）。

## 10.1 目的と最大リスク

設計最大の未知は **「ヘッドレスコンテナで `claude /login`（claude.ai の OAuth）が完了できるか」**。
ここが通らないと「各ユーザーが自分のアカウントで login」という前提（[01](../dev/01-architecture.md)）が崩れる。
Phase 0 はこの 1 点を最小コストで潰すことに集中する。コードは書かず、手動操作で手触りを得る。

## 10.2 検証する仮説（チェックリスト）

- [ ] H1: コンテナ内で `claude /login` の認証 URL を取得し、**ホスト側ブラウザ**で認可を完了できる。
- [ ] H2: コールバック方式（Approach A: localhost コールバック）と、貼り戻し方式（Approach B: code paste）の
      どちらが成立するかを判別できる。
- [ ] H3: 認証情報が `~/.claude/.credentials.json`（= bind mount したホーム）に永続し、
      **コンテナ再作成後も再ログイン不要**で claude が動く。
- [ ] H4: `~/.claude/settings.json` をマウントで持ち込め、`remoteControlAtStartup` 等が反映される。
- [ ] H5: コンテナ内で生成した SSH 公開鍵を Bitbucket に手動登録し、`git clone` できる。
- [ ] H6: tmux 上で claude を `--session-id` 起動 → 切断 → `--resume` で会話を復帰できる。
- [ ] H7: 方式 A（対話ログイン）で **remote-control が機能**する。対して `setup-token`（`CLAUDE_CODE_OAUTH_TOKEN`）
      では remote-control が張れないこと（要件 E2 と衝突）を確認する。

## 10.3 前提

- Docker / Docker Compose が入った Linux マシン（同一マシンでブラウザが使えると Approach A が楽）。
- ホストの uid が 1000（bind mount のため。`id -u` で確認。異なる場合は 10.8 参照）。
- claude.ai アカウント（検証者本人）。Bitbucket アカウントと対象リポジトリ。

## 10.4 構成（`phase0/`・検証完了後に削除済み）

| ファイル | 役割 |
|----------|------|
| `Dockerfile` | Workspace 最小イメージ（node 22 + claude code + git/tmux/ssh）。ツールは `/usr/local`、ホームは空けておく。|
| `docker-compose.yml` | `host` ネットワーク + `./data/home` をホームに bind mount。|
| `data/home/` | 永続ホーム（`.claude` / `.ssh` / `repos` が貯まる。git 無視）。|

> 設計原則の確認: CLI を `/usr/local/bin` に置くことで、ホームを丸ごと永続化しても CLI が
> shadow されない。これは本番（EFS にホーム永続）と同じ考え方（[03 §3.2（現 dev/09 §9.5 aws ターゲット）](../dev/09-deploy.md)）。

## 10.5 手順

### 1. ビルドと起動
```bash
cd phase0
mkdir -p data/home          # uid 1000 で作成（root 所有だと書けない）
docker compose build
docker compose up -d
```

### 2. ターミナル接続 + tmux
```bash
docker compose exec workspace bash
claude --version            # 動作確認
tmux new -s main            # 本番の「tmux にアタッチ」を手で再現
```

### 3. `claude /login`（H1/H2）
公式挙動（[02 §2.6](../dev/08-integrations.md#85-claude-認証オンボーディングl2-の本丸)）では、ヘッドレス環境は**自動でコード方式（方式 A）**に切替わる。
```bash
claude                      # 初回起動。未ログインなら /login を案内、または明示的に /login
```
- **方式 A（本命・コード貼り戻し）**: 表示 URL を `c` でコピー → 自分のブラウザで開く → 認可 →
  画面の**ログインコード**をターミナルに貼り戻す。貼付け不可なら `echo "<code>" | claude auth login`。
- **（参考）localhost コールバック**: host ネットワークかつ同一マシンなら自動コールバックが返ることもある。
  返らなくても方式 A で完了できる。
- **記録**: 自動コールバック/コード貼り戻しのどちらになったか、URL の形、コード桁数、詰まった点。

### 4. 認証の永続確認（H3）
```bash
ls -l ~/.claude/.credentials.json      # 生成を確認
exit; exit                              # コンテナから出る
docker compose down                     # コンテナ破棄（data/ は残る）
docker compose up -d
docker compose exec workspace bash
claude                                  # 再ログイン不要で起動できれば合格
```

### 5. settings.json / remote-control（H4）
```bash
# ホスト側で雛形を置く（例）
cat > data/home/.claude/settings.json <<'JSON'
{ "remoteControlAtStartup": true, "skipDangerousModePermissionPrompt": true, "theme": "dark" }
JSON
# コンテナ内で claude 起動時に反映されるか確認
```

### 6. SSH 鍵 → Bitbucket 登録 → clone（H5）
```bash
# コンテナ内
ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519
cat ~/.ssh/id_ed25519.pub                       # → Bitbucket の SSH keys に手動登録
ssh-keyscan bitbucket.org >> ~/.ssh/known_hosts # ホスト鍵投入（本番は事前配布）
ssh -T git@bitbucket.org                        # 疎通テスト
mkdir -p ~/repos && cd ~/repos
git clone git@bitbucket.org:<org>/<repo>.git
git -C <repo> status
```

### 7. 決定論的 session-id の手触り（H6）
```bash
SID=$(uuidgen --sha1 --namespace @url --name "$HOME/repos/<repo>|slot01")
cd ~/repos/<repo>
claude --session-id "$SID"           # 新規
# 会話 → Ctrl+b d で tmux デタッチ、または exit
claude --resume "$SID"               # 復帰
```
- 本番では Agent がこの判定（jsonl 有無で `--resume`/`--session-id`）を担う（[07 §7.4（現 dev/04 §4.2 セッションモデル）](../dev/04-workspace-agent.md#42-セッションモデル)）。

## 10.6 記録する観察項目

PoC の成果は「動いた/動かない」ではなく**手順の確定**。次を残す。

- `/login` の成立方式（A/B）、提示 URL・コールバックポートの実体、所要操作。
- ヘッドレス特有の詰まり（ブラウザ未起動、ポート到達性、コード貼り戻しの UX）。
- `.credentials.json` の中身の性質（期限・リフレッシュ有無 → 状態判定 [06 §6.7](../dev/05-api-contracts.md) の設計に反映）。
- bind mount のパーミッション問題の有無。
- Agent に必要な操作の最終リスト（git/session/settings/sshkey/login）。

## 10.7 完了条件（Exit criteria）

- H1〜H3 が満たされ、`/login` 手順が [02 §2.6](../dev/08-integrations.md#85-claude-認証オンボーディングl2-の本丸) に文書反映される。
- H4〜H6 を確認し、Phase 1（Agent 化）に必要な操作一覧が確定する。
- これらで [01 未決 #3（旧 requirements、現 roadmap に統合）](../roadmap.md)（`/login` 対話フロー）をクローズ。

## 10.8 既知のリスクと代替

- **uid 不一致**: ホストが uid 1000 でない場合、`Dockerfile` に `ARG UID/GID` を足してビルド引数で合わせるか、
  ホーム配下を `chown` する。
- **コード貼り戻しが基本**: コールバックが返らないのは想定内。公式にヘッドレスは方式 A（コード方式）に
  自動切替わる（[02 §2.6](../dev/08-integrations.md#85-claude-認証オンボーディングl2-の本丸)）。本番もこれを主経路にする。
- **remote-control 要件との両立**: `setup-token` 方式は remote-control 不可。E2 を満たすには方式 A が必須。
  H7 で実機確認する。
- **隔離は最小**: Phase 0 は検証優先で egress 制限等を省く。隔離強化は Phase 2 以降（[04](../dev/07-security.md) / [09 §9.7（現 dev/09 §9.6 パリティと相違点）](../dev/09-deploy.md#96-パリティと相違点)）。
- **claude のインストール方式**: 本 PoC は npm グローバル。ホストは native installer を使用。
  どちらでも可だが、ホーム永続と干渉しない配置（ホーム外）を必須要件とする。
