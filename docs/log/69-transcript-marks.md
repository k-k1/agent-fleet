# 69. 会話のこの一文に線を引く——転写のマーカーを共有セッションまで届ける

- 状態: **P0〜P2 実装済み**（2026-08-19、develop 未マージ）。決定は [decisions/0050](../decisions/0050-transcript-marks.ja.md)。
- 関連: [docs/59](59-session-sharing.md)（共有の権限・DTO・所有者 Workspace 停止時の振る舞い） /
  [docs/55](55-fork-at-message.md)（転写の `anchorId`） /
  [docs/68](68-session-changed-files.md)（ミラーのヘッド直下の帯という置き場） /
  [docs/28](28-i18n.md)（2 言語） /
  `console/src/features/mirror/transcript/capabilities.ts`（能力が無い＝操作要素を出さない）

長い会話の中の「ここ」を指す手段が、いま Console には無い。プランには
[コメント](../../console/src/features/mirror/planComments.ts)が付けられるが、それはプラン
本文（DocView）の中だけの話で、**会話そのもの**——ミラーの転写——には何も残せない。

本書は、転写の文字列を選んで色を付け、その印を**共有先にも同じ位置で見せる**設計である。

---

## 69.1 やりたいこと

1. ミラーで転写の文字を選択し、色を選んでマーカーを引く。
2. 引いたマーカーは**共有セッションの側でも同じ箇所に出る**。
3. **誰が引いたマーカーかが分かる**（所有者か、どの共有先か）。
4. 共有先（RW）も自分でマーカーを引ける。

---

## 69.2 材料はほぼ揃っている

新しく発明するのは「保存先」だけで、残りは既にある部品の組み合わせになる。

| 要る物 | 既存資産 | 状態 |
|---|---|---|
| 選択箇所を安定して指す | `console/src/features/viewer/quoteMarks.ts` — `selectionAnchor` / `applyQuoteMarks` / `clearQuoteMarks`。W3C Web Annotation の TextQuoteSelector 相当（**引用文字列 + 出現番号**）で、複数要素にまたがる選択も `Range.surroundContents` を避けてテキストノード単位で `<mark>` に包む | プランコメント（`features/viewer/DocView.tsx:125`）で実運用中。純粋関数部（`occurrenceOf` / `indexOfNth`）はテスト済み |
| ミラーと共有で同じ描画 | `features/mirror/transcript/` は**両者の共通資産**。差は `TranscriptCaps` だけ | 既存 |
| ターンの安定した同一性 | `Turn.anchorId`（claude=message uuid / codex=turn id / opencode=`msg_…`） | **既に共有 DTO の allowlist に入っている**（`control-plane/session_share.go:702`） |
| 所有者側 state を共有先へ届ける経路 | 引き継ぎ提案が同型の先例: Agent に実体（`~/.config/agent-fleet/session-handoffs/<name>.json`）→ CP が `GET /api/shared-sessions/{id}/handoff-proposals` で中継 → allowlist DTO（`sharedHandoffDTO`） | 既存。もう 1 本同じ形を足すだけ |
| ポーリングで印が消えないこと | `MarkdownView` の `innerHTML` 書き換えは `useEffect(…, [source, …])`（`MarkdownView.tsx:247`）。**`source` が変わらない再描画では本文 DOM を作り直さない** | 実測済み（DocView が同じ前提で動いている） |

---

## 69.3 アンカー——何に対して「ここ」と言うのか

### 69.3.1 `idx` は使えない

`transcript.Turn.Idx` には Agent 自身が理由を書いている
（`workspace/agent/internal/transcript/transcript.go:113`）。

> Idx cannot serve: it is a line/message ordinal that **moves under compaction**

compaction で行番号がずれた先に付いたマーカーは、利用者から見ると**もっともらしく間違って
いる**。`anchorId` を使う（fork が同じ理由で同じ選択をしている——[docs/55](55-fork-at-message.md)）。

### 69.3.2 ⚠️ グループ相対の part 番号も使えない（窓の境界でずれる）

`groupTurns()` は同じ role の連続ターンを 1 ブロックへ畳み、`last.parts.push(...parts)` で
**parts を連結する**（`transcript/model.ts:219`）。したがって「ブロックの N 番目の part」は
**そのブロックに何行畳み込まれたかに依存する**。

ミラーも共有ビューも転写を tail 窓（`WINDOW = 400`）でしか持たず、**両者の窓は一致しない**
（共有先はまだ上へ遡っていない、など）。窓の先頭がブロックの途中から始まれば、同じブロックの
part 番号が両側でずれる。ここを詰めずに実装すると「共有先だけマーカーが 1 つ隣にずれる」という、
再現条件の見えない壊れ方をする。

### 69.3.3 決め——アンカーは「元ターン由来の root キー ＋ 引用 ＋ 出現番号」

```
turn = <anchorId>            … anchorId があるとき
turn = h:<hash(元ターンの text)>  … 無いとき（フォールバック。FNV-1a — 要るのは
                                  「両側が同じ文字列から同じ値を出す」ことだけ）
mark = { id, turn, part, kind, quote, nth, color, author, created_at }
```

`part` は元ターン内での part 番号（ターン本文そのものは `-1`）。DOM とクライアントの
引き当てに使う 1 本のキーが `markRootKey(turn, part)` = `"<turn>#<part>"`（本文は `#b`）。

- `nth` は**転写全体でもブロック全体でもなく、その root ひとつの描画後テキストの中**で数える。
  root に対応するのはワイヤの part 1 つで、その本文は共有 DTO を**素通り**する（後述 69.4）ので、
  **所有者と共有先で同じ文字列を数えることが保証される**。
- 元ターン内の part 番号は DTO が parts を 1:1 で順序保存する（`sharedTranscriptDTO` は
  part を落とさずフィールドだけ削る）ので両側で一致する。
- 確定したターンの本文は不変なので、`quote + nth` は実質壊れない。壊れたときは
  `applyQuoteMarks` の設計どおり**付かないだけ**で、別の箇所へ誤って付くことはない。

### 69.3.4 root を描画層まで運ぶ

`groupTurns()` に `Group.origins?: string[]`（`parts[i]` の root キー）を足す。

- **`Part` に生やさない**。`partsOf()` は `t.parts` を**参照のまま返す**（`model.ts:182`）ので、
  part オブジェクトを書き換えると保持中のターン state を汚す。並行配列なら wire 型に触らず、
  part ごとの新規オブジェクト確保も増えない。
- `foldParts()` は `{p, i}` の `i`（group.parts への添字）を保っているので、`origins[i]` で引ける。
- 各散文要素は `data-mark-root={origins[i]}` を持って描画される。マーカーの適用・選択の採取は
  **この属性を持つ要素を root として**行う。

### 69.3.5 ⚠️ 数える土台は「正規化テキスト」——選択の文字列と textContent は別物

2026-09-04 の不具合（「選択してもピッカーが出ないことがある」）の真因と、その決め。

採取側の `Selection.toString()` が返すのは**描画テキスト**で、復元側の `textContent` の連結は
**生テキスト**。この 2 つは同じにならないので、素の `indexOf` は境界をまたぐ選択で必ず外れる。
`selectionAnchor` は `null` を返し、`MarkLayer` は**何も言わずにピルを出さない**——利用者からは
「出るときと出ないときがある」に見える（Chromium 実測）。

| 選択 | `Selection.toString()` | 生テキスト |
|---|---|---|
| 段落内のソース改行をまたぐ（`breaks:false` の本文） | `"リソース␣一覧"`（空白 1 個） | `"リソース\n一覧"` |
| `<br>` をまたぐ（`breaks:true` のプロンプト本文） | `"one\ntwo"` | `"onetwo"`（`<br>` は textContent に出ない） |
| 段落をまたぐ | `"…。\n\nsecond"` | HTML の生の改行だけ |
| 箇条書きの項目をまたぐ | `"first\nsecond"` | 区切り無し |

エージェントの本文は 100 字ほどで折り返されるので、**少し長めに選べばほぼ必ず改行をまたぐ**。
1 テキストノード内・`` `code` `` チップ→本文・要素境界始まりは通っていたため、「たまに出ない」
という顔をしていた。

**決め**——採取側も復元側も、**空白の連なりを 1 個の空白へ畳んだテキストの上で数える**
（`quoteMarks.ts` の `normalizeQuote` / `flattenRoot`）。

- `flattenRoot` は正規化テキストと同時に「正規化 → 生」の対応表（`rawAt`）を作る。塗るときは
  そこで生の位置へ戻すので、`<mark>` の切り出しは今までどおりテキストノード単位。
- ブロック要素と `<br>` の境界には、生テキストに無くても区切り（空白）を 1 つ入れる。
  ブロックの隙間の空白ノードは塗らない（`<ul>` の直下に `<mark>` を作らないため）。
- **保存済みの `quote` も塗る直前に畳む**ので、改行入りで保存された古い印もそのまま引き当たる。
- 吸収するのは**空白の形だけ**なので、「一致しなければ付かないだけ・別の箇所へ誤って付かない」
  （69.3.3）は変わらない。
- ⚠️ **jsdom ではこの差を再現できない**（jsdom の `Selection.toString()` は生テキストを返す）。
  そのため採取の入口を `anchorForRange(root, range, selected)` に割り、実ブラウザの選択文字列を
  手で渡してユニットテストで固定した。往復（採取 → 塗り）の確証は headless Chromium で取る。

同じ `selectionAnchor` は `DocView` のプランコメント引用も使うので、この修正は**そちらの
「引用ピルが出ない」も同時に直している**。

---

## 69.4 ⚠️ 塗れる場所を限る——引用文字列そのものが共有先へ渡る

共有 DTO は `cwd` / `file` / `files` / `filePath` / 編集の座標を**意図的に落としている**
（[docs/59](59-session-sharing.md) §3、`sharedTranscriptDTO` の allowlist）。

マーカーの `quote` は位置を復元するために JSON でそのまま共有先へ渡る。つまり
**所有者がファイルチップや diff 行を塗ると、allowlist が落としたはずの座標が `quote` として
復活する**。表示されなくてもネットワークには乗る。

対策は「送るときに検査する」ではなく「**構造的に allowlist 内の文字しか引用できないようにする**」。

- マーカーを引ける root は、**共有 DTO を素通りする本文フィールドを描画した要素だけ**:
  ターン本文 `text`、`kind=text` / `plan` / `answer` / `output` / `prompt` の part。
- 引けない: ファイルチップ（`mirror-turn-files`）、`ToolTrace` のパス、`ContextLine`（branch・cwd）、
  差分行、添付ファイル名、変更ファイル帯。**選択ピル自体を出さない**（能力が無い＝出さない）。
- **同じ表を 3 か所が持つ。** 塗る場所を絞るのは Console（`MARKABLE_KINDS` — `data-mark-root`
  を出す要素と、選択ピルを出す条件）だが、`kind` は**保存時に Agent が**（`markProseKinds`）、
  **中継時に CP が**（`sharedMarksDTO` と POST の入口）もう一度検査する。片側が緩んだだけでは
  漏れないようにするため。3 つの表は同じ内容でなければならない。

これで「共有先へ渡る `quote` は、共有先が既に読める本文の一部」が構造的に保証される。

> 当初は「CP が中継時に、その `anchorId` が共有窓の転写に無いマーカーを落とす」網も考えたが、
> それには marks を中継するたびに転写も取り直すことになり、**共有先 1 人あたりの所有者
> Workspace への往復を面ごとに増やさない**という §69.5.2 の方針と正面から衝突する。
> 実装したのは上の `kind` 検査で、往復を増やさずに同じ「座標を運ばせない」を担保する。

---

## 69.5 保存と配送

### 69.5.1 実体は Agent（所有者の Workspace）

引き継ぎ提案と同じ置き方にする。CP DB に会話由来の本文を複製しない
（[docs/59](59-session-sharing.md) §3 の「catalog は本文を複製しない」に揃える）。

```
~/.config/agent-fleet/session-marks/<session>.json   (0600, 親 0700)

GET    /sessions/{name}/marks          → { marks: [...] }
POST   /sessions/{name}/marks          → 追加（id は呼び出し側が採番）
DELETE /sessions/{name}/marks?id=mk_…  → 削除
```

⚠️ **CP の `routes.go` にも同じパスを登録する**。Agent に足しただけでは所有者の Console からも
届かない（`control-plane/routes.go:293-295` の handoff-proposal と同じ 3 行が要る）。

### 69.5.2 共有先への配送

`GET /api/shared-sessions/{id}/marks` を新設し、`handoffProposals` を雛形にする
（`session_share.go:639`）——認可 → **転写と同じレート制限バケツ**（共有先 1 人あたりの
所有者 Workspace への往復を面ごとに増やさない）→ 所有者 Workspace が `running` か →
`ownerGET` → `sharedMarksDTO`。

共有先の書き込みは `POST` / `DELETE /api/shared-sessions/{id}/marks`（RW のみ、69.6）。

**新しいポーリングは増やさない**。ミラーは転写のポーリングに、共有ビューは既にある
handoff-proposals のポーリングに相乗りさせ、実際の往復は `useMarksController` 側で
15 秒に間引く（印は「もう一方の誰か」が引いたものが遅れて見えるだけの補助情報で、転写と
同じ毎秒で取り直す価値が無い）。

### 69.5.3 所有者 Workspace が停止中

転写と同じく取得できない（`owner_workspace_stopped` / 409）。**閲覧のためにWorkspace を
自動起動しない**（[docs/59](59-session-sharing.md) §3）。マーカーだけ別扱いにする理由は無い。

---

## 69.6 誰が引けるか——RW 共有先も引ける。ただし承認フローには載せない

[docs/59](59-session-sharing.md) §2 の RW は「操作を**提案**でき、所有者の承認後にだけ
Agent へ送る」。この承認が要る理由は、提案が**エージェントを動かす**（他人のセッションと
トークンを消費する副作用がある）からである。

マーカーはエージェントに届かない。転写にも入らない。**注釈の層**であって操作ではない。
1 本線を引くたびに所有者の承認待ちに積むのは、承認の意味を薄めるだけで誰の役にも立たない。

- **RW の共有先は承認なしで直接書ける。** ただし書けるのは**自分のマーカーだけ**
  （追加と、自分が付けたものの削除）。
- **所有者は誰のマーカーでも消せる**（自分の Workspace に置かれている以上、最終権限は所有者）。
- **RO は書けない。** `POST`/`DELETE` は 403。
- 冪等: `id` は呼び出し側が採番（`mk_` + 8 バイト乱数 hex）し、Agent 側は **create-only**。
  再送は保存済みと同じ id なので no-op になり、`X-Agent-Fleet-Operation-ID` の台帳を
  持ち出す必要が無い（あの台帳は「二重実行すると困る副作用」のためのもの）。
- ACL は毎回評価する。RO へ降格した相手はその時点から書けない。既に書いたマーカーは
  残る（消すのは所有者の判断）。

---

## 69.7 誰が引いたか分かるようにする

**色と作成者を同じ軸に載せない。** 色は「利用者が意味づけに選ぶもの」（重要／要確認／後で読む）で、
作成者は「事実」である。1 つの軸に押し込むと、共有先が 3 人いる会話で色が意味を失う。

| 軸 | 表現 |
|---|---|
| 色（利用者が選ぶ） | `<mark>` の背景。4 色（黄・緑・青・桃）。ライト／ダーク両方の値を `styles/tokens.css` に置く |
| 作成者 | `<mark>` の**下線**（作成者ごとに割り当てた色。`border-bottom` ではなく `box-shadow: inset` で引く——枠線だと行送りが変わって本文が揺れる）＋ 印をクリックすると出るカード（作成者・時刻・引用・消す） |
| 一覧 | ミラーのヘッド直下、変更ファイル帯（`FileChangeStrip`）と同じ置き方の折りたたみ帯（`MarkStrip`）。「色・引用・作成者」を並べ、クリックでその位置へスクロール |

所有者（スロット 0）には下線を引かない。1 人で使っているセッションで全部の印に線が入ると
うるさいだけで、誰が引いたかが問題になるのは共有して 2 人目が現れた時だから。

「誰が」の主経路は一覧帯である。`<mark>` の下線は会話を読みながらの識別用で、名前そのものは
出さない（本文の可読性を壊すため）。

- 表示名は所有者側と同じ規約に従う: 所有者は所有者の login id、共有先は共有先の login id
  （[docs/59](59-session-sharing.md) §3 で user ターンの名前に login id を出しているのと同じ扱い）。
- ⚠️ **これは新しい露出である**: 共有先 A が共有先 B の login id を知ることになる。所有者は
  個別に共有するので、A は B の存在を知らないことがある。伏せると「誰が引いたか分からない」＝
  要件を満たさないので**出す**が、共有作成モーダル（`ShareCreateModal`）に
  「共有先どうしがマーカーの作成者名を見ます」の一行を出して、伏せずに伝える。

---

## 69.8 描画層への配線

`TranscriptCaps.marks` が無ければ印は描かれず、選択ピルも出ない（`readOnly` フラグにしない——
「出すが押せない」を招く。`capabilities.ts` の規約）。

ただし「読めるが引けない」（RO の共有先）は**能力の有無では割れない**——印の表示は読み手にも
要るからである。そこは wiring の中身が持つ:

```ts
interface TranscriptMarksWiring {
  byRoot: Map<string, TranscriptMark[]>; // root キー → その root の印
  all: TranscriptMark[];                 // 一覧帯（新しい順）
  canEdit: boolean;                      // false → 選択ピルを出さない
  add(m: NewMark): void;
  remove(id: string): void;
  canRemove(m: TranscriptMark): boolean; // 所有者は誰の印でも／共有先は自分のだけ
  authorLabel(author?: string): string;  // "" = 所有者、自分は「あなた」
  authorSlot(author?: string): number;   // 下線の色（0 = 所有者、以降は login id の昇順）
  find(id: string): TranscriptMark | undefined;
}
```

作るのは `useMarksController`（`transcript/useMarks.ts`）で、ミラーと共有ビューは
エンドポイントと「自分は誰か」だけを変えて同じものを呼ぶ。アンカーの決め方は
`transcript/marks.ts`（React も I/O も無い純粋なモジュール——`model.ts` から呼ぶため）。

被せる順序は DocView と同じ——`MarkdownView` が `innerHTML` を描いた**あと**（子の effect が
先に走る）。⚠️ ただし転写はポーリングのたびに再描画されるので、**毎回塗り直してはいけない**
（400 ターン × 毎秒の DOM 書き換えになる）。逆に「印が変わったときだけ」にすると、
`MarkdownView` が本文を作り直した回（テーマ変更など）に印が消えたまま戻らない。そこで
`paintTurnMarks` は「印の内容・本文の長さ・いま実際に載っている印の数」の 3 つで判断する。

選択の採取はプランコメントと同じ作法: `selectionchange` をデバウンス購読する（タッチの長押し
選択は `mouseup` を出さないので、そちらだけでは拾えない）。ピルとカードはターンごとではなく
転写ぜんぶで 1 つの浮遊レイヤー（`MarkLayer`）——ターンの数だけ購読を張ると 400 ターンぶんの
listener になる。

---

## 69.9 上限・掃除・失敗

| | 値 | 理由 |
|---|---|---|
| `quote` | 300 文字で切る | `planComments.MAX_QUOTE` に揃える。アンカーとしてはこれで十分で、一覧も潰れない |
| 1 セッション | 200 件 | |
| 作成者 1 人あたり | 100 件 | 1 人が帯を埋め尽くさない |
| ファイル | 256 KiB | 暴走ループの歯止め（handoff の `handoffProposalMaxBytes` と同じ発想） |

- ⚠️ **セッション削除／スロット再利用（`DELETE /sessions/{name}?reclaim=1`）でマーカーファイルも
  消す。** セッション名はスロット名で再利用されるので、消し忘れると**新しいセッションに前の
  セッションのマーカーが出る**。引き継ぎ提案（`session-handoffs/`）が同じ構造で、実装時に
  確認したところ**消していなかった**ので併せて塞いだ（`removeSessionSideFiles`）。どちらも
  cleanup アーカイブには入れない——消される会話についての注釈で、復元しても要らない。
- 保存に失敗しても会話は壊さない。マーカーは補助機能なので、黙って付かない方に倒す。

---

## 69.10 段階

| | 内容 | |
|---|---|---|
| **P0** | 所有者がミラーで引く／消す。Agent のストア＋REST、CP の `rest` 登録、`Group.origins`、`data-mark-root`、`<mark>` の描画、4 色、2 言語 | ✅ |
| **P1** | 共有先へ配送（`GET /api/shared-sessions/{id}/marks` ＋ DTO）。共有ビューは読める | ✅ |
| **P2** | RW 共有先が引く（`POST`/`DELETE`・CP が author を刻む）、作成者の下線とカード、一覧帯、`ShareCreateModal` の注記 | ✅ |
| **P3**（任意） | 一覧からの「マーカー箇所だけ抜き出してコピー」、キーボード導線（[docs/29](29-keyboard-system.md)） | — |

---

## 69.11 実機で確かめること（未実施）

実装は入っているが、下記は自動テストでは押さえられない。develop へ入れる前に実機で見る。

1. **スマホの長押し選択と横スワイプのセッション切替が競合しないか**
   （`mobile-swipe-session-rotate` の系。転写全域に選択操作を足すのは初めて）。
2. **ストリーミング中のターンの挙動。** いまは `pending`/`queued` にだけ印を置かせない
   （`turnKey` が空を返す）。伸びている最中の assistant ターンには置けてしまうので、
   本文が伸びたときに出現番号がずれないかを実機で見る。ずれるなら「確定したターンだけ」へ
   絞る（`working` 中の最終ブロックの `data-mark-root` を落とす）。
3. **`anchorId` の実カバレッジ。** kind ごとに空になる行がどれだけあるか。多ければ
   69.3.3 のハッシュ・フォールバックが主経路になるので、衝突（同一本文の繰り返し）の挙動を
   先に決める。
4. **ライト／ダークのコントラスト**を CDP で実測する（`light-mode-contrast-audit` の型）。
   4 色 × 下線 × 選択中の重なりは、目視で「読める」と言える範囲を簡単に外れる。
5. **共有先の実機確認。** CP のテストは stub Agent 相手なので、実際の 2 アカウントで
   「所有者が引く → 共有先に出る」「RW が引く → 所有者に出る」「他人の印は消せない」を通す。
