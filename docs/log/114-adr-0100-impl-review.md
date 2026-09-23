# 114-adr-0100-impl-review. ADR 0100「画像生成スタジオ」実装のレビュー

- 対象: [ADR 0100](../decisions/0100-image-generation-studio.ja.md)（決定 1〜12・影響・改訂 1〜7）に対する
  実装ブランチ。巡ごとに節を追記し、既存の節は書き換えない。
- 読み方: 🔴＝直さないと ADR の約束が本番で壊れる（マージ不可）、🟡＝実装時に明記・修正する、
  🔵＝提案。行番号は各巡で取り込んだ HEAD のもの。
  `agent/` は `workspace/agent/`、`mcpx/` は `workspace/agent/internal/mcpx/`、
  `ig/` は `workspace/agent/internal/imagegen/`、`sx/` は `workspace/agent/internal/sessionx/`、
  `con/` は `console/src/` を指す。
- 手順: ① 差分を前回の指摘から独立に読み、ADR の契約と実コードを突き合わせる。② 2 巡目以降は
  前回の指摘を 1 件ずつ **反映済み／不十分／未反映** で判定する。実 CLI は起動しない。
  試験は陰性対照（変異で赤になるか）を自分で確かめる。

---

## 1. 第 1 段（契約凍結＋P0 前提作業）のレビュー（2026-09-23）

- 対象: `temp/susa5m4`（PR #924）の `ad0d58519`（A: 番人）・`e34453e45`（B: 合図の剥がし手）・
  `3ace348f1`（C: 型・ルート・メタ・MCP 宣言）。develop `b5c59d694` に merge して読んだ。
- 検証: `go test ./internal/imagegen/ ./internal/mcpx/ ./internal/sessionx/ .`（workspace/agent）は
  1949 件緑。**自分で変異を 7 本入れ、全部赤になった**: 識別不能時の「同じフォルダに結ばれた
  セッションがある」条件を外す／`run_image_trial` の呼び出し側 `AgentTrial` 照合を外す／
  `settleInitialPrompt` の「pending からだけ」を外す／起動時回収を空にする／合図の `]` 末尾条件を
  外す／絶対パス側の拒否リストを外す／`WriteSessionMetaKeepingLock` の `Studio` 保持を外す。
  PR 本文の変異 13 本の申告と合わせ、A・B・C の陰性対照は揃っている。

### 1.1 新たに見つけた点

#### 🔴1 作成時にスタジオの `session` を結ぶ口が無い。Managed の人格ターンは必ず断られる

決定 2 の③は「Agent は**起動より前に**メタへ `Studio` を書き、**スタジオの `session` を結んでから**
`initial_prompt` を送る」（ADR:115-118）。実装の作成処理は `Meta.Studio` を書くだけ
（`sx/session_handlers.go:998-1019`）。スタジオ側への結びは無く、フックも無い。
凍結したルートで結べるのは `POST /imagegen/studios/{id}/bind`（`ig/studio.go:132-136`）だけ。
ところがセッション名は作成処理の中で採番されるので、ペインが bind できるのは作成の応答を
受け取った後になる。Managed では初回ターンを作成処理の中で同期に `h.Send` する
（`sx/session_handlers.go:1049-1060`）ので、人格ターンで最初に呼ぶ `get_image_studio` は、
ペインが bind するより先に `mcpStudioCall` のスタジオ側照合（`mcpx/mcp_studio.go:109-111`）で
「結びが変わりました」と断られる。TUI は配達が非同期なので多くは間に合うが、保証は無い。
**型は正しいが、本番でこの順序にはならない。** ストアのレーンが後から埋めるにしても、
凍結すべき継ぎ目がいまの契約に無い。

直し方（最小）: `imagegen` にフック（例 `BindStudioOnCreate(studio, session string) error`）を
宣言し、作成処理が `slot.publish` と起動より前に呼ぶ。スタジオが無い・別の生きたセッションに
結ばれているときは作成を 4xx で断る（1 スタジオ 1 セッション）。ストアが入るまでは、フック未設定なら
`studio` 付きの作成を 501 で断る。`ImageStudioBind` の用途は「結び替え・外す」と注記する。
これが入れば、いまは存在確認なしで任意の UUID がメタに書ける点（`:1007`）も同じ所で閉じる。

#### 🟡1 recreate が引き継いだ `Studio` を結び直す主体がいない

逸脱 8 は、recreate で `Studio` を引き継ぎ（`sx/session_handlers.go:1591`）、スタジオ側の追従は
後続のストアのレーンに任せるとしている。追従が無いあいだ、新しいスロットは次の状態になる。

- メタに `Studio` があるので、`generate_image` は広告から外され、呼び出し時の再検査でも断られる。
- スタジオのツールは広告されるが、呼ぶと必ず断られる。

つまり画像の手段が何も無いセッションになる。🔴1 のフックを recreate でも呼ぶ
（`studio.session` が旧名のときだけ新名へ条件付きで書き換える）ことを、いまの契約に書くこと。

#### 🟡2 `run_image_trial` の待ちと heartbeat の置き場が決まっていない

ADR の試走ツールは、最長 120 秒待ち、10 秒ごとに heartbeat を送り、パス・seed・警告・所要時間を
返す（ADR:140）。ツールの説明文も既に「Waits up to 2 minutes」と約束している
（`mcpx/mcp_stdio.go:1194`）。ところが中継は `agentDo`（15 秒・heartbeat 無し、
`mcpx/mcp_studio.go:128-130,141`）で press の応答をそのまま返す。
その応答 `ImageStudioPressResult` は版・グループ・ジョブ id しか持たない（`ig/studio.go:196-201`）。

`jobWire` は seed・files・warnings・error・elapsed_ms を持つ（`ig/jobs.go:1069-1101`）ので、
MCP 子が press の後に `GET /imagegen/jobs` を heartbeat 付きでポーリングすれば、契約を変えずに
実装できる。「待つのは MCP 子で、press は即答」とコメントか PR に明記し、MCP のレーンが取りこぼさないようにすること。

#### 🟡3 相対パスを browse root から解決するので、エージェントの cwd 相対が別のファイルを指す

逸脱 1 は、`generate_image` の相対パスを browse root から解くようにした（`ig/inputs.go:157-164`）。
しかしエージェントの cwd はリポジトリ（または worktree）で、`docs/ref.png` と書けばその中を
指したつもりでいる。browse root が home なら `~/docs/ref.png` を開くことになる。同名の別ファイルが
あれば**黙って違う絵を参照する**。無ければ理由の読めない 400 になる。
MCP 子は自分の cwd を知っている（`mcpOwningSession` が `os.Getwd` を読む）。
`mcpGenerateImage`（`mcpx/mcp_imagegen.go:206-`）と、今後の `set_image_draft` の `inputs` で、
相対パスを子の cwd で絶対化してから送ること。Console の InputPicker は browse root 相対のままでよい。
経路が違うので、解決の基準も経路で分けてよい。

#### 🟡4 合図の剥がし手が全セッションで「`[studio ` で始まり `]` で終わる最終行」を消す

`StripStudioSignal`（`ig/studio_signal.go:19-30`）と `stripStudioSignal`（`con/features/mirror/transcript/model.ts`）は、
スタジオに結ばれていないセッションにも効く。利用者が最終行に `[studio notes]` のような行を
書けば、転写・自動タイトル・返信候補・ブランチ名から黙って消える。ADR の合図は
`→ get_image_studio]` で終わる（ADR:212）。末尾条件をその文字列にすれば誤爆はほぼ無くなる。
合図を出す側（Console のレーン）はまだ無いので、**形を狭めるなら今が安い**。定数は Go と TS に
1 つずつあり、一致は試験で固定済み。

#### 🟡5 下書きの型に `full_steps` が無く、localStorage から移すと失われる（凍結型への 1 欄追加）

今のペインの下書きは `fullSteps` を持つ（`con/features/imagegen/draft.ts:61`）。`jobRequest` も
`full_steps` を持つ（`ig/jobs_http.go:55`）。ところが `ImageStudioDraft`（`ig/studio.go:19-43`）にも
`ImageStudioPress`（`:189-192`、mode と session だけ）にも、この欄が無い。スタジオを作った瞬間
（決定 2 ①の「localStorage の下書きを移す」）に、人の「試走をそのまま本番の steps で」が
表せなくなる。決定 2 の「ADR 0081 の利用者は何も失わない」（ADR:132）に反する。
下書きに `full_steps`（人だけ）を足すか、press 本文に載せる。**凍結型の変更なので、この段で入れる。**

#### 🔵1 番人の読める元に scratch base が入らない

Files ペインの絶対パスは scratch base（`/tmp/claude-<uid>` 等）も読める（`agent/fs.go:164-168`）。
一方、番人は browse root と生成物 root の 2 つだけ（`ig/inputs.go:166-177`）。これは ADR の決定 4
どおりだが、エージェントが scratchpad に置いた絵を `generate_image` の参照に渡すと、今後は断られる。
ツールの説明文かガイドに「参照は browse root か生成画像の下に置く」と 1 行書いておくと、
エージェントが迷わない。

#### 🔵2 agy に渡す参照の置き場が変わった（実機で 1 回）

agy は参照パスをプロンプトの文字列で受け取り、自分で開く（`ig/agy.go:449-458`、cwd は `~/wd`）。
渡すパスは利用者のパスから `~/.cache/agent-fleet/generated/console/inputs/<set>/` に変わった。
隠しディレクトリの下を agy のツールが読めるかは測っていない。受け入れの実機の回で 1 度確かめる。

#### 確認して問題が無かった点

- 固定コピーは provider の引数 `Request` にだけ載り、原本は `JobSpec` の記録専用欄から各 `jobRec` へ
  写される（`ig/jobs.go:235-265,363-366`）。サイドカーと `jobWire` は原本から書く（`:529,1179`）。
  番人を通っていない spec は `Enqueue` が断る（`:291-293`）。
- 掃除は 4 経路にある。投入失敗（`ig/jobs_http.go:83-86`）、`finish`、待機中の個別取消、グループ取消
  （`ig/jobs.go:623-649,791-793,889-891`・判定は `:631`）、起動時（`agent/main.go:187`）。
  `/imagegen/generate` の同期経路は `defer` で消す（`ig/http.go:555-564`）。
- 番人は root の FD の下で `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` を使う。FIFO を `O_NONBLOCK` で
  開いた後に種別で断り、上限を `LimitReader` で二重に掛ける（`ig/inputs_open_linux.go:21-64`）。
  provider 側の読み口は `readRequestFile` 1 か所で、AST 走査で固定されている。
- `generate_image` は広告から除外され（`mcpx/mcp_stdio.go:1570-1575`）、呼び出し時にも再検査される
  （`mcpx/mcp_imagegen.go:214-219`）。スタジオのツールは両側を照合する（`mcpx/mcp_studio.go:100-115`）。
  名前は文字列リテラル。
- `InitialPromptState` について:
  - Managed は `slot.publish` で pending を書き、同期に確定する（`sx/session_handlers.go:1022-1060`）。
  - TUI は全ての戻り口で状態を返す（`sx/session_io.go:853-932`）。
  - 起動時に pending を回収する（`sx/locks.go:283-311`）。
  - 一覧のスナップショットによる上書きからは保護している（`sx/locks.go:169-172`）。
- `Studio` の欄が通る箇所: 作成・fork（引き継がない）・recreate・`wireSession`・CP の `sessionWire`・
  DB ミラー（`control-plane/workspace_handlers.go:700-710,737,757`、移行 sqlite 0071／pg 0056）。

### 1.2 総評

新規の指摘は 🔴 1、🟡 5、🔵 2 件。番人（A）と剥がし手（B）は、陰性対照まで含めて良い出来。
C の型とルートもおおむね ADR どおり。ただし 🔴1 の「作成時の結び」は凍結すべき継ぎ目が欠けていて、
Managed の人格ターンが本番で必ず断られる。🟡5 も凍結型への追加。
**この 2 件を直すまでマージは不可。** 🟡1〜4 は同じ PR で直すのが安いが、🟡2・🟡3 は後続レーンへの
明記でも可。
