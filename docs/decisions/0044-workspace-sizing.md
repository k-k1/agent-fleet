# 0044. Workspace のサイズは数値 3 軸で持ち名前付きサイズは UI 層に置く／`~` は「ファイル数」で置き場を分ける

- 状態: 採用・実装中（2026-08-15。設計と実測は docs/63）。**Fargate の有効タスクサイズ全 74 通り**と
  **EFS の I/O** を AWS sandbox で実測した上で決めた。実測が当初案を 2 つ壊している
  （Elastic throughput は小ファイルに効かない／キャッシュだけローカルへ逃がしても効かない）。
- 関連: [63-workspace-sizing.md](../log/63-workspace-sizing.md) /
  [62-ecs-start-latency.md](../log/62-ecs-start-latency.md)（同じ ECS の起動レイテンシ側） /
  [history/p3-7-aws-adapter.md](../log/p3-7-aws-adapter.md) §20b.7.4（EFS を選んだ凍結仕様） /
  [0012-go-internal-refactor.md](0012-go-internal-refactor.md)（アダプタは CP に状態を持たない）

## 背景

ECS ランタイムで Workspace のタスクサイズとディスクをユーザー毎に変えたい。per-user のメモリ上限は
既に `user_limit.mem_limit` → `fargateSize()` → タスク定義まで通っているが、(1) CPU を独立に選べない、
(2) `disk_gb` が ECS では表示にしか使われていない、(3) 既定 1024/2048（2 GiB）が実運用に対して小さい。

決定の前に 2 つ実測した（docs/63 §63.2 / §63.4）。

- **Fargate の有効サイズは飛び飛びで、刻みも一様ではない。** 8 vCPU 帯は 4096 MiB 刻み、
  16 vCPU 帯は 8192 MiB 刻み。既存の `fargateTiers` は全帯 1024 刻みを前提にしており、
  **34 GiB 等を設定すると無効な組み合わせを生成して Start がまるごと失敗する**バグがあった。
- **EFS が遅いのは「バイト数」ではなく「ファイル数」に対してである。** 1 ファイルあたり
  約 14.5 ms の固定ペナルティがあり、帯域差は 1 MiB あたり約 1 ms しかない。
  並列度を 16→64 に上げても、vCPU を 2→4→8 に上げても改善しない。

## 決定 1 — サイズは数値 3 軸（bytes / cpu units / GiB）で持ち、名前付きサイズは UI と MCP に置く

`user_limit` に `cpu_limit`（vCPU units・0 = 未設定）を追加し、**メモリ（bytes）・CPU（units）・
ディスク（GiB）の 3 軸を独立に保持する**。名前付きサイズ（S/M/L…）は保存形式ではなく、
Console と MCP が 3 軸へ展開する**選択肢の見せ方**として実装する。

名前付きサイズを保存の正にする案（当初の有力案）を採らない理由:

- 既存の `mem_limit`（bytes）は API・MCP・Console・二段クォータまで通っており、**段位へ移行すると
  既存値の移行と全経路の書き換えが要る**。3 軸なら `ALTER TABLE` 1 本の追加で済む。
- **docker ランタイムは任意バイト指定が意味を持つ**（`WS_MEMORY=5g` 相当の粒度）。段位に丸めると
  オンプレ側の表現力を落とす。CPU も `--cpus` に素直に入る。
- Fargate の飛び飛び制約は `fargateSize()` が**有効な組へスナップして吸収する**（既にその形）。
  保存側を段位にしなくても、無効な組み合わせがタスク定義に届くことはない。
- 段位の利点（UI が制約に素直・説明が簡単）は、**選択肢を UI 層に置けばそのまま得られる**。

`fargateTiers` は帯ごとの刻み（`stepMiB`）を持つ形に直し、`fargateSize` は要求 CPU を下限として
扱う（メモリだけでなく CPU からも帯を選ぶ）。

## 決定 2 — ディスクは 200 GiB を境に ephemeral と EBS を使い分け、管理者には 1 つの数として見せる

- **1 – 200 GiB は ephemeral storage**（21 未満の指定は 21 に切り上げ。実測で 21 未満・201 以上は API が拒否）
- **200 GiB 超は ECS 管理 EBS**
- 分岐は CP の中に閉じ、管理者から見える概念は「ディスク GB」1 つに保つ
- 既定は **20 GiB**（＝無料枠。`disk_gb` 未設定なら `EphemeralStorage` を設定しない）。
  デプロイ既定は `AF_ECS_WS_DISK_GB` で変更できる

価格は実測でほぼ同額（ephemeral $0.097/GB-月・EBS gp3 $0.096/GB-月）だが、**ephemeral は 20 GiB まで
無料**で、インフラ IAM ロールもボリューム作成・アタッチ・フォーマットの起動時上乗せも要らない。
200 GiB 超と IOPS 指定だけが EBS の領分。

## 決定 3 — `~` の置き場は「平均ファイルサイズ」で分ける

EFS のペナルティは 1 ファイル約 14.5 ms、帯域差は 1 MiB 約 1 ms。したがって
**平均ファイルサイズが 1 MiB を超えるものは EFS に置いてもローカルの 2 倍以内に収まる**。
この基準で `~` を分ける。

| 中身 | 置き場 | 根拠（実測） |
|---|---|---|
| 認証・接続情報・identity（`homeKeep` の 7 つ） | **EFS** | 永続が絶対・100 MiB 未満 |
| `~/repos` の追跡ファイルと未コミット変更 | **EFS** | 永続が絶対。`git clone` 4.9s / `git status` < 0.4s で EFS でも耐える |
| `~/.npm`・`ms-playwright` 等の大きい tarball 系 | **EFS** | `.npm` は 20.6 GiB あるがファイル数は 6,756（平均 3.1 MiB）＝ EFS が苦手としない形 |
| `node_modules`・`target`・`dist`・`.venv` | **ローカル** | 平均 17 KiB。`npm ci` が 9.4 倍遅い主因 |
| `go-build`・`uv`・`go/pkg/mod` | **ローカル** | `uv` は 1 GiB に 101,949 ファイル（平均 10 KiB）＝ EFS へ書くと単純計算 26 分 |
| `~/.local`（CLI 実体） | **保留** | 24,223 ファイル。CLI 起動コストを未測定 |

この分割が成立するのは、**再取得が高いものはファイル数が少なく、ファイル数が多いものは再生成が安い**
という関係が実測で成立しているため（パッケージマネージャは配布物を tarball で持ち、展開物と
中間生成物が小ファイルの山になる、という構造から来る）。偶然の一致ではない。

**採らなかった案**:

- **`~/repos` ごとローカル** — 効果は最大だが、自動アイドル停止で**未コミットの作業が消える**。
- **キャッシュ類を一律ローカル** — 実測がこの配置そのもの（npm キャッシュはローカル・書き込み先だけ EFS）
  であり、それでも 9.4 倍遅かった。**支配項は生成物の書き込み**でキャッシュの読み出しではない。
- **EFS を Elastic throughput へ** — 小ファイルには効かない（tar 展開 98.3s vs bursting 98.0s）。
  効くのは逐次帯域のみ（1 GiB 書き込み 2.6s ＝ 394 MB/s）。導入するとしても理由は帯域と
  バーストクレジット枯渇への保険であって、ビルド速度ではない。
- **タスクサイズを上げて解決する** — vCPU 2→4→8 で EFS 側は変化なし。買えない。

## 決定 4 — home を EBS に載せる案は採らない（Fargate では原理的に不可）

`ServiceManagedEBSVolumeConfiguration`（サービス経路＝現構成）には終了ポリシーが**無く**、
タスク停止で必ず削除される。`TaskManagedEBSVolumeConfiguration`（`RunTask`）には
`TerminationPolicy.DeleteOnTermination=false` があるが、**既存ボリュームを指す項目が API に無い**ため
残したボリュームを再アタッチできない（ECS は常に新規作成する）。持ち越しは `SnapshotId` 経由のみで、
それは「クラッシュ時に home が前回スナップショットまで巻き戻る」「復元直後は S3 からの遅延
ハイドレートで遅い」「停止に数分かかる」「Service Connect を失う」を同時に抱える。

**金額の問題ではない**（EBS $0.096 < EFS $0.36 /GB-月）。Fargate に「速くて永続」が無いだけである。
本当に必要になったときの答えは EC2 起動タイプ ＋ インスタンス stop（停止してもボリュームが残る）で、
これは別案件として検討する。docs/62 の `(d) EC2 起動タイプ＝却下` は理由が
「scale-to-zero が消える」だったので、インスタンス stop を前提にすれば**再検討の余地がある**。

## 決定 5 — 退避は既定で有効にする（既定 50 GiB）／生成物は作業コピーを作った時点で逃がす

決定 3 は実装したが、**出荷状態では一度も発火していなかった**。原因は 2 つで、どちらも
「入れた」と「効く」の差である。

1. **作業ディスクの既定が 0（＝ Fargate 無料枠 20 GiB）だった。** 退避は作業ディスクが
   30 GiB 以上のときだけ有効になる設計（docs/63 §63.6.1）なので、既定のままではどのデプロイでも
   条件を満たさない。→ **既定を 50 GiB に上げる**（`WsDiskGiB` / `AF_ECS_WS_DISK_GB`）。
   無料枠超過分は **$0.097/GiB-月・タスク稼働中のみ**課金で、30 GiB 上乗せは 24/7 でも月 $2.9。
   `WsDiskGiB=0` で従来どおりに戻せる。**既存スタックは自動では上がらない**（CFN はパラメータ値を保持する）。
2. **生成物の退避が手動だった。** `af-scratch node_modules` は「既にある木」しか動かせず、
   そのときには **1 回目の `npm ci` が EFS 上で走り終えている**（＝ 105 秒を払い済み・移動自体も遅い）。
   効き幅を取れるのは**空のうちに symlink を張る**形だけなので、**Agent が clone / worktree 作成の
   直後に `af-scratch --auto` を叩く**（docs/63 §63.6.3）。

**追跡物は絶対に動かさない**——実体があるものは `git check-ignore` が無視と答えたときだけ移す。
既存の作業コピーには適用しない（再開時に巨大な木を移すとセッション起動が止まるため）。
代償は `[ -d node_modules ] || npm install` 型のスクリプトが誤認すること（`AF_WS_SCRATCH_AUTO=0` で無効化）。

## 影響

- `control-plane/mem.go` — 帯ごとの刻みを持つ形へ。既存バグの修正を含む
- `control-plane/migrations/0044_user_limit_cpu.sql` / `migrations-pg/0027_user_limit_cpu.sql`
- `control-plane/store.go`・`store_sqlite.go`・`store_postgres.go` — `UserLimit.CPULimit`
- `control-plane/workspace_lifecycle.go` — `resolveWorkspaceCPUUnits` / `resolveWorkspaceDiskGB`
- `control-plane/limits.go` — テナント上限 `max_workspace_cpu` / `max_workspace_disk_gb`
- `control-plane/runtime_ecs.go` — CPU の反映・`EphemeralStorage`・200 GiB 超の EBS
- `control-plane/runtime_docker.go` — `--cpus`（native は cgroup を持たないので従来どおり無視）
- `control-plane/tenants.go`・`mcp.go` — 設定経路
- `console/src/features/settings/tenantMembers.tsx` — 名前付きサイズの選択 UI
- `workspace/entrypoint.sh` — 置き場の分割（ECS のときだけ有効化）
- `workspace/af-scratch.sh` — `--auto`（マーカーから生成物を引き当て、空のうちに symlink）
- `workspace/agent/scratch.go` — clone / worktree 作成後の best-effort 呼び出し
- `deploy/aws/ecs/cfn/30-ingress.yaml` — `WsDiskGiB` の既定を 0 → 50
- `workspace/workspace-notes.md` — 永続モデルの記述を ECS について書き換える（利用者への約束）
