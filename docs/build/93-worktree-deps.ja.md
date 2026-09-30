---
audience: "worktree の依存とビルドキャッシュを扱う人"
source_of_truth: "各節が名指すコード。コンテナ内の実測は、いつ・どこで測ったかを添えて記す"
updated: "2026-09"
---

# 93. Worktree の依存とビルドキャッシュ（言語別・実測）

[English](93-worktree-deps.md) | 日本語

Workspace のセッションは基本的に 1 セッション = 1 worktree で走るので、同じレポの worktree が
同時にいくつも並ぶ。このとき効いてくるのは **メモリではなくディスクと、共有されるかどうか** で、
エコシステムごとに答えが違う。本書が持つのは仕組みと根拠で、作法そのものは読み手ごとに別の場所にある:

- **すべての Workspace のすべてのエージェント**は運用ガイド
  （[workspace-notes.md](../../workspace/workspace-notes.md)）を読み込んだ状態で始まり、状況に応じた
  topic file を必要なときに読む:
  [notes/worktrees.md](../../workspace/notes/worktrees.md)（"Dependencies in a worktree"）と
  [notes/environment.md](../../workspace/notes/environment.md)（`/scratch`・ディスク・キャッシュの掃除）。
  これらはイメージに焼かれて届くが `docs/` は届かないので、ここへはリンクできず、短い版を自前で持つ。
- **このレポの `console/`** には専用の手順が [AGENTS.md](../../AGENTS.md)
  （"`console/node_modules` in a worktree"）にある。
- **`ecs` ランタイムがビルド生成物をタスクローカルのディスクへ逃がす理由**と、その裏付けの EFS 実測は
  [ADR 0044](../decisions/0044-workspace-sizing.ja.md) の決定 3 と決定 5 にある。

前提となる永続モデルは 2 つ:

- **recreate は作業コピー（`~/repos`）を消す**——worktree ごとの依存ツリーも一緒に。
- **ホームのパッケージキャッシュは recreate を越えて残る**（`~/.npm` `~/go/pkg/mod`
  `~/.cache/go-build` `~/.cache/uv` `~/.gradle` `~/.m2` `~/.cargo`）。例外が 1 つあり 93.1 に書く:
  `ecs` ランタイムでは、その一部が停止のたびに空になるディスクに載る。

つまり**再インストールは安い / 重複インストールは高い**。削るべきは「同じものの N 個目のコピー」で、
キャッシュではない。

## 93.1 新しい作業コピーに Workspace 自身が行うこと

worktree 間で依存ツリーを勝手にリンクする仕組みは無い。`node_modules` の共有は手作業（93.3）。
自動で起きるのは次のもの:

| 仕組み | どこで動くか | 何をするか | コード |
|---|---|---|---|
| サブモジュールの種まき | 全ランタイム・`git worktree add` のとき | best effort: 親クローンが自分のストアに持っていて、worktree 側のパスがまだ空のサブモジュールを、そのストアから clone する（hardlink）。飛ばしたもの・失敗したものは後に続く通常の update に任せる。実測は [build/04](04-agent.ja.md) §4.6 | `finishNewWorktree` → `seedSubmodulesFromParent`（`workspace/agent/internal/gitx/git.go`・`git_submodule_seed.go`）|
| ビルド生成物の退避 | `$AF_WS_SCRATCH` があるときだけ・新しい clone か新しい worktree のとき（既存の worktree への再起動では動かない）| `af-scratch --auto` を叩き、目印ファイルの隣のビルド出力ディレクトリを、なるべく空のうちに `/scratch` への symlink にしようとする。best effort（エラーはログに残して握りつぶす）| `scratchAutoRelocate`（`workspace/agent/scratch.go`）・`workspace/af-scratch.sh` |
| ホームのキャッシュの退避 | `$AF_WS_SCRATCH` があり、**かつ** `/scratch` が `AF_WS_SCRATCH_MIN_GB`（30 GiB）以上のとき・コンテナ起動時 | `~/.cache/go-build` `~/.cache/uv` `~/go/pkg/mod`（`AF_WS_SCRATCH_DIRS` の既定値）を `/scratch/home` への symlink に置き換えようとする。`/scratch/home` に書けなければ丸ごと、移動に失敗すればそのディレクトリだけ飛ばす | `workspace/entrypoint.sh` |

**`$AF_WS_SCRATCH` を受け取るのは誰か。** control plane がこれを設定するのは `ecs` アダプタだけ
（`control-plane/internal/runtime/runtime_ecs.go` の `registerTaskDef`）。`ecs-ec2` アダプタは意図して
入れない——そこではホームが既にローカルの EBS にある（`runtime_ecs_ec2.go` の、ADR 0045 決定 10-3 を
引く注記）。docker と native のアダプタも設定しない。workspace イメージは `/scratch` ディレクトリを作る
（`workspace/Dockerfile`）ので docker や `ecs-ec2` にもあり、native の traditional モードはイメージを
使わずホストでビルドした agent を動かす。つまりディレクトリがあることは何の目印にもならない: 2 つの
退避の行は変数を見る（サブモジュールの種まきは変数に依らない）。変数がある環境では `/scratch` は
タスクローカルで、タスク停止で空になる。

**`af-scratch --auto` が見る目印**（深さ 3 まで・`AF_WS_SCRATCH_AUTO_DEPTH`）: `package.json` →
`node_modules`、`Cargo.toml` か `pom.xml` → `target`、`pyproject.toml` → `.venv`、`build.gradle` /
`build.gradle.kts` → `build`。正の一覧は `af-scratch.sh` の `case` にある。既存の symlink には触らず、
既存のディレクトリは `git check-ignore` が無視対象と言うときだけ移す。`AF_WS_SCRATCH_AUTO=0` でこの
処理ごと切れる。

**作業ディスクの大きさ**が、ホームのキャッシュを移すかどうかを決める。`ecs` アダプタの配備既定値は
50 GiB（`ecsDefaultWorkDiskGiB`・上書きは `AF_ECS_WS_DISK_GB`）で、30 GiB の閾値を上回る。この既定値
より前に作ったスタックは作成時の値のまま（ADR 0044 決定 5）。`AF_ECS_WS_DISK_GB=0` は Fargate の無料枠
20 GiB に戻す——閾値を下回るので、キャッシュは EFS に残り、ビルド出力だけが移る。

## 93.2 早見表

`/scratch` の列は `ecs` で 93.1 が*試みる*ことを書いたもの: ビルド出力は見つかった目印ファイルの隣だけ
（`AF_WS_SCRATCH_AUTO=0` なら無し）、ホームのキャッシュはディスクが 30 GiB 以上のときだけ。どの
プロジェクトでもそうなる保証ではない。

| エコシステム | 既定で共有されるもの | worktree 毎に増えるもの | `ecs` で `/scratch` に載るもの | 作法 |
|---|---|---|---|---|
| Node (npm) | `~/.npm`（tarball キャッシュのみ）| `node_modules` 数百MB（93.3）| `node_modules` | lock 一致時**だけ**親クローンへ symlink。合わないなら温まったキャッシュから `npm ci --prefer-offline` |
| Go | `~/go/pkg/mod` と `~/.cache/go-build` | 実質なし | ディスクが 30 GiB 以上なら両キャッシュ | 何もしない。**効くのはメモリ側**——テストの並列度を絞る |
| Python | `~/.cache/uv`、`~/.cache/pip`（ダウンロードのみ——素の `pip install` の入れ先は共有の `~/.local`、93.5）| `.venv` 数十〜数百MB（見積もり）| ディスクが 30 GiB 以上なら `~/.cache/uv`。`pyproject.toml` があれば `.venv` | `uv` で WT 毎に `.venv` を作る |
| JVM | `~/.gradle` と `~/.m2` | `build/` か `target/` | `build/` か `target/` | そのまま。daemon の止め方だけ注意 |
| Rust | `~/.cargo`（registry）| `target/` 数GB（見積もり）| `target/` | WT 毎のまま。共有 target ディレクトリは並列ビルドを直列化する（93.7）|

ディスクを見るのは `df -h ~`（`$AF_WS_SCRATCH` がある環境では `df -h /scratch` も）。キャッシュの掃除
——`npm cache clean --force` / `uv cache prune` / `go clean -cache`、または Settings > Machine >
Tool caches——は notes/environment.md の "Disk" が扱う。これらのキャッシュは**全 worktree 共有**
なので、他セッションのビルド中は消さない。

## 93.3 Node — 唯一「明示的に共有しないと損する」やつ

`node_modules` は WT 毎に丸ごと増える。**実測:** このレポの親クローンの `console/node_modules` は
2026-09-29、`$AF_WS_SCRATCH` の無い Workspace で、ディスク上 559MB（`du -sh`・見かけのサイズ 494MB）、
20,719 ファイルだった。2026-08 の実測は 349MB。依存とともに育つので、どちらの数字もその時点の値として読む。

**親クローンの実体を symlink で共有できる**。条件は 1 つ、**lockfile が親と同一**であること
（2 つを `cmp -s` で比べ、`node_modules` を親のものへリンクする）。コマンドはここには繰り返さない:
このレポのコマンドは AGENTS.md に、すべてのエージェントが受け取る一般則とその危険は
notes/worktrees.md にある。どちらも素の `ln -s` ではなく `ln -sfT` で張る（理由は下記）。

**2026-08 の実測**（このレポの `console/`・npm 10.9.8 / node 22.23.2 / Vite 7 系）。`console/` は
その後 Vite 8 系に上がっており（`console/package.json`）、本書ではこれらを測り直していない。

- vitest は両 project とも symlink 越しで通る。本番ビルドも通る。**bundler は既定で symlink を
  辿る**ので、解決に手当ては要らない。
- ⚠️ 例外が 1 つあり、設定 1 行が要る: Vite の `…?url` import はリンクの*実体側*へ解決され、Vite が
  既定で許すルートの外になるため `Error: Denied ID …` で拒否される。`console/vite.config.js`
  （`afFsAllow`）が `node_modules` の実パスを `server.fs.allow` に足している（実体インストールでは
  何も変えない）。
- ⚠️ **`npm ci` を symlink のまま打つと、親の実体が空になる。** リンクは実体ディレクトリへ置き換わり、
  **共有していた他セッションはどれも依存を失う**。install 系を打つ前に必ずリンクを外すこと。
- ⚠️ **`rm -rf node_modules/`（末尾スラッシュ）もリンクを貫通して同じように消す。**
  スラッシュ無しならリンクだけが消える。
- `npm install <pkg>` はリンクを実体ツリーへ黙って置き換える。壊れはしないが、その worktree は
  共有をやめて自前のコピーを抱える。

**`af-scratch --auto` が `node_modules` を既に symlink にしている場合**（`$AF_WS_SCRATCH` がある環境の
新しい clone か worktree、93.1）、素の `ln -s` は何も共有しない。ディレクトリを指す既存の
symlink に対して `ln -s <target> node_modules` を打つと、新しいリンクはそのディレクトリの*中*に作られ、終了コードは 0 に
なる。**実測**（2026-09-29・素のディレクトリで・GNU coreutils の `ln`）。先に作られたリンクを外してから
張ること（`rm -rf node_modules`、スラッシュ無し）。`ln -sfT` は symlink なら置き換え、実体ディレクトリ
なら拒否する。親自身の `node_modules` も同じように退避されていれば実体は `/scratch` にあり、停止後は
`~/repos` 側のリンクは残るが指す先が無くなる。退避なしで clone された親（仕組みができる前か、
`AF_WS_SCRATCH_AUTO=0`）は実体をホームに持ったまま。

lockfile が食い違うときは共有せず、温まった `~/.npm` から `npm ci --prefer-offline` で入れる。
pnpm は入っていないが Node 22 は `corepack` を同梱しているので、pnpm を使うプロジェクトなら
content-addressable store があり、この問題自体を持たない。

## 93.4 Go — 何もしなくてよい / 効くのはメモリ

モジュールキャッシュ（`~/go/pkg/mod`）とビルドキャッシュ（`~/.cache/go-build`）は worktree 毎では
なく利用者毎（`go env GOMODCACHE GOCACHE`・2026-09-29 に確認）なので、worktree が増えても増えるのは
実質ゼロ。代わりに詰まるのはメモリで、`go test ./...` は**パッケージ単位で並列に**コンパイル・実行する。
混雑時は `-p 2` で絞る。このレポ自身のコマンドは AGENTS.md の "Running the Go tests" にある。

`GOTOOLCHAIN` は `auto`（Go 配布物の `go.env`）なので、`go.mod` が新しい版をピンしていればその
toolchain がモジュールキャッシュへダウンロードされる。ホームにあれば recreate を越えて残り、払うのは
1 回で済む。`ecs` で作業ディスクが 30 GiB 以上のときは、モジュールキャッシュもビルドキャッシュも
`/scratch` にある（93.1）ので、両方とも——ダウンロードした toolchain も——停止のたびに作り直しになる。

## 93.5 Python — 既定の `pip` が一番まずい

イメージは `/etc/pip.conf` に `break-system-packages = true` を書いている（`workspace/Dockerfile`）ので、
Debian の Python に付いた PEP 668 の印は素の `pip install` を止めない。`dev` で打つと**エラーには
ならず**、pip は `~/.local` へのユーザーインストールに落ちる。ここは永続するうえ**全プロジェクトで
共有**されるため、worktree 毎に要る版が違った時点で静かに壊れる。（pip のダウンロードキャッシュ
`~/.cache/pip` も共有されるが、こちらは無害——Settings > Machine > Tool caches が並べるキャッシュの
1 つ、`workspace/agent/tool_caches.go`。）

WT 毎に仮想環境を切るのが正で、イメージが入れている `uv` を使う:

```bash
uv venv && uv pip install -r requirements.txt
```

uv の Linux での既定はキャッシュからの hardlink なので、2 個目の worktree はディスクをあまり食わない
——キャッシュと `.venv` が同じファイルシステムにある場合に限る。ファイルシステムを跨ぐと uv はコピーに
落ちる。これは uv のドキュメントの記述で、ここでの実測ではない。`ecs` で効いてくる: 作業ディスクが
30 GiB 未満だと uv のキャッシュはホームに残り、退避された `.venv` は `/scratch` にある。また
`af-scratch --auto` が `.venv` を先回りで作るのは `pyproject.toml` があるときだけで、`requirements.txt`
だけのプロジェクトは `af-scratch .venv` を手で打つ。

**仮想環境を worktree 間でコピー / symlink してはいけない**（絶対パスを埋め込んでいる）。

## 93.6 JVM — 共有は済んでいる。止め方だけ注意

`~/.gradle` と `~/.m2` は元から全 worktree 共有で、worktree 毎なのはビルド出力（`build/`・`target/`）
だけ。JDK はイメージに焼かれていない。JDK の見つけ方とヒープ・daemon の作法は
[notes/build.md](../../workspace/notes/build.md) が正。entrypoint は `~/.gradle/gradle.properties` が
無ければ種をまき、そこには daemon のアイドル 2 分での終了などの制限が入る（`workspace/entrypoint.sh`）。

worktree 特有の注意が 1 点: **`./gradlew --stop` はその利用者の、その Gradle 版の daemon をすべて
止める**。コンテナ内のセッションはみな同じ利用者で同じ `~/.gradle` を使うので、他セッションのビルド中に
打つとそれも巻き添えになる（Gradle のドキュメントの挙動で、ここでの実測ではない）。自分が終わったときに
打つのはよいが、「重いから」と見境なく打たない。

## 93.7 Rust ほか（イメージに無い言語）

Dockerfile は Rust の toolchain を入れていないので、`rustup` は自分で入れる。`~/.cargo` はホームに
あって永続し（退避の対象一覧に無い）、registry キャッシュはそこで自動的に共有される。

target ディレクトリは数GB になる（見積もり）。複数の worktree で 1 つの共有 target ディレクトリ
（`CARGO_TARGET_DIR`）を指せばそのディスクは浮くが、cargo は target ディレクトリにビルドロックを取る
ので、並列セッションが**互いのビルド完了を待って直列化する**（"Blocking waiting for file lock on build
directory"——cargo の挙動で、ここでの実測ではない）。セッションが同時にビルドするなら worktree 毎の
target を既定にし、共有はその並列性をディスクと引き換えにするものと考える。

全体の形: **ホーム側のキャッシュは黙っていても共有される / worktree 側の出力を共有するのは例外**で、
道具がそれを許す場合に限る——93.3 の lockfile 条件下の Node の `node_modules`。root が無いので、ユーザー空間へ入れるインストーラを選ぶこと——
`rustup`、`uv tool install`、ホームの Node を通した `npm i -g`。
