# 68. セッションが直したファイルへ、ミラーから一手で届くようにする

- 状態: **P0〜P2 実装済み**（2026-08-17）。決定は [decisions/0049](../decisions/0049-session-changed-files.md)。
- 関連: [docs/44](44-markdown-code-editor.md)（File ペインの表示/編集モード） /
  [docs/55](55-fork-at-message.md)（転写の anchor） /
  [docs/59](59-session-sharing.md) §3（共有 DTO は座標を落とす） /
  [docs/29](29-keyboard-system.md)（コマンドパレット）

「このセッションは結局どのファイルを直したのか」に、Console はまだ答えられない。
本書はその一覧を**ミラーの中に**作る設計である。

---

## 68.1 問題——今その質問に答える道が無い

似た画面は 4 つあるが、どれも**セッション軸を持っていない**。

| 面 | 実体 | 軸 | 欠けているもの |
|---|---|---|---|
| ミラーのツール行 | `console/src/features/mirror/transcript/blocks.tsx` `ToolTrace`/`ToolRun` | ターン | **2 件以上は既定で畳まれる**。しかも**ミラーは開く手段を持っていない**（後述） |
| 左レール ファイル→変更 | `features/project/FilesChanges.tsx` | 作業コピー | 同じ作業コピーを触った他セッションの変更と混ざる |
| SCM ペイン 変更 | `features/scm/ChangesView.tsx` | リポジトリ | 同上 |
| コマンドパレット `changed` | `features/keys/CommandPalette.tsx` | 全 dirty repo | 同上 |

結果として、いま「さっき直したあのファイル」に辿り着く唯一の道は
**ミラーを遡る → 畳まれたツール行を開く**であり、その先にファイルを開くボタンも無い。

### 68.1.1 ⚠️ ミラーは `caps.openDiff` を渡していない（既存の穴）

`ToolTrace` は編集ツールを「差分ペインを開くボタン」として描くが、それは
`caps.openDiff` がある時だけで、無ければ**その場で展開するだけ**に縮退する
（`transcript/capabilities.ts` の設計意図では *ペインを持たない共有ビュー* 用の縮退）。

ところが `MirrorView.tsx` が組み立てる `caps` に `openDiff` が**無い**。
ミラーはペインを持っている側なのに、共有ビューと同じ縮退経路を走っている。
本件の一部としてここも塞ぐ。

---

## 68.2 材料はもう届いている——ただし一度も読まれていない

ワイヤ語彙 `transcript.Part` には最初から編集の座標がある
（`workspace/agent/internal/transcript/transcript.go`）。

```go
File  string `json:"file,omitempty"`   // kind=tool: edit/write target (openable as a diff)
Edits []Edit `json:"edits,omitempty"`  // before/after per edit
```

Console 側の `Part` にも `file?: string` は宣言されている。
**が、`console/src` 全体で一度も読まれていない**（`edits` だけが使われている）。
つまり本機能に必要な配管の半分は既に敷かれていて、繋がっていないだけである。

### 68.2.1 kind 別カバレッジ（実測）

| kind | `File` | `Edits` | 備考 |
|---|:--:|:--:|---|
| claude | ○ | ○ | `toolEdits()` が Edit/Write/MultiEdit を展開 |
| codex | ○ | ○ | `*** Update File:` などの patch ヘッダから |
| opencode | ○ | ○ | 同上（edit 系ツール） |
| cursor | ○ | ○ | P1 で対応。jsonl は tool 名の allowlist（`Write`/`Edit`/`MultiEdit`/`Delete`）、**ACP（managed）は `tool_call.kind`** |
| copilot | ○ | ○ | P1 で対応。`edit` / `create` / `write`（events.jsonl は全経路で書かれる） |
| kiro / agy | ✕ | ✕ | ツール名と短いラベルしか持たない |
| shell / ssm | — | — | 転写が無い |

kiro / agy / shell / ssm は載らない——**その場合は帯ごと出さない**（§68.7）。

#### cursor / copilot をどう載せたか（P1・実測 2026-08-17）

設計時は「`Info` に畳む前のパスを取り直すだけ・差分は出ない」と見積もっていたが、
**ディスクに残っていた実転写を読んだら before/after も来ていた**ので、+N −M まで出る。

| | 観測できた tool 名 | 編集の形 |
|---|---|---|
| cursor（jsonl） | `Shell` `Glob` `Grep` `Read` `Write` `WebSearch` `WebFetch` `GetMcpTools` `CallMcpTool` | `Write` = `{"path","contents"}` |
| copilot（events.jsonl） | `view` `bash` `edit` `grep` | `edit` = `{"path","old_str","new_str"}` |

⚠️ **判定は allowlist にする（「read 以外は編集」にしない）。** 名前が変わった版で
後者だと **Read / view しただけのファイルが「変更ファイル」に並ぶ**——一覧が黙って嘘を
つく側に倒れる。allowlist なら取りこぼしても「行が出ない」で済む。実測できた名前
（`Write` / `edit`）に、同じ語彙群の兄弟（`Edit`/`MultiEdit`/`Delete`、`create`/`write`）を
足してある。存在しなければ一致しないだけ。

⚠️ **cursor の managed（ACP）経路では tool 名を見ない。** ACP は `tool_call.kind`
（`read`/`edit`/`delete`/`move`/…）と `locations` で**プロトコル自身が分類している**ので、
そちらを使う（`title` は "Write /tmp/x" のような表示文字列で、そこから名前を復元するのは
まさに避けたい文字列契約）。before/after は入力の**形だけ**を見て取る。

---

## 68.3 一覧の意味——転写と git の**突合**にする

「セッションが直したファイル」には出所の異なる 2 つの答えがある。

| | ① 転写由来（`Part.File`） | ② git 由来（作業ツリーの status） |
|---|---|---|
| 意味 | **エージェントが編集した**という記録 | いまソースがどうなっているか |
| セッション帰属 | 正確 | 曖昧（同じ作業コピーを複数セッションが順に使う） |
| 時系列・ターン紐付け | できる | できない |
| 取り消された編集 | 履歴として残り続ける | 消える |
| シェル/フォーマッタ由来の変更 | 拾えない | 拾える |
| kind 依存 | あり（§68.2.1） | なし |

**①だけでは「もう revert したファイル」が居座り、②だけでは「セッションの仕事」という
軸そのものが消える。** どちらも単独では嘘になるので、**①を行の母集合とし、②を各行の
状態として重ねる**（＝突合）。

行が持つ状態語彙:

| 状態 | 意味 | 出し方 |
|---|---|---|
| 未ステージ / ステージ済 / 未追跡 | ②に一致がある | porcelain XY から。`FilesChanges.tsx` の `changeBadge()` と同じ語彙を使う |
| コミット済み | ①にあり②に無く、セッション開始以降のコミットに現れた | P2。`git log --since=<createdAt> --name-only` |
| 作業ツリーに差分なし | ①にあるが②にもコミットにも無い | ⚠️ **「取り消された」とは言わない**（§68.9.2） |
| 作業コピー外 | 転写のパスが `~/repos/<作業コピー>` の下でない | 行は出すが差分は開けない |

⚠️ **「差分なし」の行を消してはいけない。** 消すと「さっき直したのに一覧に居ない」に
なり、利用者から見れば機能が壊れている。灰色で残すのが正しい。

---

## 68.4 ⚠️ クライアントで集計してはいけない（tail 窓）

ミラーは転写を**全部は持っていない**。開いた時に末尾の窓だけを取り
（`WINDOW = 400`・`?since=0&tail=1&limit=…`）、上へスクロールすると `before=` で
遡って継ぎ足す。

したがって `turns` から集計すると、

- 長いセッションで**件数が足りない**、
- しかも**上へスクロールする度に件数が増える**（数えるほど増える一覧）

という、無言で間違うたぐいの壊れ方をする。

### 68.4.1 前例がある——ToDo と同じ形に乗せる

Agent 側には既に「**窓ではなく全転写**を走査し、`/messages` の同じレスポンスに
同梱する」派生値がある——ToDo である（`session_transcript.go` の
`claude.CollectTasks(lines)` → `resp["tasks"]`、汎用経路も `td.Tasks` で同じ）。

ここに `resp["files"]` を足す。得られるもの:

- **新エンドポイントもポーリングも増えない**（既存の転写ポーリングに相乗り）
- 窓に依存せず**セッション全体で完全**
- 停止済みセッションでも同じように出る（転写は残っている）

### 68.4.2 パスの正規化

転写のパスは kind ごとに座標系が違う（claude は絶対パス、codex は patch ヘッダ由来）。
`toBrowseRel(p, cwd, root)`（`session_transcript.go`）が既にあり、`SendUserFile` の
パスを browse-root 相対へ寄せるのに使われている。同じものを使う。

⚠️ **ただし join キーに browse 相対パスを使ってはいけない。**
`browseRoot()` は `AF_BROWSE_ROOT` で差し替えられるが、`fs/changes` の `path` は
`"repos/" + repo + "/" + rel` を**常に**返す。既定では一致するが、`AF_BROWSE_ROOT` が
home 以外を指すデプロイでは静かにズレる。
→ **突合は `(repo, rel)` で行う。** browse 相対パスは FileView に渡すためだけに持つ。

---

## 68.5 置き場——ミラーのヘッド直下の帯

ミラーの縦の並びは `ViewHead → ContextBar → TaskChecklist → 各種 attention → 本文`。
ここに 1 段挟む。

```
⬤ claude · agent-fleet@wip-soudsk5                    [ミラー] [⋯]
▓▓▓▓▓▓░░░░  128k / 200k
☑ ToDo 3/5 · ◌ 変更ファイル帯を実装中
✎ 変更ファイル 7   MirrorView.tsx ほか  +91 −5                      ▾
```

展開時:

```
✎ 変更ファイル 7                        [新しい順 ▾]  [SCM で開く ↗]
  ● MirrorView.tsx         console/src/features/mirror     +42 −3
  ● FileChangeStrip.tsx    console/src/features/mirror     +49 −0   未追跡
  ○ session_transcript.go  workspace/agent                 +31 −2   差分なし
  ⌫ legacy.css             console/src/styles                       削除
```

**雛形は `TaskChecklist`**（`transcript/blocks.tsx`）をそのまま踏襲する——
`key={session}` で親が再キーする・開閉を `localStorage` に per-session 保存する・
`DisclosureContent` で閉じても mount したまま `inert` にする。ToDo の隣に並ぶものが
ToDo と違う作法で畳まれると、それだけで別機能に見える。

### 68.5.1 行の作法

- **主表示はベース名、右にリポジトリ相対ディレクトリを薄く**（`FilesChanges.tsx` と同じ構図）。
  フルパスは `title`。
- **既定の並びは「最後に触った順」**。`パス順` に切替可。最頻の質問が「さっき直したやつ」だから。
- **既定クリック＝差分**、`Ctrl/⌘+Enter`＝別ペイン（パレットの慣習と揃える）。
  行メニューは `表示 / 編集で開く / SCM で表示 / パスをコピー`。
- ⚠️ **未追跡ファイルの差分は空**なので、その行はファイル自体を開く。
  `FilesChanges.tsx` が既に踏んでいる罠で、同じ扱いを踏襲する。
- `+N −M` は転写の `Edits` から `diffStat()`（`blocks.tsx`）で出す——差分ペインと
  **同じ行差分器**なので数字がズレない。`Edits` を持たない kind では出さない。

---

## 68.6 コマンドパレットに 4 つ目のモード

`command / changed / file` の隣に **`session`（このセッションの変更）** を足す
（`CommandPalette.tsx`）。`changed` モードの行と操作（Enter＝開く・`Ctrl/⌘+Enter`＝別ペイン）を
そのまま再利用し、母集合だけを差し替える。

- 対象は**アクティブなセッション**。無い（shell/ssm/未対応 kind）ときはモードを出さない。
- 帯は「見ながら気づく」面、パレットは「手を離さず飛ぶ」面。同じ一覧の 2 つの入口である。

---

## 68.7 縮退——能力が無いものは**出さない**

`transcript/capabilities.ts` の憲法は「a capability that is absent means the affordance is
NOT RENDERED」。本件もそれに従う。

- 共有セッションビュー（docs/59）には**帯を出さない**。共有 DTO は
  **差分の本体（old/new）は残すがパスを落とす**ので、開ける座標が存在しない。
  → `caps` に足すのは任意フィールドで、共有側は埋めない。
- `files` が空、または kind が `File` を出さない（§68.2.1）→ 帯そのものを描かない。
  「0 件」と書かれた空の帯は、対応していないのか本当に 0 件なのか区別できない。

---

## 68.8 仕様（採る形）

### 68.8.1 ワイヤ（`GET /sessions/{name}/messages` の `files`）

```jsonc
"files": [
  {
    "path":  "repos/agent-fleet@wip-soudsk5/console/src/features/mirror/MirrorView.tsx",
    "repo":  "agent-fleet@wip-soudsk5",   // 作業コピー名。空 = 作業コピー外
    "rel":   "console/src/features/mirror/MirrorView.tsx",
    "verb":  "edit",                       // edit | add | delete（転写から見た最後の操作）
    "added": 42, "removed": 3,             // Edits を持つ kind のみ。無ければ省略
    "count": 3,                            // このファイルを触った編集ツール呼び出しの数
    "lastIdx": 812,                        // 最後に触ったターンの transcript idx
    "lastTs": "2026-08-17T12:04:11+09:00",
    "sidechain": true                      // サブエージェントだけが触った場合
  }
]
```

- `(repo, rel)` が突合キー、`path` は FileView 用（§68.4.2）。
- 並び順はサーバが `lastTs` 降順で確定させる（クライアントで並べ替える時だけ触る）。

### 68.8.4 `transcript.Part.Verb`（追加した任意フィールド）

`verb` を**推定に頼らない**ために、パーサが知っている場合だけ申告できる欄を `Part` に足した。

⚠️ **「Edits が無い＝削除」と読んではいけない。** codex の delete だけが意図的に
before/after を持たないが、それは codex が `*** Delete File:` を読んでいるから言えることで、
**そもそも差分本体を運ばない kind**（cursor / copilot・§68.2.1）に同じ規則を当てると、
そのエージェントが触ったファイルが**全部「削除」になる**。よって:

- パーサが知っている（codex）→ `Verb` を明示。`update` は `edit` に正規化。
- 知らない（claude / opencode）→ `Edits` から導出（全ハンクが純挿入なら `add`、他は `edit`）。
- 何も無い → **`edit`**（安全側）。`delete` には決してしない。

同じ理由で、codex の rename（`*** Move to:`）は `Info` に `"<src> → <dst>"` を残したまま
`File` を**行き先のパス**にした。従来の `File` は矢印入りの文字列で、開ける座標ではなかった。

### 68.8.2 突合（Console 側）

既に横断の `GET /fs/changes` があり、`path` は `repos/<repo>/<rel>`・`repo` も持つ。
帯はこれを 1 回引いて `(repo, rel)` で join する。`useFilesStore` の `bump()` に
乗せておけば、ステージ/破棄の後に勝手に更新される。

### 68.8.3 fork とサブエージェント

- **fork したセッションは、引き継いだ履歴の編集も数える。** fork はコンテキストごと
  引き継ぐ＝そのセッションが立っている土台であり、レビュー対象としてはむしろ含まれて
  いる方が正しい。区切りが要ると分かったら `ForkAt` の anchor で切れる余地は残す。
- **サブエージェント（sidechain）の編集も数える。** 誰が触ったかではなく何が変わったかを
  見る面なので。ただし `sidechain` 印を持たせ、P1 で行に出す。

---

## 68.9 段階

| | 内容 |
|---|---|
| **P0** | Agent: `CollectFiles()` を claude / 汎用の両経路に足し `resp["files"]`。Console: 帯（`FileChangeStrip`）＋ `fs/changes` 突合＋ `caps.openDiff` 配線＋パレットの `session` モード。i18n は en/ja 両方 |
| **P1** | ✅ 済 — cursor / copilot の対応（§68.2.1・差分本体まで取れた）／ターン末尾のファイルチップ行（`turnFiles.ts`）。`sidechain` 印は P0 で入っていた |
| **P2** | ✅ 済 — 「差分なし」から**コミット済みだけを切り出す**（§68.9.2）／左レールのセッション行メニューからの導線 |

「帯だけ作って様子を見る」で止められる形にしてある。P1・P2 は P0 の形を変えない。

---

## 68.9.1 実装した形（P0・P1 / 2026-08-17）

| 層 | 置き場 |
|---|---|
| 語彙 | `internal/transcript/files.go` — `FileEdit` / `FileTouch` / `EditVerb` / `EditStat` / `FileEditsInTurn`、`Part.Verb` |
| claude | `internal/agents/claude/transcript.go` `CollectFileEdits(lines, from)` |
| codex | `internal/agents/codex/transcript.go` — `Verb` 明示＋rename の `File` を行き先へ |
| 集計 | `session_files.go` `sessionFileTouches(...)` ＋ 両ハンドラで `resp["files"]` |
| cursor | `internal/agents/cursor/transcript.go`（jsonl・名前の allowlist）/ `driver.go`＋`mirror.go`（ACP・`tool_call.kind` と `locations`） |
| copilot | `internal/agents/copilot/transcript.go`（`edit` / `create` / `write`） |
| コミット済み | `session_files.go` `handleSessionCommittedFiles` / `committedSince` ＋ `GET /sessions/{name}/committed` |
| Console | `features/mirror/sessionFiles.ts`（突合・並び・開き方）/ `FileChangeStrip.tsx`（帯）/ `transcript/turnFiles.ts`＋`TranscriptTurn.tsx`（ターン末尾のチップ）/ `MirrorView.tsx`（`caps.openDiff` と公開ストア）/ `CommandPalette.tsx`（4 つ目のモード）/ `sessions/SessionRow.tsx`（レールの行メニュー） |

**走査コストの扱い（§68.10-9 の答え）。** 全転写を毎ポーリング数え直すのは無理だった——
`+N −M` は行差分（LCS）であり、編集 1 件あたり最大 2 万文字ぶんの表になる。転写は
**追記のみ**なので、折り畳んだ位置から先だけを畳む形にした（`sessionFileTouches` の
`from`）。無効化は 3 条件——転写のパスが変わった／**先頭レコードの指紋が変わった**（同じ
長さのまま書き換えられた場合）／畳んだ数より短くなった。

⚠️ **store 由来の kind は最後の 1 ターンを畳んではいけない。** opencode は既存メッセージへ
part を足し続けるので（`genericMutableTail` と同じ理由）、そのターンを確定として畳むと
**ポーリングの度に同じ編集を数える**。可変な尾だけは毎回コピーに畳み直している。

**`+N −M` は 2 箇所で数えることになった。** Agent が数え、Console の差分ビューも数える。
食い違えば「帯の数字と、その行を開いた差分の中身が合わない」という、画面上はもっともらしい
壊れ方になるので、`EditStat` は `viewer/DiffView.tsx` の `lineDiff` と**同じ規則**（LCS ＋
同じサイズガード）にし、**同一の表**を両側のテストに置いた
（`internal/transcript/files_test.go` ↔ `features/viewer/lineDiffStat.dom.test.tsx`）。

---

## 68.9.2 ⚠️ P2 で「取り消された」を出すのをやめた

設計時は「差分なし」を **コミット済み / 取り消された**の 2 つに割ると書いていた。実装して
みると、割れるのは片側だけだと分かった。

- **コミット済みは肯定できる。** `git log --since=<セッション作成時刻> --name-only` に
  そのパスが現れれば、それは事実である。
- **「取り消された」は肯定できない。** 差分が無くコミットにも出てこない理由は他にもある——
  セッション開始**より前**のコミットに入っていた／別の作業コピーで起きた／その後
  改名された／`--max-count` の窓から溢れた。これらを一括で「取り消し」と表示すると、
  **UI が根拠のない断定をする**ことになる。

よって出すのは **「コミット済み」だけ**で、残りは P0 と同じ「差分なし」のままにした。
一覧全体の方針（肯定できることだけを言う）と同じ線である。

⚠️ **時刻が根拠なので、同じ作業コピーで並行していた別セッションのコミットも入る。**
実害が小さいのは、突き合わせる相手が**このセッションが編集したファイル**に限られている
から——「自分が触ったファイルが、その後コミットされた」は誰がコミットしたかに関わらず
正しい。

エンドポイントは `GET /sessions/{name}/committed`。git 作業コピーでない・時刻が読めない・
コマンドが失敗した、のいずれでも**空を返す**（バッジが出ないだけで、帯は出る）。

---

## 68.10 実機で確かめること

1. **長いセッション**（転写 1000 行超）で、上へスクロールしても件数が変わらないこと（§68.4 の回帰）。
2. **停止済みセッション**でも同じ一覧が出ること。
3. **worktree 違い**——同じ `rel` を持つファイルが 2 つの作業コピーにある時、開く先が正しいこと。
4. **subdir 起動**（`Meta.Subdir`）のセッションで相対パスの基準がズレないこと。
5. **未追跡ファイル**の行がファイルを開く（空の差分ペインにならない）こと。
6. codex / opencode で `+N −M` が差分ペインと一致すること。
7. 対応していない kind（kiro / agy / shell）で**帯が出ない**こと。
8. 共有セッションビューに帯が出ないこと。
9. ポーリング 1 周の増分コスト——`CollectTasks` ほかで既に全行を舐めているので、
   **新しい走査を足すのではなく同じパスに相乗りさせる**か、`(path, size, mtime)` でメモ化する。
   → §68.9.1 で追記前提の逐次畳み込みにした。実セッションでの体感は未計測。

### 68.10.1 いま確かめた範囲（2026-08-17・P0＋P1）

自前の headless Chromium で**実バンドル**を撮って確認した（`scripts/shots` のスタブ経由・
CP も Workspace Agent も無し）。見たのは **1・2・5・7・8 ではなく描画と導線**である:

- 帯が ja / en・dark / light で出て、行が「名前＋薄いディレクトリ＋増減＋状態バッジ」に
  なること。**バッジの列が揃うこと**——増減を持たない行（削除・差分本体を持たない kind）で
  `margin-left:auto` を載せた要素ごと消えて、バッジがファイル名に貼り付いていた。
  空でも `.mfl-stat` を描いて直した。
- コマンドパレットの 4 つ目のモードが**出る／選べる／5 行を新しい順で並べる**こと
  （Ctrl+P → Tab×3 を CDP で叩いて確認）。
- ターン末尾のファイルチップが、畳まれた「2 件のツール」行とフッターの間に出ること（P1）。
- cursor / copilot の編集の形は**ディスクに残っていた実転写**から取った（§68.2.1）——
  ただし読んだのは既存の記録で、**この変更を通した実セッションでは未確認**。
- 「コミット済み」バッジが出ること（P2）と、レールの行メニュー「変更ファイル」が
  ミラーを開いて帯を展開すること（CDP でメニューを開いて押して確認）。
  `committedSince` 自体は使い捨ての git リポジトリを作る単体テストで押さえてある
  （窓の外のコミットを拾わないこと込み）。

**まだ実機で見ていない**: 長い転写での件数不変（1）、停止済みセッション（2）、
worktree 違いの開き先（3）、subdir 起動（4）、未追跡行の遷移（5）、codex / opencode の
実転写（6）、未対応 kind と共有ビューでの非表示（7・8）。ここは単体テストで
押さえてあるだけで、実セッションでは未検証。

---

## 68.11 検討して採らなかったもの

- **mtime 走査で「触られたファイル」を出す** — ビルド成果物・`node_modules`・ログで汚染される。
  セッションの仕事を表さない。
- **新しい `PaneKind`（`sessionfiles`）を作る** — docs/65 が `.drawio` で出した結論と同じで、
  面を 1 つ増やす方が安い。常設タブが欲しいという声が出てから足す（P0 の形は変わらない）。
- **専用エンドポイント＋専用ポーリング** — 転写のポーリングが既にあり、ToDo が先例を作っている。
  2 本目のポーリングは同期ズレ（帯とツール行が別々の時刻を見る）を生む。
- **左レールのセッション行に件数バッジ** — レールは既に情報が多い。導線はメニューに置き、
  バッジは要望が出てから。
- **git だけで作る（転写を使わない）** — 実装は一番安いが、「セッションの仕事」という
  軸が消えて既存の `FilesChanges` の焼き直しになる。
