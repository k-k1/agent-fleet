# Phase 0 PoC — クイックスタート

ヘッドレスコンテナで `claude /login` が通るか等を実機検証する最小構成。
詳細な観察項目と完了条件は [../docs/10-phase0-poc.md](../docs/10-phase0-poc.md)。

```bash
cd phase0
mkdir -p data/home            # uid 1000 で作る（root 所有だとコンテナが書けない）
docker compose build
docker compose up -d

# コンテナに入って tmux 上で claude を起動
docker compose exec workspace bash
tmux new -s main
claude            # 初回 → /login（Approach A: host network でブラウザ・コールバック）

# 後片付け（ホーム data/ は残る）
docker compose down
```

- 永続確認: `docker compose down && up -d` 後も `data/home/.claude/.credentials.json` が残り、再ログイン不要なら合格。
- うまくコールバックが通らない場合は manual code paste（Approach B）を試す。詳細は手順書参照。
