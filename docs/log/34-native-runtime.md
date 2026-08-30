# 34. ネイティブ Runtime（AF_RUNTIME=native）— Docker なしでのワークスペース実行

> 状態: 🚧 実装済（単体テストまで）・実機（素の WSL2）検証待ち / 対象ブランチ: feat/native-runtime

## 34.1 動機

WSL2 で Docker（Docker Desktop も native dockerd も）を導入できない環境で agent-fleet を
動かしたい、という要望（2026-07 検討セッション）。既存の選択肢は:

1. WSL2 に native Docker Engine を入れる（`deploy/local/README-wsl.md` の推奨構成）— **引き続き推奨**。
2. UI/CP 開発だけなら Docker 不要 — ただしワークスペース機能は全滅。
3. コンテナを使わない Runtime アダプタを足す — 本書。

`Runtime` / `RuntimeFactory`（docs/dev/09 §9.2）は最初からポート&アダプタで切ってあり、
docker / ecs と並ぶ第3のアダプタとして `native` を追加した。**用途は個人利用・開発検証に
限定**する（§34.5 の制約）。

## 34.2 仕組み

`AF_RUNTIME=native`（別名 `wsl`）で CP を起動すると、ワークスペースは docker run の代わりに
**workspace-agent のホストプロセス**として起動される（`control-plane/runtime_native.go`）。

| コンテナ実行 | ネイティブ実行 |
|---|---|
| `docker run` イメージ | `AF_NATIVE_AGENT_BIN`（既定: PATH の `workspace-agent`）を fork |
| bind-mount `<dataDir>/home` → `/home/dev` | `HOME=<dataDir>/home` を注入（**仮想 HOME**） |
| mount `<dataDir>/claude-config` → `/var/lib/af/claude` | `CLAUDE_CONFIG_DIR=<dataDir>/claude-config` |
| `-p 127.0.0.1:<port>:7700` | `AGENT_ADDR=127.0.0.1:<port>`（loopback bind） |
| docs mount `<dataDir>/docs:ro` | `AGENT_DOCS_DIR=<dataDir>/docs`（agent fs.go の上書き env） |
| `docker inspect` で状態 | `<dataDir>/agent.pid` ＋ `/proc/<pid>/cmdline` 照合 |
| `docker stop -t`（SIGTERM→SIGKILL） | プロセスグループへ SIGTERM →猶予後 SIGKILL ＋ tmux ソケット掃除 |
| tini が孤児 reap | CP が `cmd.Wait()` で reap（CP 再起動後の孤児は init が回収） |

設計上のポイント:

- **仮想 HOME 方式**: 前セッション検討案の「実ホームを `AF_WORKSPACE_ROOT` として操作し、
  ツール状態だけ別 HOME に逃がす」（homeDir() の workspaceRoot/runtimeHome 分離）は**採用しない**。
  Docker と同じ `<dataDir>/home` をそのまま HOME にすることで、
  - Agent 側の `homeDir()` リファクタが不要（コンテナと完全に同じ規約で動く）、
  - `cleanHome` / recreate / ファイルブラウザ denylist / ディスク使用量集計が無変更で成立、
  - 同一 dataDir を docker ⇄ native で行き来してもデータ互換、が得られる。
  ユーザーの実ホームを荒らさない、という分離目的も HOME 差し替えだけで達成できる。
- **env は明示構築**（継承しない）: CP プロセスの環境には `AF_MASTER_KEY` や OAuth secret が
  載っている。ワークスペースプロセスへは必要な変数だけを組み立てて渡す（テストで
  `AF_MASTER_KEY` 非漏洩を固定）。
- **tmux 分離**: 既存の `AF_TMUX_SOCKET` seam（tmuxx.Cmd、docs/32 M1 事故の再発防止機構）に
  ワークスペース名を渡す。ホストユーザー自身の tmux や他ワークスペースのサーバへは構造的に
  届かない。Stop 時の `tmux -L <name> kill-server` も同ソケット限定。
- **lifecycle fencing**: 複数CPのDB leaseに加え、`dataDir/lifecycle.lock` をkernel flockして
  native Start／Stopと共有RW操作を直列化する。旧holderがheartbeatを失っても新holderは旧操作の
  静止を待つ。未確定StartはPIDと`/proc/<pid>/stat`の起動時刻を照合して自分のspawnだけを回収し、
  cancel後のStopはSIGKILL・tmux掃除・pidfile削除へ進まない。
- **State の意味論は docker と同一**: 生存プロセス=running / pid ファイル残骸（クラッシュ）
  =stopped / 正常停止後（pid ファイル除去済）=none。「通常の停止状態は none」という Console
  依存を保つ。pid 再利用誤認は `/proc/<pid>/cmdline` の argv[0] 照合で防ぐ。

## 34.3 使い方（素の WSL2）

```bash
# ホストに必要なもの: go / node(nvm) / tmux / git / claude 等の各 CLI（chromium は任意）
deploy/local/run-dev.sh native      # env でも可: AF_RUNTIME=native deploy/local/run-dev.sh
```

run-dev.sh は起動スクリプトの単一エントリポイント（サブコマンド `local`/`wsl`/`native`/
`reset`）。native のときは docker 工程（rtk vendor・イメージビルド・スモーク・JVM
provision）を全部スキップし、`workspace-agent` を `/tmp/af-agent` にホストビルドして
`AF_NATIVE_AGENT_BIN` で CP へ渡す。tmux / git / claude がホスト PATH に無ければ警告する。
データ初期化は `deploy/local/run-dev.sh reset [--all]`（native の agent プロセス残骸も
掃除してから消す。README-wsl §5）。

Dockerfile / entrypoint.sh 相当の初期化（claude の自動 install/update、settings.json seed、
opencode plugin seed、toolchains 適用など）は**行われない**。ホスト環境をそのまま使う。

## 34.4 HOME の分離とデータの場所（Windows からの参照）

ネイティブ実行の「HOME」は同一 OS ユーザーのまま env 差し替えだけで3層に分かれる:

| 層 | 場所（run-dev.sh 既定） | 使う者 / 中身 |
|---|---|---|
| WSL ユーザーの実 `~` | `/home/<user>` | CP プロセス自身。ワークスペースは書き込まない（実ホームのドットファイルは荒れない）。ただしデータ置き場自体は既定でこの下（`WS_DATA=~/.local/share/agent-fleet`） |
| ワークスペースの**仮想 HOME** | `~/.local/share/agent-fleet/<key>/home`（default テナント・単一ユーザーなら `<key>`=`dev`） | workspace-agent と全セッション（claude/git/tmux/npm…）が `HOME=` ここで動く。`~/repos`・`~/.gitconfig`・`~/.ssh`・`~/.config/agent-fleet`（接続情報）・claude の `~/.local/bin` 自前インストールが全部この下に閉じる。docker 版が `/home/dev` へ bind-mount するのと同一レイアウト＝docker⇄native でデータ互換 |
| Claude 状態の別置き | `~/.local/share/agent-fleet/<key>/claude-config`（`CLAUDE_CONFIG_DIR`） | 仮想 HOME の**外**に置く理由はコンテナ版と同じ（[history/p3-5-member-console.md](p3-5-member-console.md) 段2）: Console ファイルブラウザ（browse root=仮想 HOME）から平文 Claude 状態を見せない |

**Windows の Explorer から参照できる**。WSL2 のファイルシステムは
`\\wsl.localhost\<ディストロ名>\`（旧 `\\wsl$\`）で公開されているので、

```
\\wsl.localhost\Ubuntu\home\<WSLユーザー>\.local\share\agent-fleet\dev\home
```

をアドレスバーに貼れば仮想 HOME（repos 含む）がそのまま開ける（WSL 側から
`explorer.exe .` でも可）。ドット始まりを隠すのは Linux 側の慣習なので Explorer では
`.local` 等も普通に見える。注意:

- **閲覧は自由、書き込みは控えめに** — 9P 共有なので、セッション実行中に Windows 側から
  同じファイルを編集すると普通のファイル競合になる（git 操作中などは避ける）。
- **`claude-config/` には触れない** — 認証トークン等の平文状態。Explorer から見える場所に
  あるからこそ、コピー・共有をしないこと。
- **`WS_DATA` を `/mnt/c` に置かない** — drvfs は I/O が激遅でパーミッションも壊れやすい。
  データルートは WSL 側 ext4 のままにする。

## 34.5 制約（割り切り）

| 項目 | 内容 |
|---|---|
| **単一ユーザー限定** | コンテナ隔離が無く、全ワークスペースが同一 OS ユーザーで動く。factory が `AUTH=dev` 以外を fail-fast で拒否する |
| メモリ上限なし | `WS_MEMORY` / per-user MemBytes は不適用（cgroup を持たない）。ホスト全体で共倒れし得る |
| ネットワーク隔離なし | per-user docker network（相互不可視）が無い。単一ユーザー前提なら実害なし |
| 実行環境はホスト任せ | **従来モードのみ**: CLI（claude/codex/opencode）、tmux、git、chromium、JDK はホスト導入が前提。焼き込みピン止め（versions.json）も無し。**rootfs モードでは解消**（rootfs がユーザーランドとピン manifest を持つ） |
| entrypoint 初期化なし | **従来モードのみ**: settings.json seed / opencode plugin / AGENTS.md seed / TZ・toolchains 適用が働かない。**rootfs モードでは entrypoint.sh がそのまま走り docker と同等** |
| ブラウザペイン | 従来モード: ホストに chromium があれば動く見込み — 未検証。rootfs モード: 初回 attach でピン版を自動導入（bwrap 配下の sandbox 可否は docs/35 P2/P4 ゲートで実測。不可なら `AF_CHROMIUM_NO_SANDBOX=1`） |

**rootfs モード（P2 で実装済み — docs/35 §35.7.2）**: `AF_NATIVE_ROOTFS=<展開済み
rootfs>`（+`AF_NATIVE_BWRAP`）で、Start のプロセス起動が bwrap ラップ（rootfs を
read-only の / に・docker と同じ bind レイアウト・単一 uid userns・pid unshare）へ
切り替わる。lifecycle（pidfile/State/Stop）は従来モードと共通のまま。native
パッケージのランチャ `af`（docs/35 §35.3.1）がこの配線を行う。「単一ユーザー限定」
「メモリ上限なし」の 2 制約は rootfs モードでも残る。

## 34.6 検証状況

- `control-plane`: `TestNativeFactoryGates` / `TestNativeRuntimeLifecycle`（helper-process で
  fake agent を実 fork し、Start/State/Stop/pidfile/クラッシュ検出/env 分離まで実証）緑。
- 実機（Docker なし WSL2 での通し起動・Console からのセッション実行）は**未実施**。
  手順は docs/35 §35.8.1 のチェックリストに凍結済み（ユーザー実施 — chromium sandbox
  実測・オフライン再起動確認を含む。結果はこの節と docs/35 §35.8 に反映する）。
