# 0095. Meta の Muse Code をセッション種別（`muse`）にする — TUI 契約ではなくベンダのプロトコルに乗る、実機 3 門の先で

[English](0095-muse-agent-kind.md) | 日本語

- Status: **proposed**（2026-09-20）・**段 1 門 A 回答済み**（2026-09-20——最終節）。
  種別そのものの実装は何も無く、門 A が入れたのはその計測に必要な配備変更だけである。以下の `file:line` は当時の develop
  `06ea94d3` で読んだ。◎ は Workspace のコンテナで **Muse Code 1.3.0-R3401.1** を使い捨てディレクトリへ
  導入して実測したもの、△ はベンダ文献のみ、× は未測。再現手順は末尾にある。
- 依頼は一文である。**Meta のコーディングエージェント Muse Code は 10 番目のセッション種別になれるか、
  なるとしたら幾らか。**
- 併読: [0015](0015-agent-managed-driver.ja.md)（この種別が実装する managed ドライバ契約） /
  [0019](0019-copilot-agent-kind.ja.md)（セッション ID を AF が採番する形） /
  [0023](0023-cursor-agent-kind.ja.md)（版ピン・自動更新・プラン依存の点検表） /
  [0026](0026-kiro-agent-kind.ja.md)（直近に出荷した種別＝「種別が触る面」の雛形） /
  [0093](0093-lcpp-agent-kind.ja.md)（managed 専用種別の地ならし。先に着地した方が費用を払う） /
  `docs/log/74-rovo-agent-kind.md`（この ADR が答えている分水嶺表）

## 背景

### Muse Code とは

Meta Superintelligence Labs が 2026-08-05 に公開したターミナル用コーディングエージェント。Muse Spark が
動力（既定モデル `muse-spark-1.2`、1.3 は 2026-09-02 から展開）。静的リンクの単一バイナリで macOS /
Linux / Windows、x86 と aarch64。**セッションプロトコルをデバッグ用の隙間ではなく製品面として公開した
最初の CLI** であり、同時に **自身の機能が Agent Fleet と重なる最初の CLI** でもある（独自の subagent・
workflow・セッション間メッセージング・スキル・メモリを持つ）。

### 分水嶺（◎=2026-09-20 実測 / △=文献のみ / ×=未測）

| 分水嶺 | 判定 | 根拠 |
|---|---|---|
| managed の契約 | **◎ これまでで最良** | `muse serve` は **stdio** 上の改行区切り JSON-RPC 2.0。`muse schema generate-json-schema` が MSP v1 をオフライン出力し、**47 メソッド・31 通知・31 エラー・234 型**、指紋 `sha256:7469c9e3…`。未認証のまま `initialize` → `initialized` → `session/start` → `turn/start` を疎通させた |
| 状態検出 | **◎ 文字列でなく契約** | `session/statusChanged` が `running` / `idle` / `notLoaded` を運ぶ。TUI スクレイプもフックもフッタ推定も要らない |
| セッション ID | **◎ AF が採番** | `session/start.sessionId` に自前 UUIDv7 を渡すとそのまま採用され、ワイヤにもディスク上の経路にも現れた。`muse exec --session-id <uuid>` も同じ欄 |
| read 正本 | **◎ 追記のみ** | `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl`（subagent は `subagent/<id>/` 配下）。`session/start` の応答がその `path` を返すので AF が推測する必要が無い |
| 版ピン＋sha256 | **◎ 両方、匿名で** | channel マニフェスト（`api.meta.ai/muse-code/channels/muse-stable`、匿名 200）が**版付きアドレスの** release マニフェストを指し、そこにプラットフォーム別の url・**sha256**・サイズがある（`aarch64_linux` も）。成果物自体も匿名で 206 ＝**焼くのにアカウントが要らない** |
| 自動更新の封殺 | ◎ | `MUSE_NO_AUTO_UPDATE=1` の env 1 本。これを立ててバイナリが在れば、ランチャーは**読むだけ**になる（読み取り専用ディレクトリで `muse --version` が成功） |
| ハーネス構築の原資 | **◎ ほぼ $0** | `--provider echo` という決定的な組み込み provider がある。`muse exec --provider echo --json` は資格情報なしで 1 セッションを完走した。アカウントは受け入れに要るのであって、構築には要らない |
| 常駐費 | ◎ | 待機中の `muse serve` は **≈ 73 MiB RSS**（74,924 KB）。登録簿の `tuiMemoryCost` は claude 230 MiB、opencode 300 MiB（`console/src/agents/registry.ts:227,469`） |
| **OS サンドボックス** | **🔴 ◎ ここでは恒久に動かない** | Linux のサンドボックスは bubblewrap（バイナリ中に `bwrap` 38・`seccomp` 50 の文字列。文献は「動作する bubblewrap と非 musl ビルドが要る。無ければサンドボックス下のシェルコマンドは全て environment failure で中断する」と言う）。門 A が実機の `bwrap` で決着させた: ユーザー名前空間は作れて全 41 能力を得るのに、**`mount(2)` と `move_mount(2)` は能力に関わらず EACCES** を返し、`fsopen` / `open_tree` は成功する——これは seccomp でも能力でもなく **AppArmor の `docker-default` プロファイル**の指紋である。muse は*埋め込み*の bwrap も同梱しており、バイナリの不在はそもそも阻害要因ではなかった |
| **共有リポジトリへの書き込み** | **🔴 ◎ 触る** | `muse exec -w create` は `<repo>/.muse/worktrees/<日付>-<hash>` を作業根に選び、`.muse/.session-worktree-reservations/` を作り、**`.git/info/exclude` に `/.muse/worktrees/` を追記した**。リンク worktree ではそのファイルは親クローンのもの＝全セッション共有 |
| **他 CLI の個人領域** | **⚠️ ◎ 既定で読む** | 初回起動が `Including your Codex personal rules and 5 skills` と出した。他 CLI 文脈を切らない限り `~/.claude` と `~/.codex` のスキル・ルールを拾う。しかも切るフラグ `--no-foreign-personal-context` は `muse exec` にはあるが **`muse serve` には無い**（実測: `unknown option`） |
| 機能の重複 | ⚠️ △ | subagent（既定 1 木 8・`agents.execution_capacity` は 1〜64）、それぞれ独自にモデルを呼ぶ背景オブザーバ 4 本、workflow（生涯 1,000 子）、**利用者横断のセッション名前空間**とピアメッセージング。いずれも AF の登録簿・ミラー・使用量台帳からは見えない |
| 認証 | △ | ブラウザサインインか API キー。`META_API_KEY`、または `muse auth set` で `~/.config/muse/auth.json` に保存。API キーは常にブラウザセッションに優先する。マネージド（MMA）アカウントはキー必須 |
| 課金 | △ | トークン従量、または定額サブスク 3 段。サブスクの枠は **5 時間あたりのプロンプト数**で数え、Meta Model API アカウントでサインインした CLI 経由でのみ有効 |
| 実ターンの挙動 | **× 未測** | `turn/start` は `{"error":{"kind":"authRequired","retryable":false}}` を返した。トークン使用量・モデル目録（未認証の `model/list` は `{"models":[],"source":"bundledCatalog"}`）・承認の往復・subagent イベントは資格情報 1 本が要る |
| TUI の文字列契約 | × | 未測。決定 2 により不要 |

### リポジトリに既にあるもの

- 種別は 9 つ（`workspace/agent/internal/session/session.go:20-28`）。登録は 2 箇所で、読み層が
  `sessionx/agent.go:28-38`、managed ドライバ 5 つが `sessionx/session_turn.go:31-37`。
  **未登録の種別は拒否されず claude に黙って正規化される**（`AgentOf` は `sessionx/agent.go:40-45`、`NormalizeKind` は `:49-54`）。
- 種別が実装する契約は `Agent` の 6 メソッド（`agents/agents.go:141-161`）と
  `Driver` ＋ `ThreadHandle` の 7 メソッド（`agents/driver.go:132-165`）。
  `Capabilities.ProcessModel` は `shared-daemon` | `per-session-child` | `tui`（`driver.go:146`）。
- `usageMeasuredForKind`（`workspace/agent/usage_fold.go:204-212`）の exact は 3 種別だけ
  （claude・codex・opencode）、copilot が partial、残りは none。
- MCP は `mcpreg.knownKinds` が 7 種別（`mcpreg/def.go:57-61`）、`MaterializedKinds` が種別ごとの
  ネイティブ設定を書く（`mcpreg/materialize.go:47,74-87`）。
- 指示配布は 6 種別（`workspace/agent/agent_instructions.go:83`）。
- Console は記述子表 `console/src/agents/registry.ts`、動的モデル一覧
  `console/src/lib/agentModels.ts:34-35`、色トークン `console/src/styles/tokens.css:109-117`（dark）と
  `:259-`（light）、使用量の積み順 `console/src/features/usage/colors.ts:81`。
- `"muse": "meta"` は **既に** モデル族の接頭辞として `workspace/agent/model_provider.go:69` にある。
- 版は Dockerfile の `ARG` でピンし（npm 4 種＝claude / opencode / codex / copilot は
  `workspace/Dockerfile:318-321`、cursor の版付き tarball は `:404-411`）、
  `workspace/agent/env_tool_versions.go` が表に出す。

## 決定

### 決定 1 — 種別スラグは `muse`

`musecode` は長い。`meta` はベンダであってエージェントではなく、`model_provider.go` のベンダ id の隣では
「Meta プロバイダ」に読める。`spark` はモデルの名前である。`muse` は同じ表にモデル族の接頭辞として既に
あるが（`model_provider.go:69`）、これは `codex` が種別になって以来ずっと生きている状態そのもので、衝突
ではなく前例である。

`session.KindMuse`、label `Muse Code`、`assistantName: "Muse"`、`short: "mu"`、`launchSuffix: "-mu"`
（`""/-cx/-cu/-ag/-cp/-ki/-oc/-sh` の中で空き。ADR 0093 が先なら `-lc` も避ける）、`cssClass: "muse"`。

**「ブランド色にする」決定と「どの値か」は別問題である。** Meta ブルーは `--kind-agy: #4285f4` と
`--kind-ssm: #6d8bf5`（`tokens.css:112,117`）に続く 3 本目の青になる。ブランド値をそのまま使えるかは、
着手前に両テーマを実描画して既存 9 種別との ΔE2000 を測って決める（docs/log/74 §8.2 が 3 本目の青で
やった手順）。積み上げグラフはそれより狭い問題である——`KIND_STACK_ORDER`（`colors.ts:81`）はチャット 7
種別だけを持ち shell / ssm を**含まない**ので、そこで隣接させてはいけないのは agy と muse の 2 青であって
3 青ではない。

### 決定 2 — `muse` は managed 専用。Terminal（CLI）経路は作らない

MSP はペインが与えるもの（状態・承認・ステアリング・fork・使用量・スキル・モデル一覧）を既に全部運ぶので、
tmux ペインを足しても得られるのは「二つ目の UI」と「維持すべき文字列契約」だけである。

**作成経路もこの門の一部で、今は逆向きに既定化されている。** `POST /sessions` は driver が空でも明示の
`tui` でも `""` に正規化する（`sessionx/session_handlers.go:650-668`）。この種別では `""` を `managed` に
解決し、明示の `tui` は 400 で拒否しなければならない。driver を送らない呼び手——ハンドオフ、spawn、
REST と MCP の create、そして自前の kind 一覧で paneless を決める Control Plane のスケジューラ
（`control-plane/scheduler_wake.go:277-285`）——が、存在し得ないペインを要求してしまう。

これは ADR 0093 決定 2 が `lcpp` について提案している形と同じで、費用も同じ——「Terminal 経路を持たない」
門は Console の 5 箇所＋サーバ側の managed→TUI 遷移
（`console/src/features/repos/{LaunchModal.tsx,StartModal.tsx,RepoRowConnected.tsx}`、
`console/src/features/sessions/{SessionMenu.tsx,useSessionActions.tsx}`、
`workspace/agent/internal/sessionx/session_driver.go:62-105`）＋上の作成経路の既定化である。
**0093 と 0095 のうち先に着地した方が払い、後の方は無料で貰う。** 着手時点でどちらも未着地なら、この費用は
本種別の見積りに入る。

### 決定 3 — `per-session-child`。セッションごとに `muse serve` を 1 本

MSP は 1 プロセスで複数セッションを抱えられる（`session/list`・接続ごとの自動購読）ので共有デーモンも
可能だが、v1 では採らない。理由は「固定の値があるから」ではなく **どの摘みがホスト単位か** である——
承認モードはワイヤ上でセッション単位なので、それだけでは決め手にならない。`muse serve --help` で実測した
ホスト寿命固定の摘みは、サンドボックス姿勢・`--sandbox-network`・`--disable-write`・
`--disable-shell`・**`--trust-workspace`**・セッションの耐久性である。**このうち 1 つは既にここで
セッション単位であり、それ以外になりようがない**——trust は*作業コピー*についての決定で、Agent Fleet の
セッションはそれぞれ自分の作業コピーを持つ。共有ホストにすると、最初のセッションのリポジトリを信頼した
ことが、以後に載る全リポジトリの信頼になってしまう（`muse exec` の方は実行単位で取れる——実測
`workspace trust: trusted source=run-flag`——ので、これは製品ではなく `serve` の性質である）。残りは
現在の制約ではなく先々の制約なので、ADR として
言い過ぎない: AF が今セッション単位で持つ軸は `Meta.Mode` と `Meta.SkipPermissions`
（`session.go:377-390`）で、ワイヤに対応があるのは承認だけである。read-only 姿勢を
`--disable-write` / `--disable-shell` に写すならセッション単位が要るが、共有ホストではそれが持てない。
副次的な理由が 2 つ——ホストが落ちれば載っている全セッションが落ち、1 セッションを止めるのに他が載った
プロセスを畳む必要が出る。待機
**73 MiB**（実測）ならセッションごとの子は買える——claude のペインの 3 分の 1 である。
`Capabilities.ProcessModel = "per-session-child"` は cursor / kiro / copilot が既に使う値なので、enum の
追加は要らない。

共有デーモンは永久却下ではなく再評価する。条件は、待機ではなく**負荷時**のセッションあたり常駐費が問題に
なる水準だと測れること、そして write / shell / trust の姿勢をワイヤ上でセッションごとに表現できることの 2 つ。

### 決定 4 — 転写は「動いている間は MSP、止まっていれば JSONL」

ホストが生きている間は `item/started` / `item/delta` / `item/completed` を `transcript.Turn` に写す。
落ちていれば読み層が
`~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` を読む（追記のみ、同じレコード）。
**この経路を AF が計算してはいけない**——`session/start` の応答が返すので、作成時に `session.Meta` へ
持つ。日付分割のパスを自力で探すのは、kiro が cwd＋mtime で踏んだ「前身を掴む」罠（ADR 0026 決定 6）と
同型である。

**セッション ID は導出せず保存する。** AF の常套手段——(dir, スロット名) の決定論的 UUIDv5、kiro の
`slotSid` がやっている形——はここでは使えない。スキーマは「retained または reserved な id は
`commandRejected` / `session_id_conflict` で拒否する」と書いており、id は 1 度きり有効である。AF は
セッションごとに 1 つ採番し、`Meta` に経路と並べて持つ。それを捨てるのは `ClearResume` **ではない**
——あのメソッドはインタフェース宣言と agy のテスト 1 本以外から呼ばれていない——recreate と fork が
`session.Meta` を明示のホワイトリストから組み直すこと（`sessionx/session_handlers.go:1381-1390` と
`:1083-1098`）であり、列挙されない欄は構造上引き継がれない。むしろ逆を警戒する: 将来 `Meta` を丸ごと
コピーする経路を作ると、1 度きりの id を持ち回って次の `session/start` が `session_id_conflict` で失敗する。Muse が v5 を受けるかは未測——プローブが渡したのはスキーマが
サーバ既定と呼ぶ v7 である。subagent の転写は `subagent/<id>/session.jsonl` にあるが、v1 では親ターン上のツール型の
項目として描き、子ごとのペインは作らない。

### 決定 5 — サンドボックスは切る。残る門は承認である

`muse serve --disable-sandbox`。実測のとおり Workspace のコンテナでは bubblewrap がサンドボックスを
組めず（`move_mount` → EACCES）、サンドボックスが ON のまま使えないと **エージェントが走らせるシェル
コマンドは全て environment failure で中断する**＝種別として成立しない。承認はこれと直交するので ON の
まま残す。承認モードはセッションごとにワイヤ上で選ばれ、AF は `approval/requested` に
`approval/decide` で答え、起動時の許可選択（docs/log/76）を `untrusted` | `on-request` | `never` に
写す。

これは免除であり、ADR として免除と明記する。Workspace の中では箱そのものが境界であり、それは他の 9 種別
——どれも自分をサンドボックスしない——でも既に同じである。

**この免除は現行の Workspace ホスト契約の下で恒久である——門 A が実機の `bwrap` で確認し、機構を名指しした。**
恒久というのは「こちらが出荷できるもので覆せない」という意味であって、変化があり得ないという意味ではない。
拒否はバイナリの不在でも、片方のマウント API だけの事情でもない。ユーザー名前空間の中でこの箱は、
`move_mount`（util-linux が優先する新 API）も、bubblewrap 自身が呼ぶ**旧来の `mount(2)`** も、どちらも
EACCES で拒む。門 A は残った曖昧さを**能力を変えて**潰した: `fsopen` と `open_tree` は名前空間が
`CAP_SYS_ADMIN` を与えた瞬間に EPERM から成功へ変わるのに、`mount` と `move_mount` は**能力が何であれ**
EACCES のままだった——つまり拒んでいるのは能力検査（なら EPERM）でも seccomp（能力を見ない）でもなく、
**AppArmor の `docker-default` プロファイル**であり、これはコンテナランタイムが当てるもので中からは
変えられない。さらに 2 つの実測がこの方針とは独立に同じ結論を支える: muse は**自前の bubblewrap を
埋め込んで**おり system の `bwrap` はそもそも阻害要因ではなかったこと、そして Debian の `bwrap` は muse が
必須とする `--ro-bind-symlink` を持たないので muse 側が拒否すること。覆るのは**ホスト側**が許す範囲
（LSM ポリシー・seccomp・ケーパビリティ）が変わったときだけである——表の全体は門 A の節にある。

### 決定 6 — `~/.config/muse/settings.json` は AF が持ち、7 つの挙動を締める

このファイルは `"schema_version": 1` が無いと **全コマンドが起動時に落ちる**ので、盲目的にマージせず書く。
**書き手はただ 1 つ、MCP materialize のそれである。** 下の締め付けと決定 11 の `mcp_servers` は同じ
ファイルの別ブロックであり、しかも Muse 自身も書く（実測: 初回実行が `settings.json` と自前の
`~/.config/muse/.settings.json.lock` を作った）。1 ファイルに 3 人の書き手と 2 つの錠は lost update に
なる。よって **このファイルを書く主体は 1 つ、muse 自身のもの**——muse 専用の設定書き手を、既存の `materializeMu`
（`mcpreg/materialize.go:94-109`）の下で直列化した read-merge-rename の単一所有者とし（隣に 2 つ目の
mutex を置かない）、**自分が所有しない鍵は全部保つ**
——利用者の `tui`・モデル既定・テレメトリの鍵は AF の書き込みを生き延びる。

ここから 2 つ、初稿が誤っていたことが出てくる。どちらも文言でなく設計の話である:

- **これは「JSON の MCP materializer にブロックを 1 つ足したもの」ではなく、専用の書き手である。** あの
  materializer はサーバ集合が変わらなければ早期 return し（`mcpreg/materialize_json.go:110-112`）、集合が
  空なら鍵ごと消す。MCP としては正しいが、**MCP サーバが 0 件の種別でも書かねばならない締め付けには致命的**
  である。よって muse は `mcpreg` の中に自前の書き手を持ち、締め付けと `mcp_servers` を 1 パスでマージする。
- **締め付けは fail-close で、門は `Resume` の内側に置く。** 今の `Materialize` は設計として失敗を
  ログに落として飲み込み（「MCP 設定が更新できなくてもセッションは起動しなければならない」）、
  `StartManagedSession` は結果を見ずに `Resume` する（`mcpx/mcp_materialize.go:30-41`）。MCP にはそれが
  正しい取引だが、この締め付けには正しくない——締め付けは安全装置であり、書き込み失敗は subagent 8 本・
  オブザーバ 4 本・workflow・承認ジャッジ・他 CLI 個人文脈を**全部有効のまま**セッションを起動させる。
  **`StartManagedSession` は置き場所として誤りである**——子が生まれる経路はそこだけではない。`Resume` は
  turn・answer・carried・bridge の各経路からも直接呼ばれ、さらに Agent 起動時の種別ごとの
  `ReconcileManaged`（`workspace/agent/main.go:189-193`、best-effort の `MaterializeAll` の後に走る）から
  も呼ばれる。再起動や子の死のあとに締め付け無しの muse が起動してしまう。よって書き込みは muse ドライバの
  `Resume` の内側、子を spawn する前に行い、失敗はエラーとして起動を拒む。前例は kiro の `ensureSettings`
  （`agents/kiro/program.go:196-208`）で、その 2 つの欠陥は直す——`sync.Once` にしないこと、エラーを
  握り潰さないこと。起動時の `MaterializeAll` は全種別で best-effort の契約を保つ。

ロックについての主張は意図的に弱くしてある。`.settings.json.lock` が現れたことが示すのは「Muse がロック
規約を**持つ**」ことであって、どの規約かではない。段 1 門 B1 で設定更新と意図的な競合の syscall を追跡し、
AF は同じ規約を取る——他の `.lock` が `flock` だから `flock` だろうと推測するのは、2 人の書き手が礼儀正しく
互いを無視する道である。

AF が設定するのは:

1. `agents.execution_capacity` を小さく。放っておくと 1 セッションが、メモリ制約のある共有ホストで
   8 エージェントを走らせる。
2. 背景オブザーバを OFF。4 本がそれぞれ独自にモデルを呼ぶ＝従量アカウントでは見えない出費、
   プロンプト枠のサブスクでは見えない枠消費になる。
3. workflow を OFF（`auto` は 1 セッションに最大 1,000 の子を許す）。
4. worktree 隔離を OFF、`-w` は渡さない。決定 7。
5. 他 CLI の個人文脈を OFF。`~/.claude` と `~/.codex` を読むことは、別種別の指示層をこの種別に黙って
   混ぜることであり、その 2 つはワークスペース方針で触れてはならない場所でもある。**経路は設定ファイル
   だけである**: `--no-foreign-personal-context` は `muse exec` にはあるが **`muse serve` には無い**
   （実測——`muse serve … --no-foreign-personal-context` は `unknown option` と答える）。決定 2 により
   AF が動かすプロセスは `serve` だけなので、フラグは選択肢にならない。鍵の綴りは門 B1 の項目とする
   （バイナリには `allow_foreign_configuration` と `foreign_personal_fallback` の両方があり、フラグ名
   からは推測できない）。
6. 承認ジャッジを OFF（`--approval-judge off` か `MUSE_DISABLE_APPROVAL_JUDGE`）。**既定 ON** で、
   Prompt 由来の承認ごとに独自のモデル呼び出しをする。従量アカウントでは利用者が頼んでいない出費で、
   決定 5 は承認を残すので頻繁に発火する。
7. このセッションの外へ手を伸ばす同梱スキル。`muse skills list` には `resume-claude`・`resume-codex`・
   `import`・`migrate`・`read-session` が最初から入っており、その役目は Claude Code や Codex の転写・
   メモ・MCP サーバを読むことである（締め付け 5 が統制するのは他 CLI 規則の*発見*で、これらは要求時に動く
   読み手であり、ワークスペース方針が触れてはならないと定めた場所にちょうど手を伸ばす）。さらに AF の
   セッション管理の外で常駐プロセスを立てる `daemon` と `host-manager`、egress の経路である
   `slack-connector` も同梱されている。
   ⚠️ **無効化の鍵は版に脆い**（実測）: `muse skills disable bundled:resume-claude` は
   `skills.activation.bundled["bundled://muse-core/skills/resume-claude/SKILL.md"] = "off"` を書く。
   鍵は skill id ではなく**パック名込みのパス**なので、1.4 で改名や移設があれば黙って再有効化される。
   これは門 B1 だけでなくドリフト検査の項目である。

Muse 自身のピアメッセージングとセッション名の権威
（`~/.local/share/muse/session-name-authority/`、利用者横断）は、v1 では AF の cross-session messaging に
**繋がない**。1 つの名前空間に 2 つの経路があり、片方がミラーに映らない——それが
`native-peer-channel-invisible-in-mirror` の起き方だった。ガイドには「在るが AF からは見えない」と書く。

### 決定 7 — 触ってよいのは自分の作業コピーのファイルだけ。ブランチも worktree もリポジトリ全体のメタデータも作らない

自分の作業コピーの管理下ファイルを編集するのは仕事そのものであり、この決定が縛る対象ではない。縛るのは
そのファイルを取り巻く**プロジェクトとバージョン管理の面**——リポジトリ全体のメタデータ、ブランチ、
worktree である。「作業コピーの外の全経路」を禁じる規則では**ない**: 決定 4・6・9 は Muse が
`~/.config/muse` と `~/.local/share/muse` に書くことを要求しており、そこは種別自身の状態で、拒否リストの
方で統制する。実測のとおり Muse の worktree 実行は `.git/info/exclude` を書き換え、それはリンク
worktree では親クローンの、全セッション共有のファイルである。よって `-w` は渡さず、worktree 隔離は OFF
（決定 6）、この種別は「ブランチも worktree も作らない種別」として宣言する。並行して書きたい利用者には AF 自身の worktree がある。それが worktree の
用途である。

### 決定 8 — 配備はピン版の焼き込み。`~/.local/bin` の影を見張る

**Muse Code はプロプライエタリなので、配布されるイメージには入らない**——Claude Code・Copilot CLI・
Antigravity が既に置かれているのと同じ規則である。Dockerfile の既定は `ARG BAKE_AGENT_CLIS=0` で、註（`Dockerfile:69-77`）が
ライセンス上の理由——プロプライエタリ CLI は再配布不可扱いなので、素の `docker build` でも混入しない——を
書いており、`deploy/compose/release.sh:53-55` は配布既定を
lean とし、`NOTICE:57-62` が読み手向けに「プロプライエタリな CLI は同梱せず、配備が初回起動時に取得する」と
明言している。よって **variant は 2 つあり、この ADR は両方を決める**（都合のよい方だけを決めない）:

- **出荷される方（`BAKE_AGENT_CLIS=0`）**: entrypoint が
  `/usr/local/share/agent-fleet/versions.json` のピンから導入し、release マニフェストの
  sha256 で検証する。匿名で取得できること（実測）がそもそもこの案を成立させている。**導入先は
  `~/.local`＝ベンダ自身のインストーラと同じ場所**であり（`workspace/Dockerfile:71`、
  `entrypoint.sh:299-349`）、この ADR が下で取り違えていた点がそこにある。AF がそこへ置くのは
  **ベンダの bash ランチャではなくバイナリ**で（門 A: manifest の成果物が*バイナリそのもの*で単体で
  動く）、AF 自身の導入分には自己更新経路が一切無い。
  ⚠️ **無条件ではない。** 門 A では明示 opt-in（`AF_MUSE_BOOT_INSTALL=1`・既定 OFF）として入れた。
  段 2 より前に存在しない種別のために新しいコンテナ全部が 299 MiB を払うのは、kiro（855 MiB）を
  既に無条件 boot-install から外したのと同じ勘定だからである。**段 2 ではこのフラグを ON にするのでは
  なく、kiro の道を最後まで行く**（利用者ごとのオンデマンド `workspace-agent install-muse`）。
- **`BAKE_AGENT_CLIS=1`**（初回起動を速くしたい自社配備）: `ARG MUSE_VERSION` ＋アーキ別 sha256 を
  ビルド時検証し、ランチャーが期待する配置（ランチャーの隣に `muse-bin-<版>` と `.muse-version`）で
  `/usr/local/share/muse` に置く。この配置が読み取り専用でも動くことは実測した。

どちらも共通で、entrypoint の `MUSE_NO_AUTO_UPDATE=1`、`env_tool_versions.go` の 1 行、そして `NOTICE`
への記載を要する——`NOTICE` はここでは事務書類ではなく、「その配備がどのプロプライエタリ CLI を取得するか」を
読み手に伝えるファイルである。

費用は 2 つ、後から発見せずここで名指しする。**容量**: ≈ 299 MiB（x86_64）/ ≈ 269 MiB（aarch64）——
release manifest の実値でちょうど 313,800,920 B と 281,942,104 B。opt-in した新しいコンテナごとの
ダウンロード（実測 19 秒）と、焼く variant ではイメージの肥大になる。上の opt-in を既定 OFF に
している理由でもある。
**影と、その誤った検知の仕方**: ベンダ導入スクリプトの既定の置き場は `~/.local/bin/muse`——そして出荷
variant では **AF 自身も同じ場所に置く**ので、**パスで検知すると AF 自身のバイナリを影として報告する**。
危ないのは場所ではなく素性である: 利用者が一行インストーラを一度走らせると、管理外の自己更新ビルドに
乗り、それは recreate でも消えない。よって検査は**版の一致**——`muse --version` が `versions.json` の
ピンと合うか——であり、直し方は既にある: self-update が OFF の起動では entrypoint が `~/.local` をピン版へ
戻す（`entrypoint.sh:336-352`、kiro の起動ガードが塞いだのと同型の穴）。接続カードが報告するのは版の
不一致であって、パスの有無ではない。門 A は両方向を実測した: 版がずれた影は置き換えられ、**ピン版**を
名乗る影は触られなかった。⚠️ 実装が間違えてはいけない細部が 1 つ——`muse --version` は
`Muse Code 1.3.0 (1.3.0-R3401.1)` と出るので、比較は括弧内のビルド id を取る必要がある。agy の
ブロックが使う `tr -dc '0-9.'` の流儀では `-R3401.1` が落ちて毎起動で不一致になる。

### 決定 9 — 資格情報は保存型 API キー、入力は 1 回

**`muse auth set --api-key-stdin`**——このフラグは省略できない。`muse auth --help` は usage を
`muse auth set [--provider <PROVIDER>] --api-key-stdin` と出し、`muse auth set --help` は「秘密を渡す
唯一の許された方法」「コマンドライン引数としては決して受け取らないのでシェル履歴に残らない」と説明する
（どちらも 1.3.0 で実測）。保存先は `~/.config/muse/auth.json`。

**ファイル拒否リストに入れるのは 1 つでなく 2 つ**: `~/.config/muse`（資格情報）と
`~/.local/share/muse`（全セッションの会話全文と、利用者横断のセッション名権威）。前例はそのままある——
`fs.go:131-137` は同じ理由（資格情報**と**セッションストア）で `.local/share/opencode`・`.codex`・`.kiro`
を既に拒否している。

子プロセスの環境変数に `META_API_KEY` を撒く形は採らない。cursor が env 注入を断った理由
（ADR 0023）がそのまま効き、さらに **API キーは常に保存済みサインインに優先する**ので、env のキーは後から
サインインした利用者を黙って無効化する。よって接続カードは **入力 1 つ**——既存のどの種別より簡単である。
切断は `muse logout`。

未決: Muse Code の**サブスク**利用者（従量ではない）は CLI のオンボーディング中にブラウザでサインインする
が、この箱にその導線は無い。段 1 の門 **B2** で、サブスクの資格情報を別の場所で作って貼れるのか、それとも v1 は
サブスク対象外なのかを決める。

### 決定 10 — 使用量とモデル一覧はプロトコルに乗る

`usage/read`・`session/tokenUsage`・`session/contextUsage` がワイヤにあるので、`muse` は
`usageMeasuredForKind` の **exact** 集合（`usage_fold.go:206`）に入る見込みである。ただし測っていない
「exact」こそあの switch が防ぐために存在する嘘そのものであり、**成功ターン 1 本はその証拠にならない**。
門は段 1 門 B1 の会計マトリクスである: キャッシュ入力が別に数えられるか、subagent とオブザーバの呼び出しが
帰属する（か、除外されると示せる）か、失敗ターンと中断ターン、`session/resume` 後の数字（二重計上が無い
こと）、そして値が累積か毎ターンか——累積カウンタを差分として畳むのが、台帳が黙って倍になる手口である。
これに満たなければ `MeasuredPartial` に落とす。そちらが正直で、`MeasuredExact` はそうではない。
**チップ 2 つには出所が無く、ADR はそれを取り繕わない。** 費用推計は種別→models.dev プロバイダの表
（`workspace/agent/usage_catalog.go:45-55`）を読むが、そこに Meta の行は無い——金額を出すにはトークン
単価をどこかから持ってくる必要がある。サブスクの残量は「5 時間あたりのプロンプト数」で、これは
`get_agent_usage`（claude / codex / agy）の形であってトークン台帳ではないうえ、Muse がそれを見せるのは
TUI の `/upgrade`＝`serve` に無い面である。v1 はトークン台帳だけで、費用チップも残量チップも無しで出荷
する可能性がある。それは段 2 の決定であり、「機能が無い」と後から発見しないようここに名指ししておく。

`model/list` がピッカーを支えるので、`agentModels.ts:34-35` の `isDynamic` に `muse` を足すこと。この 1 行は
新種別のたびに漏れてきた（copilot・cursor）もので、症状は「モデル選択肢が既定だけ」である。

### 決定 11 — MCP: 共有設定ファイルに書き込む新方言

`mcp_servers` は同じ `settings.json` の中のブロックで、`transport: stdio | streamable_http`、
`command`/`args`/`env` か `url`/`headers`、`enabled`、そして **`mode`（既定 `required`）——required の
サーバが起動に失敗すると実行全体が中断する**。AF は **全サーバを `mode: optional`** で materialize し、
利用者に選ばせない。壊れたテナントのサーバのせいでエージェントが起動を拒むのは筋が悪く、しかも登録簿に
その選択を置く場所が無い——`secrets.MCPServer`（`workspace/agent/internal/secrets/secrets.go:302-324`）は
`enabled`・`targets`・`kinds`・`timeoutMs` は持つが `mode` を持たないので、選ばせるなら欄の新設＋ワイヤ＋
Console＋保存済み定義の移行が要る。段 2 で発見するのではなく、ここで対象外と名指ししておく。

**第 2 の経路があり得るので、段 1 で選ぶ。** `session/start` と `session/resume` は `config` を取り、
今そこに許されている唯一のメンバーが `mcpServers` である（`SessionConfig`、スキーマで実測）。セッション
ごとのサーバ一覧がワイヤで効くなら、MCP は共有設定ファイルに入る必要が無くなる——締め付けは依然として
入るが、書き手は独立した 2 ブロックのマージでなくなり、「設定の書き手＋MCP 方言」の作業パッケージが縮む。
門 B1 でサーバ 1 本を両方の経路で送り、実際に接続された方を採る。

`${VAR}` 展開があり、stdio サーバには `MUSE_SESSION_ID` が渡る。`muse` は `knownKinds`
（`mcpreg/def.go:57-61`）と `MaterializedKinds`（`materialize.go:47`）に入る。プロジェクトスコープは
`mcpproj` の `kindInfos`（`mcpproj/inspect.go:50-58`）に **`HasProjectScope: false`** で入る——agy と同じ形
——Muse のプロジェクトスコープ綴りが文献に無いからである。`fileSpecs`（`inspect.go:36-44`）には行を足さない
ので、muse は点検対象でも複製先でもない。これは種別についての静的な事実であって、実行時に他種別のファイルへ
落ちる仕掛けではない。フック（`.muse/hooks.json`、`Stop` や
`Notification` を含む 15 のライフサイクルイベント）は**使わない**。フックが報せることはプロトコルが既に
報せており、リポジトリ内のフックファイルは共有状態だからである。

### 決定 12 — プロジェクト層はホスト単位の決定 1 つ（`--trust-workspace`）を要求する。利用者層とフリート層は実測の穴であり、`instrSupportedKinds` はそれを待つ

プロジェクトスコープは**タダではない**——初稿と第 2 稿はここを間違えていた。Muse がリポジトリ自身の
`AGENTS.md` / `CLAUDE.md` を読むのは workspace を信頼した後だけで、信頼していないセッションは実測のとおり
そう言ってそのまま進む: `rules file at …/AGENTS.md exists, but the workspace is untrusted, so it is
skipped for this session; restart with --trust-workspace`。そして trust は**ワイヤに無い**——MSP スキーマ
に `trust` の語は 1 度も現れない（実測 0 件）。よってプロジェクト規則を効かせる唯一の道は
`muse serve --trust-workspace` であり、それはホスト単位＝決定 3 の「1 セッション 1 ホスト」の形の下では、
子を spawn するときに AF が下すセッションごとの決定になる。

帰結が 2 つ。**AF は `--trust-workspace` を渡す**——利用者がそこでセッションを起こした作業コピーに対して
渡さなければ、このプロジェクトが規約を置いている `AGENTS.md` を黙って落とすことになる。そして同じフラグは
**リポジトリのスキル**も読み込む＝クローンが挙動を持ち込める。これは文献どおりの `.agents/skills/` の
仕組みであり、Console がその作業コピーで任意の種別を起動するときに既に下している信頼の決定と同じもので
ある。ガイドには含みでなく明記する。

ガイド向けの注記: 探索順は union ではなく**優先**である。実測では両方あるとき
`CLAUDE.md is ignored this session because AGENTS.md takes precedence` と出る。このリポジトリでは無害だが、
本体を `CLAUDE.md` に置き `AGENTS.md` をスタブにしている利用者のリポジトリでは、エラーも無く失われる。

残る 2 層は決定 6 のスイッチでは解けず、しかも**仕組みは 1 つでなく 2 つ**である。他 CLI の個人文脈を切るのは
Muse が `~/.claude` と `~/.codex` を読むのを止めるだけで、どちらも届けはしない。
`agent_instructions.go:119-146` は別々に適用する——フリート方針は種別ごとの `ApplyFleetNotes`（claude だけは
`/etc` 配下のファイルとして届く、さらに別経路）、利用者自身の文章は種別ごとの `ApplyUserInstructions`。
それぞれ書込先も成果物も違う。`instrSupportedKinds:83` は「両方を持つ種別」の一覧である。

**Muse の書込先は 2 つとも確定していない**——echo provider の実行を信頼あり／なしの両方で追跡しても、開いた
経路はプロジェクト直下の `.agents` の探索だけだった（その provider ではルールの組み立てが走らないため）。
よって `muse` が `instrSupportedKinds` に入るのは**両方の書込先を別々に実測した後**（段 1 門 B1 が会計
マトリクスと同じ実行で測る）。段 2 には両方の apply 経路と Console の種別別配布状態を含める。それまでは
プロジェクト指示だけで出荷し、ガイドにそう書く——利用者に「フリート方針も届いているはず」と誤解させない。

### 決定 13 — 能力宣言と、本当に新しい唯一の能力 `Permissions`

`Capabilities` は `ProcessModel` だけではないのに、前の稿はそこしか決めていなかった。MSP は
`turn/steer`・`session/fork`・`session/setModel`・`session/setReasoningEffort` を運ぶので `Steer`・
`Fork`・`DynamicModel`・`DynamicEffort` は true で、`muse --help` にも
`--reasoning-effort none|minimal|low|medium|high|xhigh|max|ultra` がある。**だがそれは何も新しくない**
——opencode が既に全部宣言しており（`agents/opencode/driver.go:73-84`）、codex も 1 つを除いて同じである。

**`DynamicMode` は false。** `ThreadSettings.Mode` は AF の plan モードで、註がそのまま
`"plan" | "normal"` と書いている（`agents/driver.go:37`）。MSP にそれを設定するメソッドは無く、
`session/setApprovalMode` が変えるのは承認姿勢——決定 5 が既に起動時の許可選択に写した軸である。1 本の
ワイヤメソッドを AF の 2 つの軸として読むのは、能力表が嘘をつき始める入口である。

**新しいのは `Permissions: true` で、これは配線作業ではない。** 今の managed 種別はすべて false を宣言して
おり、`Interaction.Kind` は `"question"（future: "approval" | "plan"）` と書かれ、註は「3 種別とも承認を
バイパスして動く」と言う（`agents/driver.go:42-52`、引用した一文は `:43`）。つまり宣言するということは、**AF 初の承認
interaction を作る**ということである——ワイヤ型・Console のカード・`approval/decide` への返答経路。
ADR 0093 は既にその一部を払っている: `workspace/agent/internal/harness/approval.go:18` が決定 5 の
`Permissions: true` を「その種別が売る性質」として名指しており、そのパッケージは develop に入っている。
managed 専用の門と同じく、先に着地した方が払う。

**`Caps.PermissionChoice` がもう半分で、しかも別の構造体である。** 読み層の `Caps` は作成要求を門で
止める: `POST /sessions` は `Caps().PermissionChoice` が false の種別の `skip_permissions=false` を拒む
（`sessionx/session_handlers.go:643-647`、`agents/agents.go:86-92`）。決定 5 は承認を残すので、この欄が
無いと起動導線がそもそも承認を要求できない。これは `guide/ref` の能力表が突き合わせる対象でもあるので、
同じ変更で文書にも入る。

`Questions` は別に true で、しかも**承認とは別チャネル**である: `userInput/requested` →
`userInput/answer`、締めが `userInput/settled`。承認は「これを実行してよいか」、user-input は利用者への
質問である。質問の種別は AF に既にある。作るのは承認の方である。

## 段 2 が触る面

下の点検表は、2 つの種別が抜けを抱えたまま出荷したあとに docs/log/43 §4 と docs/log/74 §8 が規則にした
ものである。アンカーは全て `06ea94d3` で読み直した。これが見積りの土台であり、省ける項目は無い。

| 面 | 箇所 |
|---|---|
| 種別の同一性 | `session.go:20-28`、`sessionx/agent.go:28-38`（登録簿。claude への黙った正規化は `AgentOf:40-45` と `NormalizeKind:49-54`）、`sessionx/session_turn.go:31-37`（managed ドライバ表）、`workspace/agent/main.go:189-193`（種別ごとの `ReconcileManaged` goroutine＝per-session-child には必須）、`sessionx/turn_end_poll.go:62-66`、`connections.go:54-60`、`sessionx/session_skills.go:80-91`（muse はスキルを持つ）、`control-plane/enkana_dict.go:199-205` |
| managed 専用の門 | `sessionx/session_handlers.go:650-668`（作成既定）、`session_driver.go:62-105`、Console の起動／driver 切替 5 箇所、`control-plane/scheduler_wake.go:277-285` |
| 接続とログイン | 接続状態、**両方**の `routes.go`（Agent と CP。kiro の前例は start・poll・delete の 3 本で `control-plane/routes.go:869-875`）、そして CP の REST プロキシ許可リスト——これの漏れが「使用量チップが出ない」の真因になる |
| モデルとベンダ | `console/src/lib/agentModels.ts:34-35`（`isDynamic`）、`workspace/agent/model_provider.go:122`（`modelKindVendor`）、モデル REST の分岐 `workspace/agent/agent_models.go:40-83` |
| 使用量 | `usage_fold.go:204-212`、費用表 `usage_catalog.go:45-55`、積み上げ色 `console/src/features/usage/colors.ts:81` |
| 指示層 | `agent_instructions.go:119-146` の両 apply 経路と `:83` の一覧（決定 12 の書込先を測った後） |
| MCP——独立に **4** 本の一覧 | 登録簿（`mcpreg/def.go:57-61`、`mcpreg/materialize.go:47,74-87`、`mcpproj/inspect.go:36-44,50-58`）、**ローカル**の `af` サーバ（`mcpx/mcp_stdio.go` の `list_models` 記述子・`a.Kind != …` の検証・`driver = "managed"` の一覧——この 3 箇所は `06ea94d3` と `73ac5cdc` の間で 6 行ずれたので、行番号でなく記号で指す）、**CP** の MCP ツール（`control-plane/internal/mcpsrv/mcp.go:295,473,487,518-550`——説明文・スキーマ・実行時検証で 3 箇所、1 箇所ではない）、そして `mcpsrv/mcp_server.go:70-74` の `mcpKnownKinds`＝同じ一覧の 4 本目。説明文は全セッションの固定トークン費なので、増やすのではなく書き換える |
| Console の面 | `console/src/types/session.ts:9-12`（`SessionKind` と表示順。これが無いと何も描かれない）、`console/src/agents/registry.ts` の記述子、`console/src/lib/settings.ts:958-966` の起動既定、`LaunchDefaults` の kind union `console/src/features/settings/agents/AgentCardParts.tsx:76`、新しい `MuseCard.tsx` とその配線元 `features/settings/agents/AgentsTab.tsx:250`、`features/settings/workspace/EnvTab.tsx`、`console/src/features/settings/mcp/mcpWire.ts:9`（`MCP_KINDS`・Go 側の写し）、`ScheduleDetailModal.tsx` の `AGENT_KINDS`、`features/mirror/{turnTime.ts:10,FileChangeStrip.tsx:69}`、`features/repos/ProjectActionPanels.tsx:34`、`console/src/lib/brandicons.ts:36` とアイコン資産そのもの、`console/src/lib/termcolor.ts:19`、そして `tokens.css` と機能別 5 スタイルシートにまたがる色の対（docs/log/74 §9.3） |
| 配備と CI | 決定 8 の 2 variant——**`/usr/local/share/agent-fleet/versions.json`**（lean のピンが載る唯一の場所）、entrypoint の boot-install と repin（`entrypoint.sh:299-352`。`MUSE_NO_AUTO_UPDATE` と版一致の検査を含む）と `workspace/Dockerfile` の `BAKE_AGENT_CLIS=1` 経路——、`env_tool_versions.go`、**`NOTICE`**（プロプライエタリ CLI は同梱せずそこに載る）、他のピン済み CLI を運んでいる release / drift ワークフローと setup action |
| 文言 | `bridge/format.go` の `kindLabel`、Console の i18n 目録（en＋ja）、利用ガイド、`guide/ref` の能力表——`scripts/docs-check.py` が `Caps()` と両言語を突き合わせる |
| テスト | MSP スキーマ指紋のドリフト検査、routes・contract テスト、焼いた版文字列を突合する e2e smoke |

## 却下した案

- **v1 で Terminal（CLI）経路も出す。** プロトコルがペイン以上を出している種別に、二つ目の UI と文字列契約を
  足すだけ（決定 2）。
- **全セッションで `muse serve` を 1 本共有する。** サンドボックス姿勢と耐久性はホスト単位で固定なので、
  セッションが互いの姿勢を継ぐ（決定 3）。セッションあたり 73 MiB が買えなくなったら再考する。
- **`META_API_KEY` を環境に置く。** 保存済みサインインを黙って上書きし、cursor が決着させた露出の議論を
  繰り返す（決定 9）。
- **Muse の subagent・workflow・オブザーバ・ピアメッセージングを出荷時のまま走らせる。** 共有ホストでの
  無制限の広がりと見えない出費を招き、Agent Fleet の 4 機能を「観測できない第 2 実装」で二重化する（決定 6）。
- **Muse のピアメッセージングを AF のそれに繋ぐ。** 1 つの名前空間に 2 つの経路、片方はミラーに映らない。
- **コミュニティの ACP アダプタ**（`muse-code-acp`）を managed の継ぎ目にする。版と指紋を持つ一次
  プロトコルの前に、第三者の翻訳層を挟んでも損しかしない。
- **kiro 形の利用者ごとオンデマンド導入。** 違いは置き場所ではない——出荷 variant も `~/.local` に置く
  （決定 8）。違いは**ピン**である: kiro のそれは自己更新し利用者の要求で入るが、boot-install は起動の
  たびに `versions.json` のピンへ戻す。ここで却下しているのは「ピンの無い自己更新バイナリ」であって、
  home ディレクトリではない。
- **プロトコルの良さだけで今すぐ採用する。** 下の 3 門は安く、どれも「いくら読んでも分からないこと」を
  見に行くものである。

## 影響

- **この種別が無料で貰うもの**（他種別が金を払ったもの）: TUI 文字列契約のテスト、フックファイル、
  ポーリング状態検出、セッション ID の発見、転写の逆解析——そして `--provider echo` があるので、
  **transport・転写・状態・ステアリング**を資格情報なしで作り回帰テストできること。最後の 1 つには境界が
  あり、それを明記する: echo はツールを動かさずルールも組み立てないので、承認・`userInput`・動的な
  model / effort・会計マトリクス・指示層はどれも鍵が要る。「作るのは $0」は配管については本当で、種別
  全体については本当ではない。
- **代わりに負うもの**: サンドボックスの免除（決定 5）、壊してはいけない設定ファイル（決定 6）、機能が
  こちらと重なるベンダ（決定 6、および「AF からは何が見えないか」を書くガイドの節）、そして 1 か月で
  1.2 → 1.3 と動いたベータ。
- **ドリフトには鍵がある**: `muse schema` はオフラインで、release マニフェストには
  `msp_schema_fingerprint` がある。焼いたバイナリの指紋と、生成した型が拠った指紋の一致をテストにすれば、
  黙ったプロトコル変更が赤いビルドになる。他のどの種別にも無い仕掛けである。
- **見積り**: managed 専用・TUI 資産なしで **表の合計 22〜33 セッション日、今日の期待値は 23〜35**
  （managed 専用の門が未払いのため。下記）——下表の単純合計で、見出しを綺麗にする
  ための丸めはしない。数字は毎巡動いており、それ自体が正直な信号である: 14〜20（算数が誤り・行が不足）→
  15〜23 → 20〜31 → 22〜33。最後の移動はほぼ 1 行——AF 初の承認 interaction を作ること——と、下向きの訂正
  1 つ（動的な軸の正体は `UpdateSettings` で、ドライバ行に既に計上されていた）である。規模の錨は種別横断の
  棚卸し（今 `kiro` に言及する Go は 90 ファイル・Console は 41 ファイル、既存種別は 1 つあたり非テスト
  2,100〜5,950 行）だが、議論すべきはこの分解の方である:

  | 作業パッケージ | 日数 |
  |---|---|
  | MSP クライアント・生成型・指紋のドリフト検査 | 3〜4 |
  | ドライバ＋`ThreadHandle`（7 メソッド）・状態・ステアリング・中断・resume 突合 | 3〜4 |
  | 転写: ライブの項目と静止時の JSONL、subagent の項目 | 2〜3 |
  | 設定の単一書き手＋締め付け 7 つ＋`Resume` 内の fail-close 配線＋MCP 方言 | 3〜4 |
  | **AF 初の承認 `Interaction` 種別**（ワイヤ・Console のカード・返答経路）＋`userInput` の質問チャネル＋許可選択の写像 | 3〜5 |
  | `Meta.Effort` の配線と Console の model / effort 操作（`UpdateSettings` 自体はドライバ行に計上済み） | 1 |
  | 接続カード・両 `routes.go`・REST 許可リスト・拒否リスト | 1〜2 |
  | 使用量＋モデル一覧＋会計テスト | 1〜2 |
  | 指示層: 両 apply 経路と配布状態（決定 12） | 1〜2 |
  | 配備 **2 variant**（boot-install ＋焼き込み）・ピン・sha256・影の検知・`env_tool_versions`・`NOTICE`・release / drift CI | 2〜3 |
  | Console の面・i18n・ガイド・`guide/ref` の能力表 | 2〜3 |

  **ADR 0093 の状態がここに効き、しかも半分着地している**: `workspace/agent/internal/harness/` は develop に
  あり（その承認まわりは同じ `Permissions: true` を名指す）、`session.KindLcpp` はまだ無い。よって今日の
  期待値は表＋その門で **23〜35 日**である。

  **この非対称は対称な言い回しでなく推奨である。** ADR は「先に着地した方が払う」と 2 度書いているが、
  実際に動いているのは片方だけである。0093 が先に着地すれば、muse の限界費用は managed 専用の門の分と、
  0093 の harness が実際に賄う範囲の承認 interaction の分だけ下がる——門が無料で承認行がこちら持ちなら
  **19〜28 日**、その行が本当に共有なら更に下。0093 を muse より前に置くことはおよそ 1 週間分の価値があり、
  この ADR はその順序を推奨する。
  誤差の向きは
  依然として上振れで、最大の未知は「MSP の 47 メソッド・31 通知のうち、"動く" ではなく "正しい" ドライバに
  何本要るか」であり、3 巡のレビューはいずれも数字を同じ向きに動かした。
- 段 1 の門が落ちたときの埋没費用は、この ADR とプローブだけ。コードは無い。

## 段階

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | この ADR のプローブ（2026-09-20 完了）: 導入・MSP 疎通・ピンとチェックサム・worktree と他 CLI 文脈の挙動・常駐費 | — |
| 1 | **門 A — ✅ 完了 2026-09-20**（門 A の節を見よ）: `bwrap` を焼いて走らせ、免除は恒久と確定、拒否している主体を名指しした（AppArmor `docker-default`）。加えて前提を 2 つ訂正（muse は自前の bwrap を埋め込む／Debian のものは `--ro-bind-symlink` を持たない）。決定 8 の出荷経路も一度通った——sha256 検証・`muse --version` とピンの一致・影の repin 両方向——そしてその過程で実在の欠陥が出た: sha256 検証が boot-install 5 か所すべてで飾りだった。**門 B1**（2〜2.5 日）: API キー 1 本と、決定 10 の会計マトリクス（キャッシュ・subagent・失敗／中断ターン・resume 後・累積か毎ターンか）、加えて `model/list`、`approval/requested` の往復 1 回、`userInput/requested` の往復 1 回、subagent 1 つ、**負荷時のホスト RSS**（決定 3 は待機 73 MiB に乗っており、その再評価条件は自分で「負荷時の実測」を求めている）、**締め付け 7 つそれぞれの効き目**（鍵の綴りだけでなく。書けるが効かない鍵は締め付け無しより悪い——決定 6 はその前に fail-close を置いているからである）、決定 12 のフリート層と利用者層の書込先（別々に）、決定 6 の締め付け 7 つの設定鍵（綴りはフラグ名から推測できない）、設定ファイルのロック規約を syscall で、`session/start.config.mcpServers` が効くか（決定 11 の第 2 経路）、そして **`-w` を渡さない通常実行**が作業コピーに何を書くか（決定 7 は今 `-w` の実測だけに乗っている）。**門 B2**（0.5 日）: この箱で走らせられないブラウザオンボーディングを前提に、**サブスク**の資格情報を別の場所で取得してここに入れられるか。⚠️ B1 は 9 項目を抱えており、ロック規約の追跡だけで半日級である。溢れた場合に段 2 へ回してよいのは MCP の第 2 経路と締め付けの鍵の綴りで、会計マトリクスは決して回さない | A と B1 に答えが出て、利用者が決定 6 の締め付けと出費を受け入れる。B2 は「不可」でもよい——その場合 v1 は従量のみとガイドに書いて段 2 へ進む |
| 2 | 実装: 種別配線、MSP クライアントと生成型、ドライバ、転写、使用量、設定＋MCP 方言、接続カード、配備、ガイド、この ADR を *adopted* へ | — |

## 未解決（段 1 で答える）

1. 決定 10 の会計マトリクスを丸ごと——ターン 1 本ではない。`MeasuredExact` か `MeasuredPartial` かが
   決まり、ここを間違えると台帳が黙って倍になる。
2. Muse が**利用者スコープ**のルールをどこから読むか。決定 12 が `muse` を `instrSupportedKinds` に
   入れられるようになる。プローブでは決着しなかった（echo provider ではルールが組み立てられない）。
3. サブスクと従量（門 B2）: この箱で走らせられないブラウザオンボーディング抜きに、サブスクの資格情報は
   作れるか。作れないなら v1 は従量のみで、ガイドにそう書く。
4. 認証後の `model/list` は目録を返すか。返すとして、copilot や cursor のようにプラン依存か
   （「Free では named model 不可」型の失敗）。
5. 承認の描き方: `approval/requested` は段階的なシェル審査の情報を運ぶ。そのどこまでを、新しいカード種別を
   増やさずにミラーの許可カードで出せるか。
6. セッションごとのホストの `session/list` が、他の AF セッションの Muse セッションまで見えてしまうか
   （ストアも利用者も 1 つ）。見えるなら、Console が決してそれらを差し出さないこと。
7. `Meta.Subdir` を持つセッションで `session/start.workspaceRoot` に何を送るか（作業コピーか、その下位
   ディレクトリか）。`--trust-workspace` が何に掛かるかがそれで決まる。`--allow-workspace-switch` が
   あるので誤っても回復はできるが、紛らわしい。

## プローブの再現（2026-09-20・Muse Code 1.3.0-R3401.1）

```bash
curl -fsSL https://dev.meta.ai/install.sh -o install.sh          # 落ちるのはランチャーだけ・素直に読める
MUSE_INSTALL_DIR=$HOME/muse-probe MUSE_NO_MODIFY_PATH=1 \
MUSE_NO_AUTO_UPDATE=1 MUSE_LOGIN=0 bash install.sh               # 約 300 MiB・アカウント不要
curl -s https://api.meta.ai/muse-code/channels/muse-stable       # 版と manifest_url・匿名で
~/muse-probe/muse schema generate-json-schema --out ./msp        # 47 メソッド・オフライン
~/muse-probe/muse exec --provider echo --json "say hi"           # 資格情報なしで 1 セッション完走
~/muse-probe/muse serve --disable-sandbox                        # stdin/stdout に JSON-RPC 行
unshare --user --map-root-user --mount --propagation unchanged \
  strace -e trace=move_mount mount -t tmpfs none /tmp/x          # 新 API: move_mount → EACCES
LIBMOUNT_FORCE_MOUNT2=always unshare --user --map-root-user --mount \
  --propagation unchanged strace -e trace=mount \
  mount -t tmpfs none /tmp/x                                     # bwrap の API: mount(2) → EACCES
~/muse-probe/muse auth set --help                                # フラグは必須
```

門 A で足した分（2026-09-20）。`apt` には root が要るので、同じパッケージを手で展開する:

```bash
curl -fsSL -o bw.deb http://deb.debian.org/debian/pool/main/b/bubblewrap/\
bubblewrap_0.12.0-1~deb13u1_amd64.deb
dpkg-deb -x bw.deb root/                                         # root 不要
./root/usr/bin/bwrap --ro-bind / / --dev /dev true               # Failed to make / slave: EACCES
strace -f -e trace=mount,move_mount,open_tree,fsopen,unshare,clone \
  ./root/usr/bin/bwrap --ro-bind / / --dev /dev true             # clone は成功・最初の mount() が EACCES
cat /proc/self/attr/current                                      # docker-default (enforce)
unshare --user --map-root-user --mount --propagation unchanged \
  grep CapEff /proc/self/status                                  # 000001ffffffffff ＝全能力
./root/usr/bin/bwrap --ro-bind-symlink /etc /etc true            # Unknown option（muse は必須とする）
~/muse-probe/muse sandbox --help                                 # windows check|setup 専用
```

## 参照した出所（2026-09-20・`06ea94d3`）

`workspace/agent/internal/session/session.go` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go,session_handlers.go}` ·
`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/mcpreg/{def.go,materialize.go}` ·
`workspace/agent/internal/mcpproj/inspect.go` · `workspace/agent/internal/secrets/secrets.go` ·
`workspace/agent/internal/bridge/format.go` ·
`workspace/agent/{usage_fold.go,agent_instructions.go,model_provider.go,env_tool_versions.go,fs.go}` ·
`control-plane/{routes.go,scheduler_wake.go}` · `control-plane/internal/mcpsrv/mcp.go` ·
`workspace/Dockerfile` · `console/src/agents/registry.ts` · `console/src/lib/{agentModels.ts,settings.ts,brandicons.ts,termcolor.ts}` ·
`console/src/styles/tokens.css` · `console/src/features/usage/colors.ts` ·
`docs/log/{36,40,43,74}-*-agent-kind.md` · `docs/decisions/0093-lcpp-agent-kind.ja.md` ·
Meta の文献 `dev.meta.ai/docs/muse-code/{,auth,subscriptions,permissions,interactive,workflows,session-messaging,rewind,configuration,extending,changelog}`。

## レビュー 第 1 巡（2026-09-20・別セッションの codex / gpt-5.6-sol）

指摘 15 件。着手前に全アンカーをツリーで読み直した。構造に関わる 4 件はここに注記せず上の決定本文を
直した——作成経路が空 driver を TUI に既定化する（決定 2）、`muse auth set` は `--api-key-stdin` が要る
（決定 9）、拒否リストは `~/.local/share/muse` も要る（決定 9）、`mcpproj` は他種別のファイルへ落ちる
仕掛けではない（決定 11）。決定 3 の根拠は内部的に弱かったので、本当にホスト単位である摘みを軸に書き直した。
決定 6・10・11・12、段 2 の点検表、段階の門、見積り表はいずれもこの巡の産物である。

逆向きの訂正も 2 件あり、それもレビューが見つけた。本 ADR は「積み上げで青 3 本を隣接させるな」と書いて
いたが `KIND_STACK_ORDER` は shell も ssm も持たないので 2 本であり、`workspace/Dockerfile:318-320` を
「npm 3 種」と引いていたが実際は `:318-321` の 4 種である。

レビューが読み直して一致を確認したもの: 9 種別、`Agent` の 6 メソッド、`ThreadHandle` の 7 メソッド、
exact / partial の使用量集合、MCP の 7 種別、指示配布の 6 種別、登録簿の RSS 値、モデル接頭辞、色トークン。
英日で主張と数値が対応していること。`scripts/docs-check.py` が緑であること。やっていないこと: ログイン、
実ターン、サブスク、実 `bwrap`、ベンダ文献の再検証——それはまさに段 1 が越えるための境界である。

## レビュー 第 2 巡（2026-09-20・同じセッション・修正後の本文に対して）

指摘 12 件。上の修正がその全部への答えであり、うち 3 件は**第 1 巡の修正自身が持ち込んだ誤り**だった。
**決定 7 の書き直しは行き過ぎていた**——「このセッションの作業コピーの外の全経路」を禁じると、Muse 自身の
状態ディレクトリへの書き込みを要求する決定 4・6・9 と矛盾する。**決定 6 は fail-open だった**: `Materialize`
は設計として失敗をログに落として飲み込み、`StartManagedSession` は結果を見ない
（`mcpx/mcp_materialize.go:30-41`）ので、書き込み失敗は締め付けが全部外れたセッションを起動させる。さらに
JSON materializer に締め付けを重ねる形では、MCP サーバが 0 件の種別で締め付けが**書かれない**
（`materialize_json.go:110-112`）。**見積り表は合計が合っていなかった**: 同じ 9 行が 15〜23 になるのに
見出しは 14〜20 だったので、行ではなく見出しを動かした。

決定 3 の根拠はまだ強すぎた——AF に今あるセッション単位の姿勢は `Meta.Mode` と `Meta.SkipPermissions`
（`session.go:377-390`）だけで、write / shell / trust の姿勢は無い——ので、本当に共有できない 1 つ
（作業コピーごとに違う workspace trust）から論じる形に直した。決定 12 は配布の仕組みを 1 つとして書いて
いたが、ツリーには 2 つある（`agent_instructions.go:119-146`）。決定 5 の「恒久」には、それが依存している
ホスト契約の限定を付けた。段 2 の点検表には、2 つの MCP サーバの kind 列挙、モデル REST の分岐、漏れていた
Console 5 箇所、決定 8 が含意していたのに表に無かった配備と CI の行を足した。`routes.go` の kiro の前例は
start だけでなく start・poll・delete（`:869-875`）である。

変更なしで確認できたもの: 決定 2 の作成経路とスケジューラのアンカー、決定 11 の `MCPServer` と `mcpproj` の
指摘、英日の対応、`docs-check` が緑であること。

## レビュー 第 3 巡（2026-09-20・新しい claude / opus セッション）

3 人目の読み手に、既に閉じた指摘を伝えたうえで「まだ誰も問うていない前提」に労力を割いてもらった。
指摘 15 件、うち 14 件が正しい。3 件は決定そのものを変えた:

- **AF が必要とする締め付けのスイッチが、AF が動かすプロセスに存在しない。**
  `--no-foreign-personal-context` は `muse exec` のフラグで、`muse serve` は `unknown option` と答える
  （再現済み）。決定 6 の「または設定の等価物」は代替ではなく唯一の経路であり、鍵の綴りは前提ではなく
  門の項目になった。
- **プロジェクト指示層はそもそもタダではなかった**（決定 12）。信頼していない workspace は `AGENTS.md` を
  飛ばしてそう言うし、MSP スキーマに `trust` の語は 1 度も出てこない＝ホストのフラグにしかなり得ない。
  「何もしなくてよい」は「`--trust-workspace` を渡す決定」に変わり、同時に「そうするとリポジトリのスキルも
  読み込まれる」という認めるべき事実になった。
- **`StartManagedSession` での fail-close は効かなかった。** `Resume` は turn・answer・carried・bridge の
  各経路と、Agent 起動時の `ReconcileManaged` からも呼ばれる。よって門はドライバの `Resume` の内側へ移し、
  前例は kiro の `ensureSettings`、その `sync.Once` とエラー握り潰しは反面教師とした。

残りは「ADR がそもそも決めていなかったこと」を足した: `Capabilities` 宣言まるごと（決定 13——当時の書き方は
「動的な軸が全部 true になる初の種別」。第 4 巡がこの枠組みを覆し、新しい能力は `Permissions` だけになった）、承認ジャッジの承認ごとの
モデル呼び出しと同梱の `resume-claude` / `resume-codex` / `import` / `migrate` を締め付け 6・7 に、1 度きりの
セッション ID（決定 4——AF の決定論的 UUIDv5 は使えない）、費用と残量の出所が無いこと（決定 10）、
`session/start.config.mcpServers` という第 2 の MCP 経路（決定 11）、MCP 種別一覧の 4 本目
（`mcpsrv/mcp_server.go:70-74`）と点検表に足りない十数箇所。見積りは再び上がって 20〜31 日になった——表が
まだ作業に足りていなかったからである。

1 件は採らなかった。決定 9 が引く `muse auth set` の usage は `muse auth --help` の出力そのままである
（レビュアーが読んだのは `muse auth set --help` で、こちらは別の usage 行を出す）。両方を確かめたうえで、
本文には「どちらのコマンドの出力か」を書き足した。

この巡で再検証していないこと: ログイン、実ターン、トークン会計、サブスクの資格情報、実 `bwrap`、MSP の
端から端までの駆動、release マニフェスト——前巡と同じ境界である。

## レビュー 第 5 巡（2026-09-20・同じ opus セッション）——結論は保つ

「結論を覆すものがあるか」を主題に置いて訊いた答えは **無し**: managed 専用であること、採否を段 1 の
3 門で決める構えは、どちらも再検討に耐えた。残存リスクは門 B1 に集中しており、そこで落ちても費用は
この ADR とプローブだけである。第 4 巡の 15 件は全件解消を確認。指摘は 12 件、うち 1 件が構造的だった。

- **決定 8 は出荷 variant の導入先を取り違えており、その誤りが自分の影の見張りを壊していた。** lean の
  boot-install は CLI を `~/.local` に置く——ベンダのインストーラと同じ場所——ので、「接続カードが
  `~/.local/bin/muse` を見つけたら報告する」は **AF 自身のバイナリを報告する**ことになっていた。検査は
  `versions.json` との版一致に直し、直し方は entrypoint の既存の repin（`entrypoint.sh:336-352`）である。
  却下していた kiro 形も、場所が同じになった以上、ピンの有無で論じ直した。
- **`Caps.PermissionChoice` が丸ごと抜けていた**（この巡の前は出現 0 件）。これが無いと
  `POST /sessions` が `skip_permissions=false` を拒むので、決定 5 の「承認は残す」を起動時に要求すら
  できない。`Capabilities` ではなく `Caps`——構造体が 2 つあり、決定 13 は片方しか決めていなかった。
- 門の穴が 3 つ、いずれも今いる場所で安く塞げる: 決定 3 自身の再評価条件が求める**負荷時**の RSS、
  門 A のイメージビルドでタダで通せる**出荷側**の配備経路、そして鍵の綴りではなく締め付けの**効き目**。

ほかに、見積りを表（22〜33）と今日の期待値（23〜35）の両方で書き、存在しない対称を繰り返す代わりに
「ADR 0093 を先に置くことがおよそ 1 週間分の価値がある」と明記した。アンカーのずれ 3 件
（`fileSpecs`・`driver.go`・`06ea94d3` と `73ac5cdc` の間で動いた `mcp_stdio.go` の 3 行）、第 4 巡で
半分しか直っていなかった決定 6 の 1 文、そして日本語コメントを英語の言い換えで引用符に入れていた箇所も直した。

この巡で独立に再実測され、本文と一致したもの: release マニフェスト（匿名・7 成果物すべてに sha256・
299.3 / 268.9 MiB）と、名指しに値する 1 つ——**ドリフトの鍵が実際に効く**こと（マニフェストの
`msp_schema_fingerprint` が手元の `muse schema` が書く指紋と一致）。この ADR が引くファイルのうち
`06ea94d3` から `73ac5cdc` の間に変わったのは 1 本だけだった。

## レビュー 第 4 巡（2026-09-20・同じ opus セッション）

指摘 15 件。効いた 3 件はいずれも、この ADR がツリーを都合よく読んでいた例である。

- **配布されるイメージはプロプライエタリ CLI を焼かないのに、決定 8 は焼く前提だった。** Dockerfile の
  既定は `BAKE_AGENT_CLIS=0`、`release.sh` は配布既定を lean とし、`NOTICE` は「プロプライエタリな
  エージェント CLI は同梱せず初回起動時に取得する」と読み手に告げている。Muse Code はプロプライエタリ
  なので出荷される姿は boot-install である。決定 8 は 2 variant を両方決める形に直し、`NOTICE` を点検表に
  入れた。
- **`DynamicMode` は誤り。** `ThreadSettings.Mode` は AF の plan モード（`"plan" | "normal"`、
  `agents/driver.go:37`）で、`session/setApprovalMode` が変える承認姿勢ではない。1 本のワイヤメソッドを
  AF の 2 つの軸として読んでいた。
- **`Permissions: true` は配線作業ではない。** `Interaction.Kind` は今 `"question"` だけで approval と
  plan は future 扱い、managed 種別はすべて `Permissions: false` を宣言している。宣言するとは AF 初の
  承認 interaction を作るということで、見積りが動いた理由もそれである。

残りは、この ADR が earned していない「初」を主張していた件が中心: opencode は既に
`Steer` / `Fork` / `DynamicModel` / `DynamicEffort` / `DynamicMode` / `Questions` を宣言しており、新しい
能力は `Permissions` だけ。ADR 0093 は**半分**着地している（`internal/harness/` は develop にあり、
`session.KindLcpp` は無い）ので承認作業の負担者が変わる。`ClearResume` は死んだコードで、recreate で欄が
落ちる実体は `session.Meta` のホワイトリスト再構築である。同梱スキルの締め付けは `daemon`・
`host-manager`・`slack-connector`・`create-plugin`・`read-session`・`doctor` が漏れており、無効化の鍵は
パック名込みのパスなのでドリフト検査の対象である。「$0 のハーネス」には、echo がツールを動かさずルールも
組み立てないという境界が要る。

逆向きの 1 件も記録する。第 3 巡の `muse auth set` の指摘は、レビュアー自身が測り直して撤回した——ADR の
引用は正しかった。

再検証していないこと: 第 2・3 巡と同じ境界に加えて、この巡の能力の主張はスキーマと AF の型定義の突き合わせ
までで、実際にドライバを動かしてはいない。

## 段 1 門 A の実測（2026-09-20）

門 A は `e627536f` で実施した。**決定 5 の免除は恒久として確定し、決定 8 の出荷経路は一度通った**
ので、両決定は本文（上）を書き換えてある。以下はすべて **実機の Workspace コンテナ** での実測
——amd64・Debian trixie・AppArmor プロファイル `docker-default (enforce)`・seccomp filter mode 2
——で、答えが問われているのはまさにこの環境である。資格情報は一切使っていない。

### A-1: bubblewrap はここでは動かない。そして「なぜ動かないか」の前提が 3 つ間違っていた

`bwrap` は Debian trixie の純正パッケージ（`bubblewrap_0.12.0-1~deb13u1_amd64.deb`・
sha256 `70aca4fa…`・`bubblewrap 0.12.0`）を使った——`workspace/Dockerfile` がいま焼くのと同じ物。

`bwrap --ro-bind / / --dev /dev true` は `bwrap: Failed to make / slave: Permission denied` で
終了 1。`--unshare-user`・`--unshare-all`・素の `--ro-bind / /` も同じ。strace で見ると正確な姿が出る:

```
clone(CLONE_NEWNS|CLONE_NEWUSER|SIGCHLD)                          = 202910   <- 成功
mount(NULL, "/", NULL, MS_REC|MS_SILENT|MS_SLAVE, NULL)           = -1 EACCES
```

**この ADR が書いていたより早く落ちる**——bind を付ける `move_mount` ではなく、マウントを 1 つも
作る前の最初の呼び出し、`/` を rslave にするところで落ちている。誰が拒否しているかは syscall の
表が答える。`unshare --user --map-root-user --mount` の中では uid 0 かつ
`CapEff: 000001ffffffffff`（`CAP_SYS_ADMIN` を含む全 41 能力）を持っている:

| syscall | userns の外（uid 1000・`CapEff: 0`） | userns の中（uid 0・全能力） |
|---|---|---|
| `mount(NULL, "/", …, MS_REC\|MS_SLAVE, …)` | **EACCES** | **EACCES** |
| `mount("none", "/tmp", "tmpfs", …)` | **EACCES** | **EACCES** |
| `fsopen("tmpfs", 0)` | EPERM | **成功**（fd 3） |
| `open_tree(AT_FDCWD, "/", OPEN_TREE_CLONE\|AT_RECURSIVE)` | EPERM | **成功**（fd 3） |
| `move_mount(…)` | EPERM | **EACCES** |

🔴 **拒否しているのは LSM で、いまや名指しできる: AppArmor の `docker-default` プロファイルである。**
**切り離したハンドルを作るだけ**の 2 本は `CAP_SYS_ADMIN` を得た瞬間に EPERM から成功へ変わる
——これは能力の層が満たされていることと、seccomp が新マウント API を濾していないことの両方を示す。
一方**マウントを接続または変更する** 2 本は **能力に関わらず EACCES** を返す。この組み合わせは
どちらの層にも作れない: カーネルの能力検査が落ちるときの errno は EPERM（userns の外の `fsopen`
がまさにそれ）であり、seccomp の `ERRNO` フィルタは能力を見ないので、2 列目でだけ `fsopen` を
通すことはできない。EACCES を返すのは AppArmor のマウント仲介である。ここでは切り離した
マウントツリーを作ることはできるが、接続することは決してできない。**このリポジトリが出荷する
どんな物でもこれは変えられない**——変えられるのはコンテナランタイムのプロファイルだけで、それが
決定 5 の言う「ホストの契約」そのものだ。

この ADR の前提が 2 つ間違っており、どちらも同じ向きに効く:

- 🔴 **「`bwrap` がイメージに無い」は、そもそも阻害要因ではなかった。** muse は**自前の埋め込み
  bubblewrap を同梱している**——バイナリ中に `bubblewrap built for TBH`・`__tbh_internal_bwrap`・
  `TBH_BWRAP_EXE`・`--tbh-bwrap-selection-v1` があり、私用マーカー無しで内部モードを叩くと
  `tbh: invalid private Linux invocation markers` と答える。エラー文言自体もそう言っている:
  「PATH に capability-valid な system bwrap が見つからず、**かつ使える埋め込みフォールバックも
  無い**」。system の `bwrap` を入れることは muse のサンドボックスを有効にもしないし、必要でもない。
- 🔴 **仮にマウントが許されていても、Debian の `bwrap` では muse の要求を満たせない。** muse は
  `--perms`・`--ro-bind-data` **と `--ro-bind-symlink`** を必須とする（"selected Bubblewrap lacks
  required --ro-bind-symlink support"）。trixie の 0.12.0 は前 2 つを持つが 3 つめには
  `bwrap: Unknown option --ro-bind-symlink` と答えるので、muse は自前の可用性プローブの段階で
  この bwrap を拒否する。

muse 自身のプローブ argv（バイナリから復元: `--ro-bind / / --dev /dev --bind <probe> <probe>
--proc /proc --unshare-pid --unshare-net --new-session --die-with-parent --chdir <probe>
/bin/sh -c 'printf ok > "$1" && printf no > "$2"'`）を再生しても、同じ最初の呼び出しで落ちる。

つまり門 A の期待どおりの答えが、1 本ではなく**独立した 3 本の道**で出た。**`bwrap` はそれでも
焼いてある**（`workspace/Dockerfile`）——ホストの LSM 方針が変わった日に再計測をコマンド 1 本で
やり直せるようにするためで、Dockerfile のコメントには「そのためであって、サンドボックスを
有効にするためではない」と明記した。

⚠️ **測り直す人への罠: この問いを CI で答えてはいけない。** `deploy/local/e2e-smoke.sh` は
`--cap-add=SYS_ADMIN` かつ `docker-default` が当たらないランナーでイメージを回す。そこで `bwrap`
が通っても Workspace については何も言っておらず、決定 5 が覆ったように見えるだけである。

**再現できず、門 B1 送りにしたもの**: ベンダの言う「サンドボックス下のシェルコマンドはすべて
環境エラーで中断する」。これにはシェルのツール呼び出しが要るが、無課金の経路では作れない——
`--provider echo` はツール呼び出しを一切出さず（実行は `echo: <プロンプト>` だけを出力して
completed になる）、`muse sandbox` は実は `windows check|setup` **専用**で、Linux の事前検査は
存在しない。上の機構からして主張はきわめてもっともらしいが、未計測である。

echo の 1 ターンが見せたことが 1 つあり、これはここではなく決定 10 に属する: たった 1 ターンで
**バックグラウンドの観測エージェントが 2 つ**（`reminder.agent.skill-reminder`・
`reminder.agent.verify-reminder`）スケジュールされ起動した。2 つめが
`invalid run configuration: provider does not support base instructions` で終わったのは、echo
プロバイダに組み立てる指示が無いからにすぎない。実プロバイダならこれらは **Agent Fleet に
見えないモデル呼び出し**になる。

### A-2: 出荷される配備経路を一度通した

manifest は 2 つとも **匿名で HTTP 200**: チャンネル（254 B）が `1.3.0-R3401.1` を名指し、版指定の
リリース manifest（1,852 B）が `artifacts.x86_linux` = checksum `71b089d0…` / size
**313,800,920 B**、`artifacts.aarch64_linux` = `5e5ea2a3…` / **281,942,104 B** を載せる（この ADR が
言う 299 MiB と 269 MiB）。`msp_schema_fingerprint` は `sha256:7469c9e3…` で、段 0 が
`schema generate-json-schema` からオフラインで得た指紋と一致する。

**配布物はランチャではなくバイナリそのもの。** その checksum はベンダのインストーラが置く
`muse-bin-<版>` の sha256 と一致し、単体で動く（`Muse Code 1.3.0 (1.3.0-R3401.1)`）。ベンダの
`~/.local/bin/muse` は**チャンネルを毎時見て自分を書き換える bash ランチャ**の方だ。だから AF は
**その名前でバイナリを置く**——AF 自身の導入分には自己更新経路がそもそも存在しない。
`MUSE_NO_AUTO_UPDATE=1` は、それを影として覆うベンダのランチャのためにある。

実機の `entrypoint.sh` のブロック・実 CDN・使い捨て `HOME` で測った:

| 検査 | 結果 |
|---|---|
| 既定（opt-in なし） | 無音の no-op・4 ms |
| boot-install（空の home） | 313,800,920 B を **19 秒**・`[entrypoint] boot-install muse 1.3.0-R3401.1` |
| 導入されたファイルの sha256 | `71b089d0…` ＝ manifest の値 |
| `muse --version` とピン | `Muse Code 1.3.0 (1.3.0-R3401.1)` ——ビルド id が `versions.json` と一致 |
| 2 回目の起動 | `boot-install: muse already present (skip)` |
| repin・版がずれた影 | 検知してピン版へ置き換えた |
| repin・**同じ**版の影 | 触らなかった——検査は版一致であり、パスを見ていない |

最後の 2 行が決定 8 の影の規則が両方向に効いている証拠で、そこが要点だ: パスで見ていたら
**AF 自身のバイナリを影として報告していた**。なお版文字列は `Muse Code 1.3.0 (1.3.0-R3401.1)` なので
比較は括弧内のビルド id を取らねばならない——agy のブロックが使う `tr -dc '0-9.'` の流儀では
`-R3401.1` が落ちて永久に不一致になる。

🔴 **boot-install の sha256 検証は飾りだった——5 か所すべてで。** 形はこうだ:

```sh
( set -e
  echo "${sha}  artifact" | sha256sum -c - >/dev/null
  install -D -m 0755 artifact "$HOME/.local/bin/…"
) && echo "boot-install ok" || echo "WARN: failed"
```

POSIX は「AND-OR リストの最後以外のコマンド」では `-e` を無視すると定める。サブシェルは
まさにその左辺なので、**中に書いた `set -e` は何もしない**。bash 5.2.37 と trixie の dash で実測:
checksum を 1 文字だけ変えると、`WARNING: 1 computed checksum did NOT match` を出したうえで
**成果物をそのまま導入し、成功として記録した**。rtk・agy・cursor・rtk の自己更新・muse が該当。
検証を errexit に頼らず**明示**（`exit` は errexit と無関係に効くので `|| exit 1`）することで直した
——⚠️ サブシェルを `if ( set -e; … ); then` へ移すのは**直らない**。if の条件もまた `-e` が
無視される文脈だからだ。修正後、陰性対照は WARN を出して何も導入せず、陽性対照は従来どおり導入する。

🔴 **決定 8 の「コンテナ起動時に boot-install する」は明示 opt-in に変えた**
（`AF_MUSE_BOOT_INSTALL=1`・既定 OFF）。299 MiB あり、段 2 より前には `kind=muse` が存在しない以上、
無条件にすればフリート中の新しいコンテナ全部が**誰も使えない CLI** を落とすことになる。これは
kiro（855 MiB）を無条件 boot-install から利用者ごとのオンデマンド導入へ移したときと同じ勘定だ。
**段 2 ではこのフラグを ON にするのではなく、kiro の道を最後まで行く**べきである
（`workspace-agent install-muse`）。

### 門 A がやらなかったこと

ログインなし・実ターンなし・サブスクリプションなし・`-w` なし・`kind` の配線なし——すべて段 2 か
門 B1 の仕事。muse は使い捨てリポジトリに対し `--provider echo` でしか動かしていない。
