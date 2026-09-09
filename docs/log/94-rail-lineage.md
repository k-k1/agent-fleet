# 94. 左ペインに系譜を出す — worktree の入れ子とセッション families の縦線

Console だけの変更。バックエンドには触っていない。必要な値
（`dir` / `createdAt` / `originSession` / `repo.parent` / `repo.worktree`）は
すべて既に wire に来ている。

関連: [0073-session-spawned-sessions](../decisions/0073-session-spawned-sessions.ja.md)
決定 1 の 2026-09-10 補遺（引き継ぎ提案が `origin_session` を継ぐようになった。
**この表示のために入れた変更**） / [87-session-spawn.md](87-session-spawn.md) §87.18 /
[89-child-session-listing.md](89-child-session-listing.md) /
[52-working-sets.md](52-working-sets.md)（別軸。今回は触っていない）

## 94.1 何が見えなかったか

左ペインは作業コピーを「base clone + その worktree」の 2 段で並べていた。
どの worktree がどのセッションの子なのかは**どこにも出ていない**。
`create_session` と引き継ぎ提案でセッションは連鎖するのに、rail はそれを
平らな slug の山として見せていた。

## 94.2 前提（調査済み・蒸し返さない）

**① worktree のメタデータストアは存在しないし、作れない。**
`gitx.Repo`（`workspace/agent/internal/gitx/git.go:73-88`）はファイルシステムの
走査そのもので、`Worktree` は `IsLinkedWorktree`、`CreatedAt` は `.git` gitfile の
mtime である。ストアを作っても手で `git worktree add` したものは空のままで、
穴の空いた表示になる。**導出でやる。**

**② フォルダ名とブランチ名は手がかりにならない。**
`slug := randSlug()`（`sessionx/session_handlers.go:762`）で、`wip-<slug>` フォルダと
`temp/<slug>` ブランチの slug は乱数であり、セッション名とは無関係。
（実例: worktree `agent-fleet@wip-sgfr3cf` / ブランチ `temp/sgfr3cf` / そこで動いて
いるセッションは `scqwtat`。）しかも**ブランチは後から改名され、フォルダは据え置かれる**。
**必ず `session.dir` を経由する。**

## 94.3 採った導出（`console/src/lib/project.ts`・純関数）

**worktree の持ち主** = その worktree を `dir` に持つセッションのうち `createdAt` が
最も古いもの（`worktreeOwners`）。worktree が作られるのは起動によってなので、その
フォルダを `dir` に持つ最初のセッションが必ず作った側である。後から同じフォルダで
立ったものは同僚であって持ち主ではない。同値・欠落は name で決定的に倒す
（`createdAt` の無いセッションは「古い」とは名乗れない、を明示的に書いた）。

**worktree W の親作業コピー** = 持ち主 S の `originSession` = P が動いている作業コピー V
（`worktreeParentFolder`）。V が解決できないときは W を group の root（base 直下）に
置く。既存の orphan worktree と同じ扱いである。

| 解決できない場合 | 常態である理由 |
|---|---|
| S が居ない | 持ち主が削除・アーカイブされた |
| S に `originSession` が無い | 誰も起こしていない。それ自身が系譜の根 |
| P が居ない | 親が削除・アーカイブされた |
| P が W 自身で動いている | 親が「子の作ったコピー」で resume された。自己入れ子は作らない |
| P が別リポジトリ（= この group に無い）で動いている | `create_session` は起動先リポジトリを選べる。**入れ子では原理的に表せない**。縦線の出番。同じ行が「P がどの作業コピーにも居ない（home の shell）」も拾う——`""` はどの group のメンバーでもない |

⚠️ **visited セットを 2 か所に置いた。** 系譜は本来 DAG だが、meta が壊れていれば
循環しうる。レンダラが固まるのは最悪の壊れ方である。

- `sessionLineages` の系譜の遡り: 閉じた地点を根として打ち切る。
- `breakLineageCycles`: `parentOf` の各辺を root まで歩き、辿り着けない辺を root に切る。
  1 本切れば循環は解けるので後続の走査は正常終了する。

**並びは各段とも `createdAt` 昇順のまま。**既存の理由（フォルダ名は `temp/<slug>` で
名前順は実質ランダム、時系列なら既存が動かず新しいものが末尾に付く）を、段が増えた
あとも各段に対して維持している。

## 94.4 縦線の色をどこから取ったか

**祖先セッションの `s.color` は採らなかった。**`Color` は
`session_handlers.go:222` のコメントどおり「terminal background hue (hex);
**SSM host color, else empty**」で、実質 SSM のホストブックマークにしか入らない。
借りると**ほぼ全部の family が無色**になる。

採ったのは ADR 0073 の決定に合わせた**彩度を落とした中立ランプ** `--lineage-0..5`
（`tokens.css`、明暗それぞれ 1 段）。3 本目のセッションパレットではない——kind 色は
「どのエージェントか」で既に使い切っており、`s.color` は上記のとおり使えないため、
この 2 軸のどちらとも競合しない位置に置いた。

- スロットは**根セッション名のハッシュ**から取る。一覧中の位置ではないので、無関係な
  セッションが増減しても family の色は変わらない。6 スロットなので衝突しうるが、
  一次の手がかりは入れ子のほうなので、衝突は「今日の rail」に縮退するだけで嘘は言わない。
- **family が 1 人のときは色を付けない**（`lineageColor` が `""` を返す）。全行に線を
  引けばただのノイズで、色が意味を持たなくなる。画面に色があること自体が
  「これには縁者が居る」を意味する。
- 🔥 **背景の塗りは採らなかった。** hover・選択・アクティブ状態、そして作業セットの
  減光（`useActiveWorkingSet` / `sessionInSet`）と競合する。**左の 2px の縦線**なら競合
  しない。選択行だけは既存の `inset 2px` アクセントバーと重なるので、縦線を 3px 内側に
  ずらして両方を出している。

**コントラスト（実測）**: dark（`--panel` #181818）6.1–7.1:1、light（#f4f5f7）4.6–5.4:1。

## 94.5 深さの上限

引き継ぎ連鎖は原理的に無制限に伸びる。狭いレールなので**インデントは 3 段で頭打ち**
（`RepoNode` の `MAX_INDENT_DEPTH`）にし、それ以上は縦線だけで表す。
**sticky も 1 段目の worktree だけに残した。**フィルタバーの下に用意されている段は
2 段しかなく、3 段目・4 段目が同じ位置に重なるとレールを食い潰す。共有ツリー
（`sharing.css`、1 段だけ）を巻き込まないよう `.proj-children .proj-children` で選択している。

## 94.6 当てた場所

| ファイル | 何を |
|---|---|
| `console/src/types/session.ts` | `originSession` を追加（サーバは `session.Session.OriginSession` で既に出していた） |
| `console/src/lib/project.ts` | `worktreeOwners` / `worktreeParentFolder` / `sessionLineages` / `lineageColor` / `repoTree` / `filterRepoTree` / `countRepoNodes`。すべて純関数 |
| `console/src/features/project/ProjectTree.tsx` | `groupedRepos` のフラット配列 → `repoTree` の森。フィルタと作業セットの絞りも木の上で行う |
| `console/src/features/project/RepoNode.tsx` | `childRepos={members.slice(1)}` の 1 段渡しをやめ、`RepoTreeNode` を**再帰**。畳んだときのバッジ集計と reveal も部分木全体へ |
| `console/src/features/sessions/SessionRow.tsx` | family の縦線。行は自分の色だけを select するので、無関係なセッションの更新では再描画しない |
| `tokens.css` / `project.css` / `sessions.css` | ランプと 2 本の縦線 |

**i18n の追加は無し**（新しい文字列を足していない）。色だけの手がかりに文字の等価物を
付けるかは、入れ子そのものが関係を語るので今回は見送った。

## 94.7 試験と陽性対照

新規 26 件（`project.test.ts` 22 / `ProjectTreeLineage.dom.test.tsx` 5、
うち 1 は既存 describe への追加）。

**なぜ `worktreeParentFolder` を切り出したか。** 最初は `repoTree` の中に条件式として
書いていて、陽性対照が **2 件 exit 0（＝壊しても試験が緑）** で返ってきた。自己入れ子と
別リポジトリの 2 つのガードを消しても、下流の `breakLineageCycles` の循環枝・
ぶら下がり枝が同じ答え（root 直下）に落としてしまうためである。**同じ答えを別の理由で
出す試験は何も証明していない。**判断を純関数へ切り出し、分岐ごとに直接の試験を当てた。
`!copy`（親がどの作業コピーにも居ない）のガードは、切り出したあとも
`!group.has(copy)` と区別が付かないと分かったので**行ごと消した**——1 行が 2 つの場合を
担う形にして、残った行を全部 load-bearing にしてある。

陽性対照——新しい試験が、対応するコードを壊したときに落ちること:

| 壊した箇所 | 壊し方 | 落ちた試験 | exit |
|---|---|---|---|
| `worktreeOwners` の「最古」 | `if (!cur \|\| olderSession(s, cur))` を外して常に上書き | `worktreeOwners` / `repoTree` / dom | 1 |
| 同・同値の決着 | `compareText(a.name, b.name) < 0` → `false` | 「breaks a createdAt tie by name」 | 1 |
| `sessionLineages` の visited | `if (seen.has(cur.name)) break;` を削る | 「terminates on a cycle」他 1 件。18 秒回って `RangeError: Invalid array length`（`path` が 2^32 に到達） | 1 |
| `breakLineageCycles` | 呼び出しを削る | 「breaks a cycle instead of recursing forever」 | 1 |
| 持ち主不在のガード | `if (!owner) return rootFolder` → `return ""` | `worktreeParentFolder` | 1 |
| `originSession` 無しのガード | 同上 | `worktreeParentFolder` | 1 |
| `originSession` を読むこと自体 | `ix.byName.get(owner.originSession)` → `get("never")` | 入れ子の試験 + dom | 1 |
| 自己入れ子のガード | `if (copy === folder) return rootFolder` を削る | 「refuses to nest a worktree under ITSELF」 | 1 |
| 別リポジトリ／作業コピー外のガード | `if (!group.has(copy)) return rootFolder` を削る | 上記 2 件 + dom | 1 |
| family 1 人は無色 | `lin.size < 2` を落とす | `lineageColor` + dom（「loner」） | 1 |
| インデント上限 | `depth >= MAX_INDENT_DEPTH ? " flat" : ""` → `""` | dom「stops indenting past three levels」 | 1 |
| ノードの縦線 | `style={... "--proj-lineage" ...}` を削る | dom「paints one family one colour」 | 1 |
| セッション行の縦線 | `(lineage ? " lineage" : "")` → `""` | dom「marks the session rows of a family」 | 1 |
| `RepoNode` の再帰 | `{n.children.length > 0 && (` → `{false && (` | dom 全般 | 1 |

🔥 **Console 側の欠落は Console 側の試験にしか映らない**（#469 / §87.18.6 と同じ）。
上表の「`originSession` を読むこと自体」を壊したまま
`(cd workspace/agent && go test ./internal/session/... ./internal/sessionx/... -count=1)`
を回すと **exit 0・両パッケージ `ok`** のままだった（実測）。今回は Go を 1 行も
触っていないので、この変更は Go の試験からは**原理的に見えない**。

全量: `cd console && npm test` → 2278 passed / 1 failed、落ちる 6 ファイルは
`src/features/viewer/` の `Denied ID`（親の `node_modules` を symlink で共有している
ことによる既知・無関係。AGENTS.md）。`npm run typecheck` exit 0 /
`npm run i18n:lint` exit 0 / `npm run css:dup` exit 0（`.lineage` が 2 ファイルに出るが、
`.proj-node.wt.lineage` と `.sess-row.lineage` で、素の `.lineage` はどこにも無い——
`.wt` / `.dead` / `.hover` と同じ既存の作法）。**exit code はパイプを通さずに取った。**

## 94.8 見た目の確認（headless chromium・実測）

`scripts/shots/server.mjs`（実バンドル + スタブ API）に、**コミットしない**一時 fixture
（2 世代の worktree 連鎖 + 別リポジトリに立った孫 + 別 family）を足して、
headless Chromium を自分で駆動して確認した。判定は目視ではなく
`getComputedStyle` の実測値である。fixture は確認後に戻してある
（README のスクリーンショットは変えていない）。

| 対象 | dark | light |
|---|---|---|
| family A（根 `sk4rq2f`）の縦線 | `rgb(196,137,164)` = `--lineage-4` | `rgb(143,81,117)` |
| 同・`payments-api@spec-sync`（**別リポジトリの孫**） | 同じ | 同じ |
| 同・`webshop@review-fixes`（depth 2 の入れ子） | 同じ | 同じ |
| family B（根 `sq3hn7v`）の縦線 | `rgb(169,143,190)` = `--lineage-3` | `rgb(113,90,140)` |
| family を持たない行（`返金 API の契約整理` 他 4 行） | `::before` 無し | 同 |
| 選択中の行の縦線 | `left: 3px`（アクセントバーと共存） | 同 |

- **(a) 畳んだとき**: 縦線はノード自身の行に乗るので、子孫が DOM から消えても
  family は読める。dom 試験
  「still shows the family on a node whose descendants are folded away」で固定した。
- **(b) 別リポジトリ**: 上表のとおり、`webshop` 配下と `payments-api` 配下に
  同じ色が出る。入れ子では原理的に表せない関係が色だけで残る。

## 94.9 やらなかったこと

- バックエンドの変更。`originSession` の意味・述語（`session.InUnattendedChain` は
  深さ判定の話で表示とは無関係）にも触っていない。
- worktree のメタデータストア（§94.2 ①）。
- 作業セット（52）の仕組み。別軸の機能なので入れ子と競合させていない。
- `filter.ts` の `sessionMatches` に親名を足すこと（任意とされていた）。
  この一帯のレビュー 2 巡目・3 巡目で出た追加欠陥はどちらも「ついでの変更」が原因だったので、
  範囲を広げていない。
