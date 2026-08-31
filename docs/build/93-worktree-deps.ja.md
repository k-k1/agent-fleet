# 93. Worktree の依存とビルドキャッシュ（言語別・実測）

[English](93-worktree-deps.md) | 日本語

Audience: worktree の依存とビルドキャッシュを扱う人
Source of truth: コンテナ実測（本書は測った結果と手順）
Updated: 2026-08

Workspace のセッションは基本的に 1 セッション = 1 worktree で走るので、同じレポの worktree が
同時に 10 個並ぶことがある。このとき効いてくるのは**メモリではなくディスクと「共有されるか
どうか」**で、エコシステムごとに答えが違う。[workspace-notes.md](../../workspace/workspace-notes.md)
（＝各エージェントが常時読む運用ガイド）には要点だけ置き、根拠と言語別の詳細をここに置く。

前提となる永続モデルは 2 つだけ:

- `~/repos` は recreate で**消える**（worktree ごとの `node_modules` / `.venv` / `target` も一緒に）。
- `$HOME` のパッケージキャッシュは**残る**（`~/.npm` `~/go/pkg/mod` `~/.cache/go-build`
  `~/.cache/uv` `~/.gradle` `~/.m2` `~/.cargo`）。

つまり**再インストールは安い / 重複インストールは高い**。削るべきは「同じものの N 個目のコピー」で、
キャッシュではない。

## 93.1 早見表

| エコシステム | 既定で共有されるもの | worktree 毎に増えるもの | worktree での作法 |
|---|---|---|---|
| Node (npm) | `~/.npm`（tarball キャッシュのみ）| `node_modules` 約 300MB+ | lock 一致時のみ親クローンへ symlink。合わないなら `npm ci --prefer-offline` |
| Go | `~/go/pkg/mod` + `~/.cache/go-build` | 実質なし | 何もしない。効くのはメモリ側（`-p 2`）|
| Python | `~/.cache/uv`（`pip` は共有なし）| `.venv` 約数十〜数百MB | `uv venv` で WT 毎に作る。hardlink なので 2 個目は安い |
| JVM | `~/.gradle` `~/.m2` | `build/` `target/` | そのまま。daemon の停止だけ注意 |
| Rust | `~/.cargo/registry` | `target/` 数GB | `target/` は WT 毎のまま。共有 `CARGO_TARGET_DIR` は不可（後述）|

ディスクを見るのは `df -h ~`。掃除は `npm cache clean --force` / `uv cache prune` /
`go clean -cache -modcache`（⚠️ **全 worktree 共有**なので他セッションのビルド中は打たない）。

## 93.2 Node — 唯一「明示的に共有しないと損する」やつ

`node_modules` は WT 毎に丸ごと増える（このレポの `console/` で実測 349MB × WT 数）。
**親クローンの実体を symlink で共有できる**が、条件と事故がある。

共有してよい条件は **lockfile が親と同一**であること（依存が違えば当然壊れる）:

```bash
cd <repo-wt>/<pkg>
cmp -s package-lock.json ~/repos/<repo>/<pkg>/package-lock.json \
  && ln -s ~/repos/<repo>/<pkg>/node_modules node_modules
```

実測（npm 10.9.8 / node 22.23.2 / vite 7 系）:

- vitest は node / dom 両 project とも symlink 越しで通る。`npm run build`（vite build）も通る。
  bundler は既定で symlink を辿るので、解決に手当ては要らない。
- **`npm ci` を symlink のまま打つと、親クローンの `node_modules` が空になる。**
  リンクは実体ディレクトリへ置き換わり、共有していた他セッションのインストールが全滅する。
  install 系を打つ前に必ずリンクを外すこと。
- **`rm -rf node_modules/`（末尾スラッシュ）もリンクを貫通して中身を消す。**
  `rm -rf node_modules`（スラッシュ無し）ならリンクだけが消えて実体は無事。
- `npm install <pkg>` はリンクを実体ツリーへ黙って置き換える（親は無事）。壊れはしないが
  共有は解けていて 300MB 増えている、という状態になる。

lockfile が食い違うときは共有せず `npm ci --prefer-offline`（`~/.npm` が温まっているので速い）。
なお pnpm は入っていないが `corepack` はあるので、プロジェクトが pnpm を使うならそちらの
content-addressable store の方がこの問題自体を持たない。

## 93.3 Go — 何もしなくてよい / 効くのはメモリ

`~/go/pkg/mod`（モジュール）と `~/.cache/go-build`（ビルド）はどちらもグローバルなので、
worktree が増えても増えるのは実質ゼロ。代わりに詰まるのはメモリで、`go test ./...` は
パッケージ単位で並列にコンパイル・実行する。混雑時は `go test -p 2 ./...`（必要なら
`-count=1` でキャッシュを無効化）。

`GOTOOLCHAIN=auto` なので `go.mod` が新しい版をピンしていれば自動でダウンロードされる。
落ちる先は永続する `~/go` なので、recreate 後も 1 回で済む。

## 93.4 Python — 既定の `pip` が一番まずい

システム python は Debian の PEP 668（externally-managed）なので、素の `pip install` は
エラーにならず `--user` インストール（`~/.local`）に落ちる。これは永続するうえ**全プロジェクトで
共有**されるため、worktree 毎に依存が違う状況では静かに壊れる。

WT 毎に venv を切るのが正で、焼き込み済みの `uv` を使う:

```bash
uv venv && uv pip install -r requirements.txt
```

`~/.cache/uv` から hardlink するので、2 個目以降の worktree はディスクをほとんど食わない。
**`.venv` を worktree 間でコピー / symlink してはいけない**（絶対パスを埋め込んでいる）。

## 93.5 JVM — 共有は済んでいる。止め方だけ注意

`~/.gradle` と `~/.m2` は元から全 worktree 共有。ヒープと daemon の既定値は
[workspace-notes.md](../../workspace/workspace-notes.md) の "Build memory" が正。

1 点だけ worktree 特有の注意として、**`./gradlew --stop` はコンテナ全体の daemon を止める**
——他セッションがビルド中に打つとそれも巻き添えになる。自分が終わったときに打つのはよいが、
「重いから」と見境なく打たない。

## 93.6 Rust ほか（イメージに無い言語）

`rustup` はイメージに無いので自分で入れることになる。入れ先の `~/.cargo` は永続し、
registry キャッシュはそこで自動的に共有される。

`target/` は数GB になるが、**共有 `CARGO_TARGET_DIR` にはしない**こと。cargo は target ディレクトリに
ビルドロックを取るので、並列セッションが互いのビルド完了を待って直列化する（「Blocking waiting for
file lock on build directory」）。WT 毎に持たせ、終わったら `cargo clean` する方が総合的に速い。

同じ考え方が他の未導入言語にも当てはまる: **キャッシュ（`$HOME` 側）は共有する / 出力ディレクトリ
（worktree 側）は共有しない**。なお root / sudo が無いので `apt install` は使えない。ユーザー空間へ
入れるインストーラ（rustup、`uv tool install`、`npm i -g`）を選ぶこと。
