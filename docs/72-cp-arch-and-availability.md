# 72. Control Plane のアーキ選択と可用性

[70](70-slot-instance-classes.md) は **Workspace が載る箱**をアーキごと選べるようにした。
本書は同じ問いを **Control Plane 自身**に向ける——CP のイメージを amd64/arm64 の 2 アーキ
インデックスにし、Fargate をどちらで走らせるかを運用者が選べるようにする。

そして途中で当然出てくるもう一つの問い、**「その CP は何台で走っているのか」**（オート
スケールと冗長化）も同じ場所に置く。アーキを変える操作はローリングデプロイであり、
ローリングデプロイの挙動は台数の設計そのものだからである（§72.7）。

関連: [70](70-slot-instance-classes.md) §70.8/§70.9（スロット側のアーキと multi-arch 化）・
[35](35-packaging.md)（出荷経路）・[64](64-ec2-persistent-workspace.md)（ecs-ec2）。
決定は [ADR 0053](decisions/0053-cp-arch-and-availability.md)。

## 72.1 現状（2026-08-23・実物で確認）

| | 実測値 | 出どころ |
|---|---|---|
| CP のタスク定義 `runtimePlatform` | **`null`** | `describe-task-definition af-af-ecs-ingress-cp:18` |
| CP が実際に走っているアーキ | **X86_64** | Fargate は省略時に既定を入れる（§72.2） |
| CP イメージのマニフェスト | **単一 amd64**（`manifest.v2+json`） | `ecr batch-get-image`（`"manifests"` 無し） |
| Workspace イメージのマニフェスト | **2 アーキ index**（amd64,arm64） | 同上（[70](70-slot-instance-classes.md) P5 の成果） |
| RDS | `db.t4g.micro` ＝ **すでに Graviton** | `describe-db-instances` |

つまり**この配備はもう ARM の上で動いている**——データ層だけが。計算層で ARM に
なっていないのは、そう決めたからではなく **CP イメージに arm64 の面が無いから**である。

`control-plane/Dockerfile` は 70 行で、**アーキ依存のダウンロードが 1 つも無い**
（3 ステージとも公式のマルチアーキイメージ）。[70](70-slot-instance-classes.md) §70.9 が
workspace について書いた「**レシピは書けている。一度も焼いていないだけ**」が、CP では
もっと強く当てはまる。分岐すら要らない。

## 72.2 Fargate ARM64 の一次情報（先に潰した）

計画ごと変わりうるのはここだけなので最初に確認した。**通る。**

- **リージョン**: Fargate で 64bit ARM が使えないのは **us-east-1 の `use1-az3` AZ だけ**。
  `ap-northeast-1` に制限は無い（[ECS 開発者ガイド: 64-bit ARM workloads]）。
- **プラットフォームバージョン**: `1.4.0` 以上。この配備は `LATEST`＝条件を満たす。
- **OS**: Linux のみ。CP は Linux。
- **⚠️ Service Connect**: ここが唯一の懸念だった。CP は Workspace の Agent へ
  Service Connect クライアントとして到達する（`30-ingress.yaml` の
  `ServiceConnectConfiguration`）ので、これが arm64 で使えないなら計画は成立しない。
  **2022 年 12 月に AWS が Fargate/Graviton 対応を発表済み**で、Service Connect の
  考慮事項一覧に CPU アーキの制限は 1 行も無い（制限として挙がっているのは Windows /
  HTTP 1.0 / スタンドアロンタスク / blue-green / ECS Anywhere / PPv2 / FIPS）。
- **タスクサイズ**: `cpu`/`memory` の組み合わせに arm64 固有の制限は記載が無い。
  CP の 512/1024 はそのまま。
- **`cpuArchitecture` の既定**: `X86_64`。**⚠️ ただし「タスクが起動するときに入る」**の
  であって、登録時に書かれるわけではない——だから `describe-task-definition` は
  `null` を返し、テンプレートにもどこにも「amd64 で動いている」とは書いていない。
  [70](70-slot-instance-classes.md) §70.8 が記録した EC2 との非対称（EC2 は `null` の
  まま既定化されない）はここと表裏である。

**価格（Pricing API・ap-northeast-1・2026-08-23 実測）**:

| | vCPU-時 | GB-時 |
|---|---:|---:|
| Fargate x86 | $0.050560 | $0.005530 |
| Fargate ARM | $0.040450 | $0.004420 |
| 差 | **−20.0%** | **−20.1%** |

⚠️ **これを節約の話にしてはいけない。** CP は 0.5 vCPU / 1 GB なので、24/7 で
**$22.49/月 → $17.99/月、差は月 $4.5** である。arm64 化の理由は費用ではなく
**選べること**（と、それを選べる状態が壊れていないと分かっていること）。

[ECS 開発者ガイド: 64-bit ARM workloads]: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs-arm64.html

## 72.3 ★ 芯: CP は QEMU を通してはいけない

⚠️ **`buildx --platform linux/amd64,linux/arm64` と素直に書くと、arm64 側は
Console の Vite ビルドと Go のコンパイルを QEMU エミュレーションで走らせる。**

[70](70-slot-instance-classes.md) §70.9.2 が workspace で測った QEMU の税は
**arm64 単体で実機 Graviton の約 5 倍**だった。そして同じ節が、なぜ 5 倍で済んだのかを
明記している——**中身がダウンロードと apt に支配された I/O 寄りだから**で、
**コンパイルが増えれば比率は動く**、と。**CP はそのコンパイル寄りの側**である。
つまり workspace の 5 倍は CP に対する見積りとして使えない。

避けられる。しかも 3 ステージのうち 2 つは**そもそも 2 回焼く必要が無い**:

| ステージ | 中身 | 焼き方 |
|---|---|---|
| `console` | Vite + React → `dist` | **成果物は JS と CSS＝アーキ非依存。** `--platform=$BUILDPLATFORM` でビルダー上に 1 回だけ。両アーキが同じ `dist` を COPY する |
| `build` | Go → 静的バイナリ | `CGO_ENABLED=0` なので **`GOARCH=$TARGETARCH` でクロスコンパイル**。Go は準備を何も要らない。`--platform=$BUILDPLATFORM` に固定すればコンパイラは常にネイティブ |
| `runtime` | debian-slim + apt + docker CLI | **ここだけ**ターゲットごと。[70](70-slot-instance-classes.md) が「税が軽い」と測った I/O 寄りの領域そのもの |

**残る emulation は `apt-get install ca-certificates git` 一発だけ**になる。
うまくやれば **CP の multi-arch 化は workspace より安い**——という見立てを、
信じるのではなく測る（§72.5）。

⚠️ **この最適化は「壊れる」形では失敗しない。** `--platform=$BUILDPLATFORM` を
1 つ落としても、`GOARCH` を落としても、**出てくるイメージは正しい**。違いは
ビルド分数だけである。だから Dockerfile のヘッダに理由を書き、計測ワークフローには
**ピンを剥がした反実仮想**（`-naive`）を並べて焼かせて、差を数字で残す。
「クロスコンパイルしています」を信念のままにしない。

⚠️ 逆に `COPY --from=docker:29-cli` には `--platform` を**付けてはいけない**。
あれはイメージの中で**実行される**バイナリなので、ターゲットに追随させる必要がある
（BuildKit は無指定の `--from=<image>` をターゲット側で解決するので、無指定が正解）。
ここを間違えると arm64 イメージの中に amd64 の `docker` が入り、症状は初回実行時の
`Exec format error` だけになる。

## 72.4 実装

### 72.4.1 焼く側

- **`control-plane/Dockerfile`**: `console` / `build` を `FROM --platform=$BUILDPLATFORM`、
  `ARG TARGETARCH` ＋ `GOARCH="${TARGETARCH:-$(go env GOARCH)}"`。
  ⚠️ **フォールバックを `$(go env GOARCH)` にしてある**のは、古いビルダーでは
  `TARGETARCH` が空になるからで、そこで空の `GOARCH` を渡すと「ホストのアーキ」ではなく
  **どのホストでも amd64** になり、arm64 のマシンで黙って誤ったバイナリが出る。
- **`deploy/compose/release.sh`**: `CP_PLATFORMS`（`WS_PLATFORMS` と同じ作法・
  **`--push` 必須**——マニフェストリストは `--load` できない・`--save` とは排他）。
  ⚠️ **`WS_PLATFORMS` とは独立**にした。2 つのイメージは別の問いに答えている——
  workspace の arm64 は「スロットが Graviton になりうる」から、CP の arm64 は
  「サービス自体を Graviton に置きたい」から。**片方だけ欲しい配備は普通にある。**
- **`deploy/release/build.sh`**: 素通し。native パッケージ（C/R）は amd64 のまま
  （[35](35-packaging.md) §35.3.1・あれは 1 台への手渡し）。
- **`.github/workflows/publish-dist.yml`**: 入力 `control_plane_arm64`（**既定 OFF**）。
  QEMU/buildx のセットアップは `workspace_arm64` と共用し、押した後に
  **レジストリへ問い合わせて 2 アーキ index であることを確かめる**。

### 72.4.2 走らせる側

- **`30-ingress.yaml`**: `CpArch`（`x86_64` / `arm64`・**既定 `x86_64`**）を足し、
  `TaskDef` に `RuntimePlatform` を**明示**。
  ⚠️ **既定値のままでも明示することに意味がある。** いまは `null` の暗黙既定なので、
  「この CP が何のアーキで動いているか」はテンプレートのどこにも書かれておらず、
  Fargate がその場で決めている。明示すればタスク定義の同一性（fingerprint）にも入るので、
  `CpArch` を動かせば確実に新リビジョン＝ローリングデプロイになる。
- **`deploy/aws/ecs/update.sh`**: 落とし穴 1（タグが ECR に無い）の兄弟として、
  **`CpArch` とイメージのアーキが噛み合っているか**を deploy 前に見る。

- **`deploy/aws/ecs/release-ecr.sh`**: ⚠️ **この経路はローカル docker を通るので、
  構造上マルチアーキを運べない**（マニフェストリストは `--load` できないので、
  そもそもローカルに存在しない）。従来は `docker tag` の `No such image` としてだけ
  現れ、「ビルドが失敗した」ようにしか読めなかった。**レジストリ間コピー（`crane copy`）
  を案内して落とす**ようにした——実デプロイ手順が `crane` を使っているのはこのためである。

⚠️ **噛み合わなかったときの症状が悪い。** `CannotPullContainerError` ですらない——
ECS はタスクを**配置できない**まま `desired=1 / running=0` で回り続け、
pull エラーのログすら出ない（[70](70-slot-instance-classes.md) §70.5 の
「配置拒否」と同じ形）。しかも `control_plane_arm64` は既定 OFF なので、
**通常のリリース版に `CpArch=arm64` を当てると必ずこれになる。**
判定は**証明できたときだけ落とす**: index ならアーキ一覧を読んで断定でき、単一
マニフェストなら中身は読めないので「arm64 を要求している」ときだけ落とす
（`AF_CP_ARCH_CHECK=0` で外せる。ここは公開ゲートではなく更新の前検査なので、
確かめられなかったことを理由に運用者を止めない）。

## 72.5 計測（`arm64-image-time.yml`）

[70](70-slot-instance-classes.md) §70.9 が workspace のために作った計測ワークフローを、
`target` 入力（`control-plane` / `workspace`）で CP にも向けられるようにした。

⚠️ **`publish-dist.yml` を検証目的で走らせてはいけない**——GHCR と dist repo への
**公開**までやる。使い捨ての `armtime-<run id>` タグへ押して捨てる形は据え置き。

出す数字は 4 つ:

| | 何を答えるか |
|---|---|
| amd64 単独 | 基準 |
| amd64 + arm64（クロスコンパイル） | **本線の値**。publish-dist に足す分 |
| amd64 + arm64（`$BUILDPLATFORM` ピンを剥がした反実仮想） | §72.3 の主張の値段 |
| 差 | クロスコンパイルが買ったもの |

**⚠️ 計測ごとにビルダーを作り直す。** 同じビルダーで続けて焼くと 2 本目が 1 本目の
レイヤを再利用し、**変えた場所がちょうど再利用される場所**なので、比較にならない。
作り直しは実物にも忠実である（publish-dist は毎回まっさらなランナーで走る）。

**⚠️ index が 2 アーキだと言うことと、arm64 の中身が arm64 であることは別の主張。**
クロスコンパイル経路はまさにその 2 つが乖離しうる場所で、`GOARCH` を間違えれば
**完全に整形式な index の arm64 側に amd64 の ELF が入る**。`imagetools inspect` は
何も言わない。だから QEMU が入っているうちに実際に起動させ、`uname -m` と
**`af-cp` の ELF `e_machine`（`b700` = AArch64）**まで見る。

**結果**: ⏳ 未実走。`workflow_dispatch` は**デフォルトブランチにファイルが無いと 404**
（`--ref` を指しても）なので、実走は develop へマージした後。

## 72.6 実機（lazmix）

⚠️ **acrt は触らない**（実ユーザーの Workspace が動いている。`ImageTag` を変えると
その人に要再起動バッジが出る）。

手順は [70](70-slot-instance-classes.md) §70.14 の実績どおり: イメージを焼く →
`crane index append` → `ImageTag` 差し替え（`update-stack --use-previous-template` で
**`ImageTag` だけ**上書き・他は `UsePreviousValue=true`・`--capabilities CAPABILITY_NAMED_IAM`）
→ CP ログで `control-plane <tag>` を確認。

**arm64 に切り替えたら確かめること**:

1. CP が実際に上がる（`runningCount` の遷移を見る。**⚠️ `ImageTag` が同じなら
   task def の revision は変わらない**ので「rev が変わるまで待つ」監視は永遠に終わらない）。
2. **Service Connect 経由で Workspace の Agent に到達できる。**
   SC の別名は**タスク起動時のスナップショット**で、引けなかったときの Cloud Map
   フォールバック（`control-plane/agent_dial.go`）は**共有 Transport を通る経路だけ**に
   効く——`http.DefaultClient` を使う経路は素通りする。**アーキを変えた CP でここが
   壊れないか**はコードから言えることではない。
3. ⚠️ **`cpuArchitecture` は「1 つのサービスで使われるすべてのタスク定義で同じ値でなければ
   ならない」と AWS のドキュメントが書いている。** ローリングデプロイ中は新旧 2 リビジョンが
   同居する（§72.7 で実測したとおり ~51 秒）ので、**アーキ切替のデプロイがそこで
   拒否されないか**は一次情報だけでは決められない。実機で確かめる項目。

**結果**: ⏳ 未実施。

## 72.7 CP のオートスケールと冗長化——実状

アーキを切り替える操作はローリングデプロイであり、その挙動は台数の設計そのものである。
**実物を見た（2026-08-23・lazmix）。**

### 72.7.1 測ったこと

| | 実測値 |
|---|---|
| `DesiredCount` | **1**（`30-ingress.yaml` にリテラルで 1。リポジトリ全体で `DesiredCount` はこの 1 箇所だけ） |
| Application Auto Scaling のスケーラブルターゲット | **0 個** |
| スケーリングポリシー | **0 個** |
| デプロイ設定 | `ROLLING` / `minimumHealthyPercent=100` / `maximumPercent=200` / **サーキットブレーカ無効** |
| RDS | **`MultiAZ: false`**（テンプレートにリテラル。パラメータですらない）・`BackupRetentionPeriod` はサンドボックスで **0** |
| ALB | 2 AZ にまたがる。ターゲットグループに**スティッキネス設定なし** |

**結論: CP にオートスケールは無く、冗長化も無い。** 1 タスク・1 リージョン・1 AZ の
データベース。これは事故ではなく、そう書いてある（が、**どこにも「そう決めた」とは
書かれていなかった**——本節がその記録である）。

### 72.7.2 ⚠️ ただし「2 台同時」はもう毎回起きている

`minimumHealthyPercent=100` ＋ `desired=1` なので、**ECS は古いタスクを止める前に
新しいタスクを起こす**。サービスイベントの実測（2026-08-23 14:36–14:37）:

```
14:36:43  has started 1 tasks: (task 71d0e0…)      ← 新
14:37:24  registered 1 targets
14:37:34  has stopped 1 running tasks: (task 41cbdd…)   ← 旧
14:37:44  has begun draining connections on 1 tasks
```

**新旧 2 つの CP が約 51 秒重なる。** アップグレードのたびに、である。

⚠️ **[70](70-slot-instance-classes.md) §70.14.10 に「見るべきは `runningCount` の
1→0→1」と書いたのは誤り**で、正しくは **1→2→1**。デプロイ無停止はこの重なりが
作っているので、ここを読み違えると「一瞬落ちる」前提で運用設計をしてしまう。

つまり **「CP は 1 台」は「CP のコードは 1 台前提でよい」を意味しない。** コードは
実際そう書かれていない——`pg_try_advisory_lock` による Workspace 単位のフェンス
（`AcquireWorkspaceOperationFence`）、レプリカ間の presence リース、golden の
`dropSupersededGoldens` が「別レプリカが同じ窓で焼いた候補」まで面倒を見ること、
`BackupHome` が「2 レプリカが同時に撃っても余分な増分 1 回で済む」と明記していること——
**設計は最初から複数 CP を織り込んでいる。**

### 72.7.3 それでも `DesiredCount` を上げてはいけない（いまは）

読んだ結果、**常時 2 台にすると壊れるものが具体的にある**。51 秒の重なりで顕在化しないのは、
確率が低いか、被害が 1 回分で収まるからにすぎない。

| | 2 台での挙動 | 重さ |
|---|---|---|
| **定時実行**（`scheduler.go`） | `tickAt` は `ListDueSchedules` → **発火 → その後に台帳を前進**（`fireOne`）。2 レプリカが同じ窓で走れば**両方が同じ slot を発火**する。コード自身が「reuse/assistant 経路には slot 単位の冪等機構が無く**プロンプトの二重配達**になり得る」と書いている | 🔴 **利用者に見える二重実行** |
| **GitHub device flow**（`oauth_github_device.go`） | 進行中フローは**プロセスメモリ**。開始した CP と polling が当たる CP が違えば flow が無い。コードのコメントが「multi-instance CP はスティッキールーティングか DB への退避が要る」と既に言っている | 🔴 **連携の設定が失敗する** |
| **golden ベイカー** / **ec2 プールのスイーパー** | どちらも AWS のタグから世界を導出するので破壊はしない。ただし 2 台が同時に焼き始めればスロットを 2 倍に食い、**空き 2 スロットの歯止め**（[64](64-ec2-persistent-workspace.md) §64.29）は片方ずつしか見ない | 🟡 費用と枯渇 |
| **使用量 / コスト収集**（`usage.go` / `cloudcost.go` / `claude_audit.go`） | 同じ窓を 2 回集める経路が二重計上になるかは**未確認** | 🟡 要調査 |
| Workspace の Start/Stop | `pg_try_advisory_lock` で**フェンス済み** | ✅ |
| SSE（`/api/events`） | 接続ごとにストアから合成するだけで**ハブを持たない** | ✅ |
| メタデータ | RDS Postgres＝共有 | ✅ |

**したがって「CP を 2 台にする」は設定変更ではなく作業**である。最低限、定時実行を
「発火の前に台帳を CAS で取る」形にし、device flow をストアへ退避する必要がある。

### 72.7.4 ⚠️ そして一番弱い環は CP の台数ではない

仮に上を全部直して `DesiredCount: 2` にしても、**両方のタスクは同じ単一 AZ の RDS を
見ている**。`MultiAZ: false` はテンプレートにリテラルで書かれていて、パラメータですら
ないので運用者には**上げる手段が無い**。可用性は一番弱い環で決まるので、**CP を 2 台に
することは、いまはその環を動かさない。**

順序を付けるなら:

1. `MultiAZ` をパラメータにする（既定 `false`。**コストが 2 倍になるので既定は動かさない**）。
   ⚠️ [ADR 0044 決定 3](decisions/0044-workspace-sizing.md) の「既定オフで出した機能は
   存在しないのと同じ」が効くので、出すなら画面か README に「入れるとき」を書く。
2. 定時実行の CAS 化と device flow の退避（§72.7.3 の 🔴 2 件）。
3. そのうえで `CpDesiredCount` をパラメータにする。

⚠️ **1 と 2 を飛ばして 3 だけ出すのが一番悪い。** 「冗長化した」と読める設定が増え、
実際には**二重発火という新しい障害**を足しただけになる。

⚠️ **サーキットブレーカが無効**なのは、この構成では**むしろ安全側**である。
`minimumHealthyPercent=100` なので新タスクが上がらなければ旧タスクが残り続ける——
`CpArch` を間違えても**現用は落ちない**（CFN のスタック更新がタイムアウトで
ロールバックするまで待つ形になる）。⏳ **実機で確かめる。**

## 72.8 フェーズ

| | 内容 | 出口 |
|---|---|---|
| **P0** ✅ | Fargate ARM64 の一次情報（リージョン・PV・**Service Connect 併用可否**）と価格実測 | §72.2。**通る**。ap-northeast-1 に制限なし・SC は 2022-12 から対応 |
| **P1** ⏳ | `arm64-image-time.yml` で CP のビルド時間を実測（クロスコンパイルあり／なし） | §72.3 の見立てが数字になる。**マージ後にしか走らせられない** |
| **P2** ✅ | Dockerfile / `release.sh` / `build.sh` / `publish-dist.yml` / `30-ingress.yaml` / `update.sh` | index を作れて、`CpArch` で選べて、噛み合わない組合せは deploy 前に落ちる |
| **P3** ⏳ | 実機（lazmix）: arm64 の CP が上がり、**Service Connect で Agent に届く** | 選択肢が本物になる |
| **P4** — | §72.7.3 の 🔴 2 件 ＋ `MultiAZ` パラメータ ＋ `CpDesiredCount` | 冗長化を語れるようになる（**本書では実装しない**） |

## 72.9 未決

1. **既定を arm64 に動かすか。** 動かさない。既定は `x86_64` のまま
   （[70](70-slot-instance-classes.md) の「既定は誰も勝手に動かさない」と同じ理由）。
   月 $4.5 は既存配備を動かす理由にならない。
2. **`control_plane_arm64` を既定 ON にするか。** P1 の数字を見てから。
   通常のリリースを 1 アーキのままにしておく限り、`CpArch=arm64` は
   「専用に焼いた版でしか使えない機能」であり続ける——**それは半端**なので、
   税が許容範囲なら ON に倒すべきである（未決なのは税を知らないから）。
3. **§72.7.4 の順序を実際にやるか。** 「1 台で足りている」なら何もしないのが正しい。
   ⚠️ ただし**足りているかどうかを測っていない**（CP タスクの CPU/メモリの実績も、
   単一障害点が実際に落ちた回数も見ていない）。やらない判断をするにも数字が要る。
