# 0087. EFS のメタデータ I/O——暫定は elastic のまま据え置き、恒久は「EFS に置くものを資格情報だけに戻す」

[English](0087-efs-metadata-io.md) | 日本語

- Status: **提案**（2026-09-17）。実装は CFN テンプレートの 1 箇所だけ入っている（下の決定 2）。
- **何を、どこで測ったか。** 数字は 3 つの出どころしか無い。①本番配備の EFS の CloudWatch
  メトリクス（`MeteredIOBytes` / `MetadataIOBytes` / `ClientConnections`、2026-09-04〜09-17）、
  ②Cost Explorer の実費と Pricing API の ap-northeast-1 の単価（2026-09-17 取得）、
  ③**Workspace コンテナ内で `strace -c` を当てた実測**——エージェントの実コードを
  テストバイナリに固めて子プロセスとして起動し、ファイル系システムコールを数えた
  （走行中の Agent への attach は `ptrace_scope=1` で拒否されるので、子として起こす形にした）。
  **本番の箱に対する strace は 1 回も行っていない。**
- 関連: [0044-workspace-sizing.ja.md](0044-workspace-sizing.ja.md)（`~` がどこに載っているか）/
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md)（`keep` ボリュームを
  導入した決定。ここで想定していた中身と実際の中身がずれている）

## 背景

### 2026-09-16 に起きたこと

本番配備が全体的に遅くなった。真因は API でもデータベースでもなく、その下のマウントだった。
Workspace の設定・資格情報を載せている EFS が `ThroughputMode: bursting` で、**バーストクレジットを
使い切っていた**。bursting のベースラインは Standard ストレージ 1 GiB あたり 50 KiB/s であり、
この EFS は設定と資格情報しか持たない（約 2.6 GiB）ので**ベースラインは約 0.13 MiB/s**。クレジットが
尽きた瞬間、全 Workspace がそこへ張り付いた——2.4 KB のファイルの `openat` に 5.3 秒かかった。

なぜ「全部」遅くなるのかは、EFS に何が載っているかで決まる。本番の Workspace は EC2 起動タイプで、
ボリュームは 3 つ:

| ボリューム | 置き場所 | 中身 |
|---|---|---|
| `home` → `/home/dev` | **インスタンスの EBS**（ホストパス） | `~/repos` を含む home の実体 |
| `claude` → `/var/lib/af/claude` | **EFS**（アクセスポイント） | `CLAUDE_CONFIG_DIR`。`projects/`・転写・設定 |
| `keep` → `/var/lib/af/keep` | **EFS**（アクセスポイント） | `~/.config`・`~/.ssh`・`~/.claude`・`~/.codex`・`~/.gitconfig`・`~/.git-credentials`・`~/.claude.json` を `~` から symlink |

`control-plane/internal/runtime/runtime_ecs_ec2.go:3650` が `CLAUDE_CONFIG_DIR` を、`:3652` が
`AF_WS_KEEP` を注入し、`workspace/entrypoint.sh:20-52` が `~` の 7 項目を `keep` 側へ symlink する。
つまり `git` を 1 回起こすたび（`~/.gitconfig`）、`claude` を 1 回起こすたび（`CLAUDE_CONFIG_DIR`）
EFS のメタデータ I/O が出る。**リポジトリの作業コピーは EBS にあるのに、遅くなるのは全部**という
一見矛盾した症状はここから来ている。

応急処置として live の EFS を CLI で `elastic` に変えた（反映は約 1 分、`/api/repos` が 20 秒 →
272 ms）。並行して、転写探索の `filepath.Glob("projects/*/<sid>.jsonl")` が毎ポーリングで
全プロジェクトディレクトリを読んでいた件を PR #711 / 0.20.2 で直し、0.21.0 として配備して
全利用者が再起動した。

### 再起動が揃った後に残ったもの

**PR #711 はメタデータ I/O を半分にしただけで、嵐は消えていない。** 09-17 午前の実測:

| | 旧 agent（09-10〜09-14） | 新 agent 0.21.0（09-17） |
|---|---|---|
| 1 箱あたり p50 | 1.04〜1.22 MB/s | **0.55 MB/s** |
| 1 箱あたり p99 | 2.75〜3.12 MB/s | 1.47 MB/s |
| 1 箱あたり最大（1 分値） | 4.02〜4.46 MB/s | 1.83 MB/s |
| 全体の最大（1 分値） | 18.2 MB/s（箱 7 個） | 5.9 MB/s（箱 4 個） |

箱の数は `ClientConnections`（Sum / period × 60）から数えた。**1 箱 = 2 接続**で、CP は EFS を
使わないので寄与しない。

🔥 **箱が 1 個だけの時間帯（09:06〜10:05 JST）の 0.44〜0.45 MB/s が「きっかり平ら」だった。**
AWS は「すべての NFS リクエストを 4 KB、または実際の要求/応答サイズの大きい方として計上する」と
明記している（EFS ユーザーガイド「パフォーマンスモード」）。したがって
**0.44 MB/s ÷ 4 KiB ≒ 110 NFS リクエスト/秒**が、利用者 1 人・待機状態の新しい床である。
利用者はこれを「1 ユーザーで 0.5 MB/s は多い」と見ている。正しい。設定ファイルを読むだけの
ファイルシステムが毎秒 110 回の往復をする理由は無い。

⚠️ **「0.2 MB/s まで落ちる」は目標にしてはいけない。** 12 日分の履歴では 0.2 MB/s になるのは
箱が 0〜1 個の時間帯だけで、稼働時間帯は 09-07 以降ずっと 1 箱 0.7〜1.9 MB/s の帯にいる。

## 実測 1: 何が 110 リクエスト/秒を出しているか

`ptrace_scope=1` なので走行中の Agent には attach できない。代わりに**エージェントの実コードを
そのままテストバイナリに固めて子プロセスとして起こし**、`strace -c -e trace=file` で数えた。
プロジェクト数・セッション数は本番の箱と同じ規模にした。

### 発生源 A: `ListMetas()` ——セッション台帳が EFS に載っている（最大）

`workspace/agent/internal/session/meta.go:17`:

```go
// MetaDir lives in the home volume (persists across Stop→Start) under the
// denylisted .config/agent-fleet, so stopped sessions survive a Workspace restart.
func MetaDir() string { … filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "sessions") }
```

🔥 **このコメントは ecs-ec2 では誤りである。** `~/.config` は `AF_WS_KEEP_DIRS` の既定
（`.config .ssh .claude .codex`）に入っているので、entrypoint が **`keep`（EFS）へ symlink して
いる**。つまりセッション台帳は EFS にある。

`ListMetas()` は `ReadDir` 1 回のあと**メタ 1 件につき `os.ReadFile` を 1 回**する。実測
（メタ 207 件・100 回呼び出し・`strace -c`）:

```
openat 21,014   read 41,406   close 21,013   getdents64 204   = 83,637 syscalls
→ 1 回の ListMetas() あたり 836 回のファイル系システムコール（openat 210 / read 414 / close 210）
```

呼び出し側は 32 箇所あり、その中に **`GET /sessions` のハンドラ**がいる。そして
`control-plane/events.go:145` のコメントが頻度を決めている——

> this tick function runs once per open tab every 4 seconds

**Console のタブ 1 枚につき 4 秒に 1 回**、CP が Agent の `/sessions` を呼び、その 1 回が
EFS 上のファイルを 207 個開いて読む。タブ 2 枚なら 2 倍になる。

`~/.config/agent-fleet` の実測は **113 MB / 約 5,400 ファイル**だった。ADR 0045 が `keep` に
想定していたのは「認証・接続・identity の 7 つ・合計 100 MiB 未満」である。実際に載っているのは:

| ディレクトリ | ファイル数 | 性質 |
|---|---|---|
| `session-status/` | 1,220 | **ポーリング経路から書かれる**（`status.Persist(sid, "working")`） |
| `pending-perm/` | 1,429 | 許可待ちの一時状態 |
| `chat-wd/` `chat-codex/` `chat-claude/` | 971 | チャットの作業ディレクトリ（93 MB） |
| `sessions/` | 207 | セッション台帳 |
| `session-injections/` `session-exit/` `codex-sid/` … | 約 1,500 | 各種の可変状態 |

資格情報は 1 つも無い。**keep の粒度がディレクトリ単位（`.config`）だったために、エージェントの
可変状態がまるごと EFS に載った**というのが事の全体である。

### 発生源 B: `subagentBases()` ——「見つからなかった」は覚えない設計の裏側（次点）

PR #711 は `jsonl_memo.go` を入れて転写探索を memo 化した。その不変条件は意図的にきつい:

> A MISS IS NEVER REMEMBERED. 転写があるのに「無い」と答えると `--session-id` が渡って claude が
> `Session ID is already in use` で落ちる。

正しい。ただし **同じ memo を使うもう一方——`subagentBases()`——は、ほとんどのセッションで
恒久的に miss する**。背景エージェントを使っていないセッションには `projects/*/<sid>/subagents`
が存在しないからで、miss は覚えないので**毎回 full sweep が走る**。

呼び出し経路は待機中の claude セッションの状態判定そのものである
（`internal/agents/claude/claude.go:213` → `BackgroundWork` → `bg.go:185` `SubagentBusy`）。
`BackgroundBusy`（/proc・安い）が false のときに必ず来る。

`strace -c` で数えた 1 回あたりのコスト（`filepath.Glob` は `projects/` を 1 回 + 一致した各
ディレクトリを 1 回ずつ `ReadDir` する）:

| プロジェクト数 | 1 回の `SubagentLogs()` あたりのファイル系システムコール |
|---|---|
| 38 | **158**（getdents64 78.8 / openat 39.9 / newfstatat 39.8） |
| 318 | **1,296** |

きれいに `(1 + プロジェクト数) × 4 + 2` である。開発配備の箱でプロジェクトディレクトリを数えると
**318 個**あった。本番の箱は 38 個で測ってある。**「全ディレクトリがきっかり同数」は 1 個ずつ
探しに行っている証拠**という見分け方は、ここでもそのまま効く。

同じ形は転写側にも残っている: memo が hit していれば 1 回の `Lstat`（実測 5 syscall/回）で済むが、
**`memoTTL = 60s` ごとに full sweep へ戻る**し、最初の turn が jsonl を書くまでは miss なので
sweep し続ける。

### 発生源 C: ポーリング経路からの EFS への書き込み

`status.Persist(sid, "working")` は `claude.go` の状態判定の中から呼ばれる。elastic では書き込みが
**$0.07/GB**（読み取りは $0.04/GB）で、1 リクエスト最低 4 KiB 課金なので、数十バイトの状態ファイルを
書くたびに 4 KiB 分が課金される。

### 合わせるとどうなるか

箱 1 個・Console タブ 1 枚・メタ 207 件・プロジェクト 38 個・待機中の claude セッション 3 本:

```
ListMetas()        208 ファイル読み  × 1/4s
SubagentBusy() ×3   117 ディレクトリ読み × 1/4s
                 → 毎秒 80 前後のファイル/ディレクトリ操作
```

NFS では 1 操作が 1〜3 往復（LOOKUP / ACCESS / OPEN / READ / GETATTR）になるので、**実測の
110 リクエスト/秒とほぼ同じ桁**に落ちる。発生源 A と B のどちらも、**利用者が何もしていない
待機中の箱で**走っている。

## 実測 2: お金

ap-northeast-1 の単価（Pricing API・2026-09-17 取得）:

| 項目 | 単価 |
|---|---|
| Elastic の読み取り | **$0.04 / GB** |
| Elastic の書き込み | **$0.07 / GB** |
| Provisioned Throughput | **$7.20 / MiBps-月** |
| Standard ストレージ | $0.36 / GB-月 |
| bursting / provisioned の I/O | **無料**（帯域に含まれる） |

🔥 **bursting では I/O が無料だった。** Cost Explorer では 09-15 まで 1 日 $0.02（ストレージだけ）
だったものが、elastic へ切り替えた 09-16 に `APN1-ETDataAccess-Bytes` が **235.21 GB / $9.47** として
現れた。障害を止めた代わりに、それまで「遅さ」として出ていたものが「請求」として出るように
なった、というだけのことである。ブレンド単価は $0.0402/GB で、メタデータ I/O は実質すべて
読み取り単価で課金されている。

課金メーターは `MeteredIOBytes` である（09-16 の CloudWatch 合計と CE の請求量が一致した）。
12 日分:

| | 1 日あたりの `MeteredIOBytes` |
|---|---|
| 平日（旧 agent） | 146〜316 GB |
| 週末 | 5〜8 GB |
| 13 日平均 | 165 GB/日 |

**旧 agent のまま elastic を 1 か月回すと約 4,940 GB ＝ $198/月。** 新 agent は実測で約半分なので
**約 $100/月**である。

### 3 通りの比較（月額）

新 agent（0.21.0）・箱 9 個・平日 22 日の前提。provisioned の必要量は、旧 agent の 1 分値最大
18.2 MB/s（箱 7 個）を 1 箱あたりに直して新 agent の改善率（約 2 倍）と箱 9 個へ振り直した
**約 11〜12 MB/s ＝ 約 11 MiB/s** をピークとする。

| | ① いまのまま elastic | ② provisioned 24 MiB/s | ③ 下の恒久対策が効いた後の elastic |
|---|---|---|---|
| I/O 量 | 約 2,600 GB/月 | —（課金対象外） | 約 300〜600 GB/月（目標 4〜8 倍減） |
| I/O 費用 | **約 $104** | $0 | **約 $12〜25** |
| 帯域の固定費 | $0 | **$172.80** | $0 |
| ストレージ | 約 $1 | 約 $1 | 約 $1 |
| **合計** | **約 $105/月** | **約 $174/月** | **約 $13〜26/月** |
| 上限 | 無し | 24 MiB/s で頭打ち | 無し |
| 壊れ方 | 請求が増える | **詰まって全 Workspace が止まる** | 請求が増える |

🔥 **provisioned は今の需要では elastic より高い。** elastic と釣り合う provisioned は
約 14 MiB/s で、それは推定ピーク（11〜12 MiB/s）と同じ高さ——つまり**余裕ゼロで買って、
やっと同額**である。ADR 0045 以降この配備が何度も学んだとおり、上限に当たった EFS の壊れ方は
「少し遅い」ではなく「全 Workspace が使えない」である。

### スループットモード変更の 24 時間制限（正確な形）

AWS の記述は「モード変更は 24 時間に 1 回」ではない。EFS ユーザーガイド
"Restrictions on switching throughput and changing provisioned amount" は、**provisioned へ
切り替えた後、または provisioned の量を変更した後**、24 時間のあいだ次の 2 つが禁止される、と
書いている:

- provisioned から elastic / bursting へ戻すこと
- **provisioned の量を下げること**

🔥 **上げることは制限されていない。** したがって provisioned を選んだ場合の危険は非対称である
——足りなければ即座に上げられるが、多すぎた分は 24 時間下げられず、elastic へも戻れない。
「値をケチると当日やり直せない」という懸念は、正確には「**戻れない**」であって「上げられない」
ではない。

### 上限に当たったと分かる指標

provisioned を採るなら、この 2 つを一緒に見ること:

- **`PermittedThroughput`**（Average）が provisioned した値そのものに張り付く。elastic の
  いまは 5,400 MB/s を返している（実測）ので、値そのものが切り替わりの確認にもなる。
- **`MeteredIOBytes`** が `PermittedThroughput` と同じ高さで**平らになる**。09-16 の
  bursting 枯渇時に見えた「1.35 MiB/s できっかり平ら」がまさにこれで、**平らな線は需要ではなく
  上限である**。需要は解放してみるまで分からない（解放直後に 1.35 → 19.5 MiB/s へ跳ねた）。
- `BurstCreditBalance` は elastic では発行されない。provisioned では再び現れる。

## 決定

### 決定 1: 暫定は「elastic のまま据え置く」。provisioned は買わない

上の表のとおり、いまの需要では provisioned は elastic より **7 割高く**、しかも上限に当たった
ときの壊れ方が「請求」から「停止」へ悪化する。**帯域の買い方を変えても 110 リクエスト/秒は
1 つも減らない。** 金額の差が意味を持つのは恒久対策が効いた後であり、そのときは elastic が
そのまま安くなる（provisioned は固定費なので安くならない）。

利用者が固定費の予測可能性を優先して provisioned を選ぶ場合の推奨値は **24 MiB/s**（$172.80/月）
とする。根拠は「推定ピーク 11〜12 MiB/s の約 2 倍」であり、かつ**旧 agent の需要を箱 9 個へ
振り直した 22 MiB/s も上回る**——つまり 0.21.0 の改善が何らかの理由で失われても詰まらない。
足りなければ即日上げられ、下げるのは翌日以降になる。CFN テンプレートの既定値もこの 24 に
合わせてある（決定 2）。

### 決定 2: `10-data.yaml` のスループットモードをパラメータにする（実装済み）

`deploy/aws/ecs/cfn/10-data.yaml:72` は `ThroughputMode: bursting` を直書きしていた。live は
CLI で elastic に変えただけで、`af-ecs-data` スタックは 2026-08-25 から更新されていない。
**次にこのスタックを更新した瞬間に bursting へ戻り、障害が再発する**——しかも戻すには
24 時間かかる。

`EfsThroughputMode`（`elastic` / `bursting` / `provisioned`、既定 `elastic`）と
`EfsProvisionedThroughputMibps`（既定 24）をパラメータ化し、資源のすぐ上に何が起きたかを
書いた。既定を `elastic` にしたのは live と一致させるためである
（`aws cloudformation deploy` は既存スタックに無い**新しい**パラメータには
テンプレートの既定値を使う。ここが `bursting` のままなら、パラメータ化そのものが地雷になる）。

### 決定 3: `MeteredIOBytes` に監視を置く

elastic には上限が無い＝暴走は遅さではなく請求として出る。1 か月気付かない、という壊れ方を
塞ぐために、`MeteredIOBytes` の 30 分平均が **8 MB/s** を超えたら鳴るアラームを置く（箱 9 個で
1 箱 0.9 MB/s 相当＝現状の約 1.6 倍）。これは「性能の警報」ではなく「**回帰の警報**」である
——PR #711 の逆をやるコードが入ったときに、請求書ではなくアラームで気付くためのもの。

### 決定 4（恒久・P0）: エージェントの可変状態を EFS から降ろす

`~/.config/agent-fleet` の 113 MB / 5,400 ファイルに資格情報は 1 つも無い。`keep`（EFS）に
載っているのは、`AF_WS_KEEP_DIRS` の粒度が `.config` というディレクトリ単位だったからにすぎない。
`meta.go` のコメントが「home ボリュームにある」と信じているのは、**そうあるべきだ**という
設計意図の記録である。

降ろし方は 2 通りあり、この ADR は **(b) を推す**。

- **(a) keep の対象を狭める**: `.config` の代わりに `.config/opencode` `.config/cursor`
  `.config/acli` …と資格情報を持つものだけを列挙する。entrypoint の 1 行で済むが、CLI が
  増えるたびに列挙を足す必要があり、足し忘れると**ログインが毎回消える**という静かな壊れ方をする。
- **(b) エージェントの状態を `.config` の外へ出す**: `~/.local/state/agent-fleet`（＝ home /
  EBS）を state ディレクトリとし、`.config/agent-fleet` からの片道移行を entrypoint か Agent の
  起動時に 1 回行う。列挙の維持が要らず、「資格情報は keep、可変状態は home」という線が
  ディレクトリ名として残る。`AF_SESSIONS_DIR` という env による差し替え口は既にある。

⚠️ **トレードオフ**: ecs-ec2 の home は**単一 AZ の EBS 1 本**である（ADR 0045 が `keep` を作った
理由そのもの）。降ろしたものは、その EBS を失えば一緒に失われる——セッション台帳を失うと
セッション一覧が空になる（転写そのものは `CLAUDE_CONFIG_DIR` 側なので残る）。資格情報を失うより
軽い、というのがここでの判断である。**資格情報（`~/.ssh`・`.git-credentials`・`.claude.json`）は
EFS に残す**——平文をローカルに残さないという ADR 0045 の線は動かさない。

### 決定 5（恒久・P1）: `projects/*` の全掃引を残り 2 箇所で止める

- **`subagentBases()` の miss を覚える。** 転写側の「miss を覚えない」不変条件は
  `SessionJSONLExists` → `--resume` / `--session-id` の分岐を守るためのものであり、
  **subagents 側にはその危険が無い**（偽の「背景エージェント無し」は、バッジが数秒遅れて
  点くだけである）。短い TTL（10〜30 秒）の否定キャッシュを入れる。⚠️ 転写側の memo に
  同じ変更を当ててはいけない——`jsonl_memo.go` の型コメントが、なぜ 2 箇所で二重に
  防いでいるかを含めて書いてある。
- **プロジェクトディレクトリを cwd から導出する。** claude は cwd をディレクトリ名に符号化する
  （`/home/dev/repos/agent-fleet` → `-home-dev-repos-agent-fleet`）。セッションの `Meta.Dir` は
  Agent が持っているので、`projects/<導出>/<sid>.jsonl` を **1 回 Lstat** して、外れたときだけ
  従来の掃引へ落ちればよい。符号化は非可逆（`.` や `@` も `-` になる）なので、**導出は
  当て推量であって真実ではない**——落ち先を必ず残すこと。
- 転写側の `memoTTL = 60s` は、hit の再探索を 60 秒ごとに full sweep へ戻す。導出が入れば
  再探索自体が安くなるので、この定数は導出の後で見直す。

### 決定 6（恒久・P2）: マウントの当て方は「できない」ことを先に確定させた

ECS の `EFSVolumeConfiguration` が持つのは `FileSystemId` / `RootDirectory` /
`TransitEncryption` / `TransitEncryptionPort` / `AuthorizationConfig` だけで、
🔥 **NFS のマウントオプションを渡す口が無い**（aws-sdk-go-v2 の型で確認）。したがって
`actimeo` / `lookupcache` / `nconnect` / `noatime` は、**タスク定義からは指定できない**。

指定したければ道は 1 つしか無い——**EC2 のインスタンス側で自分でマウントし、ホストパスの
ボリュームとして渡す**。ecs-ec2 では `home` が既にその形（`/af-home/<membershipID>/dev` の
ホストバインド）なので、前例はある。ただし:

- アクセスポイントによる per-member の root-dir 隔離と POSIX uid 写像を、自前のマウント＋
  サブディレクトリで再現することになる（隔離の担保がテンプレートからコードへ移る）。
- transit encryption は efs-utils の stunnel 経由なので、`nconnect` は併用できない。
- 効き目は**未測定**である。決定 4・5 が「往復の回数そのもの」を削るのに対し、これは
  「1 往復あたりの重さ」と「キャッシュの効き」を変える話で、順番としては後になる。

**結論: P2 は決定 4・5 を入れて測り直した後に判断する。** 先にやると、効かなかったときに
隔離の作り直しだけが残る。

### 決定 7: 採らなかった案

- **provisioned を買う**（決定 1 のとおり、今の需要では高い）。
- **bursting へ戻す**: ベースラインを需要に届かせるには Standard ストレージを 1 TiB 弱まで
  水増しする必要があり、$368/月。provisioned より高い。
- **EFS をやめて S3 にする**: `~/.gitconfig` や `~/.ssh` は POSIX のファイルとして開かれる。
  S3 を挟むには FUSE か起動時の取り込み／終了時の書き戻しが要り、後者は**箱が SIGKILL された
  ときに資格情報の更新を失う**。`keep` の存在理由（ADR 0045）と正面からぶつかる。
- **EBS だけにする**: 単一 AZ の EBS を失うとログイン情報まで失う。これが `keep` を作った理由
  そのものである。
- **ポーリング間隔を延ばす**: 4 秒を 8 秒にすれば I/O は半分になるが、UI の遅さと引き換えに
  なる。**1 回のポーリングが 800 回のファイル操作をすること自体が異常**なので、頻度ではなく
  1 回の重さを直す。頻度の調整は、重さを直した後に残った分だけを対象にする。

## 実機での検証手順

決定 4・5 を入れた後、次の順で確かめる。⚠️ ベンチが全部緑でも配備の配線は測れていないので、
**必ず CloudWatch の数字で裏を取ること**。

1. **単体（`strace`）。** この ADR と同じ形のプローブ——実コードをテストバイナリに固めて
   子として起こし、`strace -f -c -e trace=file` で数える——を回し、
   `SubagentLogs()` の 1 回あたりが 158 → 1 桁に、`ListMetas()` の 836 が **EFS 上では 0** に
   （home へ移ったので）なることを確認する。
2. **箱 1 個の床。** 利用者 1 人だけが起動している時間帯を作り、`MetadataIOBytes` の 5 分値が
   平らになるのを 1 時間見る。**0.44 MB/s（≒110 req/s）が出発点**で、決定 4・5 が効けば
   0.1 MB/s 前後まで落ちるはず。落ちなければ発生源が他にある証拠で、そのときは
   `~/.gitconfig` 参照（`git` の起動回数）と `claude` 自身の転写書き込みを次に疑う。
3. **稼働時間帯。** 箱の数を `ClientConnections` から数え（1 箱 = 2 接続）、1 箱あたりの
   MB/s を旧 agent（1.04〜1.22）・新 agent（0.55）と並べる。
4. **請求。** `ce get-cost-and-usage` の `APN1-ETDataAccess-Bytes` を 3 営業日見る。
   DAILY より細かい粒度は payer アカウントで opt-in していないと使えない（実測: HOURLY は
   `AccessDeniedException`）。
5. **回帰の門番。** 決定 3 のアラームが鳴らないことを 1 週間確認する。

## 未解決の問い

- **`claude` 自身がどれだけ出しているか。** 転写の追記・設定ファイルの stat 反復は
  `CLAUDE_CONFIG_DIR`（EFS）で起きるが、エージェント側と切り分けて測っていない。決定 4・5 の
  後に残った床の正体はここである可能性が高い。切り分けには、claude を子プロセスとして
  `strace` 下で起こす形が使える。
- **Fargate ランタイムはもっと悪い。** `runtime_ecs.go:684` は **`home` ごと EFS に載せている**
  ——`~/repos` の作業コピー全部が EFS になる。本番は ecs-ec2 なので今回の測定には出てこないが、
  同じ測り方をすれば桁が 1 つ違うはずである。この ADR はそこを決めない。
- **NFS の属性キャッシュがどれだけ吸収しているか。** 上の syscall 数は VFS 層の数であって
  NFS 往復の数ではない。ディレクトリの内容は `acdirmin`（既定 30 秒）まで、ファイル属性は
  `acregmin`（既定 3 秒）までローカルで答えられる。実測の 110 req/s と syscall 数の比から
  「1 操作あたり 1〜3 往復」と見積もったが、**直接は測っていない**。本番の箱で
  `/proc/self/mountstats` を読めば per-op の RPC 数が取れる（root は不要）。
- **決定 4 の (b) を入れたとき、既存の箱の移行で何が起きるか。** 片道移行の最中に
  Agent が落ちた場合の半端な状態を、どちらの側を正とするかで決める必要がある。
