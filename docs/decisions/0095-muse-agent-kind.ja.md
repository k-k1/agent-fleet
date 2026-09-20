# 0095. Meta の Muse Code をセッション種別（`muse`）にする — TUI 契約ではなくベンダのプロトコルに乗る、実機 2 門の先で

[English](0095-muse-agent-kind.md) | 日本語

- Status: **proposed**（2026-09-20）。実装は何も無い。以下の `file:line` は当時の develop
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
| **OS サンドボックス** | **🔴 ◎ ここでは動かない** | Linux のサンドボックスは bubblewrap（バイナリ中に `bwrap` 38・`seccomp` 50 の文字列。文献は「動作する bubblewrap と非 musl ビルドが要る。無ければサンドボックス下のシェルコマンドは全て environment failure で中断する」と言う）。イメージに `bwrap` は無く、この箱ではユーザー名前空間は作れるのに **`move_mount` が EACCES** ＝ bubblewrap はマウントを木に付けられない |
| **共有リポジトリへの書き込み** | **🔴 ◎ 触る** | `muse exec -w create` は `<repo>/.muse/worktrees/<日付>-<hash>` を作業根に選び、`.muse/.session-worktree-reservations/` を作り、**`.git/info/exclude` に `/.muse/worktrees/` を追記した**。リンク worktree ではそのファイルは親クローンのもの＝全セッション共有 |
| **他 CLI の個人領域** | **⚠️ ◎ 既定で読む** | 初回起動が `Including your Codex personal rules and 5 skills` と出した。`--no-foreign-personal-context` を渡さない限り `~/.claude` と `~/.codex` のスキル・ルールを拾う |
| 機能の重複 | ⚠️ △ | subagent（既定 1 木 8・`agents.execution_capacity` は 1〜64）、それぞれ独自にモデルを呼ぶ背景オブザーバ 4 本、workflow（生涯 1,000 子）、**利用者横断のセッション名前空間**とピアメッセージング。いずれも AF の登録簿・ミラー・使用量台帳からは見えない |
| 認証 | △ | ブラウザサインインか API キー。`META_API_KEY`、または `muse auth set` で `~/.config/muse/auth.json` に保存。API キーは常にブラウザセッションに優先する。マネージド（MMA）アカウントはキー必須 |
| 課金 | △ | トークン従量、または定額サブスク 3 段。サブスクの枠は **5 時間あたりのプロンプト数**で数え、Meta Model API アカウントでサインインした CLI 経由でのみ有効 |
| 実ターンの挙動 | **× 未測** | `turn/start` は `{"error":{"kind":"authRequired","retryable":false}}` を返した。トークン使用量・モデル目録（未認証の `model/list` は `{"models":[],"source":"bundledCatalog"}`）・承認の往復・subagent イベントは資格情報 1 本が要る |
| TUI の文字列契約 | × | 未測。決定 2 により不要 |

### リポジトリに既にあるもの

- 種別は 9 つ（`workspace/agent/internal/session/session.go:20-28`）。登録は 2 箇所で、読み層が
  `sessionx/agent.go:28-38`、managed ドライバ 5 つが `sessionx/session_turn.go:31-37`。
  **未登録の種別は拒否されず claude に黙って正規化される**（`sessionx/agent.go:41-53`）。
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
- 版は Dockerfile の `ARG` でピンし（npm 3 種は `workspace/Dockerfile:318-320`、cursor の版付き tarball は
  `:404-411`）、`workspace/agent/env_tool_versions.go` が表に出す。

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
やった手順）。`KIND_STACK_ORDER`（`colors.ts:81`）で青を 3 つ隣接させないこと。

### 決定 2 — `muse` は managed 専用。Terminal（CLI）経路は作らない

MSP はペインが与えるもの（状態・承認・ステアリング・fork・使用量・スキル・モデル一覧）を既に全部運ぶので、
tmux ペインを足しても得られるのは「二つ目の UI」と「維持すべき文字列契約」だけである。これは ADR 0093
決定 2 が `lcpp` について提案している形と同じで、費用も同じ——「Terminal 経路を持たない」門は Console の
5 箇所＋サーバ側の managed→TUI 遷移
（`console/src/features/repos/{LaunchModal.tsx,StartModal.tsx,RepoRowConnected.tsx}`、
`console/src/features/sessions/{SessionMenu.tsx,useSessionActions.tsx}`、
`workspace/agent/internal/sessionx/session_driver.go:62-105`）。**0093 と 0095 のうち先に着地した方が
払い、後の方は無料で貰う。** 着手時点でどちらも未着地なら、この費用は本種別の見積りに入る。

### 決定 3 — `per-session-child`。セッションごとに `muse serve` を 1 本

MSP は 1 プロセスで複数セッションを抱えられる（`session/list`・接続ごとの自動購読）ので共有デーモンも
可能だが、v1 では採らない。理由はプロトコル自身の言葉にある——ホストの **サンドボックス姿勢とセッションの
耐久性はホストの寿命の間固定で、ワイヤ上で交渉できない**（`muse serve --help`）。ある AF セッションの
作業根・拒否リスト・承認姿勢を、別のセッションが決めてよい道理は無い。待機 **73 MiB**（実測）なら
セッションごとの子は買える——claude のペインの 3 分の 1 である。`Capabilities.ProcessModel =
"per-session-child"` は cursor / kiro / copilot が既に使う値なので、enum の追加は要らない。

### 決定 4 — 転写は「動いている間は MSP、止まっていれば JSONL」

ホストが生きている間は `item/started` / `item/delta` / `item/completed` を `transcript.Turn` に写す。
落ちていれば読み層が
`~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` を読む（追記のみ、同じレコード）。
**この経路を AF が計算してはいけない**——`session/start` の応答が返すので、作成時に `session.Meta` へ
持つ。日付分割のパスを自力で探すのは、kiro が cwd＋mtime で踏んだ「前身を掴む」罠（ADR 0026 決定 6）と
同型である。subagent の転写は `subagent/<id>/session.jsonl` にあるが、v1 では親ターン上のツール型の
項目として描き、子ごとのペインは作らない。

### 決定 5 — サンドボックスは切る。残る門は承認である

`muse serve --disable-sandbox`。実測のとおり Workspace のコンテナでは bubblewrap がサンドボックスを
組めず（`move_mount` → EACCES）、サンドボックスが ON のまま使えないと **エージェントが走らせるシェル
コマンドは全て environment failure で中断する**＝種別として成立しない。承認はこれと直交するので ON の
まま残す。承認モードはセッションごとにワイヤ上で選ばれ、AF は `approval/requested` に
`approval/decide` で答え、起動時の許可選択（docs/log/76）を `untrusted` | `on-request` | `never` に
写す。

これは免除であり、ADR として免除と明記する。Workspace の中では箱そのものが境界であり、それは他の 9 種別
——どれも自分をサンドボックスしない——でも既に同じである。`bwrap` を焼いてホストが許す日が来たら見直す
（段 1 の門 A）。

### 決定 6 — `~/.config/muse/settings.json` は AF が持ち、5 つの挙動を締める

このファイルは `"schema_version": 1` が無いと **全コマンドが起動時に落ちる**ので、盲目的にマージせず
書く。読み書きはストアの作法を通す（素の `os.WriteFile` は同居セッションの鍵を落とす）。AF が設定するのは:

1. `agents.execution_capacity` を小さく。放っておくと 1 セッションが、メモリ制約のある共有ホストで
   8 エージェントを走らせる。
2. 背景オブザーバを OFF。4 本がそれぞれ独自にモデルを呼ぶ＝従量アカウントでは見えない出費、
   プロンプト枠のサブスクでは見えない枠消費になる。
3. workflow を OFF（`auto` は 1 セッションに最大 1,000 の子を許す）。
4. worktree 隔離を OFF、`-w` は渡さない。決定 7。
5. 他 CLI の個人文脈を OFF（`--no-foreign-personal-context` か設定の同等物）。`~/.claude` と `~/.codex`
   を読むことは、別種別の指示層をこの種別に黙って混ぜることであり、その 2 つはワークスペース方針で
   触れてはならない場所でもある。

Muse 自身のピアメッセージングとセッション名の権威
（`~/.local/share/muse/session-name-authority/`、利用者横断）は、v1 では AF の cross-session messaging に
**繋がない**。1 つの名前空間に 2 つの経路があり、片方がミラーに映らない——それが
`native-peer-channel-invisible-in-mirror` の起き方だった。ガイドには「在るが AF からは見えない」と書く。

### 決定 7 — この種別はリポジトリにも他セッションの机にも触れない

実測のとおり Muse の worktree 実行は `.git/info/exclude` を書き換え、それはリンク worktree では親クローンの
ファイルである。よって `-w` は渡さず、worktree 隔離は OFF（決定 6）、この種別は「ブランチも worktree も
作らない種別」として宣言する。並行して書きたい利用者には AF 自身の worktree がある。それが worktree の
用途である。

### 決定 8 — 配備はピン版の焼き込み。`~/.local/bin` の影を見張る

release マニフェストがプラットフォーム別に url・sha256・サイズを出すので、既存の型がそのまま効く:
`ARG MUSE_VERSION` ＋アーキ別 sha256 をビルド時検証、ランチャーが期待する配置（ランチャーの隣に
`muse-bin-<版>` と `.muse-version`）で `/usr/local/share/muse` に置き、entrypoint が
`MUSE_NO_AUTO_UPDATE=1` を輸出し、`env_tool_versions.go` に 1 行足す。この配置が読み取り専用でも動くことは
実測した。

費用は 2 つ、後から発見せずここで名指しする。**イメージ容量**: 1 イメージあたり ≈ 299 MiB（x86_64）/
≈ 269 MiB（aarch64）。kiro をオンデマンド導入に追いやった 855 MiB よりはるかに小さいが、ただではない。
**影**: ベンダ導入スクリプトの既定の置き場は `~/.local/bin/muse` で、PATH で先勝ちし、recreate でも消えない
——古い `~/.local/bin/workspace-agent` が Agent を乗っ取ったのと同じ罠である。利用者が一行インストーラを
一度走らせると、管理外の自己更新ビルドに恒久的に固定される。だから接続カードは影を見つけたら報告する。

### 決定 9 — 資格情報は保存型 API キー、入力は 1 回

`muse auth set`（キーは stdin）で `~/.config/muse/auth.json` に保存し、`~/.config/muse` をファイル拒否
リストに足す。子プロセスの環境変数に `META_API_KEY` を撒く形は採らない。cursor が env 注入を断った理由
（ADR 0023）がそのまま効き、さらに **API キーは常に保存済みサインインに優先する**ので、env のキーは後から
サインインした利用者を黙って無効化する。よって接続カードは **入力 1 つ**——既存のどの種別より簡単である。
切断は `muse logout`。

未決: Muse Code の**サブスク**利用者（従量ではない）は CLI のオンボーディング中にブラウザでサインインする
が、この箱にその導線は無い。段 1 の門 B で、サブスクの資格情報を別の場所で作って貼れるのか、それとも v1 は
サブスク対象外なのかを決める。

### 決定 10 — 使用量とモデル一覧はプロトコルに乗る

`usage/read`・`session/tokenUsage`・`session/contextUsage` がワイヤにあるので、`muse` は
`usageMeasuredForKind` の **exact** 集合（`usage_fold.go:206`）に入る見込みである。ただし実ターンを 1 回
測った後に限る——測っていない「exact」こそ、あの switch が防ぐために存在する嘘そのものである。
`model/list` がピッカーを支えるので、`agentModels.ts:34-35` の `isDynamic` に `muse` を足すこと。この 1 行は
新種別のたびに漏れてきた（copilot・cursor）もので、症状は「モデル選択肢が既定だけ」である。

### 決定 11 — MCP: 共有設定ファイルに書き込む新方言

`mcp_servers` は同じ `settings.json` の中のブロックで、`transport: stdio | streamable_http`、
`command`/`args`/`env` か `url`/`headers`、`enabled`、そして **`mode`（既定 `required`）——required の
サーバが起動に失敗すると実行全体が中断する**。AF は利用者が明示しない限り `mode: optional` で materialize
する。壊れたテナントのサーバのせいでエージェントが起動を拒むのは筋が悪い。`${VAR}` 展開があり、stdio
サーバには `MUSE_SESSION_ID` が渡る。`muse` は `knownKinds`（`mcpreg/def.go:57`）と `MaterializedKinds`
（`materialize.go:47`）の両方に入る。プロジェクトスコープの Muse 綴りは文献に無いので、`mcpproj` は
muse に対して他種別のファイルを読むだけとし、複製先にはしない。フック（`.muse/hooks.json`、`Stop` や
`Notification` を含む 15 のライフサイクルイベント）は**使わない**。フックが報せることはプロトコルが既に
報せており、リポジトリ内のフックファイルは共有状態だからである。

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
- **利用者ごとのオンデマンド導入（kiro の形）。** 855 MiB では正当化できても 299 MiB では過剰で、しかも
  自己更新バイナリを利用者の home に置く＝決定 8 が見張っている影そのものになる。
- **プロトコルの良さだけで今すぐ採用する。** 下の 2 門は安く、どちらも「いくら読んでも分からないこと」を
  見に行くものである。

## 影響

- **この種別が無料で貰うもの**（他種別が金を払ったもの）: TUI 文字列契約のテスト、フックファイル、
  ポーリング状態検出、セッション ID の発見、転写の逆解析——そして `--provider echo` があるので、
  **資格情報もトークン予算も無しに**ハーネスを作り回帰テストできること。既存 9 種別でこの 6 つが全部
  揃っていたものは無い。
- **代わりに負うもの**: サンドボックスの免除（決定 5）、壊してはいけない設定ファイル（決定 6）、機能が
  こちらと重なるベンダ（決定 6、および「AF からは何が見えないか」を書くガイドの節）、そして 1 か月で
  1.2 → 1.3 と動いたベータ。
- **ドリフトには鍵がある**: `muse schema` はオフラインで、release マニフェストには
  `msp_schema_fingerprint` がある。焼いたバイナリの指紋と、生成した型が拠った指紋の一致をテストにすれば、
  黙ったプロトコル変更が赤いビルドになる。他のどの種別にも無い仕掛けである。
- **見積り**: managed 専用・TUI 資産なしで ≈ **12〜18 セッション日**。kiro より小さくならないのは、
  節約分（ペイン無し・スクレイプ無し）を設定方言・MSP クライアントと型・承認の往復・subagent の描画・
  配備に使い切るからである。規模の錨: 今 `kiro` に言及する Go は 90 ファイル・Console は 41 ファイル、
  既存種別は 1 つあたり非テスト 2,100〜5,950 行。
- 段 1 の門が落ちたときの埋没費用は、この ADR とプローブだけ。コードは無い。

## 段階

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | この ADR のプローブ（2026-09-20 完了）: 導入・MSP 疎通・ピンとチェックサム・worktree と他 CLI 文脈の挙動・常駐費 | — |
| 1 | **門 A**: `bwrap` を焼いて実 Workspace イメージでサンドボックスを再測——決定 5 が立つか撤回か。**門 B**: API キー 1 本で実ターン 1 回——`usage/read` の数字、`model/list`、`approval/requested` の往復、subagent 1 つ、そしてサブスクの資格情報をそもそも入力できるか。1〜2 セッション日 | 両門に答えが出て、利用者が決定 6 の締め付けと出費を受け入れる |
| 2 | 実装: 種別配線、MSP クライアントと生成型、ドライバ、転写、使用量、設定＋MCP 方言、接続カード、配備、ガイド、この ADR を *adopted* へ | — |

## 未解決（段 1 で答える）

1. 実ターンのトークン会計: `usage/read` は input / output / キャッシュを分けて返すか。subagent と
   オブザーバの呼び出しを含むか。（`MeasuredExact` が正直かどうかが決まる。）
2. サブスクと従量: この箱で走らせられないブラウザオンボーディング抜きに、サブスクの資格情報は作れるか。
   作れないなら v1 は従量のみで、ガイドにそう書く。
3. 認証後の `model/list` は目録を返すか。返すとして、copilot や cursor のようにプラン依存か
   （「Free では named model 不可」型の失敗）。
4. 承認の描き方: `approval/requested` は段階的なシェル審査の情報を運ぶ。そのどこまでを、新しいカード種別を
   増やさずにミラーの許可カードで出せるか。
5. セッションごとのホストの `session/list` が、他の AF セッションの Muse セッションまで見えてしまうか
   （ストアも利用者も 1 つ）。見えるなら、Console が決してそれらを差し出さないこと。

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
  strace -e trace=move_mount mount -t tmpfs none /tmp/x          # move_mount → EACCES
```

## 参照した出所（2026-09-20・`06ea94d3`）

`workspace/agent/internal/session/session.go` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go}` ·
`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/mcpreg/{def.go,materialize.go}` ·
`workspace/agent/{usage_fold.go,agent_instructions.go,model_provider.go,env_tool_versions.go}` ·
`workspace/Dockerfile` · `console/src/agents/registry.ts` · `console/src/lib/agentModels.ts` ·
`console/src/styles/tokens.css` · `console/src/features/usage/colors.ts` ·
`docs/log/{36,40,43,74}-*-agent-kind.md` · `docs/decisions/0093-lcpp-agent-kind.ja.md` ·
Meta の文献 `dev.meta.ai/docs/muse-code/{,auth,subscriptions,permissions,interactive,workflows,session-messaging,rewind,configuration,extending,changelog}`。
