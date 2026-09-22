# 0095. Meta の Muse Code をセッション種別（`muse`）にする — TUI 契約ではなくベンダのプロトコルに乗る、実機 3 門の先で

[English](0095-muse-agent-kind.md) | 日本語

- Status: **adopted**（2026-09-21）。段 1 の 3 門は 2026-09-20 に回答済み、段 2 は 2026-09-21 に
  15 個の作業パッケージで着地した（末尾の実装記録 P2-1〜P2-15＝下の作業パッケージ表の全行に加え、
  どの行も持っていなかった MCP のワイヤ経路と fork、決定が約束していて表に行の無かったガイド記述
  2 件、そして能力 2 行を ✓ にし af サーバ自身の環境の 401 を見つけた実ターン 3 本）。種別は 2 つの前提（プロプライエタリのバイナリが導入済み・資格情報がある）の先で起動
  メニューに出る。**作られていないもの**はガイドの能力表と末尾節に名指してあり、あそこの空欄は
  「まだ検討中」ではなく「まだ作っていない」を意味する。
  P2-16（コンテキスト使用量ゲージ）実機確認済み（2026-09-22）: `usedTokens=21747`・
  `windowTokens=1007997`（ワイヤ上に在り、`windowSource=recorded`）・6.7 秒で完了。
  `caps.contextBar` を `true` に反転し、ガイドの行を ✓ に更新した。
  以下の決定の `file:line` は当時の develop `06ea94d3` で読んだもので、実測が動かした箇所は実装記録が
  訂正している。◎ は Workspace のコンテナで **Muse Code 1.3.0-R3401.1** を実測したもの、△ はベンダ
  文献のみ、× は未測。再現手順は「実機プローブの再現」節にある。
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
| read 正本 | **◎ 追記のみ** | `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` ——**根セッション 1 つにファイル 1 本で、subagent のレコードは同じファイルに `stream.id` 違いで混ざる**（門 B1 実測。`subagent/<id>/` ディレクトリは存在しない）。`session/start` の応答がその `path` を返すので AF が推測する必要が無い |
| 版ピン＋sha256 | **◎ 両方、匿名で** | channel マニフェスト（`api.meta.ai/muse-code/channels/muse-stable`、匿名 200）が**版付きアドレスの** release マニフェストを指し、そこにプラットフォーム別の url・**sha256**・サイズがある（`aarch64_linux` も）。成果物自体も匿名で 206 ＝**焼くのにアカウントが要らない** |
| 自動更新の封殺 | ◎ | `MUSE_NO_AUTO_UPDATE=1` の env 1 本。これを立ててバイナリが在れば、ランチャーは**読むだけ**になる（読み取り専用ディレクトリで `muse --version` が成功） |
| ハーネス構築の原資 | **◎ ほぼ $0** | `--provider echo` という決定的な組み込み provider がある。`muse exec --provider echo --json` は資格情報なしで 1 セッションを完走した。アカウントは受け入れに要るのであって、構築には要らない |
| 常駐費 | ◎ | 待機中の `muse serve` は **≈ 73 MiB RSS**（74,924 KB）。**負荷時はホスト＋子で 137〜147 MiB**（門 B1。最大値は subagent を生んだターン）。登録簿の `tuiMemoryCost` は claude 230 MiB、opencode 300 MiB（`console/src/agents/registry.ts:227,469`） |
| **OS サンドボックス** | **🔴 ◎ ここでは恒久に動かない** | Linux のサンドボックスは bubblewrap（バイナリ中に `bwrap` 38・`seccomp` 50 の文字列。文献は「動作する bubblewrap と非 musl ビルドが要る。無ければサンドボックス下のシェルコマンドは全て environment failure で中断する」と言う）。門 A が実機の `bwrap` で決着させた: ユーザー名前空間は作れて全 41 能力を得るのに、**`mount(2)` と `move_mount(2)` は能力に関わらず EACCES** を返し、`fsopen` / `open_tree` は成功する——これは seccomp でも能力でもなく **AppArmor の `docker-default` プロファイル**の指紋である。muse は*埋め込み*の bwrap も同梱しており、バイナリの不在はそもそも阻害要因ではなかった |
| **共有リポジトリへの書き込み** | **🔴 ◎ 触る** | `muse exec -w create` は `<repo>/.muse/worktrees/<日付>-<hash>` を作業根に選び、`.muse/.session-worktree-reservations/` を作り、**`.git/info/exclude` に `/.muse/worktrees/` を追記した**。リンク worktree ではそのファイルは親クローンのもの＝全セッション共有 |
| **他 CLI の個人領域** | **🔴 ◎ 既定で読み、モデルまで届く** | 初回起動が `Including your Codex personal rules and 5 skills` と出した。門 B1 がその帰結を実測した: 切らないまま実ターンを回すと、**モデルの答えに `~/.claude/CLAUDE.md` の中身がそのまま出た**——利用者の Claude Code のルールが Meta へ行く。止めるフラグ `--no-foreign-personal-context` は `muse exec` にはあるが **`muse serve` には無い**（実測: `unknown option`）。効く鍵は `context.foreign_personal_rules` と `context.foreign_personal_skills` の **2 本**で、片方だけでは片方が残る |
| 機能の重複 | ⚠️ △ | subagent（既定 1 木 8・`agents.execution_capacity` は 1〜64）、それぞれ独自にモデルを呼ぶ背景オブザーバ 4 本、workflow（生涯 1,000 子）、**利用者横断のセッション名前空間**とピアメッセージング。いずれも AF の登録簿・ミラー・使用量台帳からは見えない |
| 認証 | **◎ デバイスコード** | `muse login` は `https://auth.meta.com/oauth/device/?code=XXXX-XXXX` を表示してポーリングする——TTY もローカルコールバックも要らず、cursor / kiro の接続カードと同じ **start→poll** の形である。保存先は `~/.config/muse/auth.json`。API キー（`META_API_KEY` / `muse auth set`）は常にアカウントログインに**優先する**ので、サブスク契約では絶対に置いてはならない |
| 課金 | **◎ ワイヤに乗る** | トークン従量、または定額サブスク。枠は `usage/read` / `usage/changed` が `{tier, weekly{usedPercent}, window{usedPercent, windowDurationMins: 300}}` で返す。Everyday Usage で実測: 10 プロンプトで 5 時間窓が 0 % → 6 %。`model/list` の価格は 4 モデルすべて **`cost: null`** |
| 実ターンの挙動 | **◎ 実測** | 門 B1: ターン・ツール呼び出し・承認の往復・`userInput` の往復・subagent・中断・失敗・resume・モデル呼び出しごとのトークン使用量——門 B1 の節を見よ。ワイヤが運ばない唯一のものが subagent とオブザーバの使用量である |
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
サーバ既定と呼ぶ v7 である。

**subagent のレコードは `subagent/<id>/` ではなく親のファイルの中にある**——門 B1 実測で、この決定の
初稿を訂正する。subagent を 1 つ走らせたセッションが作った `session.jsonl` は 1 本だけで、子のレコードは
そこに混ざり `stream: {kind: "session", id: <子のセッション id>}` で区別されていた。ストアのどこにも
子ごとのディレクトリは無い。これは読み層を単純にする（追うファイルは 1 本）と同時に、決定 10 の会計の
穴を埋める唯一の材料でもある。v1 では subagent の活動を親ターン上のツール型の項目として描き、子ごとの
ペインは作らない。

### 決定 5 — サンドボックスは切る。承認の門もそれと一緒に落ちる

`muse serve --disable-sandbox`。実測のとおり Workspace のコンテナでは bubblewrap がサンドボックスを
組めず（`move_mount` → EACCES）、サンドボックスが ON のまま使えないと **エージェントが走らせるシェル
コマンドは全て失敗する**＝種別として何の仕事もできない。⚠️ 門 B1 がその場合を実際に走らせたところ、
症状はベンダの「environment failure で中断する」より狭く、そして AF にとってはより悪い: `toolCall` の
項目が `status: "failed"`・`visibleOutput: "bwrap: Failed to make / slave: Permission denied"` で
返る一方、**`turn/completed` は `terminal: "completed"`** と言う。つまりフラグを付け忘れたセッションは
Console 上では健康に見えたまま、シェルを触る仕事だけが静かに全部落ちる。だからドライバはフラグを
「渡したつもり」にせず spawn 時に検査する。承認はこれと直交するので ON の
まま残す。承認モードはセッションごとにワイヤ上で選ばれ、AF は `approval/requested` に
`approval/decide` で答え、起動時の許可選択（docs/log/76）を `untrusted` | `on-request` | `never` に
写す。

> 🔴 **実測で訂正（P2-6・2026-09-21）。「承認はこれと直交するので ON のまま残す」は誤りであり、
> この ADR で読者が絶対に鵜呑みにしてはいけない唯一の一文である。** 承認はサンドボックスと直交して
> いない: `--disable-sandbox` はホストの確定した権限プロファイルを
> `filesystem.mode: "unrestricted"`（規則 0 本）・`local_command_network.mode: "enabled"` に解決し、
> 何も制限されていないので**どのツール呼び出しも承認層に届く前に `policy_decision: "allow:policy"`
> になる**。`approvalMode: "onRequest"` 下の実ターンで測定したところ、muse のセッションは作業コピー内の
> `tool:bash` と、`workspaceRoot` の外にあるファイルへの書き込みの**両方**を、
> `approval/requested` 0 件で実行した。しかも狭めることもできない: このフラグがある間、
> `--disable-write`・`--disable-shell`・`--sandbox-network <mode>` はそのプロファイルを 1 バイトも
> 変えない。つまりサンドボックスの免除はツールの門を一緒に持って行く。`Caps.PermissionChoice` は
> false にし、ガイドには「muse のセッションはこのコンテナに対して `shell` と同じ届き方をする」と
> 書いた。ワイヤ側が今も honour しているものを含む全記録は P2-6 にある。

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

### 決定 6 — `~/.config/muse/settings.json` は AF が持ち、8 つの挙動を締める

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
  である。よって muse は `mcpreg` の中に自前の書き手を持ち、締め付けを 1 パスでマージする。（門 B1 でこの
  書き手の仕事から `mcp_servers` の半分が消えた——決定 11 を見よ。ワイヤ経路が効くので、AF がサーバ
  ブロックをファイルへ書く必要はもう無い。）
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

**ロック規約は推測でなく実測である**（門 B1・syscall）: `open(".settings.json.lock",
O_RDWR|O_CREAT, 0666)` → `flock(LOCK_EX)`（BSD の advisory flock・ブロッキング・`LOCK_NB` 無し）→ 同じ
ディレクトリに一時ファイル → `fchmod` → `fsync` → `rename` で差し替え → ディレクトリの `fsync` →
`flock(LOCK_UN)`。AF はこれをそのまま取る。設計を変える実測が 2 つある。**muse はロックを取る*前*に
ファイルを読む**（実測: 読みは `flock` の 11 syscall 前）ので、muse 自身の更新も「保護されていない読み」
の read-merge-write であり、AF は書いたあとに読み直して確認しなければ自分のマージが生き残ったと言えない。
そして**muse は握られたロックを無期限に待つ**ので、AF が遅い処理を挟んでロックを持ち続けると利用者が
叩く `muse` コマンドが全部止まる。

AF が設定するのは（綴りと効き目は門 B1 の実測。どの 2 つが「綴りだけ」なのかも含めて門 B1 の節に表がある）:

1. `agents.execution_capacity` を小さく。放っておくと 1 セッションが、メモリ制約のある共有ホストで
   8 エージェントを走らせる。
2. 背景オブザーバを OFF。4 本がそれぞれ独自にモデルを呼ぶ＝従量アカウントでは見えない出費、
   プロンプト枠のサブスクでは見えない枠消費になる。**そして実測では、出費よりターン時間に効く: 同じ
   一行の答えが 17.9 秒 対 4.5 秒**——答え自体は 6.3 秒で出ているのに、終端の門がオブザーバを待つ
   （`eot_gate_ms: 11518`）。設定鍵は見つからず、効いた経路は子の環境変数
   `MUSE_EXPERIMENTAL_{SKILL,GOAL,VERIFY,TODO,MEMORY,SCOPE}_REMINDER=0` である。
3. workflow を OFF——`run.workflow_trigger_mode: "off"`、実測でツール一覧から `muse.workflow` が消える
   （`auto` は 1 セッションに最大 1,000 の子を許す）。
4. worktree 隔離を OFF、`-w` は渡さない。決定 7。子を一切許さない配備では
   `run.subagent_delegation_mode: "off"` も——実測で `muse.subagent_*` 6 本が丸ごと消え、これが決定 10 の
   台帳を exact にする最も安い方法である。
5. 他 CLI の個人文脈を OFF。`~/.claude` と `~/.codex` を読むことは、別種別の指示層をこの種別に黙って
   混ぜることであり、その 2 つはワークスペース方針で触れてはならない場所でもある。**これは仮定ではない:
   実測で、実ターンの答えに `~/.claude/CLAUDE.md` の中身が出た**——利用者の Claude Code のルールが
   Meta へ行った。**経路は設定ファイル
   だけである**: `--no-foreign-personal-context` は `muse exec` にはあるが **`muse serve` には無い**
   （実測——`muse serve … --no-foreign-personal-context` は `unknown option` と答える）。決定 2 により
   AF が動かすプロセスは `serve` だけなので、フラグは選択肢にならない。鍵は **2 本**
   ——`context.foreign_personal_rules: false` と `context.foreign_personal_skills: false`——で、片方だけ
   では片方が ON のまま残る。
6. 承認ジャッジを OFF（`--approval-judge off` か `MUSE_DISABLE_APPROVAL_JUDGE`）。**既定 ON** で、
   Prompt 由来の承認ごとに独自のモデル呼び出しをする。従量アカウントでは利用者が頼んでいない出費で、
   決定 5 は承認を残すので頻繁に発火する。⚠️ 締め付け 5 と同じくこのフラグも `muse serve` には無く、
   門 B1 は設定鍵を**見つけられなかった**。候補は環境変数だけで、効き目は未測である。
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
8. **既定は contributor でないモデル——ただし選ぶのは利用者。** 門 B1: ホスト既定のモデルは
   `muse-spark-1.3-contributor` で、目録の説明文は "Your content, including inter-session messages,
   may be used for product improvement" と書いてある。設定鍵は無く、経路は
   `session/start.modelId`（と `session/setModel`）で、これは resume 後にモデルを渡さない場合も含めて
   全てのモデル呼び出しで honour される。

   これは AF が黙って決めてよい唯一の例外で、利用者が決着させた（2026-09-20）: **設定モーダルの
   エージェント設定に置く利用者向けの設定**であって、隠しピンにはしない。AF の既定は contributor で
   ないモデル——安全な向きであり、設定を一度も開かない利用者が得る値——で、contributor 版も選べる
   ままにする（同じモデル・同じ価格で、違うのは Meta が内容をどう扱えるかだけである）。締め付けの
   機構は変わらない。変わるのは、値が定数ではなく**利用者の設定から来る**ことと、その設定が
   「contributor を選ぶとは何か」を平易な言葉で説明することである。影響:
   `console/src/features/settings/agents/` に操作を足し（両言語の i18n も）、ガイドで得失を説明し、
   起動既定のストアが model / effort と同じ形でこれを運ぶ（`console/src/lib/settings.ts`）。
   この一覧の他の項目は引き続き AF が決める締め付けである。
   ⚠️ セッションの*保存メタデータ*と `session` 射影は contributor の id を返し続ける（5 セッション
   すべてで実測）ので、Console はモデル表示を `session/tokenUsage.modelId` から取る。射影から取っては
   ならない。

**黙って失敗する形が 2 つあり、鍵ごとの振る舞い試験を必須にする**（門 B1）: muse が厳密に解釈する節の
中で綴りを間違えると `muse serve` は **`initialize` の前に rc=3 で落ち**、それ以外の場所で間違えると
ホストは何の診断も出さずに起動して**締め付けだけが効いていない**。「JSON を書いた」は締め付けが
効いている証拠にならない。

Muse 自身のピアメッセージングとセッション名の権威
（`~/.local/share/muse/session-name-authority/`、利用者横断）は、v1 では AF の cross-session messaging に
**繋がない**。1 つの名前空間に 2 つの経路があり、片方がミラーに映らない——それが
`native-peer-channel-invisible-in-mirror` の起き方だった。ガイドには「在るが AF からは見えない」と書く
（P2-14 で記述。同時に、ベンダ側の門が閉じていること——2 つのツールはここではどの組み立て済みツール
一覧にも出ないこと——も実測した）。

### 決定 7 — 触ってよいのは自分の作業コピーのファイルだけ。ブランチも worktree もリポジトリ全体のメタデータも作らない

自分の作業コピーの管理下ファイルを編集するのは仕事そのものであり、この決定が縛る対象ではない。縛るのは
そのファイルを取り巻く**プロジェクトとバージョン管理の面**——リポジトリ全体のメタデータ、ブランチ、
worktree である。「作業コピーの外の全経路」を禁じる規則では**ない**: 決定 4・6・9 は Muse が
`~/.config/muse` と `~/.local/share/muse` に書くことを要求しており、そこは種別自身の状態で、拒否リストの
方で統制する。実測のとおり Muse の worktree 実行は `.git/info/exclude` を書き換え、それはリンク
worktree では親クローンの、全セッション共有のファイルである。よって `-w` は渡さず、worktree 隔離は OFF
（決定 6）、この種別は「ブランチも worktree も作らない種別」として宣言する。並行して書きたい利用者には AF 自身の worktree がある。それが worktree の
用途である。

**門 B1 が、この主張のもう半分——これまで仮定だった側——を実測した。** `-w` を渡さずに 5 セッション・
実ターン 6 本（シェルのツール呼び出し・ファイルを書いた subagent・中断・resume）を回して、作業コピーに
増えたのはエージェントに作らせたファイルだけだった: `.muse/` も
`.muse/.session-worktree-reservations/` も無く、`.git/info/exclude` は git 既定とバイト一致、
`git worktree list` も 1 件のまま。共有状態の危険は `-w` に固有で、渡さないことで足りる。

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
  ⚠️ **無条件ではなく、そして「門 A 成果物の要否」の結論として今はツリーにも無い。** 門 A では明示
  opt-in（`AF_MUSE_BOOT_INSTALL=1`・既定 OFF）として入れた。段 2 より前に存在しない種別のために新しい
  コンテナ全部が 299 MiB を払うのは、kiro（855 MiB）を既に無条件 boot-install から外したのと同じ勘定
  だからである——そして同じ勘定で opt-in 自体も抜いた（種別が無い間、誰もそこへ到達できない）。
  **段 2 は kiro の形を書く**（利用者ごとのオンデマンド `workspace-agent install-muse`）。抜いたコードが
  知っていたことを、段 2 が再発見せずに済むようここに残す: 成果物の URL は
  `https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=<版>&file=<asset>`
  で `<asset>` は `muse-x86-linux` / `muse-aarch64-linux`、検証する sha256 は版別 release manifest の
  `artifacts.{x86_linux,aarch64_linux}.checksum`、落ちてくるのはバイナリ実体で `muse` の名前で置く。
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

### 決定 9 — 資格情報はデバイスコードのアカウントログイン。API キーはその代替

**門 B1 による訂正で、これは注記でなく接続カードの形が変わる話である。** `muse login` は
**デバイスコード**方式だった: `https://auth.meta.com/oauth/device/?code=XXXX-XXXX` を表示して、利用者が
手元のブラウザで承認するまでポーリングする。TTY もローカルコールバックも要らず、この箱にできないことは
何も無い——cursor と kiro の接続カードが既に実装している **start → poll** と同じ形である
（kiro の前例は start / poll / delete の 3 本、`control-plane/routes.go:869-875`）。実サブスクで端から端まで
実測した: managed 経路でターンが回り、`turn/completed` は `terminal: "completed"` を返した。

🔴 **サブスク契約では AF が API キーを書いてはならない。** `muse auth set --api-key-stdin` も
`META_API_KEY` も保存済みのアカウントログインに**優先する**ので、書いた瞬間に利用者は定額から従量へ
黙って移る。よって API キーは従量利用の経路としてのみ残し、その入力方法は変わらない:
`muse auth --help` は usage を
`muse auth set [--provider <PROVIDER>] --api-key-stdin` と出し、`muse auth set --help` は「秘密を渡す
唯一の許された方法」「コマンドライン引数としては決して受け取らないのでシェル履歴に残らない」と説明する
（どちらも 1.3.0 で実測）。どちらの経路でも保存先は `~/.config/muse/auth.json`。

**ファイル拒否リストに入れるのは 1 つでなく 2 つ**: `~/.config/muse`（資格情報）と
`~/.local/share/muse`（全セッションの会話全文と、利用者横断のセッション名権威）。前例はそのままある——
`fs.go:131-137` は同じ理由（資格情報**と**セッションストア）で `.local/share/opencode`・`.codex`・`.kiro`
を既に拒否している。

子プロセスの環境変数に `META_API_KEY` を撒く形は採らない。cursor が env 注入を断った理由
（ADR 0023）がそのまま効き、さらに **API キーは常に保存済みサインインに優先する**ので、env のキーは後から
サインインした利用者を黙って無効化する（サブスクなら従量へ落とす）。

**門 B2 は「可」で閉じる**: サブスクにこの機械でのブラウザオンボーディングは要らないので、サブスクは
v1 の対象で、ガイドに「従量のみ」の但し書きは不要である。よってカードは入力 1 つではなく**導線 2 つ**に
なる——「サインイン」（デバイスコード・start→poll・既定）と「API キーを使う」（秘密 1 つ・従量）で、
切断は `muse logout`。

### 決定 10 — 使用量とモデル一覧はプロトコルに乗る

`usage/read`・`session/tokenUsage`・`session/contextUsage` がワイヤにあり、門 B1 の会計マトリクスを
走らせた。**判定は `MeasuredPartial`**（`usage_fold.go:204-212`）で、理由はこの決定が恐れていたものでは
なかった。キャッシュ入力はきれいに分かれており（`cacheReadTokens` は `inputTokens` の内側で、サーバ導出の
「1 回だけ数えた」`promptTokens` が別にある）、累積を差分にする必要は無く、失敗ターンは何も報告せず、
中断ターンは使った分だけを報告し、`session/resume` は使用量イベントを **1 つも**再生しない——二重計上は
どこにも無い。

🔴 **足りないのは帰属である。** subagent とオブザーバのモデル呼び出しは `session/tokenUsage` に
**決して**畳み込まれない——スキーマが 1 行でそう書いており、門 B1 がそれを実測した: subagent 1 つを
含むターンはワイヤ上 88,077 プロンプトトークンを報告したが、耐久ログには 6 回の呼び出しで 116,816 が
記録され、うち 2 回の持ち主は `subagent-1`（`owner_type: "native_child"`）だった。通知だけを畳むと
このターンを **24 % 過少**に数える。よって exact には代価が要る: 通知ではなく耐久ログの
`goal_usage_attribution`（持ち主を持っている）を畳むか、決定 6 の締め付け（subagent とオブザーバを OFF）
に頼るか。**締め付けた状態ならワイヤの数字は完全である**ので、安い v1 は「switch は `MeasuredPartial`・
締め付けは ON・exact への扉は開けたまま」である。

**チップは 1 つが出所なし、もう 1 つは ADR が形を取り違えていた。** 費用推計は種別→models.dev プロバイダの表
（`workspace/agent/usage_catalog.go:45-55`）を読むが、そこに Meta の行は無く、認証後の目録も 4 モデル
すべて **`cost: null`** なので、トークン単価は依然としてどこにも無い——v1 はトークン台帳を出し、費用
チップは出さない。残量の方は**ワイヤに乗っている**: `usage/read` と、こちらが聞かずに飛んでくる
`usage/changed` が `{tier, weekly{resetsAtMs, usedPercent}, window{resetsAtMs, usedPercent,
windowDurationMins: 300}}` を運ぶ——`get_agent_usage`（claude / codex / agy）が既に話す形であって、この
決定が仮定した「TUI の `/upgrade` にしか無い」ではなかった。⚠️ ただしそれは問い合わせではなく**その
ホストが最後に観測した値**である: 実測で、完了を 1 度も見ていない `usage/read` は `{}` を返す。決定 3 の
「セッションごとに 1 ホスト」では、**起動直後のセッションは最初のターンが終わるまで残量チップを持たない**。

`model/list` がピッカーを支えるので、`agentModels.ts:34-35` の `isDynamic` に `muse` を足すこと。この 1 行は
新種別のたびに漏れてきた（copilot・cursor）もので、症状は「モデル選択肢が既定だけ」である。

### 決定 11 — MCP はワイヤに乗せる（`session/start.config.mcpServers`）。共有設定ファイルは利用者のものとして残す

**門 B1 がこの決定の保留を、安い方に倒して決着させた。** stdio サーバ 1 本を
`session/start.config.mcpServers` だけで渡し（`capabilities.requestedCapabilities: ["sessionMcp"]`＝
許諾された。`MUSE_ENABLE_SESSION_MCP` は**立てていない**）、実接続が成立した: サーバ側のログに muse の
ハンドシェイク（`clientInfo {"name":"tbh","version":"0.1.0"}`・MCP `2025-06-18`）と `MUSE_SESSION_ID`・
config の `env` 追加が残り、そのツールは `mcp__<server>.<tool>` の名前でモデルに届いた。`mode: "optional"`
もワイヤで受け付けられる。

よって **AF は MCP をセッションごとにワイヤで渡し、`mcp_servers` ブロックを一切書かない。** 帰結は 3 つ:
決定 6 の設定書き手は締め付けだけを運び、lost update の面もそれだけに縮み、そして利用者横断のファイルでは
表現できない**セッションごとのサーバ集合**が可能になる。
⚠️ **サーバが起動するのは `session/start` ではなく最初のターンである**（実測: セッションを作るだけの
実行 3 本ではプロセスすら生まれなかった）ので、セッション作成時に MCP の疎通を見る実装は永遠に
「未接続」を読む。

ファイル経路は **書かないが文書化する**: 利用者自身の `mcp_servers` ブロックは、AF が所有しない他の鍵と
同様にそのまま保たれる。だからその形はやはり重要である:

`mcp_servers` は同じ `settings.json` の中のブロックで、`transport: stdio | streamable_http`、
`command`/`args`/`env` か `url`/`headers`、`enabled`、そして **`mode`（既定 `required`）——required の
サーバが起動に失敗すると実行全体が中断する**。AF は **全サーバを `mode: optional`** で materialize し、
利用者に選ばせない。壊れたテナントのサーバのせいでエージェントが起動を拒むのは筋が悪く、しかも登録簿に
その選択を置く場所が無い——`secrets.MCPServer`（`workspace/agent/internal/secrets/secrets.go:302-324`）は
`enabled`・`targets`・`kinds`・`timeoutMs` は持つが `mode` を持たないので、選ばせるなら欄の新設＋ワイヤ＋
Console＋保存済み定義の移行が要る。段 2 で発見するのではなく、ここで対象外と名指ししておく。AF 自身の
サーバをワイヤで渡すときも同じ理由で `mode: optional` にする。

`${VAR}` 展開があり、stdio サーバには `MUSE_SESSION_ID` が渡る（実測）。`muse` は `knownKinds`
（`mcpreg/def.go:57-61`）と `MaterializedKinds`（`materialize.go:47`）に入る。プロジェクトスコープは
`mcpproj` の `kindInfos`（`mcpproj/inspect.go:50-58`）に **`HasProjectScope: false`** で入る——agy と同じ形
——Muse のプロジェクトスコープ綴りが文献に無いからである。`fileSpecs`（`inspect.go:36-44`）には行を足さない
ので、muse は点検対象でも複製先でもない。これは種別についての静的な事実であって、実行時に他種別のファイルへ
落ちる仕掛けではない。フック（`.muse/hooks.json`、`Stop` や
`Notification` を含む 15 のライフサイクルイベント）は**使わない**。フックが報せることはプロトコルが既に
報せており、リポジトリ内のフックファイルは共有状態だからである。

### 決定 12 — プロジェクト層はホスト単位の決定 1 つ（`--trust-workspace`）を要求する。利用者層はルールファイル 1 本で、AF の 2 経路がそれを分け合う

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

**門 B1 が両方の書込先を実測し、答えは「muse にはルールファイル 1 本とスキル根 1 つしかない」だった。**
候補の場所にマーカーを置き、モデル自身の答えから読み返し、syscall 追跡を第 2 の証人にした:

- **利用者層のルール: `~/.config/muse/AGENTS.md`**（`$XDG_CONFIG_HOME/muse/AGENTS.md`）。そこへ置いた
  文章が答えに出た。`~/.config/muse/CLAUDE.md` も探索されるので、プロジェクト層と同じ「AGENTS.md 優先」が
  ここでも効くと見られるが未測である。`$museHome` 配下は読まれない。
- **フリート topic の経路は新しい仕組みを要さない**: `~/.config/muse/skills/<名前>/SKILL.md` を手で置く
  だけで `muse skills list --source user` が拾う（install 手順も lock ファイルの登録も不要）。よって
  `fleetskills.Apply`（`agent_instructions.go:135-141`。今は claude / codex / opencode）は muse でも
  そのまま使える。
- 🔴 **ただし `ApplyFleetNotes` と `ApplyUserInstructions` はその 1 本の `AGENTS.md` を分け合うしかない。**
  他の種別は steering のディレクトリ（kiro の `agent-fleet-guide.md` と `agent-fleet-user.md`）か、別々の
  成果物を持つ。muse はファイルが 1 本なので、段 2 が明示的に決める必要がある——区切り付きの節に分けて
  AF が `~/.config/muse/AGENTS.md` を所有し（順序は `applyInstructionsLocked` と同じくフリート方針が先）
  ガイドにそう書くか、マーカーで利用者の文章にマージするか。muse の指示層が kiro より*やりにくい*唯一の
  場所である。

よって `muse` は段 2 で両方の apply 経路とともに `instrSupportedKinds`（`agent_instructions.go:83`）に
入り、Console の種別別配布状態も一緒に来る。

⚠️ ガイド向けの範囲注記: `foreign_personal_*` が統制するのは**個人**層だけである。両方の締め付けを ON に
して workspace を信頼しても、muse は*リポジトリ側*の `.claude/CLAUDE.md`・`.claude/skills/`・
`.codex/skills/`・`.claude-plugin/plugin.json` を探索する（実測）。このリポジトリではそれらは実在し、
プロジェクト文脈として読まれる。

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

**門 B1 は両方の往復を実際に回した。実測の細部 2 つは、驚きとしてではなくドライバの仕様として書く。**
第 1 に、**ホストはどちらも通知で届ける**——スキーマが併記する server-initiated な `approval/request` /
`userInput/request` ではない。要求形だけに答えるクライアントは、ターンを
`attention: ["approvalPending"]` のまま永久に放置する（実際にそうなった）。第 2 に、**どちらも再送
される**。2 度目に答えると `-32056 userInputAlreadySettled` が
`settlement.outcome: "answered"` を添えて返るので、ハンドラは冪等にし、このエラーを失敗でなく成功として
読まねばならない。

形そのものは扱いやすい。承認は `toolName`・`rawArgs`・`judgeEscalated`・`protectedWrite`・`subject`
（`kind: "shell"`・`command`・段ごとに解析済み `argv` を持つ `stages[]`）を運び、`onRequest` モードでの
`availableChoices` はちょうど **2 つ**——`allow_once`（`decision: "approved"`・`scope: "once"`）と
`abort`（`acceptsFeedback: true`）だけである。つまり最初の許可カードにスコープ選択は要らず、未解決 5 も
答えが出た: 描くのは `subject.command` と段の argv で足りる。`requirementId` はそのまま返すこと——多段の
競合を防ぐ番人である。そして `userInput` の質問は AF の既存の interaction に欄ごと対応する:
`{id, header, question, selection: {mode: "single"}, options: [{label, description}]}`。

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
- **ピンの無い、自己更新する利用者ごと導入。** オンデマンド導入そのものではない——段 2 は意図して
  kiro の形を取る。ほとんどの利用者が使わない種別のために新しいコンテナ全部が 299 MiB を払うのが、
  kiro を boot-install から外した勘定だからである。置き場所も違いではない（出荷 variant も `~/.local`
  に置く。決定 8）。却下しているのは**ピンを落とすこと**で、導入は `versions.json` のピンへ戻し、
  接続カードは `muse --version` の括弧内ビルド id をそれと突き合わせる。
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
| 1 | **門 A — ✅ 完了 2026-09-20**（門 A の節）: 免除は恒久と確定し、拒否している主体を名指しした（AppArmor `docker-default`）。前提も 2 つ訂正した（muse は自前の bwrap を埋め込む／Debian のものは `--ro-bind-symlink` を持たない）。決定 8 の出荷経路も一度通り、その過程で実在の欠陥が出た: sha256 検証が boot-install 5 か所すべてで飾りだった。配備側の成果物はその後この版で抜いた——「門 A 成果物の要否」節を見よ。 **門 B1 — ✅ 完了 2026-09-20**（門 B1 の節）: サブスクのプロンプト 10 本で、会計マトリクス（キャッシュ・subagent とオブザーバの帰属・失敗ターンと中断ターン・resume 後・累積か毎ターンか）、`model/list`、`approval` の往復、`userInput` の往復、subagent 1 つ、負荷時 RSS、締め付け 5 つの効き目と 6 つの綴り、ロック規約の syscall、指示層の書込先、MCP のワイヤ経路、`-w` 無しの実行が作業コピーに書くもの——を買った。 **門 B2 — ✅「可」**: `muse login` はデバイスコードなので、サブスクは対象である | **3 門とも回答済み。** 残るのは計測でなく利用者の判断: 決定 6 の締め付け（製品改善データについての方針判断である締め付け 8 を含む）と出費を受け入れるか |
| 2 | 実装: 種別配線、MSP クライアントと生成型、ドライバ、転写、使用量、設定の締め付け書き手、接続カード、配備、ガイド、この ADR を *adopted* へ | — |

## 未解決

8 つとも答えが出た。5 つは段 2 より前に、6〜8 は P2-14 の枠を使わない実測で。

1. ✅ 決定 10 の会計マトリクス——**`MeasuredPartial`**。理由は subagent とオブザーバの使用量がワイヤに
   出ないことであって（門 B1-1）、キャッシュでも累積の畳み方でもない。その 2 つはきれいだった。
2. ✅ Muse が**利用者スコープ**のルールを読む場所——`~/.config/muse/AGENTS.md` 1 本で、AF の 2 つの apply
   経路がそれを分け合う。フリート topic は `~/.config/muse/skills` へ（門 B1-5）。
3. ✅ サブスクと従量（門 B2）——**デバイスコードなのでサブスクは対象**。
4. ✅ 認証後の `model/list` は 4 モデルを返す（`source: providerCatalog`・`contextLimit 1007997`・
   `outputLimit 128000`・`cost: null`）。プラン依存は見られなかったが、🔴 **既定が
   `muse-spark-1.3-contributor`** である——締め付け 8 はそのために在る。
5. ✅ 承認の描き方——`subject.command` と `subject.stages[].argv`、選択肢 2 つ、スコープ選択は不要
   （門 B1-2）。
6. ✅ **越える。ただし起動順による**（P2-14）: 後に起動したホストは先のセッションを `notLoaded` で
   一覧し、先に起動したホストは後のセッションを見なかった。ストアはセッションのものではなく利用者の
   ものである。Console がそれらを差し出さないのは、駆動側が `session/list` を一切呼ばないからで、
   それは意図ではなく試験で固定した。
7. ✅ **下位ディレクトリ**（P2-14）: AF は `m.CWD()` を送り、ホストはそれを記録し、
   `--trust-workspace` はその path に掛かる。利用者が感じる帰結は問題ない——1 つ上にある作業コピー
   自身の `AGENTS.md` も組み立てられる——が、🔴 それは作業コピーが git リポジトリだからで、遡りは
   リポジトリのルートで止まる。
8. ✅ **鍵は在り、使えない**（P2-14）。どちらの enterprise プレーンにも cron の member は無く、唯一の
   レバー `run.toolset` はツール面全体の名前による許可一覧で、実測で MCP のツール（AF 自身の `af`
   サーバを含む）まで消し、後の版が改名したツール名 1 つでホストの起動そのものを拒否する。よって
   ADR の 2 つ目の枝を取る: ガイドに「その実行は Agent Fleet からは見えない」と書き、その一文の
   両半分を実機試験 2 本が保つ。

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

門 B1 で足した分（2026-09-20）。駆動役は `~/msp2.py`（ホストの通知に答える 150 行の MSP クライアント）と
シナリオごとのスクリプトだが、肝心なところはそれ無しで再現できる:

```bash
~/muse-probe/muse login                                          # デバイスコード。TTY もコールバックも不要
# 無料の神託 2 つ。前者は綴り違いを名指しし、後者はサブスクのプロンプトを 1 本も使わずに
# 締め付けの「効き目」を見せる（echo 実行が組み上げた toolset を耐久ログに書くので）。
echo '{"schema_version":1,"settings":{"agents":{"zzz":1}}}' > d.json
~/muse-probe/muse config validate --plane defaults --file d.json # unknown_member location=…
HOME=/tmp/fh XDG_CONFIG_HOME=/tmp/fh/cfg XDG_DATA_HOME=/tmp/fh/data \
  ~/muse-probe/muse exec --provider echo "hi"                    # 書かれた session.jsonl の
                                                                 # toolset.active_tools を見る
# 効く締め付けと、失敗の仕方が 2 通りあること
printf '%s' '{"schema_version":1,"agents":{"zzz":1}}' > $XDG_CONFIG_HOME/muse/settings.json
~/muse-probe/muse serve --disable-sandbox </dev/null; echo $?    # 3・"malformed settings file"
printf '%s' '{"schema_version":1,"contxt":{"foreign_personal_skills":false}}' > …/settings.json
~/muse-probe/muse skills list --source user                      # 無言。締め付けだけが効いていない
# 設定のロックと、それが本当に効いている陰性対照
strace -f -e trace=%file,%desc ~/muse-probe/muse skills disable bundled:resume-claude \
  --scope built-in                                               # .lock に flock(LOCK_EX) → rename
python3 -c 'import fcntl;f=open(".settings.json.lock","r+");fcntl.flock(f,fcntl.LOCK_EX);input()' &
~/muse-probe/muse skills enable bundled:resume-claude --scope built-in  # 解放まで止まる
# ルールがどこから来るか: マーカーを置き、モデルに「見えているもの」を答えさせる
echo AFPROBE-USER > ~/.config/muse/AGENTS.md                     # 利用者層。届く
echo AFPROBE-PROJECT > <ws>/AGENTS.md                            # プロジェクト層。--trust-workspace が要る
```

⚠️ 実ターンは **使い捨ての `HOME`** に偽の `~/.claude` / `~/.codex` マーカーを置いて回すこと。他 CLI の
個人文脈を切らないまま実ターンを回すと `~/.claude/CLAUDE.md` の中身が Meta へ行く——それがこの計測の
中身であり、利用者本人のファイルでやってはならない。

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

### 焼いたイメージ

`dev-image.yml` でこのブランチから `workspace`（amd64）を焼き、公開されたイメージに 3 つとも入って
いることを確認した: `/usr/bin/bwrap`（80,248 B——手元で展開したパッケージと同サイズ）、イメージ
config の `MUSE_NO_AUTO_UPDATE=1`、そして `versions.json` の `muse=1.3.0-R3401.1` と
`muse_sha256=71b089d0…`（キー 29 個）。Workspace に Docker は無いので、`docker run` ではなく
`crane config` / `crane export` で確認した。

⚠️ **途中で無関係の破損を見つけた。次は develop に当たる。** 最初の焼きは
`CHROMIUM_VERSION=153.0.8010.47-2~deb13u1` で `E: Version … was not found` と落ちた。`.deb` は
security プールに残っているが、Debian の*索引*はいま `153.0.8010.52-1~deb13u1` しか載せておらず、
これは develop の直近の緑の焼き（05:38）と今回（06:30）の間に入った。この ADR とは無関係で、上の
焼きは `bake_optional_tools=false` で迂回したが、**chromium のピン**（と対の `chromium_cft` /
`chromium_dl`）は別途上げる必要がある。

### 門 A がやらなかったこと

ログインなし・実ターンなし・サブスクリプションなし・`-w` なし・`kind` の配線なし——すべて段 2 か
門 B1 の仕事。muse は使い捨てリポジトリに対し `--provider echo` でしか動かしていない。

## 段 1 門 B1 / B2 の実測（2026-09-20）

門 B1 は `11099efd` 上で、**Muse Code 1.3.0-R3401.1** と実際の Muse Code サブスク（tier `27681…`・
Everyday Usage）を **`muse login` のデバイスコード**でサインインして実施した（B1-0）。以下はすべて
stdio 上の `muse serve` で、`session/start` には毎回 `modelId: muse-spark-1.3` を明示した。`-w` は渡さず、
`muse auth set` は実行せず、`META_API_KEY` も置いていない（どちらもアカウントログインに優先して、サブスクを
従量課金へ落とす）。

**実行の隔離のしかた。これが数字の意味を決めるので書いておく。** `HOME` は使い捨てディレクトリにして
*偽の* `~/.claude/CLAUDE.md` と `~/.codex/AGENTS.md` のマーカーだけを置いた——他 CLI 個人文脈の計測が
利用者本人のファイルを Meta へ送ることが構造上あり得ないようにするためである。`XDG_DATA_HOME` も
使い捨てにして、プローブのセッションが利用者のストアに入らないようにした。`XDG_CONFIG_HOME` だけは本物を
使った。アカウントログインはそこに在り、`auth.json` を複製したり symlink 越しに触らせたりすると
リフレッシュトークンが回って利用者のログインを壊しかねないからである。ワークスペースも使い捨ての git
リポジトリで、このリポジトリでは一度も走らせていない。

**費用: サブスクのプロンプト 10 本**（ほかに無料の実行 2 本——echo provider と、モデル呼び出しの前に落ちた
失敗ターン）。5 時間窓は **0 % → 6 %**、週次ブロックは 0 % → 2 % 動いた。この tier では文献の
「5 時間あたり 10〜50 プロンプト」よりずっと余裕がある計算になるが、百分率は整数で単位も明示されていない
ので、これは下限であって換算ではない。

### B1-0: 資格情報はデバイスコードで、ブラウザの受け渡しではない——門 B2 は「可」

`muse login` は URL と 8 文字のコード（`https://auth.meta.com/oauth/device/?code=XXXX-XXXX`）を表示して
ポーリングする。TTY もローカルコールバックも要らず、この箱にブラウザは要らない。**cursor と kiro の接続
カードが既に実装している start → poll と同じ形**であり、実際にサブスクのセッションが動いた: managed 経路で
`turn/completed` が `terminal: "completed"` を返した。

よって **門 B2 は「可」で閉じ**、決定 9 は注記でなく本文を訂正する。資格情報はデバイスコードによる
アカウントログインで、API キーは*代替*である。この ADR がこれまで書いていた「サブスクにはこの箱に無い
ブラウザオンボーディングが要る」は誤りだった。拒否リストと接続カードは変わらない——ただし入力は「AF に
貼る秘密」ではなく「利用者が手元のブラウザに貼るコード」になる。

### B1-1: 会計マトリクス——`MeasuredPartial`。理由はキャッシュではない

`session/tokenUsage` は**ターンごとではなくモデル完了ごと**に 1 回飛び、生のプロバイダ計数とサーバ導出の
「1 回だけ数えた」値の両方を運ぶ:

| 性質 | 実測 |
|---|---|
| キャッシュ | **分離されており、しかも `inputTokens` の内側**: 2 回目の呼び出しは `inputTokens 21857`・`cachedTokens`/`cacheReadTokens` `20721`・`cacheWriteTokens 0`・`promptTokens 21857` だった。このプロバイダでは `promptTokens == inputTokens` でキャッシュはその*部分集合*＝足すと二重に数える |
| 毎ターンか累積か | 1 イベントに両方。2 呼び出しのターンが `promptTokens` 20776 → 21857 を出し、2 つ目の `cumulative.promptTokens` が **42633 = 20776 + 21857** だった。毎イベントを畳む**か**最後の `cumulative` を取るかの**どちらか**で、両方はしない |
| 文脈占有と出費 | 同じイベントで `session/contextUsage.usedTokens` は **22252**、`cumulative.totalTokens` は **44007**。占有は最後の呼び出しであって累計ではない。文脈チップを累積から作ると 2 倍に読む |
| 失敗ターン | `turn/completed` が `terminal: "failed"`・`error{kind: "modelError", message, retryable: false}` で返り、`session/tokenUsage` は **1 本も出ない**。`usage/read` も `{}` のまま＝**失敗ターンは台帳に何も足さない** |
| 中断ターン | `turn/interrupt` → `turn/completed` が `terminal: "cancelled"`・`reason: "cancelled after tool result reconciliation"`。既に走ったモデル呼び出しの `session/tokenUsage` がちょうど 1 本。**二重計上も取りこぼしも無い**——ただしプロンプト枠は消費される |
| `session/resume` の後 | **別プロセスのホスト**で resume した: 履歴の項目は再生されるが `session/tokenUsage` は **0 本**（飛んだ通知は `session/branchChanged` だけ）。次のターンの `cumulative.promptTokens` は 41236 = 20764（再起動前）+ 20472。**累積はプロセスを跨いで生き、数え直さない** |
| subagent | 🔴 **ワイヤの数字から除外される。** 下記 |
| 背景オブザーバ | 🔴 **ワイヤの数字から除外される。** 下記 |

🔴 **罠は「累積か差分か」ではなく「持ち主」である。** subagent を 1 つ生んだ（`muse.subagent_spawn`）
ターンは `session/tokenUsage` を **4 本**出し、合計は `cumulative.promptTokens 88,077` だった。一方、同じ
ターンの耐久ログにはモデル呼び出しが **6 回**記録されている——4 回の持ち主が `main-root`、2 回が
`subagent-1`（`owner_type: "native_child"`・`input_tokens` 14,294 と 14,445）。子の **28,739 プロンプト
トークンはワイヤに一度も現れない**ので、通知だけを畳むとこのターンを **24 %** 過少に数える（116,816 のうち
88,077）。スキーマは 1 行でそう書いており——「Subagent/workflow-child usage is never folded in — it rides
the owning items」——実測がそれを行動に移せる形にした。背景オブザーバも同じで、オブザーバを有効にした
ターンは `session/tokenUsage` を **1 本**しか出さず、`reminderChild` の項目 3 つが行ったモデル呼び出しは
そのどこにも入っていない。

耐久ログの方は帰属を持っている: モデル完了のたびに
`owner{owner_id, owner_type: main_root | native_child, requester_kind}` と `quantity` を持つ
`goal_usage_attribution` レコードが先行する（⚠️ 各 2 回出て、片方は `reported: false` のゼロ——素朴に
畳むとそれを飛ばし損ねる）。

**よって決定 10 は `MeasuredPartial` に着地し、それは注意書きではなく実測の判定である。** exact への道は
2 つあり、どちらも段 2 の決定である: 通知でなくセッション JSONL の `goal_usage_attribution` を畳むか、
決定 6 の締め付け（subagent とオブザーバを OFF）に頼るか。後者では「exact」は「締め付けたホストでの
exact」を意味し、締め付け自体は効くものの、後から `agents.execution_capacity` を上げられる利用者が居れば
台帳は黙って partial に戻る。正直な v1 は「締め付け ON の `MeasuredPartial`」である。

決定 10 が要る数字がもう 2 つ。認証後の目録は 4 モデルすべて **`cost: null`** なので、ベンダ由来の
トークン単価は依然として無い。そして `usage/read` は、**そのホストが完了を観測するまで `{}`** を返す——
サブスクの窓は Meta への問い合わせではなく、このプロセスが最後に見たフレームである。決定 3 の
「セッションごとに `muse serve` 1 本」では、**起動直後のセッションは最初のターンが完了するまで残量チップを
持たない**。完了後は `usage/changed` が
`{tier, weekly{resetsAtMs, usedPercent}, window{resetsAtMs, usedPercent, windowDurationMins: 300}}` を
こちらが聞かずに押してくる。

### B1-2: 承認は「スキーマが併記する要求」ではなく通知で来る

往復自体は動き、配線も安いが、ドライバの初稿はここで必ず固まる。MSP は `approval/request` を
*server-initiated request* として宣言しているのに、ホストが実際に送ってきたのは **`approval/requested`
通知**だった。要求形だけに答えるクライアントはターンを永久に止める（実測: ホストを殺すまで
`attention: ["approvalPending"]` のままだった）。

```
session/statusChanged  {"status":"running","attention":["approvalPending"]}
approval/requested     {approvalId, currentRequirementId{approvalId,sourceIndex}, toolName:"bash",
                        judgeEscalated:false, protectedWrite:false, rawArgs:"{\"command\":…}",
                        subject:{kind:"shell", command:"…", stages:[{argv:["echo","…"],
                                 argvComplete:false, position:1, totalStages:1,
                                 resolution:{kind:"unresolved"}}]},
                        availableChoices:[{choiceId:"allow_once",decision:"approved",scope:"once"},
                                          {choiceId:"abort",decision:"abort",scope:"once",
                                           acceptsFeedback:true}]}
approval/decide        → {status:"accepted", terminal:true}   その後 approval/updated・approval/resolved
```

決定 13 に効くことが 3 つ。`attention: ["approvalPending"]` は**一級の状態信号**なので、AF の「許可待ち」
状態は推定で作らなくてよい。`onRequest` モードの `availableChoices` はちょうど **2 つ**——1 回だけ許可、
または（任意でフィードバック付きの）中止——なので最初の許可カードにスコープ選択は要らず、この ADR の
未解決 5 も答えが出る: カードが描くのは `subject.command` と `stages[]` の argv で足りる。そして
`requirementId` はそのまま返すこと——多段の競合を防ぐ番人である。

`userInput` は本当に別チャネルで、AF の既存の質問 interaction に欄ごと対応する:

```
userInput/requested  {userInputId, toolName, questions:[{id:"color_pref", header:"Color",
                      question:"Which colour do you prefer?", selection:{mode:"single"},
                      options:[{label:"Red (Recommended)",description:"…"},{label:"Blue",…}]}]}
userInput/answer     {userInputId, answers:[{questionId, selectedLabel}], commandId, sessionId}
                     → accepted、その後 userInput/settled
```

⚠️ どちらのチャネルも**再送する**: 同じ `userInput/requested` が 2 回届き、2 度目に答えると
`-32056 userInputAlreadySettled` が `settlement.outcome: "answered"` を添えて返った。AF のハンドラは
冪等にし、このエラーを「報告すべき失敗」ではなく成功として読むこと。

### B1-3: 締め付け——綴り 5 つ確定・効き目 4 つ実測・黙って失敗する形が 2 つ

鍵は `~/.config/muse/settings.json` にあり、綴りはもう推測ではない:

| 締め付け | 実測した鍵 | 実測した効き目 |
|---|---|---|
| subagent の上限 | `agents.execution_capacity`（1 以上の整数。`0` は拒否される） | 綴りは、`agents` の*他の*メンバーがあると `muse serve` が起動を拒むことで確定。同時に走る子への効き目は**未測** |
| subagent を丸ごと OFF | `run.subagent_delegation_mode: "off"` | ◎ **`muse.subagent_*` 6 本が丸ごと**モデルのツール一覧から消える（28 → 21。同じプロンプト・同じ形のセッションで） |
| workflow OFF | `run.workflow_trigger_mode: "off"` | ◎ `toolset.active_tools` から `muse.workflow` が消える（23 → 22）。**`--provider echo` で無料で**測れる——echo 実行は組み上げた toolset を耐久ログに書く |
| 他 CLI 個人文脈 OFF | **1 本でなく 2 本**: `context.foreign_personal_rules: false` **と** `context.foreign_personal_skills: false` | ◎ 両方向。設定しないと実ターンの答えに偽の `~/.claude/CLAUDE.md` の中身が出た——**利用者の Claude Code のルールが Meta に届く**。`foreign_personal_rules: false` を入れると同じプロンプトがプロジェクトのファイルだけを答え、syscall 追跡でも `$HOME/.claude` と `$HOME/.codex` は開かれなくなる。`foreign_personal_skills: false` 単独では `muse skills list` から他 CLI のスキル 2 本が消え、ルールは残る |
| 同梱の「他 CLI 読み」スキル OFF | `skills.activation.bundled["bundled://muse-core/skills/<id>/SKILL.md"]: "off"` | ◎（段 0）`muse skills list` が `off` と出す。鍵は依然としてパック名込みのパスなので、ドリフト検査の項目のままである |
| 背景オブザーバ OFF | ⚠️ **設定鍵は見つからなかった**: `run.reminder_observers` はランタイムの `RunConfigurationSettings` に実在する欄だが enterprise の検証器は拒否し、settings.json のどの綴りも効かなかった。効いたのは env 経路——`MUSE_EXPERIMENTAL_{SKILL,GOAL,VERIFY,TODO,MEMORY,SCOPE}_REMINDER=0` | ◎ しかも出費より価値がある: オブザーバ ON だと一行の答えのターンが **17.9 秒**——6.3 秒で答えが出た後、`eot_gate_ms: 11518` の終端の門が `skill-reminder` / `goal-reminder` / `verify-reminder` の 3 本のモデル呼び出しを待つ。OFF にすると同じプロンプトが **4.5 秒**で終わる。見えない枠消費だけでなくターン時間の問題である |
| 承認ジャッジ OFF | ⚠️ 未測。`--approval-judge` は `muse` と `muse exec` にあるが **`muse serve` には無い**（締め付け 5 と同型）。バイナリには `MUSE_DISABLE_APPROVAL_JUDGE` がある。取った承認 1 件では `judgeEscalated` は `false` だった | — |
| **8 つ目——contributor でないモデルのピン** | セッションごとの `session/start.modelId`（設定鍵は無い） | ◎ すべてのモデル呼び出しで honour され、`modelId` を渡さない resume 後も同じ（`session/tokenUsage.modelId: "muse-spark-1.3"`、耐久の `run_model` は `source: "startup"`）。🔴 **ただしセッションの保存メタデータと `session` 射影は `muse-spark-1.3-contributor`**——ホスト既定——を返す。`session/listChanged` でも `session/resume` の `session.modelId` でも、測った 5 セッション全部で。射影からモデルチップを作ると「あなたのコードは製品改善に使われている」と嘘を表示することになり、逆向きの間違いの方が危ない |

🔴 **黙って失敗する形が 2 つあり、向きが正反対である。** muse が厳密に解釈する節の中で綴りを間違えると
ホストが死ぬ: `{"agents":{"zzz":1}}` で `muse serve` は `initialize` の前に **rc=3** と
``load settings for serve composition: malformed settings file … unknown field `zzz`, expected
`execution_capacity``` を出して終了する——つまり AF のドライバは「設定の書き込みは子の起動を拒ませ得る」
ものとして扱い、ハングではなく接続エラーとして見せねばならない。一方それ以外の場所での綴り間違いは
**完全に無言**である: 未知のトップレベル節や `context.foreign_personal_skil` は、締め付けが効かないまま
ホストを機嫌よく起動させる（実測。同じ表の中に正しい綴りという陽性対照がある）。この 2 つの間に診断は
無い。決定 6 が書き込みの前に fail-close を置く理由がこれであり、鍵ごとに**振る舞いの**試験（無料の
`--provider echo` toolset 神託が 2 つを賄う）が要る理由でもある。「JSON は書いた」は試験にならない。

**設定ファイルのロック規約、syscall で**（muse 自身のログ行によれば `config/src/settings.rs:140`）:

```
open(".settings.json.lock", O_RDWR|O_CREAT, 0666)   ← 本体ではなく横の錠ファイル
flock(fd, LOCK_EX)                                  ← BSD の advisory flock・ブロッキング・LOCK_NB 無し
lstat("settings.json"); open(".settings.json.tmp-<pid>-0", O_WRONLY|O_CREAT|O_EXCL)
write; fchmod 0644; fsync; rename(tmp, "settings.json"); fsync(dirfd)
flock(fd, LOCK_UN)
```

AF は同じ規約を取る: `.settings.json.lock` に `flock(LOCK_EX)`、同じディレクトリに一時ファイル、
`rename`、ディレクトリを `fsync`。実装が落としてはいけない細部が 2 つ。**muse はロックを取る*前*に
`settings.json` を読む**（実測: 読みは `flock` の 11 syscall 前）ので、muse 自身の更新は保護されていない
読みの read-merge-write であり、両者がロックを使っていてもファイルは同時更新に対して安全ではない。AF は
書いた後に読み直して確認する必要がある。そして **muse は握られたロックを無期限に待つ**（陰性対照: 別
プロセスから `LOCK_EX` を握ったまま `muse skills enable` を起動すると 8 秒止まり、解放した瞬間に進んだ）
ので、AF が遅い処理を挟んでロックを持つと利用者の `muse` コマンドが全部止まる。

**段 2 が費用を見積もるべき、形のよい第 2 の経路がある——enterprise 設定プレーンである。**
`muse config status` はシステムファイル 2 本を解決する（syscall で実測:
`/etc/muse/enterprise-defaults.json` と `/etc/muse/enterprise-policy.json` を
`openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)` で開く）。そして
`muse config validate --plane defaults|policy` は**無料・オフラインの綴り神託**で、落ちたメンバーを正確に
名指しする（`unknown_member location=settings.agents.execution_capacity`・`semantic_invalid`・
`wrong_type`）。8 つの締め付けのうち 5 つが defaults プレーンで通り
（`agents.execution_capacity`・`run.workflow_trigger_mode`・`run.subagent_delegation_mode`・
`context.foreign_personal_rules`・`context.foreign_personal_skills`・`skills.activation.bundled.<id>`）、
いずれも `binds=defaults user_overridable=true` と報告された。Workspace の中から `/etc` は書けないので、
これは実行時ではなく**イメージ**の決定である: 締め付けをイメージに焼けば、決定 6 の利用者ごとの設定
書き手もロックも lost update の窓も fail-close も丸ごと不要になる——見積もりの数日分である。この ADR の
決定にしないのは 3 つの留保があるからである: プレーンは `MUSE_EXPERIMENTAL_ENTERPRISE_CONFIG` の裏に
あること、`policy`（上書き不可の方）プレーンが受け取るのは
`{settings.capability_ceilings, privacy, model_egress}` だけで、その `privacy.foreign_personal_rules` の
値の語彙は外から発見できないこと（bool と妥当そうな文字列 15 個が全部拒否された）、そして `defaults`
プレーンの値は利用者が自分の settings で上書きできること。

### B1-4: `session/start.config.mcpServers` は効く——決定 11 はこちらを採る

自分の argv・環境・ハンドシェイクを記録する偽の stdio MCP サーバで実測した。ワイヤだけで渡し
（`capabilities.requestedCapabilities: ["sessionMcp"]`＝許諾された。`MUSE_ENABLE_SESSION_MCP` は
**立てていない**）、モデルのツール一覧に **`mcp__afprobe.af_probe_ping`** が現れ、サーバ側のログには
muse が `clientInfo {"name":"tbh","version":"0.1.0"}`・MCP プロトコル `2025-06-18` で接続し、
`MUSE_SESSION_ID` と config の `env` 追加を渡したことが残った。`mode: "optional"` もワイヤで通る。

⚠️ **サーバが起動するのは最初のターンであって `session/start` ではない**——セッションを作るだけの実行を
3 本走らせてもプロセスは 1 つも生まれず、最初これが「未対応」に見えた原因である。セッション作成時に
MCP を疎通確認する実装は永遠に「未接続」を読む。

よって決定 11 は安い方に決まる: **AF は MCP をセッションごとにワイヤで渡し、共有設定ファイルに
`mcp_servers` を書かない。** 設定の書き手は締め付けだけを運び、lost update の面もそれだけに縮み、
ファイル経路では表現できないセッションごとのサーバ集合が可能になる。ファイル経路は「利用者自身の設定に
在り得るもの」として文書に残し、AF はそれを保つ。

### B1-5: 指示層——利用者層のルールファイルは 1 本、フリートの経路は既にある

マーカーを置いてモデルに「見えているもの」を答えさせ、syscall 追跡を第 2 の証人にして測った:

- **利用者層: `~/.config/muse/AGENTS.md`**（＝`$XDG_CONFIG_HOME/muse/AGENTS.md`）。そこへ置いた内容が
  モデルの答えに出た。`~/.config/muse/CLAUDE.md` も探索されるので、プロジェクト層と同じ
  「AGENTS.md 優先」が効くと見られるが未測。`$museHome`（`~/.local/share/muse/AGENTS.md`）は読まれない。
- **プロジェクト層**は端から端まで再確認した: `--trust-workspace` 付きでリポジトリの `AGENTS.md` が
  モデルに届き、`CLAUDE.md` は届かない。
- **フリートの経路は新しい仕組みを要さない。** `~/.config/muse/skills/<名前>/SKILL.md` を手で置くだけで
  `muse skills list --source user` が拾う（install も lock ファイルの登録も不要）ので、AF の既存の
  `fleetskills.Apply(dir, topics)`（`agent_instructions.go:135-141`。今は claude / codex / opencode）が
  muse でもそのまま効く。
- 🔴 **ただし muse の利用者層のルールファイルは 1 本しかなく、AF の apply 経路は 2 つある。** 他の種別は
  ディレクトリ（kiro の `~/.kiro/steering/agent-fleet-{guide,user}.md`）か別々のファイルを持つ。muse では
  `ApplyFleetNotes` と `ApplyUserInstructions` が **`AGENTS.md` を分け合う**——つまり AF がそのファイルを
  所有し（区切り付きの節・フリート方針が先）、利用者が自分で書いた内容は危険にさらされる。段 2 が
  明示的に決めること（マーカーでマージするか、所有してガイドにそう書くか）。muse の指示層が kiro より
  *やりにくい*唯一の場所である。
- ⚠️ ガイド向けの範囲注記: `foreign_personal_*` が統制するのは**個人**層だけである。締め付けを入れて
  workspace を信頼した状態でも、muse は*リポジトリの*`.claude/CLAUDE.md`・`.claude/skills/`・
  `.codex/skills/`・`.claude-plugin/plugin.json` を探索する。このリポジトリではそれらは実在し、
  プロジェクト文脈として読まれる。

### B1-6: 負荷時の常駐費と、`-w` 無しの実行が書くもの

**決定 3 の再評価条件は満たされ、答えは「セッションごとに子を持つままでよい」である。** 各ターンの間
0.5 秒ごとに測ったホスト＋子の RSS の最大値は **137〜147 MiB**（最大の 147,372 KiB は subagent を生んだ
ターン）で、待機時の **73 MiB** に対する値である。この形のセッション 10 本で約 1.4 GiB——登録簿の
claude 230 MiB のペイン 5 枚と同じ桁で、共有デーモンが節約する分の 3 分の 1 でしかない。

**決定 7 はもう `-w` の実測だけに乗っていない。** `-w` を渡さない実ターン 6 本を 5 セッションで
——シェルのツール呼び出し、ファイルを書いた subagent、中断、resume——回して、作業コピーに増えたのは
エージェントに作らせたファイルだけだった: `.muse/` も `.muse/worktrees/` も
`.muse/.session-worktree-reservations/` も無く、`.git/info/exclude` は git 既定とバイト一致、
`git worktree list` も 1 件のまま。共有状態の危険は `-w` 固有で、渡さないことで足りる。

### B1-7: サンドボックスを ON にしたまま（門 A が送ってきた項目）

`--disable-sandbox` **を付けない**ホストでターンを 1 本。この箱には system の `bwrap` がそもそも無い
（＝これは門 A がバイナリ内に見つけた muse の*埋め込み* bubblewrap である）:

```
item/completed  {kind:"toolCall", tool:"bash", status:"failed",
                 failureReason:"process exited with status 1",
                 visibleOutput:"bwrap: Failed to make / slave: Permission denied"}
turn/completed  {terminal:"completed"}          ← ターンの方は成功する
```

ベンダの文言は「サンドボックス下のシェルコマンドは全て environment failure で中断する」だが、実際は
もっと狭く、そして AF にとってはもっと悪い: **ツール呼び出しが失敗し、ターンは正常に完了する。**
エージェントは失敗を文章で説明して終わった。つまり `--disable-sandbox` 無しで起動した Muse セッションは
Console 上で健康に見え——ターンは回り、答えも出る——シェルを触る仕事だけが `Failed to make / slave` で
全部落ちる。これは門 A が追った最初の `mount(NULL, "/", …, MS_REC|MS_SLAVE)` の EACCES に、逆側から、
system の `bwrap` を一切介さずに到達したもので、A-1 の「埋め込み bwrap」の結論を独立に裏づける。決定 5 は
変わらない。足されたのは「失敗がターンの層では無言である」ことで、だから**ドライバは spawn 時に
フラグを検査する**。

### 門 B1 が測らなかったこと

`agents.execution_capacity` の同時実行数への効き目。承認ジャッジ自身のモデル呼び出し（設定経路も `serve`
のフラグも見つからず、取った承認 1 件では `judgeEscalated` は false）。ワイヤ MCP 経路に `sessionMcp` が
*必須*かどうか（毎回要求し、毎回許諾された）。`session/fork`・`turn/steer`・`session/setModel`・
`session/setReasoningEffort` と `session/list` のセッション横断の見え方。`~/.config/muse/CLAUDE.md` の
優先順位。従量アカウントでの `usage/read`。そして muse 自身の `cron_*` ツール——測った全ての締め付けの
下でもツール一覧に残る。**自分の将来の実行を予約できるエージェント**が Agent Fleet のスケジューラの隣に
居るという話で、鍵が見つかっていない **9 つ目の締め付け候補**である。

## 門 A 成果物の要否——トランクに残す価値があるもの、抜くもの

門 A は、存在しない種別のために `develop` へ 5 つの変更を入れた。1 つずつ、何がそれを決めるのかと一緒に
検め直す:

1. 🔴 **`workspace/Dockerfile` の `bubblewrap`——抜く。** 理由は「ホストが許すものが変わった日に再計測を
   1 コマンドで」だった。これは門 A 自身の実測に触れると保たない: 阻害要因は *3 つ*あり、うち 2 つは muse
   の内側（自前の bubblewrap を埋め込む／Debian の 0.12.0 は muse 必須の `--ro-bind-symlink` を持たない）
   なので、**system の `bwrap` は再計測の対象ですらない**。しかも再計測は無くても 1 コマンドである——門 A
   自身が root 無しの `dpkg-deb -x` でやった——うえ、B1-7 は system の `bwrap` が 1 つも無い状態、つまり
   フリートが実際に出荷している構成で、**muse の埋め込み版を通して**同じ拒否を再現した。対して費用は、
   全 Workspace イメージがパッケージを 1 つ恒久的に運ぶことである。加えて muse 自身の探索は「PATH 上の
   capability-valid な `bwrap`」を先に見るので、焼くと muse が返す拒否の種類が変わる。そして
   `deploy/local/e2e-smoke.sh` は `--cap-add=SYS_ADMIN` かつ AppArmor 非適用で走るので、そこではその
   `bwrap` が**緑になり得**、決定 5 が覆ったように読める。フリートに対する唯一の効果が「CI を 1 つ
   誤解させること」のパッケージに、イメージの席を与える価値は無い。知識はこの ADR に残る——次の読み手が
   実際に見つけるのはそちらである。
2. 🔴 **`ARG MUSE_VERSION` ＋両 arch の sha256 ＋ `versions.json` の `muse` / `muse_sha256`——抜く。**
   それらが存在する理由だった entrypoint の `AF_MUSE_BOOT_INSTALL` ブロック（項目 3）も一緒に。持ち主の
   居ないピンは両方の悪いところ取りである: `deploy/local/cli-drift-check.sh` は `muse` を知らないので、
   1.4 が出ても誰も気づかず**ピンは構造的に古びる**。一方、門 A 自身が勧める「kiro の利用者ごとオンデマンド
   導入」を段 2 が取るなら、この形は使われない。コードが知っていて ADR が書いていなかったことは、代わりに
   文章として残した: 成果物の URL は
   `https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=<版>&file=<asset>`
   （`<asset>` は `muse-x86-linux` / `muse-aarch64-linux`）、sha256 は版別 release manifest の
   `artifacts.{x86_linux,aarch64_linux}.checksum`、導入するのはバイナリ実体で名前は `muse`。
3. **`e2e-smoke.sh` の muse 行——一緒に抜く。そして引き継ぎが心配していたことは事実誤認だった。**
   あのアサーションは Dockerfile の `ARG` と焼かれた `versions.json` を突き合わせる＝自分のツリーの中の
   2 箇所の比較で、ネットワークに出ない。上流の manifest が動いても赤くなりようがない。危険だったのでは
   なく、単に「出ていくピンの試験」である。
4. **`ENV MUSE_NO_AUTO_UPDATE=1`——残す。** 1 行で、費用は無く、効き目は現在形で種別と独立である: 利用者が
   今日ベンダの一行インストーラを走らせれば `~/.local/bin/muse` に bash ランチャが載り、既定で毎時
   自分を書き換える（`MUSE_UPDATE_INTERVAL_SECONDS=3600`・実測）。それを封じるのは、`kind=muse` が
   実現するかどうかと関係なく価値がある。
5. **`entrypoint.sh` の `set -e` 修正——残す。この検討の対象外。** muse とは無関係の実在の欠陥修正である:
   `( set -e … ) && ok || WARN` のサブシェルは AND-OR リストの左辺で、POSIX はそこで `-e` を無視すると
   定めているため、boot-install 5 か所の sha256 検証は未検証の成果物を入れて「成功」と報告していた。

**門そのものの形について。** 親セッションの自己評価——当たったのは*実際に出荷するものを一度走らせる*
項目で、外したのは*既に測ったことを再確認する*項目——は正しく、門 B1 は同じ型を両方向で繰り返した。
ここで「高くて無価値」になりかけたのはサンドボックスの再現（B1-7）で、もし ADR の予測どおりの答えが出て
いたら何も買えていなかった——期待される答えは ADR 自身が先に書いていたからである。それが 1 プロンプトの
価値を持ったのは、答えが**予測どおりではなかった**からだ: ターンは完了する、つまり失敗は AF が報告する
層では見えない。抽出すべき規則はそこにあり、「再計測するな」ではない——**どちらの結果でも何かが変わる
とき、その検査は席に値する**。締め付けの計測がきれいな例である:「鍵は書けるが効かない」と「鍵はホストを
殺す」はどちらも実在し、どちらも見つかり、どちらも予測されていなかった。`bubblewrap` を焼くのは逆の例で、
あのビルドのどの結果も決定を変えなかった。

## 段 2 へ進む判断材料は揃ったか

**価格以外は揃った。** 門 A がサンドボックスを恒久的に答え、門 B1 が会計・承認・user input・指示層の
書込先・締め付け・MCP 経路・負荷時の常駐費・`-w` 無しの書き込み面を答え、門 B2 は「可」で閉じた。測った
ものの中に結論を覆すものは無い: managed 専用の MSP、`per-session-child`、`settings.json` の締め付け、
`--disable-sandbox`、`--trust-workspace`。

見積もりの動きは差引ほぼゼロで、危険の置き場所が変わった:

| 変化 | 日 |
|---|---|
| MCP 方言が設定の書き手から消える（ワイヤ経路が実測で動いた。B1-4） | −0.5 |
| 設定の書き手は小さくなるが、ロックが仕様として確定し、鍵ごとの振る舞い試験が増える（B1-3） | +0.5 |
| 承認と userInput のワイヤ形が判明（通知での配送と `-32056` の冪等性を含む）。AF の質問 interaction は欄ごと対応 | −0.5 |
| 使用量: `MeasuredPartial` の v1 は `MeasuredExact` より安いが、モデル id の射影の罠と「最初のターンまで残量チップが無い」は新しい Console の仕事 | +0.5 |
| 指示層: fleetskills の経路はそのまま使え、`AGENTS.md` の所有をどうするかの決定が増える | ±0 |
| 配備: 段 2 が作るものは（門 A の opt-in を抜いたので）また未着手に戻った | +0.5 |

よって表は **22〜33 セッション日**、managed 専用の門が未払いの今日の期待値は **23〜35 日**、
**ADR 0093 が先に着地すれば 19〜28 日**——この推奨は変わらず、むしろ裏づけが増えた。0093 と共有する項目
（managed 専用の門、承認 interaction）はどちらも、muse が実際に必要とする仕事そのものだと確認できたからである。

**段 2 に入る前に人が決めることが 2 つあり、1 つは決着した。** 締め付けはプライバシーに触れる: 既定
モデルは `muse-spark-1.3-contributor` で、その説明文自身が「あなたの内容（セッション間メッセージを
含む）は製品改善に使われ得る」と書いている。🟢 **2026-09-20 決着: 設定モーダルのエージェント設定で
利用者が選ぶ。既定は contributor でないモデル**（決定 6 の締め付け 8）——AF は選択を隠しもしないし
黙って決めもせず、ガイドが「contributor を選ぶとは何か」を説明する。残るのは出費である:
サブスクでは見えない枠の正体はオブザーバと subagent で、締め付けがそれを閉じる。従量アカウントでは
**ベンダがトークン単価を出していない**（`cost: null`）ので、費用チップは v1 に載せられず、ガイドがその
理由を書く必要がある。

## 段 2 実装記録（2026-09-21）

段 2 は 2026-09-21 に、作業パッケージ表の順で着手した。この節には、上の決定が知らなかったことで
実装が実測したものを、着地ごとに 1 項目ずつ置く。

### P2-1: MSP クライアント・生成型・ドリフトの錠（`workspace/agent/internal/msp/`）

最初の作業パッケージ。234 の型・47 メソッド・31 通知・31 エラーコードは、ベンダ自身のオフライン
エクスポートから `internal/msp/schemagen` が生成する。ワイヤの語彙を手で保守しないためである。
Consequences が約束した「ドリフトの錠」は 3 つの検査で、バイナリを要るのは 3 つ目だけ:
生成物が同梱の束ねと一致すること、束ねの指紋が定数と一致すること、そして——バイナリがあれば——
導入済みバイナリが同じ指紋を出すこと。**各々に陰性対照を付けた**。走らなかった検査と通った検査は
見分けがつかないからである。

3 つの実測が決定を訂正・補強した。

**1. `--provider echo` は `muse serve` に届かない。よって「構築 ≈ $0」はターンの手前で止まる。**
分水嶺の表の harness 行と、それを限定する Consequences の箇条書きは、どちらも `--provider echo` に
乗っている。1.3.0-R3401.1 で実測: `--provider <MODE>` は **`exec` の起動時フラグ**であり、
`muse serve --help` に同等のものはない（serve の姿勢フラグはサンドボックス系・`--trust-workspace`・
`--no-session-log` だけ）。ワイヤ越しでは `session/start` が `providerId: "echo"` と
`modelId: "echo"` を**受け取り**、返す session にその両方を記録する（`"providerId": "echo"`,
`"modelId": "echo"`）——そのうえでターンは
`turn/completed.error.kind = "authRequired"`、`"not logged in: run /login to add an API key"`
で終わる。

つまり認証不要の面は実在するが、表の主張より狭い。`initialize`・能力の付与・`session/start`・
転写パスまでは無料で、**ターンは必ず資格情報を要する**。そして決定 2 により AF が走らせる
プロセスは `serve` だけである。帰結は注記ではなくテストダブルであり、`internal/msp/msptest` が
パイプ越しのインプロセスホストとして入った。これは同時に、専有バイナリが決して置かれない CI で
試験群を走らせられる理由でもある。実機テストは `MUSE_LIVE=1` の裏に置き、ターンの手前で止める。

**2. ホストは stable surface が宣言していない通知を出す: `session/started`。** 束ねが宣言する
通知は 31 個で、これはその中にない。240 KB のスキーマ中に文字列はちょうど 1 回だけ現れ、それは
`session/listChanged` 自身の説明文の中である（「行の誕生は `session/started` に残り、アンロードは
`session/closed` と対になる」）。実測では、`serve` 越しの `session/start` はその応答より**前**に
`session/started` を出す。よって生成した通知表は復号のための地図であって**許可リストではない**。
ディスパッチャは未宣言のメソッドをプロトコル違反として扱わず記録して捨てること、そしてドライバは
「見えるのはこの 31 個だけ」と仮定してはならない。

**3. ホストは UUIDv7 を厳密に検証する。決定 4 がテストになる。** v4 を載せた `commandId` は
`-32602 invalidParams`、`"invalid session/start commandId: expected UUIDv7"` で拒否される。
AF は自前で発番し（`msp.NewCommandID`、RFC 9562 v7、新規依存なし）、実機試験は両方向を主張する:
ホストは `session/start.sessionId` で我々の id をそのまま採り、v4 は拒む。後者がなければ、前者は
「何も検証しないホスト」でも通ってしまう。

### 0093 着地後の見積りの訂正: 19〜28 日ではなく 22〜33 日

ADR 0093 は 2026-09-21 に *adopted* へ到達した。上の予測はそれが 2 つを払うと仮定していたが、
実際に払われたのは 1 つである。

- **managed 専用の門は本当に無料になった。** 0093 は `Caps.ManagedOnly`
  （`agents/agents.go:97-105`）を種別非依存のフラグとして導入し、サーバ側の managed→TUI 遷移は
  それで分岐する（`sessionx/session_driver.go:73`）。Console 側の箇所も同様。muse にとっては
  `Caps()` の 1 行である。
- **承認 `Interaction` は 1 日も払われていない。** lcpp は `Permissions: false` を宣言し、自前の
  承認門を既存の question 種別に流している——コメント自身がそう書いている
  （`agents/lcpp/driver.go:57-59`、`approve` は `:586` で `Kind: "question"` を組む）——そして
  `agents/driver.go:49` はいまも `"question" (future: "approval" | "plan")` のままである。
  もう半分の `Caps.PermissionChoice` は 0093 以前から claude・cursor・kiro・copilot・agy が
  立てており、これも元から muse の支払いではなかった。

よって 3〜5 日の承認の行は丸ごと残り、期待値は表どおりの **22〜33 セッション日**である。muse が
lcpp に倣って承認を question 種別に写すかどうかは段 2 の設計判断であって、先取りしてよい節約では
ない。決定 13 は `approval/requested` が `toolName`・`rawArgs`・`judgeEscalated`・
`protectedWrite`・`subject.stages[].argv` を運ぶことを実測しており、それを 2 択の質問に畳むと
その情報は落ちる。

### P2-2: kind とドライバとスレッド（`workspace/agent/internal/agents/muse/`）

2 つ目の作業パッケージ。`session.KindMuse`・両レジストリ・ライフサイクルの分岐 4 か所・起動時の
reconcile・shutdown・deny-list——そしてドライバ本体（ThreadHandle の 7 メソッド全部、状態の写像、
承認と userInput の往復、steering、中断、resume の突き合わせ）。転写・設定ファイルの締め付け・使用量・
接続カード・Console 面・配備はまだ先である。

**承認の Interaction はいまのところ question 種別であり、これは決定ではなく負債である。** 決定 13 は
AF 初の承認 `Interaction` を求め、決定 5 は承認を有効に保つ。つまり承認に答えられないセッションは
ツールを 1 つも実行できない。承認種別ができるまで、ドライバは保留中の承認から question 種別の
Interaction を組む——kiro の ACP `session/request_permission` と lcpp 自身の門が既にしている流用と
同じである。その代償は正確に書いておく価値がある: ワイヤは `toolName`・`rawArgs`・`judgeEscalated`・
`protectedWrite`・各ステージの解析済み `argv` を運ぶのに、2 択の質問はコマンド行しか残さない。
したがって `Capabilities.Permissions` は **false** のままで、承認の作業パッケージが両方をまとめて
引き上げる。

同じ理由で false を宣言している能力がほかに 3 つある——能力とは「端から端まで実測した経路」の主張
だからである。`CanTranscript`・`CanFork`・`CanForkAt` は各々の作業パッケージ待ち。`DynamicMode` は
恒久的に false で、MSP に AF の plan mode を設定するメソッドが無く、`session/setApprovalMode` は
別の軸だからである。

**ADR が持っていなかった締め付けの経路: `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL`。**
決定 6 の締め付け 5 はプライバシーの代償があるもので——これが無いと実ターンが利用者自身の
`~/.claude/CLAUDE.md` をモデル入力に組み立てる——ADR はその経路を settings ファイル**だけ**と記録して
いた。`--no-foreign-personal-context` が `muse serve` に無いからである。バイナリ自身の文字列表が
それ用の環境変数を名指しており、実際に効く: 使い捨て HOME にマーカーを植えて `muse exec` で実測した
ところ、`1` を設定すると「Including your Claude Code and Codex personal rules」のバナーと、植えた
マーカーの耐久ログ上の痕跡の**両方**が消え、`0` と未設定ではどちらも戻る。ドライバは spawn 時に、
オブザーバ 6 変数と `MUSE_DISABLE_APPROVAL_JUDGE` と併せてこれを設定する。⚠️ 実測は `exec` での
ものであり、`serve` での同等確認には実ターンが要る。よって設定の作業パッケージは依然として鍵ごとの
振る舞い試験を負っている（元から負っていたものではあるが）。

**ワイヤが先に露見させたはずの生成器の欠陥。** `RequestReceipt` は名前付きの空オブジェクトなのに、
型生成器はこれを「型無しの `properties: {}` を持つ*プロパティ*」と同じ扱い——`json.RawMessage`——で
描いていた。そのゼロ値は `""` にマーシャルされる。つまり AF は must-answer のサーバ要求すべてに、
`{}` ではなく文字列で答えるところだった。名前付きの空オブジェクトは `struct{}` として描くようにし、
マーシャル結果をテストで固定した。

検証は `msptest` と、`MUSE_LIVE=1` の裏の実機試験群である。実機側が担うのは偽ホストでは測れないもの
だけ——spawn の argv・子の環境・handshake・`session/start` がベンダ自身のバイナリに対して通ること、
2 回目の Resume が生きたホストを再利用し 1 セッションに 2 つ目の子を作らないこと、drop 後の Resume が
保存済みセッションを**再読込**して利用者の履歴を黙って分岐させないこと、殺された子が死んだハンドルに
なること。ターンの手前で止まるので枠は消費しない。

### P2-3: 転写と、決定 4 の「停止中」半分の訂正

3 つ目の作業パッケージ。実行中の `item/*` ストリーム、ミラーのターンモデル、subagent の項目
——そして実測が覆した決定 4 の前提 1 つ。

🔴 **`session.jsonl` はワイヤの記録を持っていない。** 決定 4 はこう書いている:「ホストが上がって
いないときは、読み取り層が `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` を
解析する。これは追記専用で、同じ記録を持つ」。実走行で測ると、追記専用ではあるが**同じ記録では
ない**。これは muse 内部の語彙によるイベントソース型の**実行時**ログである:
`runtime.session` / `runtime.session.task` の封筒が `started`・`model_request_configured`・
`provider_request_options_configured`・`model_response_created`・`assistant_message_committed`・
`goal_usage_attribution`・`terminal` などを運ぶ——1 プロンプトの echo セッション 1 本で、
68 記録のうち 43 が `runtime.session` だった。ファイルの中に `Item` は 1 つも無い。これを読む
パーサは、まさに Consequences が「この kind には要らない」と謳っている転写のリバース
エンジニアリングそのもので、しかも安定の約束が無い内部形式に対して行うことになる。

プロトコル自身の答えは `session/read` で、`SessionHistory.items`——安定面——を返す。ただしここでは
使えない。理由は muse ではなく Agent Fleet 側にある: `Agent.Transcript` はミラーだけでなく使用量の
集計からも呼ばれる（`sessionx/session_usage.go`）ので、フリート全体の使用量問い合わせが muse
セッション 1 本につき 73 MiB のホストを 1 つ起こすことになる。

**よって停止中の半分は AF 自身のストアにする。** 形は ADR 0093 決定 3 が lcpp で既に使っている
もの——AF が見た item の追記専用ログを `muse-transcripts/<スロット sid>.jsonl` に置き、実行中の
ストリームが書き、`Transcript` が読み戻す。lcpp との違いは、似ているからこそ書いておく価値がある:
あちらはストアが会話**そのもの**だが、こちらは会話はホストが持ち、これは AF が観測したものの
**写し**である。その代償は「AF が見ていない間に走ったターン」で、決定 2（managed 専用・AF が唯一の
書き手）のもとでは Agent が turn の途中で死んだ場合にしか起きず、次の Resume での `session/read`
が埋め戻しの正規手段である。決定 4 のセッション id についての論旨は何も変わらない。変わるのは
名指していたファイルだけである。

実装が固定した小さめの発見が 3 つ:

- **item は改訂されるので、ストアは書き込み時でなく読み出し時に畳む。** `item/started`・任意個の
  `item/delta`・`item/completed` は `itemId` を共有し `revision` が上がっていく。1 item 1 行を
  保とうとすると read-modify-write になり、クラッシュ時に並行の追記を失う。観測をすべて追記して
  読み出しで改訂を畳めばそれが起きない。順序は初見順である——改訂順や id 順に並べると、後に続く
  テキストより後で完了したツール呼び出しがターンの中で入れ替わる。
- **delta は永続化しない。** 後続の `item/completed` が全文を運ぶので、断片を全部書くとストアが
  ストリーミング粒度の分だけ膨れてから捨てられる。断片はメモリに置き `Transcript` が重ねる——
  これがミラーの逐次表示の正体である。
- **ターンの区切りは `turnId` ではない。** user メッセージが user ターンを開き、開いている
  assistant ターンを閉じる。assistant 側の item は 1 つの assistant ターンに畳まれる。item は
  `turnId` を持つが、steer されたターンは注入の前後の item を抱え、ターン間の compaction は
  そもそも持たないので、それで束ねると落ちる。

よって `Caps.CanTranscript` は true になり、ガイドの能力表は「停止中の muse セッションも履歴が
見える」と言う——それが誰の写しなのかを脚注で添えて。

### P2-4: 設定の書き手・締め付け・`Resume` の fail-close

4 つ目の作業パッケージ。`~/.config/muse/settings.json` の AF 側の書き手はちょうど 1 つになり、
置換でなく併合し、muse 自身のロック手順を取り、ロックの外で検証し、失敗すればドライバの
`Resume` の中でセッション開始を拒む——`StartManagedSession` ではない。Resume はターン・回答・
持ち越しセッション・ブリッジの各経路と起動時の `ReconcileManaged` からも直接届くからである。

**設定側の締め付け 6 つはすべて振る舞いで検証済みになり、枠の消費は 0 だった。** 決定 6 は鍵ごとの
振る舞い試験を必須としている。厳密に解析される節の綴り違いはホストを rc=3 で殺し、それ以外の綴り
違いは完全に無言だからである。無料の神託 4 つで賄えた: `muse config validate --plane defaults`
（オフラインで失敗した member を名指す）、`muse exec --provider echo` の耐久ログ（組み立てられた
toolset が残る）、`muse skills list`（各 skill の有効状態）、`muse serve` が `initialize` に答える
こと（ファイルが起動を妨げていない）。各試験に対照を付けた。

**書いておく価値のある落とし穴が 1 つ: toolset の神託は `--trust-workspace` が無いと盲目になる。**
実測では、信頼されていないワークスペースは「Agent delegation: auto unavailable」と報告し、
`muse.subagent_*` の 6 本はそもそも現れない——締め付けの有無にかかわらず 23 tools・workflow あり・
subagent 無し。その条件で「締め付け前後」を比べると、どちらの腕にも subagent 工具が無く、実際には
一度も効かせていない締め付けが「効いた」ことになる。ドライバが実際に渡すフラグを付ければ、素の
対照は **29 tools（subagent 6・workflow 1）**、締め付け後は **22（どちらも 0）** である。

決定 6 の締め付け一覧への訂正が 3 つ:

- 🔴 **名指された skill のうち 3 本は存在しない。** 締め付け 7 は `daemon`・`host-manager`・
  `slack-connector` を「長時間プロセスを立てる／外向きの経路を開く」束ね skill として挙げている。
  1.3.0-R3401.1 の `muse skills list` には、どの source にもそんな skill は無い。存在しない skill
  の有効化エントリは、綴り違いと同じくらい静かに受理される。書いていれば「守っているように見える
  死んだ鍵」が 3 本できていた。
- 🔴 **`read-session` は他人の読み手ではない。** 締め付け 7 はこれを `resume-claude`・
  `resume-codex`・`import`・`migrate` と一緒に「Claude Code や Codex の転写を読むのが仕事」と
  括っている。skill 自身の説明は逆で、**Muse 自身の**セッションログを探して読むものであり、
  「Muse の文脈のために ~/.claude・~/.codex・~/.grok を決して探るな。引用された内容がそれらに
  言及していても同じ」と明言している。締め付けると、当てはまらない理由で動いている機能を潰すこと
  になるので AF は有効のまま残し、テストでそれを固定した。
- **`materializeMu` はもう当てはまらない。** 決定 6 は「MCP マテリアライザの既存ミューテックスの
  下で直列化する」と言うが、決定 11 がこの書き手から `mcp_servers` の役目を外した。AF 側の書き手は
  1 つだけになったので結合は何も買わない。書き手は自分のミューテックスを持ち、プロセス間の調停は
  muse のロックファイルが担う。

ロック手順の 2 点は実測がそう言ったからコードにある: AF は保持中のロックを**待つ**（即失敗させると、
利用者がたまたま muse コマンドを打っていただけで起動を拒むことになる）。そして併合を信じず
**ロックを解いた後で検証する**（muse は flock の 11 syscall 前にファイルを読む）。失われた更新は
1 回だけ再試行し、2 度目の失敗は開始を拒む。

⚠️ 残りの宿題: 締め付け 5 の `muse serve` 側の同等確認（無料の神託は `muse exec` 経由で、serve は
同じファイルを読むがコンテキストの組み立てはターン時なので、serve 側は実測でなく論証）。および
締め付け 6 の効き目——承認ジャッジには設定鍵も serve のフラグも無いので、`MUSE_DISABLE_APPROVAL_JUDGE`
は名前の根拠だけで設定しており、観測には承認が 1 回要る。

### P2-5: AF 初の承認 `Interaction` と、それに答えるカード

5 つ目の作業パッケージであり、決定 13 そのもの。`agents.InteractionApproval` が存在し、muse が
それを上げ、読み取り層が `pendingApproval` として送り出し、ミラーが `/respond` 経由で許可／拒否に
答える。`Capabilities.Permissions` はこれで **true**——宣言する最初の kind になった。

**question 種別の流用でなく作ることを裏づけた調査。** Agent Fleet には既に権限の面があった。
`SessionState` には hook 経路の時代から `permission` の値があり、`PermissionCard` がそれを描く。
それが managed に使えないのは、直せる類の理由ではない: ツリーで実測したとおり、あのカードの 3 つの
ボタンは tmux のモーダルをキーで駆動する——`sendKeys(["Enter"])`・`["Down","Enter"]`・
`["Down","Down","Enter"]`——そして managed セッションにはキーの落ちるペインが無い。これが決定 13 の
「managed では（ペインにすら）存在しない」の正体であり、`Caps.PermissionChoice` の条件が
「**Console から**答えられること」である理由でもある。このパッケージまで、muse がその条件を満たして
いたのは承認を question 種別に畳むことによってだけで、それはコマンド行を残して、ツール名・
保護対象書き込みの印・ジャッジの委譲・各ステージの解析済み argv を捨てていた。

よって承認は独自の種別・独自の積荷・独自の動詞を持つ:

| | question | approval |
|---|---|---|
| 問うこと | 答えを選ぶ | このツールを走らせてよいか |
| 拒否すると | エージェントは続行 | そのツールが止まる |
| 運ぶもの | `[]transcript.Question` | summary・tool・command・ステージ別 argv・protectedWrite・judgeEscalated |
| 答え方 | `decision: "answer"` ＋ 選択 | `decision: "allow"` ／ `"deny"` |
| ワイヤ鍵 | `pendingQuestions` | `pendingApproval` |

**既存の消費者 2 つがこの違いを学ぶ必要があり、どちらも以前は静かに間違っていた。**
`applyManagedAnswerAll`（オペレーターの一括回答ツール）と `applyManagedQuestion`（チャット
ブリッジのボタン）はどちらも `Kind != "question"` で門を張り、「質問はありません」「もう回答済み
です」と返す。承認に対してそれは単に不親切なのではなく**事実が逆**である——セッションはツールで
ブロックされているのに、オペレーターは「何も待っていない」と告げられる。両方とも承認を名指し、
答えられる操作を案内するようにした。

カードが出す選択肢は**許可と拒否のちょうど 2 つ**——scope の選択も「常に許可」も無い。これは
簡略化ではない: 門 B1 は `onRequest` モードが `allow_once` と `abort` のちょうど 2 つを提示すると
実測しており、3 つ目のボタンはランタイムが同意していない永続性を約束することになる。
`pendingApproval` は停止中のセッションには出さない——`pendingQuestions` と同じ規則で、
誰も答えられないカードは無いより悪いからである。

⚠️ カードの描画は dom テストで担保しており、スクリーンショットではない。muse セッションはまだ起動
できない（バイナリ未導入・起動メニューに無い）ので、このパッケージのものは画面で見ていない。目視の
確認は Console 面のパッケージが担う。

### P2-6: 接続状態・ログイン経路、そして端末を持ってはいけないログイン

6 つ目の作業パッケージ。`GET /connections` が muse について何と言うか、デバイスコードの
サインイン（start → poll）、API キーの代替経路、切断、Control Plane のプロキシ許可リストの
同じ 4 本、そして `Resume` の資格情報の門。

**muse にはサインイン済みかを尋ねる手段が無い。** `whoami` も `auth status` も無く、ワイヤにも
無い——47 のメソッドに認証の動詞は 1 つも無い。あるのはファイル 1 つ
`~/.config/muse/auth.json` だけなので、AF はそれを読む。結果は先例より悪いのではなく**良い**:
kiro と codex は調べるたびに子プロセスを起こすので答えを 30 秒キャッシュするが、ファイル読みは
`/connections` のポーリング毎回でも安いので、サインインは最大半分待たされずに次の 1 回で出る。

読み方を決めたのは 3 つの実測で、どれも「まともな読み方」が自信を持って間違う筋である。すべて
1.3.0-R3401.1 での測定で、**プロンプトは 1 本も使っていない**。

1. 🔴 **`api_key` があることは従量課金を意味しない——アカウントのサインインもそれを書く。**
   本物のデバイスコードのサインインは `access_token`・`api_base_url`・`api_key`・
   `mechanism: "oauth"`・`obtained_via: "device_code"`・`user_email`・`user_full_name` を並べて
   残す。`muse auth set --api-key-stdin` は `api_key` を**それだけ**残し、`mechanism` も
   `obtained_via` も無い。つまり鍵の有無で判定するカードは、サブスク契約の利用者全員に
   「使うたび課金されています」と告げる——決定 9 が守ろうとしているものの正反対である。
   判別子は `mechanism` で、`metered` は Agent 側でそこから導出するので、どの面もこの事情を
   知らなくてよい。
2. 🔴 **`muse logout` はファイルを消さない。** `{"schema_version":1,"providers":{}}` を mode 600
   で残す。したがって接続済みとは「`providers.meta` が資格情報を持つ」であって、「ファイルが
   ある」では決してない——後者は最初の logout 以降ずっとサインイン済みと読む。
3. 🔴 **`muse auth set` は provider の項目を丸ごと置き換える。** デバイスコード形のファイルの上で
   実測したところ、残ったのは `api_key` だけだった——アクセストークンも mechanism もメールも
   消えた。アカウントのサインインの上に API キーを書くのは「次のターンから優先される」のでなく
   **サインインを破壊する**。よってカードの API キー経路は、アカウントのサインインがある間は
   警告ではなく**拒否**する（`409 account_login_present`）。利用者はまず切断し、その 2 手が
   確認の代わりになる。

**🔥 このログインはツリーで最初の「端末を持ってはいけない」ログインであり、PTY は静かに壊す。**
既存の接続カードはすべて `agents.StartFlow`（＝`pty.Start`）で CLI を駆動する。PTY 上で実測すると
`muse login` はデバイス URL を印字したあと "Press Enter to open it in your browser:" で**止まる**
——Meta へのポーリングを始めないし、このコンテナに開くブラウザも、キーを押す者もいない。TTY を
外すと同じバイナリは URL を出したあとそのまま "Waiting for approval…" に進み、kiro や cursor の
流儀どおり自分でポーリングする。`agents.StartPipeFlow` がその形である: stdin は `/dev/null`、
stdout と stderr は 1 本のパイプ、それ以外——`Clean`・`WaitFor`・フローストアと TTL の回収——は
そのまま。試験は対照として PTY の腕を持つ。パイプのフローが黙って PTY に戻っても、パイプ側だけの
主張はすべて通ってしまうからである。

実装が決めた小さいこと 3 つ:

- **poll の信号は資格情報であって、子プロセスの最後の行ではない。** muse は何か言う前に
  auth.json を書くし、その文面は契約ではない。ただし資格情報を書かずに**終了した**子は
  決着した失敗（コードの期限切れ、承認の拒否）なので、それを言えばカードのポーリングは
  もう起こり得ないことを 15 分待たずに終われる。報告するのは `Flow.Ended`。未知の flow id は
  「まだ」のままにする——TTL の回収でもその状態に来るので、承認について何も語らないからである。
- **`FlowStore` に `Get` が要った。** 待ち続けるつもりのフローを覗くだけの poll は、`Take` して
  `Put` で戻すことができない: `Put` は**新しい** id を振るので、クライアントが持っている id が
  孤児になる。最初にその書き方をしていて、どのログインも 2 回目の poll で何も見つけられなく
  なるところだった。
- **`META_API_KEY` は報告するが、手は出さない。** muse 自身の `login --help` が「アカウントの
  ログインより常に優先される」と言うので、これを設定した配備では `connected: true`・
  `metered: false`・使うたび課金、が同時に成り立つ。AF は注入もしない（決定 9 が決着済み）し、
  利用者の設定を剥がしもしない——剥がすのは意図的な選択を黙って上書きすることになる——ので、
  矛盾を**見えるように**する `env_key` がある。この失敗は誤りではなく請求書として現れるからである。

**`Resume` は資格情報が無いと拒否する**ようになった。位置は締め付けの門の**後**で、前ではない:
締め付けは安全機構でホストを起こしうる全経路で適用されなければならず、あとから来るサインインが
締め付けられていないファイルを見つけてはならない。門が無いと、未認証のセッションは
`session/start` を受理したうえで**すべての**ターンを `authRequired` で終える（P2-1）——Console では
健全に見え、何も答えられない。

⚠️ **カード本体は Console 面のパッケージへ移した。作業パッケージ表はその点で誤っている。** 表は
「接続カード」をこの行に置くが、カードは表が Console 面に分類している 3 つ無しには正直に描けない:
`SessionKind`（これが無いと何も描かれない）、`kindDisplayName` とバッジが読むレジストリの記述子、
そして kind の色——`--kind-lcpp` の先例に従えば、既存 10 kind **と**両テーマの意味色を避けた実測の
色相＋ headless の描画確認を意味する。ここで試せば、色も名前も無いバッジを見せることになった。
よってこの行は**サーバ側の全部**——状態・両 `routes.go` の 4 本・CP の許可リスト——を着地させ、
Console 面のパッケージがカードを引き受ける。そこで P2-5 の承認カードが負っているスクリーンショット
も一緒に取れる。

### P2-6 追補: ターンでしか測れない宿題 2 件と、1 本目が代わりに見つけたもの

上の Consequences には、無料の神託では答えられない 2 件が残っていた——締め付け 5 の `muse serve`
側の同等確認と、締め付け 6 の効き目。実プロンプトを 5 本使った（利用者の許可は 4〜6 本）。1 件は
答えが出た。もう 1 件は**測れないことが分かった**——そしてその理由は答えよりも重い。

計測の道具立て（再利用できるので書き残す）: 投げ捨て `HOME` に**偽の** `~/.claude` と `~/.codex`
マーカーを植え、`XDG_DATA_HOME` も投げ捨て側にして耐久ログを読めるようにし、`XDG_CONFIG_HOME`
だけ利用者の**本物**の `~/.config` に向けて資格情報を複製せずに見つけさせる——複製はトークンの
更新を持って行って元のサインインを失わせかねない。利用者の `settings.json` は一度も書いていない。
そこに他 CLI 文脈の締め付けが入っていないことが、env 変数を唯一の差として切り出せる理由である。

**✅ 宿題 1 — 締め付け 5 は `serve` でも効く。そして「論証止まり」だったのには理由があった。**
`MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=0` では植えた `~/.claude/CLAUDE.md` のマーカーが
耐久ログに現れ、`=1` では現れない。env 経路は `exec` からの推測ではなく `serve` の実測になった。

一緒に出た方法論の発見が 2 つあり、どちらも**偽の緑**を作るものだった:

- 🔴 **神託はモデルの返答ではなく耐久ログである。** 「見えている AFPROBE で始まる語を挙げよ」と
  聞いたのに、モデルは**どちらの腕でも**マーカーを出さずに答えた。返答を読む試験なら両腕は同一に
  見え、一度も効かせていない締め付けを「効いた」と結論していた。
- **バナーも `serve` では神託にならない。** "Including your Claude Code and Codex personal rules"
  は `serve` の耐久ログにはどちらの腕でも現れない（`exec` では 2 つの信号のうち 1 つだった）。
  両腕を区別するのは植えたマーカーだけである。
- `~/.codex/AGENTS.md` のマーカーは**どちらの腕でも**出なかった。つまり `serve` が組み立てる範囲は
  バナーの言い方より狭い。ただし「Codex は読まれない」とは結論しない——読む側のパスを確かめて
  いないので、「このパスではなかった」までである。

**🔴 宿題 2 — 締め付け 6 は測れない。承認がそもそも起こり得ないからである。** `echo` を走らせる
ターンは、明示的な `approvalMode: "onRequest"` の下で、`approval/requested` 0 件のままツールを
実行した。耐久ログが理由を言っており、それはモードではない:
`runtime.session.permission_profile_committed` は `approval: "on_request"` を記録している（AF の
モードは受理されている）のに、ツール呼び出しには `policy_decision: "allow:policy"` が付く——承認層に
届く前にポリシーが許している。AF の実際の起動で確定する `resolved_snapshot` はこうだった:

```
approval: "on_request"
filesystem: { mode: "unrestricted", rules: [], workspace_roots: [], protected_metadata: false }
local_command_network: { mode: "enabled", targets: [] }
reviewer: "human"
```

session/start だけの 5 本（無料・ターン無し）が原因を切り出し、逃げ道を全部閉じた:

| 起動 | filesystem | 規則 | ローカル網 |
|---|---|---|---|
| `--disable-sandbox --trust-workspace` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --sandbox-network proxy-only` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --disable-write` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --disable-write --disable-shell` | `unrestricted` | 0 | `enabled` |
| `--trust-workspace --sandbox-network restricted`（免除なし） | `managed` | 6 | `restricted` |

すなわち **`--disable-sandbox` が原因の全部であり、より狭いフラグ全部を上書きする。**
`--trust-workspace` はプロファイルを一切変えない——help の "load[s] each session workspace's skills
and rules" どおりで、AF がこれを渡す理由はそのまま有効である。ワークスペースの信頼のせいではないか
という疑いも 2 本目のターンが潰した: `workspaceRoot` の**完全に外**にある `/tmp` への
`tool:write_file` も `allow:policy` になり、承認なしでファイルが書かれた。

よって連鎖は閉じており、今のホスト契約の下では恒久である: 門 A の AppArmor の発見が
`--disable-sandbox` を強制する → そのフラグが権限プロファイルを平らにする → 平らなプロファイルは
どのツールもポリシーで許す → ポリシーで許されたツールは承認層に届かない。
`MUSE_DISABLE_APPROVAL_JUDGE` は名前の根拠のまま設定を続け、**ここでは検証不能**のまま残る——
「まだ検証していない」ではない。

同じ 5 本から出たワイヤの小さい事実 3 つ（すべて無料）:

- **`ApprovalMode` はワイヤ上 4 値**——`allowAll`・`promptUnmatched`・`onRequest`・`denyUnmatched`
  ——で、ホスト自身の `component_ceilings.approval` は 3 値（`on_request`・`prompt_unmatched`・
  `allow_all`）。つまり `denyUnmatched` はプロトコルにはあってこのバックエンドの上限には無い。
  AF は 2 値だけ写し、それ以上写さない理由をコードに書いた。
- **2 つの層がモードについて食い違う。** 開始したセッションは要求した値をそのまま返す
  （`session.approvalMode.mode`・source `startup`）——4 値すべてで——のに、確定した強制プロファイルは
  4 値すべてで `approval: "on_request"` と言う。どちらが支配するかは決着していない。免除の下では
  どちらでも同じであり、AF はモードを送り続ける（費用 0 で、姿勢が変わればそのまま正しいから）。
- `muse serve --help` が "Approval mode ... is selected on the wire, so there is no approval flag
  here" と明言しており、AF の経路が唯一の経路であることを裏づける。

**結果として着地したもの。** `Caps.PermissionChoice` は **false**——利用者に見える側、2 つの設定が
同じ挙動になる起動操作だから。`Capabilities.Permissions` は **true のまま**: これは「ドライバが承認
Interaction 種別を支えている」という宣言で、それは作ってあり試験もあり正しい。そして Agent
プロセスの外に消費者が無いので、利用者に何も約束しない。承認カードと Interaction のコードは P2-5 の
まま残す——承認を上げられる姿勢になったときにそのまま使える。`guide/ref/agents.md` は両言語に
脚注 13 が付いた。あの表で「—」が**機能の不在ではなく安全側の薄さ**を意味する唯一の行であり、muse の
セッションはこのコンテナに対して `shell` と同じ届き方をする。

### P2-7: 配備——ピン・オンデマンド導入・費用 0 のドリフト錠

7 つ目の作業パッケージで、決定 8 が既に考え抜いていた分。`workspace-agent install-muse` を
kiro の形で、`versions.json` のピンと sha256、`BAKE_AGENT_CLIS=1` の経路、`NOTICE` の記載、
`env_tool_versions` の行、ドリフト行とリリース契約。

ピンは `1.3.0-R3401.1` で、**転記ではなく検証済み**である: 手元のプローブのバイナリの sha256 が
release manifest の `artifacts.x86_linux.checksum`（`71b089d0…e2a33`・313,800,920 B）と一致し、
manifest の `msp_schema_fingerprint` は P2-1 で生成した `msp.SchemaFingerprint` とバイト一致した。
Dockerfile に書く数字が、コードが対象にしたファイルを指していることの独立な 2 つの確認。

**起動ガードは無く、それは欠落ではなく決定 2 の帰結である。** kiro は起動毎に再固定できるのは
ペインのプログラムにガードを前置できるからで、muse は managed 専用＝ペインのプログラムがそもそも
無い。よって更新も影の修復も **HTTP 経路だけ**が行う。ドライバの `Resume` は、利用者が頼んでいない
数分のダウンロードで止まる代わりに、サブコマンドを名指して拒否する。

**🔴 版の突合は決定 8 が警告した罠で、届く範囲は installer より広い。** `muse --version` は
`Muse Code 1.3.0 (1.3.0-R3401.1)` で、ピンは括弧内のビルド id。素の semver 抽出は `1.3.0` を返し、
これはピンと永久に一致しない＝毎回「stale」で 299 MiB を落とし直す。その正規表現は仮の話ではなく、
Agent 共有の `extractVer` そのものであり、設定 UI の「ツールのバージョン」の行がそこを通る。
よって `toolSpec` に `VerRe` の穴を足し、muse の行がそれを設定する。そして
`extractVer` と `museParseVersion` が実測の行で**今も食い違うこと**を試験が固定するので、
根拠がコメントの中で腐ることがない。

⚠️ 同じ罠の小型版を、間違ったほうを先に書いて踏んだ: 交替 1 本の正規表現（`\(…\)|semver`）は
**動かない**。実際の行では素の版が括弧のより**左**にあり、一致は交替の順より位置で選ばれるからである。
正規表現 2 本を順に試す形にした。表の試験が 1 回目で捕まえた。

**このドリフト錠はリポジトリで唯一「資格情報を要さない」エージェント契約**であり、それはこの ADR が
muse 固有の利点として名指した性質そのものである。`muse-contract.yml` は本物のプロプライエタリな
バイナリに対して 3 つを検証し、費用は 0: manifest の指紋が生成した定数と一致すること、導入した
バイナリが**自分で同じ指紋を出力**すること（`muse schema`・オフライン）、そして `muse --version` が
今もビルド id を括弧で運ぶこと。資格情報が要らないので、リリース監視は**無人で dispatch する**
——cursor や kiro のリリース端が意図的に手動運転へ回されるのとは違う。意図的にやらないのは
ターンを打つことで、`serve` 越しのターンは利用者個人のサブスクを消費する。

実装が決めた小さいこと 2 つ:

- **変異試験が「checksum が検証されていない」ことを見つけた——コードではなく試験の側で。**
  `verifySha256` の呼び出しを消しても、新しいファイルの他の試験は全部緑だった。これはこの
  リポジトリが既に出荷したことのある欠陥の形（boot-install の `set -e` 事件・sha256 検証 5 か所が
  同時に飾りだった）なので、ダウンロード URL を差し替え可能な var にし、**ハッシュの合わない
  ファイルを与えて拒否を要求する**試験を足した（合うハッシュの腕を対照に）。
- **muse はドリフト行のうち唯一 `setup-agent-cli` に分岐を持たない。意図的である。** 契約が
  自分で artifact を落とすのは、manifest 自身の checksum と指紋を突き合わせることが検査の本体
  だからで、共有インストーラに渡すと検査対象が検査の外に出る。action のコメントがドリフト対象との
  一致を主張しているので、両方のファイルにそう書いた。

### P2-8: Console 面——掃引で選び、見て決めた色

8 つ目の作業パッケージ。`SessionKind`・レジストリ記述子・kind の色と CSS の双子 7 か所・接続
カード・i18n 目録・ブリッジのラベル・ガイド。これで kind は初めて起動メニューに出る——両方が
揃ったときだけ、という門つきで（バイナリが導入済みで、資格情報がある）。

**色は選んだのではなく掃引した。** `console/scripts/kindcolor/muse.mjs` が候補を既存 10 本の
`--kind-*` と同じ画面に出る意味色に対して両テーマで採点し、最悪値を報告する。算数より効いたのは
探索への制約 3 つで、どれも「間違った答え」を出した走行から来ている:

- **彩度の帯。** 制約なしだと上位は全部 chroma 20 のくすんだピンクだった——くすんだ色は彩度の高い
  どの色からも遠いので点数が出る——そして copilot と opencode の隣に**3 つ目のくすんだバッジ**を
  置くことになった。帯は出荷中の kind から実測する（`--chroma`。灰色 2 つは意図的に帯の外）。
- **意味色の周りの色相除外。** lcpp の回は赤を**分類として**却下した——誤りの色相のバッジは dE が
  いくつだろうと「エラー状態」に見える——ので、良い点数で通すのではなく色相ごと除外する。
  ⚠️ 最初に**反転して**書いたため、意味色**以外**を全部除外していた。掃引は誤りの赤そのものの
  色相を答え、それに気づいたのは印字した表である。黙ったフィルタなら信じていた。
- **最後の一歩は絵。** 明テーマについて掃引自身の答えは `#5f376b`（最小 dE 15.9）だった。描画すると
  灰色がかったプラムで、kiro の鮮やかな菫と cursor のマゼンタの隣では**色の仲間ではなく 3 つ目の
  灰色**に見える——彩度の帯が防ごうとしたのと同じ間違いの、もう一段先。`#6b2d7e` は dE を 3 点
  払って色を買い、それでも余裕は先例の下限の 2 倍ある。**dE は候補を順位づけ、見分けのつかない
  ものの間を決めたのは目で見たほう。**

最終値: 暗 `#dbabe6`（最小 dE 17.9 vs cursor・コントラスト 7.11）／明 `#6b2d7e`（最小 dE 12.9
vs kiro・コントラスト 7.21）。Meta のブランド青は意図的に採らない——agy と ssm がその領域を持って
おり、この配色は常にブランド忠実性より見分けやすさを選んできた。

**描画ハーネスは自分自身の欠陥を 2 回見つけた**——それが残す理由である
（`console/scripts/kindcolor/chips.mjs`）。本物のスタイルシートから値を読むようにしたところ、
(1) `--name: value;` の正規表現が**コメントの中**で一致を始めた——コメントは各色相を「どの面に対して
測ったか」で説明しており `--bg/--panel/--bar/--active-bg: 7.11` で終わる——値が次の宣言まで走り、
`--kind-lcpp` を丸ごと飲んだ。次に (2) 変数をインラインの `style=""` 属性で注入したため、
いくつかのトークン値が**二重引用符を含むフォントスタック**で属性を途中で終わらせ、`--kind-*` が
**全部**落ちて 11 個の無色のチップが描かれた（意味色はフォントより前の宣言なので正常に見えた）。
どちらも壊れているようには見えなかった。1 つ目は個数アサートが捕まえ、2 つ目は**絵を見て**初めて
分かった。

**出荷済みの転写の欠陥を、試験ではなくチェックリストを辿って見つけた。** ミラーのターン脚注は
`endTs || ts` で、muse は assistant ターン全体を**1 行**に畳み、最初の item の時刻しか持たなかった
——90 秒のターンが 90 秒早く刻印される。`turnTime.ts` の冒頭自身が「脚注は答えが着いた時ではなく
ターンが**始まった**時を出していた」と書いており、muse が静かにそれを再導入していた。畳み込みが
item ごとに `EndTS` を進めるようにし、muse はあのコメントの第 3 の族（opencode・copilot）に加わった。

意図的にやらなかったこと 2 つ。カードに **`LaunchDefaults` の塊は置かない**——モデルと effort の
操作は別パッケージで、許可の選択は実測により無いので、設定群の唯一の行が無反応の「既定」ピッカーに
なる。そして **muse は `MCP_KINDS` / `mcpreg.knownKinds` に入れない**——ここでどの作業パッケージも
持っていない項目が表に出た: 決定 11 は MCP サーバーをワイヤ（`session/start.config.mcpServers`）に
載せると決め、設定の書き手は正しく `mcp_servers` を書かず、**誰も送っていない**。ガイドの
「連携（MCP）サーバーを受け取る」行は `—` のままで正直であり、ワイヤ側は未構築のまま残る。

### P2-9: モデルと effort の操作、そして誰も選ばなかったはずの既定

9 つ目の作業パッケージ。`GET /agents/muse/models` の裏の `model/list`、reasoning effort の一覧、
Console の操作 2 つ、そして作業パッケージ表が持っていなかったもの 1 つ。

🔴 **起動時の既定は「データ共有への同意」であり、しかも見えなかった。** 決定 6 のクランプ 8 は
「AF は contributor でないモデルを既定にする」と言う。表のどの行もこれを持っておらず、他の kind と
同じ形で作れば確実に間違って出荷された: モデル未選択の起動は `modelId` を送らず、ホストは自分の
既定を適用する。実機の目録での既定は `muse-spark-1.3-contributor`（`isDefault: true`）で、しかも
`description` を持つ行は contributor の 2 本だけ、その文面は「Your content, including inter-session
messages, may be used for product improvement.」である。ピッカーを一度も開かない利用者は、
どの画面にも何も出ないまま、会話をそのように使われることになっていた。

そこで「利用者はモデルを選ばなかった」の解決は Console ではなく**ドライバ**で行う:
`session/start` は、上流がその主張をしていない最新の行を名指しし、`UpdateSettings` の `ClearModel`
もホストの既定ではなく同じ行に解決する。この置き場所が要点で、予約実行も MCP 起点のセッションも
チャットブリッジも `openSession` を通り、そのどれも Console の設定を読まない。したがって Console
側の保存値は空文字のままにする——安全であり、版にも強い。`DEFAULT_AGENT_LAUNCH` に
`muse-spark-1.3` を焼き込めば次の版で陳腐化する。contributor 版はピッカーに残す。クランプ 8 は
利用者に選択を残すために意図的に緩められたものであり、その選択が何を意味するかの一文が
ピッカーの下に付いた。

判定は**論理和**である——`-contributor` の接尾辞**または**製品改善を名指す description。実測では
両者は完全に一致するので、今日は冗長だ。それでも論理和にするのは、2 つが**逆向きに**壊れるから
（接尾辞が変われば文が残り、文が変われば接尾辞が残る）、そして 2 種類の誤りの値段が同じでは
ないからである。偽陽性は「AF が既定で選ばないモデル」1 つ、偽陰性は利用者の会話そのもの。

**effort の一覧は生成された enum そのもので、写しではない。** 生成器が目録の全文字列 enum に
`…Values` スライスを出すようにしたので、`msp.ReasoningEffortValues` がピッカーの提示する値であり、
同時にドライバが検証に使う値でもある——1.4 で上流が足した値が「出るのに拒否される」ことは構造上
起きず、指紋の錠がそのまま効く。提示する全値をドライバ自身の検証器に通す試験を置き、対照として
未宣言の値を 1 つ入れてある。

小さいもの 3 つ:

- **目録の取得は稼働中セッションのホストを使い回す。** `model/list` はクエリ（`commandId` も
  耐久記録も無い）なので稼働中のセッションが答えられ、1 本も無いワークスペースだけがプロセス起動を
  払う。プローブ用ホストの argv からは `--trust-workspace` を意図的に落とした——trust は作業コピー
  についての決定で、目録のクエリには作業コピーが無い。クランプは先に適用する。これはこの経路の
  必要ではなく、ドライバ側の不変条件のほう。
- 🔴 **この目録はセグメント型の操作に収まらない。しかも収まらないときの挙動は「切れる」ではない。**
  実物の 4 つの id で headless に描くと、`.choice-seg` は行いっぱいに広がり、2 行目に折り返し、
  その行自身の「既定のモデル」ラベルの**上に描く**。effort の 9 値も同じ。ずっとこれを守ってきた
  個数の規則（`> 8`）は 26 文字の id 4 本を素通しするので、幅の条件を足した。**「あふれる」は
  推測で、「ラベルを覆う」が実測**——両者を見分けたのはスクリーンショットである。
- **道中で出荷済みの欠陥を 2 つ見つけた。** `DEFAULT_AGENT_LAUNCH` に `lcpp` の行が無く、lcpp の
  起動既定は読み込みのたびに捨てられていた——そのコメント自身が copilot について記録している失敗の
  再演である。そしてこのカードの警告 3 本は Markdown のアスタリスクで書かれていたが、設定画面には
  それを描くものが無く、`**per use**` がそのまま利用者に届いていた。この家の作法は `_strong` の
  別キーなので、今回は素のテキストに直した。カードが `**` を一切描かないことを dom 試験で押さえる。

⚠️ 変異試験はまた元が取れた: 11 個の変異のうち 10 個は期待どおり試験を落とし、1 個は緑のまま通った
——contributor の注記のアサートが、ピッカーの選択肢ラベルに含まれる「contributor」の語に一致して
いたので、注記を丸ごと消しても何も変わらなかった。今は注記自身の一文をアサートしている。

### P2-10: 使用量——値段の無い台帳、問い合わせできないチップ、そして誰にも見えていなかった 2 つの kind

10 個目の作業パッケージ。会計の申告、サブスク枠のチップ、MCP の面、そして使用量ビューの色。

**トークン台帳はほとんど何も要らなかった。転写が既に数字を持っているからである。** `applyUsage`
が `item.Usage` をターンに畳み込み（P2-3）、畳み込み・水位線・ターン別の帰属は共有のものが動く。
このパッケージが足すのは**申告**のほうだ: `usageMeasuredForKind` は **`MeasuredPartial`** を返し、
その理由はここだけでなくコードにも残す価値がある——ワイヤ自身の数字は綺麗で（キャッシュ入力は
分離済み・差分計算不要・失敗ターンは何も報告しない・resume は再生しない）、部分的なのは
**所有**のほうだからだ。門 B1 の実測では、あるターンがワイヤ上で 88,077 prompt トークンと報告する
一方、ホストの耐久ログは 6 回の呼び出しで 116,816 を記録し、うち 2 回は subagent のものだった。
AF のクランプは subagent とオブザーバを止めるので**実際には**ワイヤが完全になる——だからこそ
partial のままにする。クランプは設定であり、この欄は**出所**についての申告だからである。

**コスト推定は無し。それを「欠落」ではなく表の中に書いた。** `usageCatalogProviders` には行を
足さずコメントを足した: models.dev に Muse Code のモデルの項目は無く、上流自身の目録も 4 本とも
`cost: null` を返す（今回もう一度実測）。両端とも空なので、この kind はコストチップの無いトークン
台帳として出荷する。ここで provider を当て推量で書けば、誰も請求していない数字が画面に出る。
行が無い状態は「うっかり忘れ」と見分けがつかない——コメントはそのためにある。

**枠チップは観測であって問い合わせではない。この違いが設計そのものである。** `usage/read` と
非要求の `usage/changed` は同じオブジェクトを運ぶので、AF は両方を 1 つのプロセス全体の値に
記録する——それが記述するのは**アカウント**であり、決定 3 の「セッションごとに 1 ホスト」の形では
5 つのセッションが同じサブスクを報告するからだ。ここから 3 つが従う:

- 新しさの判定は到着順ではなく**ホスト自身の `observedAtMs`**。2 つのホストは順序を入れ替えて
  届きうるし、後戻りするチップは「使用量が返金された」ように読める。
- `GET /muse/usage` は手元に何も無いとき稼働中のホストに聞くが、**何も起動しない**: 枠の表示 1 回に
  299MB のプロセス起動は見合わない。
- 🔴 `{ok: false, authed: true}` は失敗ではなく実在の状態である。実測では、`usage/read` はその
  ホストが完了を 1 度も見ていない間 `{}` を返す。つまり muse セッションが起動直後のワークスペースには
  読みが無く、そこで 0% と出せば「今週はまだ丸々使える」と伝えることになる。WsBar の既存の
  「認証済みだが読めない」経路がまさにその表示（「—」のチップ）なので、そこに着地する形を選んだ。

枠の長さは定数ではなくワイヤの欄（`windowDurationMins`・実測 300）なので、仮定せずそのまま運ぶ。
muse の 1 行目が claude や codex の「5時間」ではなく「現在の枠」なのはそのためである。

**使用量ビューは lcpp も見えていなかった。** `KIND_STACK_ORDER` はグラフが色を与える kind の
一覧で、ここに無い kind は灰色の「その他」に畳まれる。項目は 7 つしかなく、lcpp の消費は
ADR 0093 以来ずっと「その他」に入っていた——間違いではなく**見えない**ので、誰も気づかなかった。
CVD 隣接で**順序づけられた**配色に 2 つ足すのは追記ではないので、再実行できるスクリプトとして
探索をやり直した（`console/scripts/kindcolor/usageorder.mjs`）: 9 個の全順列を、通常視と 3 種類の
2 色覚について、両テーマで、最悪の隣接ペアで採点する。

- 素直に末尾へ足すと**最悪の隣接 ΔE は 10.8**——lcpp の黄と muse のライラックが 3 型色覚で
  潰れる組み合わせで、誰も検査しようと思わないペアである。
- 探索の答えは **17.0**。置き換える 7 kind の順序（CVD 13.0）よりも良い。効いているペアは通常視で
  kiro|copilot、3 型色覚で copilot|agy。
- そのうえで帯を描画し、両テーマ・4 視覚すべてを**見た**。数値は順列を順位づけるだけで、
  算数が通したペアが 1 枚の塊に見えないことを確かめるのは絵のほう——この配色自身の歴史
  （灰色 2 つ）が、その一歩がある理由である。

小さいもの 2 つ: `list_models` は `kind=muse` を門前払いしていた（モデル一覧パッケージ自身の
取りこぼしを MCP 側から見つけた）。`get_session_usage` の説明に、muse の数字が何を含み何を
含まないかを書いた——`cumulative` を読むアシスタントには、subagent のトークンが欠けていることを
知る手立てが他に無いからである。

**このパッケージでやらなかったこと（忘れてはいない）:** コンテキスト使用量ゲージ。
`session/contextUsage` はワイヤにあるが、発火するのはターンの周りだけなので、`contextBar` を
宣言すれば「端から端まで実測した経路」ではなくスキーマから主張した能力になる——他の 4 つの
false の cap が既に守っている規則である。

### P2-11: 指示層——1 つのファイル・2 つのブロック・そして「うちのものではない」文章

11 個目の作業パッケージ。決定 12 が明示的な選択として残していたもの: muse の利用者スコープの
ルールファイルは**1 つ**で、AF の 2 つの適用経路がそれを分け合わねばならない。

**答えは「所有」ではなく「印」。** 決定 12 は 2 択を出していた: AF が
`~/.config/muse/AGENTS.md` を区切り付きで丸ごと所有するか、印で利用者の文章に混ぜるか。後者を
採る。理由は決定 6 が `settings.json` について言うのと同じで、**このファイルは AF のものでは
ない**からだ。Agent Fleet が有ろうが無かろうが、利用者が Muse Code 向けの自分のルールを書く場所
であり、丸ごと所有すれば次の reconcile でその文章が消える。この家はもう一度その代金を払っている
（docs/log/60 damage 1＝AF が起動のたびに CLI のファイルを `cp -f` で消していた）。

つまり状況は codex とまったく同じなので、機構も codex のものをそのまま使う: `mdblock`・1 つの
`AGENTS.md`・reconcile の呼び出し順（fleet → user）に並ぶ 2 つの AF 所有ブロック・印の外は無変更。
`mdblock` は印の綴りが kind ごとにずれないために在るのであり、ここで使うことが muse を
「strip して append」の 7 つ目の写しにしない唯一の方法である。

実測から来た細部が 3 つ:

- **配布状態は「ファイル」ではなく「ブロック」を測る。** muse の `AGENTS.md` はフリート方針が
  着いた時点で存在するので、`fileExists`（kiro と copilot はこれ。成果物が 1 ファイルずつだから）
  では利用者の指示が書かれる前に「配布済み」と表示される。agy と codex が既に避けている罠。
- **AF は `AGENTS.md` だけを書き、`CLAUDE.md` は書かない。** muse は両方を探しに行き、
  プロジェクト層で実測された優先順位は「AGENTS.md が勝ち、CLAUDE.md はこのセッションでは飛ばす」
  である。両方書くのは、muse が「捨てた」と言うファイルを書くということになる。2 つ目を作らない
  ことを試験で押さえた。
- **スキルの側はコードが要らなかった。** `fleetskills.Apply` を `~/.config/muse/skills` へ、
  それだけ。今回も実機で測り直した——`muse skills list --source user` は置いただけの `SKILL.md` を
  導入手順もロックファイルの項目も無しで一覧する。

検証は `reconcileAgentInstructions` 経由の 2 経路と、費用ゼロの実機確認: 投げ捨て HOME へ本物の
書き手で書き、上流自身の `muse skills list --source user` を「本当に登録されたか」の権威として使う。
⚠️ 最初は書き方を間違えた——草稿は `muse config validate --file` を `settings.json` に対して走らせて
いたが、あの副命令が取るのは企業設定の**文書**（`{schema_version, settings}`）であって設定ファイル
ではない。しかも同じコマンドの写しが投げ捨て環境**無し**で、つまり利用者自身のホームに対して
走っていた。どちらも同じ誤りである: 神託を「何に答えるか」ではなく「名前」で選んだ。

### P2-12: MCP をワイヤに載せる——どの作業パッケージも持っていなかった残件と、文書と違う綴り

P2-8 はこれを閉じたのではなく**表に出した**: 決定 11 は連携サーバーを
`session/start.config.mcpServers` に載せると決め、設定の書き手は正しく `mcp_servers` を書かず、
**誰も送っていなかった**。`internal/agents/muse/` に `mcpServers` の言及は 0 件だった。
このパッケージがその穴である。

🔴 **ワイヤの transport の綴りは決定 11 が書いているものではなく、間違えるとセッションごと落ちる。**
決定 11 は設定ファイルのブロックを `transport: stdio | streamable_http` と書いている（そちらは
正しい）。ワイヤの union の HTTP 側は `streamableHttp` である。しかもこの union は**閉じている**
（スキーマ自身の `x-msp-openness: "closed"`）ので、宣言外の値は「そのサーバーが起動しない」では
なく **`session/start` のデコードごと失敗**＝セッションが始まらない。今回、実機で測った:

```
-32602 invalid session/start config: mcpServers does not match the supported shape
```

つまり HTTP 連携を 1 つ持っている利用者は、**すべての** muse セッションが起動しなくなっていた——
しかも ADR の本文がそこへ誘導する。直し方は「書き写すのをやめる」こと: 生成器が閉じた union の
判別子の定数を出すようにした（`SessionMCPServerConfigTransportStdio` /`…StreamableHTTP`）。
union を平らにする処理が、まさに腕を見分ける唯一の `const` を落としていたからである。実機試験は
両腕を持つ——AF の本物の直列化が受理されること、そして**対照として**ファイル側の綴りが拒否される
こと。どんな文字列でも受け取るホストなら、前者だけでは通ってしまう。

**どのサーバーも `mode: optional` で渡し、利用者に選ばせない。** ワイヤの既定は `required` で、
required のサーバーが起動に失敗すると走行ごと中断する——テナントの連携 1 つの宛先が落ちている
だけで、利用者のエージェントが起動しなくなる。レジストリにその選択を置く場所も無い
（`secrets.MCPServer` は `enabled`・`targets`・`kinds`・`timeoutMs` のみ）。決定 11 が既に
「範囲外」と名指している。

🔴 **`MaterializedKinds` はこの問いの一覧ではない。そう読んだ結果は既に 1 度出荷されている。**
あれは「af がどの kind の設定ファイルを書くか」であり、muse にはファイルが無い——足せば、完全に
served な kind に対して永久に `skipped` と報告することになる。ところが
`selfReportToolAvailable` はそれを「このセッションは af の MCP サーバーを受け取るか」の代用として
使っていて、muse についてその答えは**真**である。これは `peerTargetAllowed` の冒頭コメントが
記録しているのと同じ誤り（同じ一覧に lcpp が無いせいで peer メッセージが黙って禁止されていた。
2026-09-21 に実機で発見）なので、答えは「意味どおりの名前を持つ 2 本目の一覧」＝
`mcpreg.ServedKinds`。これが無いと muse のセッションは `af_report` を呼べと言われ、その道具を
持たない——症状は「報告が来ない」だけである。

⚠️ 変異試験はこのパッケージで 2 度効いた。1 つはコンパイルが通らず何も証明しなかったので、通る形で
やり直した（未使用変数は測定ではない）。もう 1 つは緑で通った: `ServedKinds` を
`MaterializedKinds` に戻しても、どの試験も何も見ていなかった——自己報告の行には試験が 1 本も
無かったからである。今は有り、対の反対側として shell / ssm も押さえてある。

muse が意図的に**持たない**もの: `mcpproj` の `fileSpecs` の行（プロジェクトスコープの検査対象でも
複製先でもない）と、`HasProjectScope: false`（agy と同じ形）。Muse のプロジェクトスコープの綴りは
文書化されておらず実測もしていない。これは kind についての静的な事実であって、実行時に他の kind の
ファイルへ落ちる話ではない。

### P2-13: fork——片側は item の id、もう片側は turn の id

能力まわりの最後。`session/fork`、`Caps.CanFork` と `Caps.CanForkAt` の両方、そして
`Capabilities.Fork`。

**この kind では 2 つの cap は一緒に動く。他になりようがない。** 他の kind で分かれるのは、
ある起動経路では fork できて別の経路ではできないから（`agents.ErrForkAtRoute` はまさにそのため
に在る）。muse は managed 専用なので落ちる 2 本目の経路が無い——そしてワイヤ上では、会話全体の
fork は**切り口の無い**地点 fork そのものである（`cutPoint` 省略＝「完了した全ターン」）。
片方だけできる kind は AF の発明になってしまう。

🔴 **アンカーは item の id、切り口は turn の id。その橋渡しは、正常系の試験では絶対に見つからない
off-by-one である。** 決着をつけたのは実測ではなくスキーマ自身の一文だ: item の `turnId` は
「所有するターン（新規ターンでは提出した `commandId` と同じ）」——つまり利用者の発言の item は、
その発言が**開始した**ターンに属する。1 つ前ではない。したがって:

- 「この発言をやり直す」（排他）は**1 つ前**のターンで切る——そして 1 つ前が無い場合は**エラー**で
  あって、会話全体の fork ではない。
- 「この発言から続ける」（包含）は**そのターン**で切る——ただし最後のターンなら、最終ターンまで
  全部残すことが会話全体そのものなので、`""` がそれを言う値になる。

どちらの向きも、間違っていても「それらしい会話」ができあがる。変異試験の 1 本目を「排他の切り口を
1 つずらす」にしたのはそのためである。

**fork は 2 つを複製する。`ForkSource` が muse のセッション id ではなく slot sid を返すのは
それが理由。** ホスト側の会話は `session/fork` が、AF 自身の item ストアは `store.ForkAt` が
複製する——そしてストアこそ `Transcript` が読むもの（transcript.go の冒頭）。muse のセッション id
を返すとストア複製の鍵が無くなり、fork したセッションは空の履歴で開く。fork の目的の正反対である。
slot sid は両方の鍵になる。

実装が決めた小さいこと 3 つ:

- **新しい id を作るのはホスト。** AF が UUIDv7 を渡す `session/start` と違い、fork の同一性は
  結果の中にしか存在しない。だから結果から読んで保存し、fork は「保存済みセッションが無い slot」
  に対して 1 度だけ試す。再試行を冪等にする id が無いからである。
- **ストア複製は致命的ではない。** ホスト側の fork が成功した後に走り、失敗はログに落とす。
  会話はどちらにせよ存在し、「履歴を写せなかったからセッションを拒否する」は描画の欠けを
  死んだセッションと交換することになる——`onItem` が既に取っている姿勢と同じ。
- ⚠️ **ハングする試験は落ちる試験より悪い。** ワイヤ試験の最初の版は捕まえた params を素の
  チャネル受信で読んでいたので、「fork を丸ごと飛ばす」変異——まさにその試験が在る理由の欠陥——で
  パッケージの 10 分タイムアウトまでブロックし、試験名すら出なかった。今は締切を持たせてある。

検証は msptest（両方向の切り口・2 つの拒否・ストア複製）、6 件の変異試験、そして費用ゼロの実機
`session/fork`——完了ターンの無いセッションの会話全体 fork は切り口もモデル呼び出しも要らず、
結果の `forkedFrom` は駆動側が読み戻す当のものである。

### P2-14: 作業パッケージ表に行が無かったガイド記述 2 件

この ADR が段 2 の仕事として名指しているのに、コードではないものが 2 つある: 決定 6 の末尾
（「存在することと AF からは見えないことをガイドに書く」）と未解決 8（「段 2 で鍵を見つけるか、
その実行は AF から見えないとガイドに書くか」）。どちらも P2-1〜P2-13 の全体と、Status を
*adopted* にする瞬間を素通りした。理由は単純で、**作業パッケージ表はコードの面で行を作っており、
「決定が約束した文章」の行が無い**からである。あの表の元になった点検表はまさにこれを止めるために
在るので、発見は点検表側のものだ: 「ガイドに X と書く」と言う決定は、表に file を持たない成果物
であり、自分の行を必要とする。

どちらも、先に実測しないと正直には書けない。そして 2 つとも枠を消費しない。

🔴 **未解決 8: 鍵は在る。そして使えない。** 探索はベンダ自身のオフライン検証器で網羅的に行った
（`muse config validate --plane defaults --file`・文書は `{"schema_version":1,"settings":{…}}` の
包み。未知の member を名指してくれる）: defaults プレーンに `cron` / `scheduler` / `automation`
という節はそもそも無く（在るのは `run`・`agents`・`context`・`skills`・`tools`・`tui`・
`telemetry` とスカラーの `model`）、`run` に cron の member は無く、`tools` は
artifact/web_search/web_fetch である。**policy** プレーンは別文書で
（`execution.permission_profiles`・`execution.approval_{scopes,modes,reviewers}`・
`execution.network_sandbox_modes`・`model_egress`）、やはり cron は無い。ツールを消せる唯一の
レバーは `run.toolset` だが、これは cron のスイッチではなく**ツール面全体の名前による
許可一覧**である。echo の神託で測ると、cron ツールと一緒に 2 つを道連れにする:

- **MCP のツールを全部。** スタブの stdio サーバの `mcp__afprobe__probe_ping` は既定の
  `{"mode":"all","source":"default"}` では組み立て済みツールに在り、
  `{"mode":"named","source":"settings"}` では消える。AF 自身の `af` サーバも同じ経路に乗るので、
  この鍵で締めた muse セッションは `af_report` を呼べと言われて当のツールを持たない——P2-12 の
  `ServedKinds` が防いでいる失敗が別の扉から入ってくる。名前を一覧に足して回避することもできない:
  MCP のツール名はセッションごとに最初のターンで発見されるもので、設定ファイルを書く時点で AF は
  知らない。
- **次の版でホストそのものを。** 知らない名前は無視されない:
  `invalid run configuration: unknown tool names: …` で、セッションが存在する前に rc=2。1.4 で
  ツールが 1 つ改名されれば、フリート中の muse セッションが起動しなくなる。

よって答えはガイドの段落であり、`TestLiveCronToolsSurviveTheClamps` と
`TestLiveTheOnlyKeyThatCutsCronCutsMCPToolsToo` が両半分を正直に保つ——前者は後の版で段落が嘘に
なれば赤、後者は鍵を却下した理由が成り立たなくなれば赤になる。ツールが実際に提供するもの（バイナリ
自身のスキーマから読んだ）: 5 欄のローカル時刻 cron によるプロンプト、既定は繰り返し、**7 日で
自動失効**、既定では実行中でも発火、取り消しは `cron_delete` だけ。

**決定 6 のもう 1 つの残件: muse 自身の peer メッセージは門があり、その門は閉じている。**
`send_session_message` と `list_peer_sessions` はバイナリのツール表には在るが、ここで組み立てられる
ツール一覧には**どの腕でも**出ない: 4 通り（`local_session_messaging.enabled` の true / false、
`MUSE_EXPERIMENTAL_LOCAL_SESSION_MESSAGING=1`、それに
`MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=1` を足したもの）で測って 29 ツール、どれも入っていない。
投げ捨て HOME の `muse session-message list` は `external agent ingress is unavailable` と答える。
機能は実在し（副命令、利用者単位の `~/.local/share/muse/session-name-authority/`）、実験扱いで、AF は
何も有効にしていない。ガイドは「存在するが AF からは何も見えない」と書く——決定 6 が求めたとおりで、
締め付けを足すのは既に閉じている扉に鍵を書き足すことになる。

✅ **未解決 6——越える。ただし起動順による。** 投げ捨て HOME 1 つにホスト 2 本・セッション 2 つ、
両方から `session/list`（クエリなので commandId もモデル呼び出しも無い）。**後に**起動したホストは
先のセッションを `status: "notLoaded"` で一覧した——スキーマ自身が言う「他のホストがロード中の
セッション」の印である。**先に**起動したホストは後のセッションを見なかった。つまりホストは自分が
見つけた時点のストアを配るのであって、読み直しはしない。越えること自体は実在するので、AF は順番に
依存してはならない: 規律は「駆動側は `session/list` を一切呼ばない」であり、
`TestTheDriverNeverListsTheUsersOtherSessions` がパッケージを走査して固定する。Console は AF 自身の
帳簿から描かれ、自分が始めていない muse の会話を差し出さない。

✅ **未解決 7——下位ディレクトリ。そして救っているのはリポジトリである。** AF は `m.CWD()` を送る
（driver.go）ので、下位フォルダへ起動したセッションは**下位フォルダ**を `workspaceRoot` として渡す。
子プロセスの cwd も `--trust-workspace` が掛かる先も同じ値である。ホストはそれを記録する:
`session/list` を下位フォルダで絞るとセッションが返り、作業コピーで絞ると返らない（対照——引数を
無視する絞り込みなら前者だけは通ってしまう）。利用者が体で感じるのはルールファイルの方で、そこは
懸念より良い答えだった: 作業コピー自身の `AGENTS.md`——workspaceRoot の 1 つ**上**——も組み立てられる。
🔴 ただしそれは AF の作業コピーが git リポジトリだからであって、同じ木から `.git` を外すと下位
フォルダの `AGENTS.md` だけになる。遡りはホームではなくリポジトリのルートで止まる。腕は 3 本、
どれも無料、3 本目はどちらのファイルも組み立てられない未信頼の対照である。

### P2-15: 実ターン 3 本で買えたものと、そこで見つかった 401

能力表の空欄は「Muse Code にできないこと」の一覧ではなく、「誰も**見ていない**こと」の一覧で
ある。利用者のサブスクに対する実ターン 3 本（打つ前に申告した。このパッケージの費用はこれで
全部）で 4 行が片付き、読むだけでは決して出なかった欠陥が 2 つ出た。

**ターン 0——worktree は無料で片付いた。** 最初のプロンプト無しの
`create_session(kind=muse, worktree=true)`: セッションは managed で新しい worktree に上がり、
`git worktree list` に出て、`muse serve` の子の `/proc/<pid>/cwd` がその worktree そのもので
ある。モデル呼び出し無しで ✓ にした——「worktree で起動する」は起動の主張であり、見たのは起動
だからである。

**ターン 1——スキルピッカーと、その下にあった穴。** 🔴 `session_skills.go` の switch に
**muse の case が 1 つも無く**、muse は `default:` に落ちて endpoint は `{"skills":[]}` を
返していた。つまりコンポーザは muse セッションに何も出していなかった——他のどの kind にも注入で
届くリポジトリ自身のスキルさえも。この ADR の作業パッケージ表はそのファイルを名指ししている
（`sessionx/session_skills.go:80-91`「muse にはスキルがある」）のに、行が素通りした。稼働中の
Agent に対して実測: 同じリポジトリで muse は `[]`、claude は native の一覧を返す——無料の陰性
対照である。

修正は muse を copilot/kiro/agy/lcpp と同じ foreign 専用のバケツに入れること。そしてターンが
肝心の半分を証明した: 置いた `.claude/skills/af-probe-topic/SKILL.md` に対しコンポーザ自身の
注入文（「… を読んで、そのスキルの指示に従って実行して」）を送ると、スキルの 1 行が返ってきた。
よってガイドの行は `—¹¹`（まだ作っていない）から `—⁴`（native の列挙は無い・foreign は注入で
届く）へ動く。別の、そして真である文になった。作られていないのは native の半分で、muse に
関しては「機構が無い」ではない: MSP は `skill/list` と、host が展開する selector を持つ `skill`
入力パートを公開している。

**ターン 2——定時実行が端から端まで。** now+2 分の `once` 予約、`session_mode: reuse`、
`agent_kind: muse`、稼働中のセッションを指す: CP が発火し（`last_status: fired`・
`reuse_run_count: 1`）、プロンプトはセッション自身の転写に `source: schedule`——ミラーが読む
バッジ——を伴って着き、セッションが答えた。✓。Console の予約ピッカーにも muse が入る
（`AgentCaps.scheduledRuns`）。

**ターン 3——引き継ぎは失敗し、その失敗が収穫だった。** 🔴 muse セッションの `af` MCP サーバは
起動し、ツールはモデルに届いた——`mcp__af_…__propose_session_handoff` が実際に呼ばれた。P2-12 の
ワイヤ経路が初めて端から端まで実測された瞬間である——そして呼び出しはこう返った:

```
tool failed: 引き継ぎ提案の保存に失敗しました: Agent API エラー (401): missing or invalid agent token
```

**muse は MCP の子の環境を洗う。codex とまったく同じである。** codex についてはそれを
`extraEnvVars` を書いた時から知っていた——af サーバは `AGENT_TOKEN`・`AGENT_ADDR`・
`AF_SESSION_NAME`・`AF_CP_BASE_URL`・`AF_MEMO_TOKEN` の明示的な転送を必要とする——のに、muse の
ワイヤ経路は何も転送していなかった。定義自身の `env` を写すだけだったからである。症状は P2-12 が
`ServedKinds` のために名指したもので、別の扉から同じものが出荷されていた: **`af_report` を呼べと
言われて呼べないセッション**。唯一の兆候は「報告が来ないこと」である。builtin はすべて影響を
受ける（他の builtin の `mcp-run` ラッパーはストアを開くのに `AF_SECRET_KEY` が要る）。そして
どの試験も捕まえられなかった——af サーバを動かす試験はどれも、環境をまだ持っているプロセスの
中で走っていたからである。

修正は、写すのではなく一覧を公開すること（`mcpreg.ForwardEnvNames`——codex の `env_vars` の
「名前」と muse のワイヤ `env` の「値」で 1 つの一覧）、転送は builtin だけ（利用者自身の stdio
コマンドに AF の資格情報は渡さない）、そして定義自身の値が勝つこと。6 腕の変異試験は全部赤。
⚠️ **実機での再確認は借りであり、ここでは走らせられない**: このコンテナのセッションを駆動して
いる Agent は配備済みビルドで、修正はソース側にある。配備後なら 1 ターンで済む——muse セッション
に `af_report` を呼ばせて、401 が起きないことを見ればよい。

ワイヤが運ぶのは「値」なので、「ベンダのホストに渡したトークンはその disk に届くか」は仮定では
なく実測した: `session/start.config.mcpServers[].env` に渡したマーカーは muse のストアのどこにも
現れない（一度きりの調べではなく、実機 MCP 試験のアサートにした）。

**このパッケージが直した、同じ形——手書きの一覧——のものがもう 3 つ。**

- 🔴 **MCP ツールの「説明」から muse が全部抜けていた**。両言語・両サーバで:
  `create_session` の `kind`、`list_models` の `kind`、`get_agent_usage`。検証側は muse を
  受け付ける。説明はエージェントが読むものなので、MCP 経由で muse の子セッションが起こされる
  ことは決して無かった。（CP 側の写しには lcpp も無い——ADR 0093 の残件として記録するだけに
  した。「lcpp がこの経路で動く」はあちらが実測して言うことだからである。）
- 🔴 **CP の `mcpKnownKinds` は 7 個**で、`mcpreg.knownKinds`（Go）と `MCP_KINDS`（TS）は 8 個
  だった: テナントの MCP サーバを muse に絞ろうとした管理者は「unknown agent kind」で拒否される。
  2 モジュール・3 言語に 3 つの写しがあるので、番人は他の 2 つをテキストとして読み、どちらの
  向きのずれでも赤になる。
- 同じ「軸に名前を付ける」直しがこの記録の中にもう 2 つ: 予約ピッカー（P2-14）と、muse を lcpp の
  隣に「居なくても no-op な kind」として名指すようにした `injectDriver` のコメント。

**そして試験そのものの欠陥が 1 つ**。muse が導入**済み**の機械で走らせて見つかった:
`install_muse_test.go` は HOME を仮に差し替えるが PATH はそのままで、`musePresent` は
`exec.LookPath("muse")` に落ちる——これは意図的で、他所に焼かれたバイナリを再ダウンロードしない
ためである。結果 3 本がここで赤・CI で緑になっていた。向きが逆である: 製品が無い場所でしか通らない
試験は、機械の試験でしかない。ヘルパーは今、`muse` を持つディレクトリだけを PATH から落として残りは
残す——PATH を空にすると、チェックサムの試験が「curl が無い」の試験に化けるからである。

**画像貼り付けは作った。そして意図的に ✓ にしていない。** `inputParts` は添付の
`.png/.jpg/.gif/.webp` を読み、本物の MSP `image` パートを送る（base64 と、必須の `mediaType`。
コンテナに無いかもしれないファイルを読む `mime.TypeByExtension` ではなく固定の対応表から採る）。
インラインにしないもの——別の種類のファイル、空ファイル、8 MiB 超、読めないパス——はパスを書いた
テキストパートへ落とす。スクリーンショットを貼った利用者が「何も言及しないターン」を送る事態に
だけはしてはならないからである。6 腕の変異試験は全部赤。その掃引は、試験で区別できないと証明
された分岐（読み取り後の長さ検査が既に覆っていた `stat` のサイズ検査）を 1 つ削らせもした。
Console の cap はオフ、ガイドの行も `—` のまま: ✓ にするのは実画像 1 ターンであり、この表は
「見たもの」しか ✓ にしない。

### P2-17: 画像貼り付け——実ターン 1 本と神託 2 つで ✓ にした（2026-09-22）

P2-15 はドライバ側（`inputParts` が添付の `.png/.jpg/.gif/.webp` を読んで本物の MSP `image`
パートを送る）を作り、行は意図的に `—` のままにした。ワイヤの形は単体試験と 6 腕の変異試験で
固定できるが、**ベンダのホストがそのバイトを受け取ってモデルに見せるか**は AF が自分について
主張できることではないからである。実ターン 1 本で決着した（`TestLiveImagePasteReachesTheModel`・
ターンは 7.9 秒で completed）。

**神託は 2 つ。そしてワイヤの方が先である。** ホストは userMessage の item に添付の**メタデータ**を
echo する（`Item.Attachments`。base64 の中身は意図的に view に返さない）。AF 自身のストアは item を
丸ごと保持するので、「ホストが画像を受け取った」はモデルの書いた語を 1 つも読まずに answerable だった:

```
host echoed an attachment: type="image" mediaType="image/png"
```

2 つ目の腕はモデルの返答で、これは締め付けの場合と違ってここでは採用できる: 合言葉は PNG の
**画素の中に**描き込んであり、プロンプトには 1 度も現れない。よって再現できたことは「画像がモデルに
渡った」の陽性証拠になる。欠けていても何も証明しないが、現れたことは echo では作れない。

⚠️ **プローブ画像は HOME を投げ捨てる「前」に描くこと。** Pillow は利用者の user site-packages に
居るので、`isolateHomeKeepMuseAuth` の後で描こうとすると `No module named PIL` で試験が自分を
skip する——黙って走らなくなる実機試験は、落ちる試験より悪い。走らせて捕まえた（最初の 1 回は
ターンを使わずに skip した）。

残りは P2-16 と同じ配置: 投げ捨て HOME（`~/.claude` のルールを Meta へ送らない）、muse の設定は
symlink で参照し複製しない、auth.json の sha256 は前後で同一。

これで `caps.imagePaste` は true、ガイドの行は ✓。脚注 11 を持つ行は 3 つ（引き継ぎ・チャット
ブリッジ・アシスタントチャット）で、うち引き継ぎが待っているのは「誰かが見ること」ではなく
**P2-15 の 401 修正を載せた配備**である。

### P2-18: 残り 3 行——1 つは ✓、1 つは人の目待ち、1 つは本当に未実装

P2-15 の env 修正を載せた配備が着地した（稼働中 Agent のバイナリに `ForwardEnvNames` が入って
いる）ので、引き継ぎの行を塞いでいた 401 を測り直せる状態になった。実 AF muse セッションで
実ターン 1 本、聞いたのは 2 つ——af のツールが一覧に在るか、そして `propose_session_handoff` を
呼ぶこと。

```
af_report, generate_image, propose_session_handoff, add_memo
PROPOSE-OK
```

**引き継ぎは ✓。** 提案は作られた（「引き継ぎ案を利用者へ提示しました…」）＝ツールが
`401 missing or invalid agent token` ではなく Agent API に届いている。同じ 1 行が P2-15 の
もう 1 つの借りも返す: `generate_image` が muse セッションに広告されている——これはループバックの
状態問い合わせが成功したときにしか起きない（`mcpImageGenAdvertise`）ので、環境転送が単体試験
だけでなく端から端まで確認できたことになる。

**チャットブリッジは、この箱の中からは決着しない。** 分岐しているのは通知の**種類**
（`answer-ready` / `question` / `permission-request` / `exit` / `session-report`）であって
エージェントの種別ではなく、このワークスペースは Discord 接続済み・5 種すべて有効である。ただし
観測できる置き場は成功すると**空になる**——通知アウトボックスもブリッジのキューも前後で空だった
——ので、「配信された」と「そもそも積まれなかった」がここからは同じ絵に見える。正直な神託は
チャンネルそのもので、それを見るのは利用者である。誰かが見るまでこの行は `—` のまま。

🔴 **アシスタントチャットとしての利用は「未確認」ではなく「未実装」。ADR はそこを取り違えていた。**
段 2 の終わりの節はこれを「そもそもエージェントごとに書かれていない経路」に分類していたが、誤りで
ある: アシスタントチャットは**エージェントごとの provider**（`chatx` の `claudeChat` /
`codexChat` / `opencodeChat` / `agyChat` / `cursorChat`。lcpp も自分の `lcppChat` を持つ）＋
Console 側の `ASSISTANT_AGENT_KINDS` でできていて、muse はそのどちらにも居ない。作るなら
`museChat` が MSP 越しに `muse serve` を駆動することになるが、対象の会話は **AF のセッションでは
ない**のでライフサイクルが新しい仕事になる（`--provider echo` は本物のアシスタントを背負えず、
ほかに headless の一発経路が無い）。フラグではなく、独立したパッケージである。

### P2-19: チャットブリッジ——唯一在る神託＝人が見ること、で ✓ にした

ブリッジの行はコンテナの中からは決着しない、と P2-18 に書いた。決着の付け方はそのとおりに
なった: Discord を短いターン 1 本のあいだ ON にしてもらい、チャンネルに何が出たかを利用者が
報告する、という形である。

```
t=0s   muse セッションへプロンプト送信
t=5s   bridge-queue: 1 件      （積まれた）
t=8s   bridge-queue: 1 件
t=9s   bridge-queue: 0 件      （引き取られて配信・削除）
```

そしてチャンネルには 20:40 に *「セッションが入力待ちになりました「ADR0095 P2-18 引き継ぎ/
ブリッジ検証（muse）」（Muse Code）」* がセッションのリンク付きで出た。行が主張しているのは
まさにそれ——muse セッションの完了が利用者のチャットに届くこと——なので ✓ とする。

**AF 側の痕跡に何ができて何ができないか。** 「キューが 1 件増えて 4 秒で消えた」は配信と整合し、
「そもそも積まれなかった」とは矛盾する。しかし**配信とリトライ枯渇による破棄は区別できない**
（どちらも空のキューで終わる）。答えが在るのはチャンネルだけである。これを書いておくのは、
P2-6 の耐久ログの教訓と同じ形だからだ: 手元の置き場が成功すると空になる設計では、**不在は証拠に
ならず**、残った神託が人間であることもある。

⚠️ **ON にして見つかった欠陥が 1 つ。muse のものではない**: claude（ペイン方式）のセッションで、
**同じ通知が同じ本文のまま何度も届く**。muse のターンはキューに 1 件しか作らなかったので、多重性は
送信側より手前——状態側が作る通知にある。`RecordSessionNotification` の answer-ready の腕は
`state == "idle" && (previous == "working" || previous == "")` で発火し、`previous == ""` の側は
**意図的**である（ペイン読みの idle heal が `status.Remove(sid)` するので、この腕が無いと
マーカーの消えた後に来た本物の Stop を取りこぼす、という実害があった）。つまり除去のあとに idle の
フックがもう一度来れば、同じ完了がもう一度発火する。`notice.PutOnce` はまさにこのために在って、
ここでは使われていない。再現手順・鍵の候補・戻してはいけない回帰を添えて、専用のセッションへ渡した。

これで能力表に残る muse の行は 1 つで、しかもそれは「見ていない」ではない: アシスタントチャットと
しての利用は**未実装**である（P2-18）。

### 段 2 の終わり——何が在り、何が無く、隣で 1 つ見つかったか

作業パッケージ表の全行が着地した（P2-1〜P2-11 と P2-13、加えてどの行も持っていなかった MCP 経路の
P2-12）。よって冒頭の Status は *adopted* である。見積りは 22〜33 セッション日で、実際は
13 個のパッケージ——そしてそのあとに、決定が約束していて表に行の無かったガイド記述 2 件のための
14 個目 P2-14 になった。その記録自身の教訓はこうだ: 点検表がコードの面を数え上げる作りなので、
名指しされた成果物が 2 つ未着のまま *adopted* が付いた。

**この節を最初に書いた時点で `—` は 8 行だった。P2-15 が実ターン 3 本を使い、今は 5 行**である。
「Muse Code に塞がれているのではなく、ただ見ていなかっただけ」の正直な内訳はこうなる:

- **実測で ✓ にしたもの**: worktree 起動と定時実行。どちらも実際の muse セッションで見た
  （P2-15）。前者はターン 0 本。
- **✓ ではなく印を付け替えたもの**: スキル/コマンドピッカー。これは「未確認」ではなく
  **未配線**だった——muse は `session_skills.go` のどの分岐にも無く、endpoint は空の一覧を
  返していた。今は他の 4 kind と同じ脚注 4 を持つ: foreign スキルが注入で届く（実測）、native の
  列挙は無い。native の半分は MSP の `skill/list` に対する本当の作業である。
- **作ったが ✓ にしていないもの**: 画像貼り付け。ドライバは本物の `image` パートを送る
  （P2-15）。cap と行は実画像 1 ターン待ちである。
- **まだ `—` で、まだ見ていないもの**: コンテキスト量ゲージ（`session/contextUsage` は在るが
  発火はターンの周りだけなので、`contextBar` を宣言するとスキーマから読んだ能力になる）、
  引き継ぎ——P2-15 が、muse セッションからの af ツールの書き込みを全部潰していた 401 を見つけて
  直したので、この行が待っているのは「誰も見ていないこと」ではなく「配備後の再実測」である——
  チャットブリッジ、アシスタントチャットとしての利用。

⚠️ **muse のものではない発見が 1 つ**。上を確認する過程で出た:
`ScheduleDetailModal.tsx` の `AGENT_KINDS` は手で維持された 6 個の一覧
（`claude, codex, opencode, copilot, cursor, kiro`）で、**agy** が抜けていた——このガイドの同じ表が
予約実行を ✓ としている kind である。つまり Console では、予約のエージェントを agy に編集できなかった。
**P2-14 で、この注記が求めたとおりに直した**——`mcpreg.ServedKinds` と同じ形である: 軸に名前を付け
（`AgentCaps.scheduledRuns`）、ピッカーの一覧はそこから**導出**する（`scheduledKinds`）。ずれた写しは
もう存在しない。lcpp と muse は一覧から外したままにした——スケジューラが拒否するからではなく、
ピッカーが出すのはガイドが ✓ にしたものだけであり、その 2 行は実ターンで動くのを見るまで `—` だから
である。

⚠️ 一段下に同じ形がもう 1 つあるが、こちらは意図的に触っていない: `scheduler_wake.go` の
`injectDriver` も手書きの managed kind 一覧で、lcpp も muse も入っていない。ただしこれは無害で、
無害である理由の方が面白い——Agent 側が作成時に `ManagedOnly` の kind を managed へ既定する
（`session_handlers.go:707`）ので、CP の一覧は既に反対側で名前の付いた軸の手前に置かれた最適化に
過ぎない。欠陥へずれようがない一覧は、ここで変える価値が無い。

### P2-16: コンテキスト使用量ゲージ — 配線済み・実機確認済み（2026-09-22）

**作業パッケージが求めたもの。** `session/contextUsage`（MSP のライブなコンテキスト窓圧力通知。
`SessionContextUsageParams`＝`usedTokens`・省略可能な `windowTokens`・`pressure`）を AF の
セッション使用量に載せ、Console のコンテキストゲージが muse セッションでも出るようにする。

**作ったもの（実ターン不要）。**

- `handle.go` — 専用の `ctxMu` ロックで保護する 3 フィールドを追加（`ctxUsed int64`・
  `ctxWindow *int64`・`ctxHasUsage bool`）。`onNotify` に `msp.NotificationSessionContextUsage`
  の case を追加してデコード・保存する。`ctxWindow` はワイヤに `windowTokens` が無い場合 nil——
  値は捏造しない。
- `context.go`（新規）— `ManagedContext(name)` が `(usedTokens, windowTokens, ok)` を返す。
  ok=false は最初の通知が来るまで継続するので、ターン完了前はゲージを出さない。また
  `agentImpl` に `agents.ContextReporter`（`ContextFill`）を実装し、チャットミラーの
  ContextBar がライブ値を拾えるようにする。窓の定数 `MuseDefaultWindow = 1,007,997`（muse-spark
  の全 4 モデルで実測）。`windowTokens` がワイヤにある場合はその値を優先する。
- `sessionx/session_usage.go` — `overlayMuseLiveUsage`（`overlayKiroLiveUsage` の並列）を追加し、
  一括 `/sessions/usage` の context ブロックを `muse.ManagedContext` から埋める。窓ソースは
  ワイヤが提供した場合 `"recorded"`、フォールバックを使った場合 `"estimated"`。

**テスト（`go test ./...` 全体 51 パッケージ 3940 本で全緑）:**
`context_test.go` に 6 本の新規テスト: 通知前ガード・通知が記録される（`windowTokens` あり）・
`windowTokens` 無しでフォールバック・最新スナップショット優先・ハンドル無しの場合の
`ManagedContext` と `ContextFill` のガード。

**実機確認——2026-09-22（実サブスクリプション 1 ターン）。**

`live_test.go` の `TestLiveContextUsage`: 投げ捨て HOME に `.config/muse` をシンボリックリンク
（kiro パターン——トークンをコピーしない）、プロンプト `"1"`、90 秒タイムアウト。

ワイヤ上の計測値:

| フィールド | 値 |
|---|---|
| `usedTokens` | **21,747** |
| `windowTokens` | **1,007,997**（ワイヤ上に在り・`windowSource=recorded`）|
| `windowTokens` なし？ | なし——このターンでホストが送ってきた |
| ターン状態 | `completed` |
| 経過時間 | **6.70 秒** |
| `auth.json` sha256 | 前後で同一（シンボリックリンクのみ・コピーなし）|

`ManagedContext` は計測値で ok=true を返した。ContextBar の経路がエンドツーエンドで確認済み。
`caps.contextBar` を `registry.ts` で `true` に反転し、ガイドの行を ✓ に更新した。

### P2-20: アシスタントチャット——`muse exec --json` で実装（ADR 0095 P2-20）

**実装内容。**

`chatx/chat_providers_muse.go` を新規作成し、`chatx.ChatProvider.Send` を実装する `museChat` 構造体を追加した。
ターンごとに `muse exec` を 1 回呼び出し、最初のイベントの `stream.id` からセッション ID を取得して次ターン以降の `--session-id` に渡すことで会話の継続性を実現する（実測: 同じ ID を渡した 2 回目の exec で `Session match: True` がトレースに出ることを確認済み）。

アーキテクチャ上の決定:

- **`muse exec --json`**（`muse serve` over MSP ではなく）。アシスタントチャットは薄い Q&A レイヤーで、ターンをまたいで管理するライフサイクルも専用ペインもない。`exec` はターンごとに起動・終了し、これは他の CLI バック型プロバイダ（codex・cursor・opencode）と同じ形である。MSP-over-serve は会話ごとにプロセスを常駐させる必要があり、マネージドセッションドライバと同等の複雑さになる一方、ターン単位での利得はない。

- **継続性には `--session-id`・`--no-session-log` は使わない**。この 2 つのフラグは競合する——muse は `--no-session-log` が一緒にあると `--session-id` を受け付けない（"a session id needs retained logging; remove --no-session-log"）。セッション ID は `ChatConversation.MuseSessionID` に保持し、各ターン後に会話レコードに書き戻す。メンバーのセッション一覧への混入は `XDG_DATA_HOME`（エージェント状態ディレクトリのサブディレクトリを指す）で回避する。

- **全クランプをすべての exec の前に適用**。`muse.EnsureClamps()`（マネージドドライバの `Resume` と同じ契約）はバイナリを呼び出す前に `settings.json` を書く。`muse.ChildEnv()`（パッケージ非公開の `childEnv` の公開ラッパーとして新規追加）が env ルートのクランプを渡す: `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=1`・6 本のオブザーバー無効化変数・`MUSE_NO_AUTO_UPDATE=1`。`--no-foreign-personal-context`（exec 専用フラグ・serve にはない）が argv レベルでの多重防護を担う。

- **プロンプトは `--prompt-file` 経由**。ペルソナの前文とプロンプトを一時ファイルに書き（リターン時に削除）、argv の長さ制限を回避する。マネージドドライバがコンテキスト注入に使うパターンと同じ。

- **セキュリティ姿勢フラグ**: `--disable-shell`・`--disable-write`・`--disable-web-tools`・`--approval-mode never`・`--approval-judge off`。アシスタントチャットは Q&A 専用でホストを変更してはならない。

**ワイヤフォーマット（Muse Code 1.3.0-R3401.1・`--provider echo` および実モデルで実測）。**

`--json` の出力は 1 行 1 JSON オブジェクト:

```json
{"stream":{"kind":"session","id":"<uuid>"},...,"payload_type":"run.output.delta","payload":{"text":"…"}}
{"stream":{"kind":"session","id":"<uuid>"},...,"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"<full reply>","reason":null}}
```

セッション ID は 1 回の exec の全イベントで同一。`terminal != "completed"` の `run.terminal.completed` には失敗理由が入る。terminal イベントの `payload.text` が権威ある完全な返答で、デルタの累積はフォールバック。

**Console 側。** `console/src/lib/settings.ts` の `ASSISTANT_AGENT_KINDS` に `"muse"` を追加した。`caps.headlessChat` は実測が済むまで立てない。

**`internal/agents/muse/program.go` への追加エクスポート:**

- `ChildEnv(base []string) []string` — `childEnv` の薄いラッパー。`chatx` は兄弟パッケージなので exec がパッケージ外で動く。
- `HasCredential() bool` — `readCredential().Present` の薄いラッパー。プロバイダの `museAvailable()` で使う（可用性チェックはネットワーク呼び出しを使ってはならない）。

**P2-20 では実ターンを使っていない。** 親タスクの指示に Meta API への実ターンには利用者の同意が要ると明記されていたため。プロバイダは実装・配線済み。エンドツーエンド検証（アシスタントモーダルからの実際のチャットターン）は利用者がテストを選択した時点に持ち越す。

`chatx/chat_providers_muse_live_test.go` にスケルトン（`MUSE_LIVE=1` ガード付き）を追加した: 1 ターン目で PONG が返りセッション ID が取得できること、2 ターン目で `--session-id` 継続が機能すること（前のターンの内容を踏まえた返答）、`--disable-shell/write` で書き込みツールが遮断されること。実ターンを打ったときにこのテストを走らせてから ✓ にする。
