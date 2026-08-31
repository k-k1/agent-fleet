# 63. Workspace のサイズ（CPU / メモリ / ディスク）をユーザー毎に指定する

> 状態: 設計中（2026-08-15）。**P0 の計測は完了** —— Fargate の有効タスクサイズ全 74 通りを
> ECS API で実測（§63.2）し、**EFS の I/O を AWS sandbox で実測**（§63.4）した。
> **決定は [ADR 0044](../decisions/0044-workspace-sizing.ja.md) に固定済み**（2026-08-15）: サイズは数値 3 軸で
> 持ち名前付きサイズは UI 層／ディスクは 200 GiB を境に ephemeral と EBS／`~` の置き場は
> **平均ファイルサイズ**で分ける／home を EBS に載せる案は Fargate では原理的に不可。
> **P1〜P2 実装済み**（§63.7）。残る未決は `~/.local`（CLI 実体）の置き場のみ（§63.5.3）。
> 関連: [62-ecs-start-latency.md](62-ecs-start-latency.md)（同じ ECS の起動レイテンシ側の調査） /
> [history/p3-7-aws-adapter.md](p3-7-aws-adapter.md) §20b.7.4（EFS を選んだ凍結仕様） /
> [admin/02-limits.md](../../guide/admin/02-limits.md)（管理者向けの上限の説明）
> 対象: `control-plane/mem.go`（Fargate サイズ表）/ `control-plane/runtime_ecs.go`（タスク定義）/
> `control-plane/store.go`・`migrations/`（`user_limit`）/ `control-plane/tenants.go`・`mcp.go`（設定経路）/
> `console/src/features/settings/tenantMembers.tsx`（UI）/ `deploy/aws/ecs/cfn/`（既定値）
> 前提: ECS 構成は 🚧 **実運用実績なし**。本書の計測は sandbox で deploy → 計測 → teardown を
> 1 セッションで閉じて行った（残存リソース 0 を確認済み）。

## 63.1 問題

per-user のメモリ上限（`user_limit.mem_limit`）は既に通っていて、`ws.MemBytes` →
`fargateSize()` → タスク定義の `Cpu`/`Memory` まで届く。足りないのは 3 つ。

1. **CPU を独立に選べない。** CPU はメモリから導出され、`AF_ECS_TASK_CPU`（既定 1024）が下限に
   なるだけ。「メモリ 8 GB のまま 4 vCPU」を表現できない。
2. **ディスクが効いていない。** `user_limit.disk_gb` は列も API も UI もあるが、ECS では
   `admin_stats.go` の表示にしか使われず、タスク定義に `EphemeralStorage` を設定していない
   ＝ **Fargate 既定の 20 GiB のまま**。
3. **ECS の既定 1024/2048（= 2 GiB）が実運用に対して小さい。** compose の既定は `WS_MEMORY=5g`、
   開発 Workspace 1 個の実測は anon 1.12 GiB / ピーク 5.30 GiB（ページキャッシュ込み）。

なお **タスクサイズは起動レイテンシに影響しない**ことは計測済み（[62](62-ecs-start-latency.md) §62.10、
256/512 と 1024/2048 で API→タスク作成が 4〜11s と差なし）。大きくしても遅くならない。

## 63.2 Fargate の有効タスクサイズ（ECS API で実測）

`RegisterTaskDefinition` は無料で、不正な組み合わせは
`No Fargate configuration exists for given values` で即座に拒否される。これを使って
ap-northeast-1（2026-08-15）で全帯を実測した。プローブしたタスク定義 23 本は削除済み。

| vCPU | `cpu` | `memory`（MiB） | 刻み | 通り |
|---|---|---|---|---|
| 0.25 | 256 | 512 / 1024 / 2048 | （3 値のみ） | 3 |
| 0.5 | 512 | 1024 – 4096 | 1024 | 4 |
| 1 | 1024 | 2048 – 8192 | 1024 | 7 |
| 2 | 2048 | 4096 – 16384 | 1024 | 13 |
| 4 | 4096 | 8192 – 30720 | 1024 | 23 |
| **8** | 8192 | 16384 – 61440 | **4096** | 12 |
| **16** | 16384 | 32768 – 122880 | **8192** | 12 |

合計 **74 通り**。`cpu=32768`（32 vCPU）は存在しない。境界外（1024/9216・2048/17408・4096/31744・
8192/62464・16384/131072）と刻み外（1024/2560・8192/17408・8192/18432・16384/33792・16384/36864）が
拒否されることも個別に確認した。

`ephemeralStorage.sizeInGiB` の有効範囲も同じ方法で確認: **21 以上 200 以下**
（20 以下は `EphemeralStorage size should be at least 21`、201 以上は `... at most 200`）。

### 63.2.1 既存バグ — `fargateTiers` の刻みが 8/16 vCPU 帯で誤っている

`control-plane/mem.go:84` の `fargateTiers` は全帯で 1024 MiB 刻みを前提に
「1024 の倍数へ切り上げる」実装になっているが、実測のとおり **8 vCPU 帯は 4096 刻み、
16 vCPU 帯は 8192 刻み**。したがって

- `mem_limit` に 31〜60 GiB の非 4 GiB 境界値（例 34 GiB）を設定すると
  `fargateSize()` は `cpu=8192, memory=34816` を返す
- これは無効な組み合わせなので `RegisterTaskDefinition` が失敗し、**Start がまるごと失敗する**

`maxMiB`（61440 / 122880）は正しいので、壊れているのは刻みだけ。今回の実装で直す。

## 63.3 ディスク軸の決定 — ephemeral と EBS の使い分け（案 B）

Fargate でタスクにディスクを足す方法は 2 つあり、価格は Pricing API 実測（ap-northeast-1・
2026-08-15）で **ほぼ同額**、違いは上限と無料枠と付帯コストに出る。

| | ephemeral storage | ECS 管理 EBS（gp3） |
|---|---|---|
| 指定範囲 | 21 – 200 GiB（実測） | 1 – 16,384 GiB |
| 単価 | $0.000133 / GB-時 ≒ $0.097 / GB-月 | $0.096 / GB-月 |
| 無料枠 | **20 GiB まで無料** | 無し |
| IOPS / スループット | 指定不可 | 3,000–16,000 IOPS / 125–1,000 MiB/s |
| 起動時の上乗せ | 無し | ボリューム作成→アタッチ→フォーマットが毎起動 |
| 必要な足回り | 無し（タスク定義に 1 フィールド） | ECS インフラ IAM ロール ＋ サービス側 `volumeConfigurations` |
| タスク停止時 | 消える | **消える**（`ServiceVolumeConfiguration` は「サービスのタスク 1 個につき 1 ボリュームを作成」） |

**どちらもタスク停止で消える**（EBS も Fargate では永続ストレージではない）。永続領域は EFS の
home 側のままであり、この軸は「作業領域の広さ」を決めるものと位置づける。

**決定（2026-08-15）**:

- 管理者から見える概念は「ディスク GB」1 つ。`user_limit.disk_gb` を ECS で実際に効かせる。
- **1 – 200 GiB は ephemeral storage**（21 未満の指定は 21 に切り上げ）、**200 GiB 超は ECS 管理 EBS**。
  分岐は CP の中に閉じ、管理者には見せない。
- **既定は 20 GiB**（＝無料枠のまま。`disk_gb` 未設定なら `EphemeralStorage` を設定しない）。
  デプロイ既定は `AF_ECS_WS_DISK_GB` で上書きできるようにする。
- 二段クォータは既存 `mem_limit` と同じ形（テナント上限 → ユーザー値 → クランプ、`memClampNote` の型）。

## 63.4 EFS の I/O 実測（2026-08-15・sandbox）

「ディスク軸を足しても、重いものが EFS 側にあるなら体感は変わらないのでは」という問いに
答えるため、**EFS が実際どれだけ遅いのか**を測った。これまで測っていたのは起動レイテンシ
（マウント 25s）だけで、ファイル I/O の数字は無かった。

### 63.4.1 計測方法

NAT / ALB / RDS を作らない最小構成（デフォルト VPC のパブリックサブネット ＋ Fargate ＋ EFS 2 本）。
**同一タスク内**に 3 つの置き場をマウントして同じ手順を回すので、CPU・ネットワーク・イメージの
差が入らない。

| ターゲット | 実体 |
|---|---|
| `local` | タスクローカル（ephemeral storage・overlay fs） |
| `burst` | EFS・**bursting**（＝本番と同じ設定）・access point ＋ **転送時暗号化 ON** |
| `elastic` | EFS・**elastic** throughput・他は同じ |

- 素材は `npm install`（vite/react/typescript/vitest/eslint ほか）で作った **5,643 ファイル / 107 MiB**
  の `node_modules`。tar 化して各ターゲットへ展開する。
- **npm のキャッシュは常にローカル**（`/tmp/npmcache`）に置き、変えるのは書き込み先だけ。
- 本番と同じく **uid 1000** で実行（access point が posix 1000 を強制するため、root で走らせると
  `tar` の chown が失敗する）。
- 同じ手順を 2 回（root 版と uid 1000 版）回し、再現することを確認した。以下は uid 1000 版。

### 63.4.2 結果（2 vCPU / 8 GB・秒）

| 項目 | local | burst | elastic | EFS / local |
|---|---|---|---|---|
| `tar` 展開（5,643 files・直列） | 0.3 | **98.0** | 98.3 | **327×** |
| `rm -rf`（同上） | 0.1 | 31.9 | 31.2 | 319× |
| `grep -r`（全バイト読み） | 0.2 | 19.5 | 15.4 | 98× |
| 小ファイル作成 2,000（直列） | 1.9 | 30.7 | 31.2 | 16× |
| 小ファイル作成 8,000（並列 16） | 5.6 | 43.5 | 43.2 | 7.8× |
| 小ファイル作成 8,000（並列 **64**） | 5.5 | 44.7 | 45.4 | 8.1× |
| **`npm ci`**（5,643 files・キャッシュはローカル） | 3.0 | **28.2** | 25.8 ※ | **9.4×** |
| `git clone --depth 1`（express） | 0.8 | 4.9 | 4.8 | 6.1× |
| `find \| wc -l` | 0.0 | 3.3 | 2.3 | — |
| `dd` 1 GiB 書き込み（`conv=fsync`） | 8.2 | 9.3 | **2.6** | 1.1× / **0.3×** |

※ elastic の `npm ci` は root 版ランの値（uid 1000 版はログ回収前に teardown した）。

### 63.4.3 分かったこと

1. **小ファイル操作は EFS が 8〜30 倍遅い。** 1 ファイルあたりの作成コストは直列で
   **15.4 ms**（2,000 files / 30.7s）。ローカルは 0.95 ms。NFS の往復レイテンシがそのまま出る。
2. **並列化では隠せない。** 並列 16 で 184 files/s、**並列 64 でも 179 files/s** と頭打ち。
   並列度を上げても天井が動かない。
3. **Elastic throughput は小ファイルには効かない。** tar 展開 98.3s vs bursting 98.0s、
   `npm ci` も同等。**効くのは逐次帯域だけ**で、そこは劇的（1 GiB 書き込みが 2.6s ＝ 394 MB/s、
   bursting の 110 MB/s とローカルの 125 MB/s を上回る）。
4. **vCPU を増やしても改善しない。** 同じ計測を 4 vCPU / 8 vCPU でも回したが、EFS 側は
   `tar` 展開 98.0s（2 vCPU）→ 109.6s（4）→ 107.2s（8）、`npm ci` 28.2 → 29.5 → 27.9 で
   **変化なし**（ローカル側は 2 vCPU から既に頭打ち）。転送時暗号化の stunnel が CPU を
   食っている、という仮説は**否定された**。タスクサイズを上げても EFS は速くならない。
5. **逐次帯域は問題ではない。** bursting でもローカルとほぼ同じ 110 MB/s 出た。
   ただし新規作成した EFS はバーストクレジットが満タンなので、**「クレジット枯渇の崖」は
   この計測では再現していない**（枯渇時の挙動は未実測）。

### 63.4.4 実際の Console への外挿

このリポジトリの `console/node_modules` は実体 **20,905 ファイル / 350 MiB**（計測素材の 3.7 倍）。
線形に外挿すると `npm ci` は **EFS で約 105s / ローカルで約 11s**。1 回のセッションで数回走る
コマンドとしては効く差になる。

### 63.4.5 価格の訂正

同時に Pricing API で取り直した実価格（ap-northeast-1・2026-08-15）:

| 項目 | 実価格 | 備考 |
|---|---|---|
| EFS Standard ストレージ | **$0.36 / GB-月** | 62 に $0.30 と書いていたのは誤り |
| EFS One Zone | $0.192 / GB-月 | |
| EFS IA | $0.0272 / GB-月 | |
| EFS Elastic throughput | read $0.04 / GB・write $0.07 / GB | bursting は $0 |
| EFS Provisioned throughput | $7.20 / MiBps-月 | |
| NAT Gateway データ処理 | **$0.062 / GB** | 62 に $0.045 と書いていたのは誤り |
| Fargate | vCPU $0.05056/時・メモリ $0.00553/GB-時 | |
| Fargate ephemeral storage | $0.000133 / GB-時（20 GiB 超過分） | |
| EBS gp3 | $0.096 / GB-月 | |

## 63.5 `~` の置き場 — 「平均ファイルサイズ」で分ける

§63.4 は、当初の想定を 2 つ壊した。

- **「Elastic throughput に変えれば速くなる」は否定された。** 小ファイルには効かない。
- **「キャッシュ類だけローカルへ移す」案も、実測済みの数字として効果がほぼ無い。** 今回の計測は
  まさにその配置（npm キャッシュはローカル・書き込み先だけ EFS）であり、それでも `npm ci` は
  9.4 倍遅かった。**支配項は生成物の書き込み**であって、キャッシュの読み出しではない。

### 63.5.1 判定基準

EFS のペナルティは **1 ファイルあたり約 14.5 ms 固定**、帯域側は **1 MiB あたり約 1 ms** しか差がない
（EFS 110 MB/s vs ローカル 125 MB/s）。したがって:

> **平均ファイルサイズが 1 MiB を超えるものは、EFS に置いてもローカルの 2 倍以内に収まる。
> 小さいものだけが致命的に遅い。**

この基準で開発 Workspace の `~` を実測すると、きれいに割れる（本セッションの Workspace・45.3 GiB / 35 万ファイル）。

| ディレクトリ | サイズ | ファイル数 | 平均 | 判定 |
|---|---|---|---|---|
| `~/.npm` | 20,610 MiB | 6,756 | **3.1 MiB** | ✅ EFS で問題ない |
| `~/.cache/ms-playwright` | 646 MiB | 599 | 1.1 MiB | ✅ EFS で問題ない |
| `~/.cache/copilot` | 1,604 MiB | 1,807 | 0.9 MiB | ✅ 概ね可 |
| `~/.gradle` | 1,557 MiB | 7,987 | 199 KiB | ⚠️ 中間 |
| `~/.cache/go-build` | 8,993 MiB | 32,956 | 279 KiB | ⚠️ 中間（数が多い） |
| `~/.local` | 5,151 MiB | 24,223 | 217 KiB | ⚠️ 中間・保留（§63.5.3） |
| `~/go/pkg/mod` | 526 MiB | 19,604 | 27 KiB | ❌ ローカルへ |
| `~/.cache/uv` | 1,069 MiB | **101,949** | **10 KiB** | ❌ 最悪（EFS へ書くと単純計算 26 分） |
| `node_modules`（生成物） | 350 MiB | 20,905 | 17 KiB | ❌ ローカルへ |

**`~/.npm` は 20 GiB あるのにファイル数は 6,756 しかない**、という点が効く。再取得が高いものは
例外なくファイル数が少なく、ファイル数が多いものは例外なく再生成が安い —— パッケージマネージャが
配布物を tarball で持ち、展開物と中間生成物が小ファイルの山になる、という構造から来る関係で、偶然ではない。

### 63.5.2 決定した配置（ADR 0044 決定 3）

| 中身 | 置き場 | 毎朝の代償 |
|---|---|---|
| 認証・接続情報（`runtime_docker.go` の `homeKeep` の 7 つ） | EFS | 無し |
| `~/repos` の追跡ファイル・未コミット変更 | EFS | 無し |
| `~/.npm`・`ms-playwright` 等 | EFS（据え置き） | 無し |
| `node_modules`・`target`・`dist`・`.venv` | ローカル | `npm ci` 約 11 秒（キャッシュは EFS にあるのでネットワーク不要） |
| `go-build`・`uv`・`go/pkg/mod` | ローカル | 初回ビルドが cold |

効き（2 vCPU/8 GB・月 160h 稼働・home 45 GiB の場合）:

| 指標 | 現状 | 決定後 |
|---|---|---|
| EFS 上のデータ | 45.3 GiB | 約 1.7 GiB ＋ `~/.npm` 等 |
| EFS 月額 | $16.31 / 人 | **$0.61 / 人**（`.npm` を残すなら ＋$7.24） |
| `npm ci`（Console 相当・外挿） | 約 105 秒 | **約 11 秒** |
| Stop→Start で失うもの | なし | キャッシュと生成物のみ（再生成可能） |

朝の待ちは「最初の `npm ci` と最初の `go build`」だけで、しかも**ネットワークに依存しない**
（`~/.npm` が EFS に残るため）。

### 63.5.3 残る未決 — `~/.local`（CLI 実体）

24,223 ファイル / 5.2 GiB。`claude` を起動するたびに Node のモジュール解決が NFS 越しに走っている
はずだが、**1 コマンドあたり何秒かを測っていない**。大きければイメージへ焼く判断になる
（pull +10s 程度と引き換え・docs/35 の `BAKE_AGENT_CLIS`）。EFS 据え置きが暫定。

### 63.5.4 検討して採らなかったもの

- **`~/repos` ごとローカル ＋ 停止時 tar 退避** — 効果は最大だが、SIGKILL・OOM・異常終了のいずれでも
  未コミット作業が消える経路が生まれる。自動アイドル停止がある以上踏めない。
- **S3 への退避／復元** — S3 ゲートウェイエンドポイント（`04949dc9`）経由で転送は無料、逐次帯域も
  十分。失敗しても「キャッシュが無い朝」で止まりデータ損失にならないので**筋は良い**が、
  §63.5.2 で朝の待ちが 1〜2 分に収まるため、最初から入れる必要はない。**後で足せる拡張**として棚上げ。

### 63.5.5 home を EBS に載せる案（Fargate では不可）

「金を払って home を EBS にすれば解決するか」を API 定義で確認した結果、**できない**。詳細は
[ADR 0044 決定 4](../decisions/0044-workspace-sizing.ja.md)。要点:

| | 型 | 終了時 |
|---|---|---|
| サービス経路（現構成） | `ServiceManagedEBSVolumeConfiguration` | 終了ポリシーのフィールドが**存在しない**＝必ず削除 |
| `RunTask` 経路 | `TaskManagedEBSVolumeConfiguration` | `DeleteOnTermination=false` で残せる |

ただし `RunTask` でも **既存ボリュームを指す項目が API に無い**（全 12 フィールドを確認: `RoleArn` /
`Encrypted` / `FilesystemType` / `Iops` / `KmsKeyId` / `SizeInGiB` / `SnapshotId` / `TagSpecifications` /
`TerminationPolicy` / `Throughput` / `VolumeInitializationRate` / `VolumeType`）。ECS は常に新規作成
するので、残したボリュームは再アタッチできず課金され続けるゴミになる。持ち越しは `SnapshotId` 経由のみ。

**金額の問題ではない**（EBS $0.096 < EFS $0.36 /GB-月）。Fargate に「速くて永続」が存在しないだけで、
本当に必要なら EC2 起動タイプ ＋ インスタンス stop（停止してもボリュームが残る）になる。
参考価格: m7i.large（2 vCPU/8 GiB）$0.1302/時 vs 同等 Fargate $0.1454/時（実測・ap-northeast-1）。

> **2026-08-15 追記: 検討済み → [64](64-ec2-persistent-workspace.md) / [ADR 0045](../decisions/0045-ec2-persistent-workspace.ja.md)。**
> sandbox で端から端まで実測した結果、**成立はする**（stop/start でも terminate → 付け替えでも home が残り、
> pull は 31.8s → 0.09s、小ファイル 2,000 個作成は EFS 30.7s に対し **0.04s**、費用も EC2 が安い）が、
> **起動は速くならず**（83.5s 対 Fargate ~84s）、罠が 4 件（**インスタンスタイプ変更を ECS が拒否する** /
> 停止インスタンスは自動 deregister されない / awsvpc の ENI 残留でパブリック IP が付かない / AZ 固定）ある。
> **本決定（決定 3 の置き場分割）が効き幅の大半を先に取る**ため、EC2 化は判定ゲート付きで見送った。

## 63.6 実装（P1〜P2・2026-08-15）

| 層 | 入ったもの |
|---|---|
| 保存 | `user_limit.cpu_limit`（migration 0044 / pg 0027）。`UserQuota` に 3 軸＋セッション数を集約 |
| 解決 | `resolveWorkspaceSize`（3 軸を一度に解いてテナント上限でクランプ）。`resolveWorkspaceMemBytes` はその薄い包み |
| テナント上限 | `max_workspace_cpu` / `max_workspace_disk_gb` |
| ECS | CPU を `fargateSize` の下限として反映。ディスクは 21〜200 GiB を `EphemeralStorage`、200 GiB 超を ECS 管理 EBS（`configuredAtLaunch` ボリューム＋サービスの `volumeConfigurations`・要 `AF_ECS_INFRA_ROLE`） |
| docker | `--cpus`（単位は Fargate units のまま保持し、渡すときに /1024） |
| 置き場 | ECS のときだけ `AF_WS_SCRATCH=/scratch` を注入。entrypoint が `go-build`/`uv`/`go/pkg/mod` を symlink で退避 |
| 生成物 | `af-scratch` ヘルパー（`af-scratch node_modules`）。手で張る経路 |
| 生成物（自動・P3） | Agent が clone / worktree 作成の直後に `af-scratch --auto <dir>` を叩き、`node_modules`/`target`/`.venv`/`build` を**空のうちに** symlink 化（§63.6.3） |
| 既定（P3） | `WsDiskGiB` / `AF_ECS_WS_DISK_GB` の既定を **0 → 50 GiB**。0 のままでは退避が一度も発火しなかった（§63.6.1） |
| Console | 上限の設定に CPU と作業ディスクを追加。S/M/L/XL/2XL は 3 軸を埋める近道 |
| MCP | `set_user_quota` に `cpu_units` を追加し、3 軸すべての post-clamp 値を返す |
| IaC | `WsTaskCpu` / `WsTaskMemory` / `WsDiskGiB`（既定は現行の挙動を変えない） |

### 63.6.1 退避のスイッチはディスクの設定そのもの

キャッシュの退避を常時有効にすると、Fargate 既定の 20 GiB（イメージ層と `/tmp` が同居）に
実測 10.5 GiB のキャッシュを載せることになり余裕が無い。そこで entrypoint は **作業ディスクの
実サイズを `df` で見て、30 GiB 未満なら何もしない**。結果として:

- 既定のままのデプロイ → 従来どおり全部 EFS（挙動の変化ゼロ）
- ディスクを広げたデプロイ → その瞬間に退避が有効になる

「機能を入れる」と「容量を用意する」が 1 つのノブになるので、容量不足で詰まる組み合わせが作れない。

**ただし既定を 0 のまま出したのは誤りだった（P3 で訂正）。** ノブが 1 つであることと、その
ノブを既定で切っておくことは別の話で、実際には **どのデプロイでも一度も発火しない**まま
「入っている」ことになっていた。既定を **50 GiB** に上げ、退避が既定で有効な状態にする。

- 50 GiB の根拠: entrypoint の発火閾値（`AF_WS_SCRATCH_MIN_GB` = 30 GiB）より上で、実測
  キャッシュ 10.5 GiB ＋ イメージ層 ＋ `/tmp` ＋ 生成物が同居しても余裕がある水準。
- 費用: 無料枠 20 GiB を超えた分だけ **$0.097/GiB-月・しかもタスク稼働中のみ**。
  30 GiB 上乗せで 24/7 なら月 $2.9、アイドル停止が効いていれば月 $1 未満。
- 逃げ道は `WsDiskGiB=0`（＝従来どおり全部 EFS）。
- **既存スタックは自動では上がらない**（CloudFormation はパラメータ値を保持する）。
  更新時に `WsDiskGiB=50` を明示する必要がある——ここは運用手順として `deploy/aws/ecs/README.md` に書いた。

### 63.6.2 まだ検証していないこと

- **ECS 管理 EBS の経路（200 GiB 超）は実機未検証。** インフラ IAM ロールが要り、参照スタックは
  それを作らない。ロール未設定なら無料既定へフォールバックする。マウント先の所有者が dev で
  ない可能性があり、その場合 entrypoint は退避をスキップして EFS のまま動く（ログに残す）。
- **`~/.local` の置き場**（§63.5.3）。
- **P3（既定 50 GiB ＋ 生成物の自動退避）も実 ECS では未検証。** ローカルのユニットテスト
  （`workspace/agent/scratch_test.go`・実物の `af-scratch.sh` を PATH に置いて叩く）と
  CP のファクトリテストまで。実機で確認すべきは「50 GiB で退避が実際に発火するか」と
  「clone 直後の symlink 越しに `npm ci` が通るか」の 2 点。

### 63.6.3 生成物の自動退避（P3）

`af-scratch node_modules` を手で張る形には、**効き幅が取れない**という致命的な穴があった。
退避できるのは「既にある node_modules」であり、その時点で **1 回目の `npm ci` は EFS 上で
走り終えている**（105 秒を払い済み）。そのうえ数万ファイルを EFS から読み直して移すので、
退避そのものも遅い。**速いのは「まだ無いうちに symlink を張っておく」形だけ**である。

そこで Agent が `gitClone` と worktree 作成の直後に `af-scratch --auto <dir>` を叩く
（`workspace/agent/scratch.go`）。マーカーから生成物の位置を決める:

| マーカー | 逃がす先 |
|---|---|
| `package.json` | `node_modules` |
| `Cargo.toml` / `pom.xml` | `target` |
| `pyproject.toml` | `.venv` |
| `build.gradle` / `build.gradle.kts` | `build` |

モノレポのため既定 3 階層まで走査し、`.git` と生成物ディレクトリ自身には降りない。安全側の規則は 4 つ:

1. 既に symlink → 触らない（利用者が親クローンへ張った共有かもしれない）
2. 実体があり **git が無視していない** → 触らない（**追跡物は絶対に動かさない**）
3. 実体があり git が無視している → 移して symlink に置き換える
4. 実体が無い → 空の逃がし先を作って symlink を張る（**この経路が本命**）

**既存の作業コピーには適用しない**（clone / worktree 作成時のみ）。停止をまたいで残った
巨大な木を再開時に移すと、セッションの起動がそのぶん止まるため。手で `af-scratch` を張る導線は残す。

代償として `[ -d node_modules ] || npm install` の形をしたスクリプトは「もう入っている」と
誤認する（空ディレクトリでも `-d` は真）。`AF_WS_SCRATCH_AUTO=0` で切れる。この事実は
`workspace/workspace-notes.md` に利用者/エージェント向けに書いた。

## 63.7 計測ハーネス

`~/af-efs/`（このセッションの Workspace）に残してある。`setup.sh`（EFS 2 本 ＋ SG ＋ cluster ＋
exec role）／ `bench.sh`・`bench2.sh`・`bench3.sh`（計測本体）／ `teardown.sh`（撤去）。
再現するときは **1 セッションで deploy → 計測 → teardown を閉じる**こと。今回の実行後、
EFS / cluster / SG / log group / IAM role / task definition がすべて 0 件であることを確認済み。

## 63.8 4 つ目のランタイムでは 3 軸の意味が変わる（`ecs-ec2`・2026-08-17）

本書が決めた 3 軸（メモリ・CPU・ディスク）は **Fargate と docker を前提にした意味**である。
[64](64-ec2-persistent-workspace.md) の `ecs-ec2` はスロットを 1 人で専有する形なので、
**同じ 3 軸が別のものを指す**——CPU は使われず、メモリは「上限」ではなく「乗る箱を選ぶ必要量」、
ディスクは作業ディスクではなく**永続 home の EBS サイズ**になる。

保存の形（ADR 0044 決定 1：ランタイム中立な独立した 3 つの数値）は**変えない**。
変えるのは「その値が何になるか」を**ランタイムが申告し、画面がその通りに言う**ことだけである。
調査は [64](64-ec2-persistent-workspace.md) §64.27、決定は
[ADR 0045](../decisions/0045-ec2-persistent-workspace.ja.md) 決定 21。

## 63.9 リソースの実測値は、ランタイムを問わず「中から」読む（2026-08-25）

§63.8 までは 3 軸を**指定する**側の話だった。ここは同じ 3 軸を**観測する**側である。

### 63.9.1 症状 — 稼働中なのにタイルが 3 つとも「–」

`ecs-ec2` 構成のメンバー詳細（テナント設定 > メンバー > 個人）で、「ワークスペースのリソース」の
メモリ・CPU・ディスクが**全部「–」のまま**だった。状態表示は「稼働中」なので、画面としては
「動いているのに何一つ測れていない」形になる。

原因は 1 か所で、`control-plane/metrics.go` の `containerStats` である。

| 軸 | 読み方 | 成立する前提 |
|---|---|---|
| メモリ / CPU | `docker inspect` で ID → ホストの `/sys/fs/cgroup/system.slice/docker-<id>.scope` | CP と Workspace が**同じホスト**にいる |
| ディスク | CP のローカル FS に `du -sb <dataDir>/home` | home のパスが**CP から見える** |

ECS のタスクには docker バイナリも対象の cgroup も home のパスも無い。**Fargate でも `ecs-ec2` でも
同じ**で、`ecs-ec2` 固有の話ではない。

`running` だけは既に手当て済みだった（§64.27 — `rt.State()` で上書きしないと強制停止ボタンが永久に
押せない）。ただしそれは真偽値 1 つを直しただけで、**ゲージ 3 本は誰も埋めていなかった**。

### 63.9.2 出どころは 1 つしか無い

コンテナの**中**では `/sys/fs/cgroup` が cgroup 名前空間でその Workspace 自身に張り替えられている。
つまりランタイムが何であれ、同じ 2 ファイル（`memory.current` / `cpu.stat`）を読むだけで済む。
ディスクも、中からなら home は自分のボリュームなので `statfs` 1 発で**使用量も容量も**分かる。

先例もある: `status.OOMKillCount` は以前からこの読み方で自分の `oom_kill` を数えていた
（docs/27 §10.2-2）。今回はそこへ軸を足しただけである。

- Agent: `workspace/agent/internal/resources`（cgroup を読む口をここへ集約。`status.OOMKillCount` は
  委譲するだけの薄い口になった）＋ `GET /workspace/stats`
- CP: `agent_client.go` の `agentStats` と、`metrics.go` の `workspaceStats`

⚠️ **`Runtime` インターフェースに `Stats()` は足していない。** 足しても分岐が生まれないからである
——docker と native は既にホスト側で読めており、ECS 系 3 つは AWS API から cgroup を取る手段が無い
ので**どれも同じ HTTP 呼び出しに落ちる**。5 実装のうち 4 つが同じ 1 行を書くインターフェースは
抽象ではなく重複である（[ADR 0058](../decisions/0058-workspace-resource-observation.ja.md) 決定 2）。
`workspaceStats` は安い順に「ホストの cgroup → State で稼働確認 → Agent へ問い合わせ」と落ちるので、
**docker / native の既存挙動は 1 バイトも変わらない**。

### 63.9.3 実装で効いた 3 つ

**① 測れなかった軸は 0 で埋めず、キーごと落とす。** `cpu_pct: 0`（本当に暇）と「CPU が測れない」は
別の事実である。ゼロで潰すと画面は測れないものを 0% として描き、**壊れたことが誰にも見えなくなる**
——今回の「–」は少なくとも異常を主張していたが、0% は何も主張しない。Agent 側の JSON は軸ごとに
ポインタ（`omitempty`）で、CP のデコードもポインタで受ける。`oom_kill_total: 0` も「present な 0」
として通す。

**② `State()` は 1 tick 1 回に束ねる。** `ecs-ec2` の `State()` は DescribeVolumes ＋
DescribeServices の実 API 呼び出しで**キャッシュが無い**。`/api/events` の tick は 4 秒で、そこでは
`workspacePayload` が既に State を引いている。stats 側で 2 本目を引くと購読者 1 人あたりの AWS
呼び出しがそのまま倍になるので、tick で 1 回引いた値を両方へ渡す形に変えた（`workspacePayload` は
state を引数で受け取る）。State をまだ引いていない呼び出し元は、値ではなく **thunk**
（`sync.OnceValue`）を渡す——ホスト側の読みが成功する構成では State を引かずに済む。

**③ 止まっている Workspace には問い合わせない。** 届かない相手を毎 tick 叩くと、タイムアウトぶん
だけ tick が遅れる（4 秒周期に 5 秒の待ちが入る）。Agent への問い合わせ用クライアントは共有の
2 分ではなく**専用の 5 秒**にしてある。共有クライアントのままだと、詰まった Agent 1 つが SSE の
tick を 2 分止める。

### 63.9.4 依存しているのは「イメージの中身」ではなく cgroup の版

この読み方はイメージに何も要求しない。`statfs` はコマンドではなく**システムコール**で、
Go の `syscall.Statfs` は libc も外部バイナリも介さず `SYS_STATFS` を直接呼ぶだけである
（CP が使う `du` の方はイメージ依存のバイナリで、そこが違う）。amd64 / arm64 の両方で
`CGO_ENABLED=0` のビルドが通ることを確認済み。

**実際に効く依存は `/sys/fs/cgroup` が v2 か v1 か**である。

- **`ecs-ec2` は v2 で確定。** スロットの AMI は `amazon-linux-2023` の ECS-optimized に
  固定されており（`deploy/aws/ecs/cfn/40-ec2-pool.yaml:21`）、AL2023 は cgroup v2 が既定。
- **Fargate は分からない。** CP は `PlatformVersion` を一切渡していない（＝ LATEST）ので、
  下回りの版を我々が選んでいない。

そこで軸ごとに **v2 → v1 の順**で読む。v1 のホストに当たったときの症状は「メモリと CPU
だけ黙って –」＝**この修正が直したはずの見た目にそのまま戻る**ので、名前の違いだけで
そこへ落ちるのは割に合わない。

| | v2 | v1 |
|---|---|---|
| メモリ | `memory.current` | `memory/memory.usage_in_bytes` |
| 上限 | `memory.max`（`"max"` = 無制限） | `memory/memory.limit_in_bytes`（**巨大値**が無制限） |
| CPU | `cpu.stat` の `usage_usec`（**µs**） | `cpuacct/cpuacct.usage`（**ns**） |
| OOM | `memory.events` の `oom_kill` | `memory/memory.oom_control` の `oom_kill` |

⚠️ 表の右列に罠が 2 つある。**v1 の「上限なし」は数値として読めてしまう**
（`9223372036854771712`）ので、閾値で弾かないと「上限 8 EiB」を分母にした使用率 0% を
描く——v2 の `"max"` のように読めずに落ちてはくれない。**単位も違う**（ns と µs）ので、
揃え忘れると使用率が 1000 倍になり、静止中の Workspace が数万 % に見える。どちらも
テストで固定した（外すと実際に 50000% と 8 EiB が出ることを確認済み）。

⚠️ **実機で測ったのは v2 側だけ**である。v1 側はフィクスチャでの検証にとどまり、v1 の
ホストを用意して確かめたわけではない。

### 63.9.5 ディスクの分母

`user_limit.disk_gb` は設定値であって実測ではなく、`ecs-ec2` では**作成時にしか効かない**
（ADR 0045 決定 21）。したがって画面の割合は**実測の容量（`disk_total`）を優先**し、無いときだけ
設定値（`disk_quota`）へ落ちる。

ホスト側の `du` が使える構成では `du` の値を優先し続ける。停止中の Workspace でも読める唯一の
数字で、棚卸しに要るからである。

| 構成 | `disk_used` | `disk_total` |
|---|---|---|
| docker / native | CP の `du -sb <dataDir>/home`（60 秒キャッシュ） | 無し（表示上のクォータのみ） |
| `ecs-ec2` | Agent の `statfs`（永続 home の EBS） | **有り**（EBS のサイズそのもの） |
| Fargate | Agent の `statfs`（作業ディスク） | 有り |

### 63.9.6 ついでに直ったもの

同じ経路を WS バーの自分用リソースチップ（`/api/workspace/stats`）も通るので、**ECS 構成では出て
いなかったチップが出るようになる**（Console は `mem_used` の有無でチップの表示を決めている）。
コンテナ内 OOM の検出（`oom_recent`）も同様に ECS で効くようになった——累積カウンタの「増加」を
見る導出は CP 側にあるので、コンテナ ID の代わりに Workspace 名で追跡する。
