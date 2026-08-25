# 0058. Workspace のリソース実測は「中から自分の cgroup を読む」1 本にし、Runtime の口は増やさない

- 状態: **採用・実装済み**（2026-08-25）。設計と経緯は [docs/63 §63.9](../63-workspace-sizing.md#639-リソースの実測値はランタイムを問わず中から読む2026-08-25)。
- 関連: [0044-workspace-sizing.md](0044-workspace-sizing.md)（3 軸を**指定する**側の決定。本 ADR は同じ 3 軸を
  **観測する**側） /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) 決定 21（同じ 3 軸がランタイムで別のものを
  指す。ディスク＝永続 home の EBS） /
  [0029-usage-accounting.md](0029-usage-accounting.md)（同じ「CP が Agent に聞く」形の先例）

## 背景

メンバー詳細の「ワークスペースのリソース」（メモリ / CPU / ディスク）が、`ecs-ec2` 構成では
**3 つとも「–」のまま**だった。稼働中の表示は出ているので、画面としては「稼働しているのに何も
測れていない」状態である。

原因は 1 か所しかない。`control-plane/metrics.go` の `containerStats` は

1. `docker inspect` でコンテナ ID を引き、
2. ホストの `/sys/fs/cgroup/system.slice/docker-<id>.scope` を読み、
3. ディスクは CP のローカル FS に対して `du -sb <dataDir>/home` を走らせる

という**「CP と Workspace が同じホストに載っている」前提の読み方**だった。ECS のタスクには docker
バイナリも対象の cgroup も home のパスも無い。Fargate でも `ecs-ec2` でも同じで、`ecs-ec2` 固有の
問題ではない。

`running` だけは既に手当てされていた（docs/64 §64.27・強制停止ボタンが永久に押せなかった件）が、
それは `rt.State()` で上書きしていただけで、ゲージ 3 本は誰も埋めていなかった。

## 決定 1 — 観測値の出どころは Workspace の中に 1 つだけ置く

**コンテナの中から `/sys/fs/cgroup` を読む。** cgroup 名前空間により、中から見た `/sys/fs/cgroup` は
その Workspace 自身に張り替えられている。つまり**ランタイムが何であれ同じ 2 ファイルを読むだけ**で
メモリと CPU が取れる。ディスクも同様に、home が載る FS の `statfs` で使用量と容量が 1 回の
システムコールで取れる。

これは新しい発想ではなく、既にそう読んでいる先例がある: `status.OOMKillCount` は以前からこの方法で
自分の `oom_kill` を数えていた（docs/27 §10.2-2）。今回はそこに軸を足しただけで、実装は
`workspace/agent/internal/resources` に集約し、`status.OOMKillCount` はそこへ委譲する薄い口にした。

## 決定 2 — `Runtime` インターフェースに `Stats()` は足さない

一見すると「ランタイム毎に違うのだからランタイムの口を増やす」のが筋に見えるが、**増やしても
分岐は生まれない**。docker と native は既にホスト側で読めており、ECS 系 3 つは AWS API から
cgroup を取る手段が無いので**どれも同じ HTTP 呼び出しに落ちる**。5 実装のうち 4 つが同じ 1 行を
書くインターフェースは、抽象ではなく重複である。

代わりに `metrics.go` に `workspaceStats(ctx, mgr, rt, state)` を置いた。安い順に:

1. ホスト側の cgroup 読み（docker / native ではこれで完結。既存挙動は 1 バイトも変えない）
2. 空振ったら `rt.State()` で `running` を確かめ、
3. 稼働中のときだけ Agent の `GET /workspace/stats` に聞く。

`Runtime` の口は 6 つのまま（Start / Stop / State / Endpoint / Token / Name）である。

## 決定 3 — 測れなかった軸は「キーごと落とす」。0 で埋めない

`cpu_pct: 0`（本当に暇）と「CPU が測れない」は別の事実である。ゼロ値で潰すと、画面は測れない
ものを 0% として描き、**壊れたことが誰にも見えなくなる**——今回の「–」がそうであったように、
空欄は少なくとも異常を主張していた。0% は主張しない。

そのため Agent 側の JSON は軸ごとにポインタ（`omitempty`）で、CP 側のデコードもポインタで受ける。
`oom_kill_total: 0` も同じ理由で「present な 0」として通す。

## 決定 3.5 — cgroup は v2 を主、v1 を副として両方読む

この読み方が要求するのはイメージの中身ではない（`statfs` はシステムコールで、
`syscall.Statfs` は libc も外部バイナリも介さない）。効くのは **`/sys/fs/cgroup` の版**である。

`ecs-ec2` は v2 で確定している——スロットの AMI は `amazon-linux-2023` の ECS-optimized に
固定（`deploy/aws/ecs/cfn/40-ec2-pool.yaml:21`）。一方 **Fargate は `PlatformVersion` を
渡していない**ので下回りを我々が選んでいない。

v2 のファイル名しか読まない実装だと、v1 のホストでの症状は「メモリと CPU だけ黙って –」
——**本 ADR が直したはずの見た目にそのまま戻る**。ファイル名と単位の違いだけでそこへ
落ちるのは割に合わないので、軸ごとに v2 → v1 の順で読む。

⚠️ v1 側には罠が 2 つある。**「上限なし」が数値として読めてしまう**
（`9223372036854771712`。v2 の `"max"` のように読めずに落ちてはくれない）ので閾値で弾く、
**単位が ns で v2 の µs と違う**ので揃える。どちらも外すと 8 EiB と 50000% が実際に出る
ことをテストで確認した。

⚠️ **実機で測ったのは v2 側だけ**で、v1 側はフィクスチャ止まりである。

## 決定 4 — `State()` は 1 tick 1 回に束ねる

`ecs-ec2` の `State()` は DescribeVolumes ＋ DescribeServices の**実 API 呼び出しでキャッシュが無い**。
`/api/events` の tick は 4 秒で、そこでは `workspacePayload` が既に State を引いている。ここに
2 本目の State を足すと、購読者 1 人あたりの AWS 呼び出しがそのまま倍になる。同じ tick の中で
値は変わらないのだから、引くのは 1 回にして両方へ渡す。

管理画面のポーリング経路のように「State をまだ引いていない」呼び出し元は、値ではなく
**thunk**（`sync.OnceValue`）を渡す。ホスト側の読みが成功する構成（docker）では、そもそも
State を引かずに済む。

## 決定 5 — ディスクは「実測の容量」を割合の分母にする

`user_limit.disk_gb` は設定値であって実測ではない。とくに `ecs-ec2` では **作成時にしか効かない**
（EBS のサイズは後から数字を変えても追随しない・ADR 0045 決定 21）。したがって実測の `disk_total`
が取れているならそちらを分母にし、無いときだけ設定値へ落ちる。

ホスト側の `du` が使える構成では、`du` の値（ホーム木そのものの大きさ）を優先し続ける。停止中の
Workspace でも読める唯一の数字で、棚卸しに要るからである。

## 捨てた選択肢

- **CP に ECS の API を叩かせて CloudWatch のメトリクスを引く。** 粒度が粗い（既定 1 分、
  Container Insights は有料）うえ、docker 構成では別経路のままになる。「同じ画面の同じタイルが
  構成ごとに別のものを意味する」のは、この面で一番避けたい形である。
- **ディスクを Agent 側でも `du` で歩く。** home が大きいほど高くつくので CP 側は 60 秒キャッシュ
  している。中からなら home は自分のボリュームなので `statfs` 1 発で済み、しかも `ecs-ec2` では
  **容量まで**分かる（`du` には分母が無い）。
- **`GET /healthz` にゲージを相乗りさせる。** 死活監視の応答を重くすると、監視が遅い時に
  タイムアウトの意味が変わる。別の口にした。
