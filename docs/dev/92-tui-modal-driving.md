# 92 — TUI モーダル駆動の実測検証プレイブック（AUQ ほか）

正: コード（本書は検証手法と実測記録）/ 主な更新トリガ: claude CLI のバージョン更新・AUQ 駆動経路（`PendingQuestions` / `handleSessionInput`）の変更 / 最終確認: 2026-08（claude v2.1.232、§6 追加）

Console のチャットは、エージェント TUI のモーダル（claude の AskUserQuestion=AUQ、プラン承認、
許可プロンプト）を **tmux send-keys で代理操作**して回答する。この結合は claude CLI 側の
UI 変更で**予告なく黙って壊れる**（エラーにならず「違う選択肢が回答される」形で現れる）ため、
(1) 挙動が怪しいとき・(2) 駆動経路を修正するとき・(3) claude CLI を更新したとき に、
本書のプレイブックで**実機の TUI に対して再検証**する。

## 1. 事件記録: AUQ 誤発動（2026-07-09、claude v2.1.204）

- **症状**: 単一選択 AUQ でユーザーが選択肢3をクリックしたのに、選択肢1が「回答済み」になる。
  併せて、クリック時の楽観エコーが「反映待ち」のまま永久に残る。
- **根因**: 単一選択クリックは選択肢ラベルを `{prompt}`（リテラル打鍵＋Enter）で送る実装
  （`onSendOne` → `sendPrompt`）だが、v2.1.204 の AUQ モーダルは**オプション行上のタイプ文字を
  完全に無視する**（旧版の「タイプ文字はオプションフィルタ」挙動は消滅）。結果、ラベルは捨てられ、
  Enter がハイライト中のデフォルト＝選択肢1を確定する。**決定論的で、選択肢1以外のクリックは毎回誤発動**。
- **エコー残留**: AUQ 回答は transcript にユーザーターンを作らないため、「ユーザーターン出現で
  エコーを消す」reconcile が永久に走らない。キー駆動化（エコーを作らない）で同時に解消する。
- **恒久修正は 2026-07-09 に適用済み**（§4 参照）。

## 2. 実測済み挙動マトリクス（claude v2.1.204 AUQ モーダル）

| 入力 | 単一選択 | 複数選択 |
|------|---------|---------|
| オプション行でテキスト打鍵 | **完全に無視**（フィルタも移動もなし） | 同左 |
| Enter | ハイライト行を確定（単一質問なら即送信） | その場トグル（カーソル据え置き） |
| 数字キー 1〜9 | **その行を Enter 不要で即確定・送信** | その行を即トグル（送信はしない） |
| ↑/↓ | 行移動（正常） | 同左 |
| ←/→ | 質問タブ移動（Right で Submit ページへ） | 同左 |
| "Type something" 行で打鍵 | テキスト登録される（正常） | テキスト登録＋自動チェック（正常） |

- 数字キーは "Type something" や "Chat about this" 行も番号対象。**テキストがペースト結合されずに
  届くと、含まれる数字が任意の行を即確定し得る**（`{prompt}` を AUQ 表示中に送る経路すべての潜在リスク）。
- 検証済みで**現在も正しく動く**キー駆動シーケンス（Console が `{keys}`/`{seq}` で送るもの。キー間 90ms）:
  - 単一選択: `Down×i, Enter`（単一質問なら即送信、複数質問なら次タブへ自動前進）
  - 単一選択の自由入力: `Down×(選択肢数)` → テキスト → `Enter`
  - 複数選択: 各チェックを `Down×n, Enter`（トグル）→ `Right`（Submit ページ）→ `Enter`
  - 複数選択＋自由入力: トグル後 `Down` で Type 行へ → テキスト（自動チェック。**Enter を打つと
    トグルが外れるので打たない**）→ `Down`（Submit/Next 行へ）→ `Enter` → 最後に `Enter`
  - 複数質問: 各質問を上記で回答（自動前進）→ Review ページで `Enter`（"Submit answers"）
- **壊れているのは単一選択クリックの `{prompt}` ラベル送信だけ**。キー駆動経路は全パターン正常（2026-07-09 実測）。

## 3. 検証プレイブック

使い捨ての claude セッションを tmux に立て、Agent の `handleSessionInput` が送るのと同じ
キー/タイミングを手で再現し、TUI の反応を capture-pane で観察する。

**注意**: fleet 本番の tmux セッション（`claude_*`）には触らない。検証セッションは作業ディレクトリを
スクラッチ領域にして新規に立てる。AUQ 1回＝claude 1ターン分のコストがかかる。

```bash
# 1) 使い捨てセッションを起動（初回は trust プロンプトに Enter が要る）
tmux new-session -d -s auqtest -x 140 -y 50 \
  "claude 'AskUserQuestionツールを使って質問を1つだけしてください。…（下の雛形）…'"

# 2) モーダル表示を待って観察
tmux capture-pane -p -t auqtest | tail -30

# 3) Agent と同じ入力を再現（-l = リテラル打鍵。Agent はキー間 90ms、{prompt} の Enter 前は
#    claude なら 20ms = inputSubmitDelay / AGENT_INPUT_SUBMIT_DELAY_MS）
tmux send-keys -t auqtest -l 'テキスト'   # {prompt}/{seq} の t 相当
tmux send-keys -t auqtest Down            # {keys} 相当（Down/Up/Right/Enter/Space/Escape）
sleep 0.09

# 4) 何が回答されたかは pane の「User answered Claude's questions:」行で確認
tmux capture-pane -p -t auqtest -S -60 | grep -A3 "answered"

# 5) 後片付け
tmux kill-session -t auqtest
```

質問を出させるプロンプト雛形（1セッションで「次: …」と追い質問すると1起動で複数パターン回せる）:

- 単一選択: 「AskUserQuestionツールで質問を1つ。header=対応方針、question=…、選択肢は次の3つ
  （この順、multiSelect=false）: 1) … 2) … 3) …。回答が返ったらその内容をそのまま出力して。」
- 複数選択: 同上で `multiSelect=true`。
- 複数質問: 「質問を2つ含むAskUserQuestionを1回のツール呼び出しで。q1: …、q2: …。」
- ラベルは**日本語＋数字混じり**（例: まず340pxに上げて様子見）を必ず含める — テキスト無視と
  数字キー即確定の両リスクを一度に検出できる。

判定の要点:

- ラベル全文を打鍵して**モーダルが無反応**であること（反応してフィルタ等が復活していたら挙動が変わった合図）。
- `Down×i, Enter` で意図した行が回答されること。
- 複数選択で Enter が**送信でなくトグル**であること。
- Type 行でテキストが登録されること（複数選択では自動チェックが入り、Enter で外れること）。

## 4. 恒久修正（2026-07-09 適用済み）

1. **本命** ✅: Console `PendingQuestions` の単一選択クリックを、menu モード（codex/opencode）と
   同じ `onSubmitKeys([Down×i, Enter])` に統一（`onSendOne` プロップは廃止）。
   `{prompt}` によるラベル送信経路を AUQ 回答から排除した。
2. **随伴** ✅: 「タイプ文字はオプションフィルタ」前提の旧コメント群を §2 の実測に合わせて書き直し。
   楽観エコー残留（§1）はクリック経路の廃止で発生しなくなった。
3. **堅牢化** ✅: Agent の入力経路で、未決の対話（question / plan 承認 / permission）がある間は
   `{prompt}` を 409（エラーコード `question_pending` / `plan_pending` / `permission_pending`）で
   拒否（MCP ドライブ等、他の `{prompt}` 送信元も塞ぐ）。ゲート判定は `promptBlocker`
   （`session_io.go`。idle / working 以外を全てブロックする whitelist 方式——plan / permission でも
   Enter がハイライト行を無音確定する同型事故があるため。ユニットテストあり）。
4. **追補（2026-08-05）**: 単一選択カードの「クリック＝即確定送信」を廃止した。クリックは
   **選択だけ**で、送信は「回答を送信」ボタン（誤クリックが取り消せず、比較していた選択肢の
   preview も消えるため）。**送るキー列は変えていない** — 単一選択＋オプション回答は従来どおり
   `Down×i, Enter` を `onSubmitKeys` で送る（`buildClaudeSubmit`。レビューページ用の末尾 Enter を
   足す `buildClaudeSeq` にはこの形を載せない）。自由入力／複数選択／複数質問は従来経路のまま。
   menu モードも同様にボタン送信へ統一（`buildMenuSeq` の単一質問は元から同じキー列）。
5. **検証**: キー駆動シーケンス自体は §2/§3 のとおり実 TUI で全パターン検証済み。修正を再適用・
   変更した際は §3 のプレイブックで 単一選択（キー駆動）／自由入力／複数選択（＋自由入力）／
   複数質問 の4パターンを回し、「User answered」の内容が意図と一致することを確認する。
   デプロイ後に実 Console からの目視確認も1回行う（回答済みカードの ✔ 位置とエコー非残留）。

## 5. 回帰チェックリスト（claude CLI 更新時）

§3 の雛形で最低限これだけ回す: ①単一選択 `Down, Enter`、②ラベル全文打鍵→無反応の確認、
③複数選択 トグル→`Right`→`Enter`、④Type 行の自由入力。①〜④のどれかが変わっていたら
`PendingQuestions.submit` のシーケンス生成と本書 §2 を同時に更新する。

## 6. preview 付き選択肢は「Type something」行が消える（2026-08-14、claude v2.1.232 で実測・修正済み）

**症状**: preview 付きの AskUserQuestion で自由入力を送信すると、claude が
`User declined to answer questions` / 「The user wants to clarify these questions」
「(No answer provided)」を返す。質問数に関係なく **選択肢のどれかに `preview` が
1つでも付いていれば** 発生する（実フリートで3回、この調査中に4回、計7回すべて同一
症状で再現）。ユーザーは「回答したのに認識されない」と見える。

**根因**: claude の AskUserQuestion モーダルは、選択肢に `preview` が付くと
**「Type something.」行（自由入力欄）が消える**（代わりに選択肢ごとの
「n to add notes」機能に置き換わる）。

```
previewなし: 1.案A / 2.案B / 3.案C / 4.Type something. / 5.Chat about this （5行・番号あり）
previewあり: 1.案A / 2.案B / 3.案C                        + 番号なしの Chat about this （Type something が無い）
```

旧 `questionKeys.ts`（`buildClaudeSeq`）の自由入力シーケンスは「Type something 行は
選択肢の直後（`Down×選択肢数`）」という **previewなしレイアウトの前提のまま**だった。
preview があると同じ `Down×選択肢数` が「Chat about this」（番号なしの最終行）に着地し、
①打鍵したテキストはメニュー行なので黙って握り潰される → ②続く Enter が
「Chat about this」を確定 → ③ claude が上記の decline 定型文を返す、という順で壊れる。
実機で1ステップずつ確認して特定した（tmux capture-pane を都度取得）。

**回避策（この判定は「質問ごと」— フォーム全体ではない）**: claude は preview の有無を
**質問（タブ）ごと**に切り替える。3問中2問だけ preview が付いていても、3問目の
タブに着けば通常レイアウトへ戻る。

**修正後のキー列**（`hasPreview(opts)` で分岐）:
- 単一選択・自由入力・**previewあり**: `n`（現在ハイライト中の選択肢に notes を開く。
  各質問タブは常に選択肢0番から始まるので Down は不要）→ 自由入力テキスト → `Enter`
  （notes だけで提出され、選択肢は選ばれない。tool_result は
  `"質問"=(no option selected) notes: <テキスト>` という形になる — これが claude 側の
  自由入力の等価物）。
- 単一選択・自由入力・**previewなし**: 従来どおり `Down×選択肢数, テキスト, Enter`。
- 選択肢そのものを選ぶ経路（`Down×i, Enter`）は preview の有無に関係なく無傷
  （previewの有無で変わるのは Type something 行の有無だけ）。

**副次的に見つかった別バグ（同じ調査で修正）**: `buildClaudeSeq` は単一質問（レビュー
ページが存在しない — claude は 1 問だけの Enter で直接提出する）でも末尾に無条件で
`Enter` を1つ余分に送っていた。previewなしの通常自由入力でも同型（提出後の空の
コンポーザに着地するだけで実害は出ていなかったが、正しくない）。単一選択かつ非
multiSelect の1問フォームだけ、この末尾 Enter を送らないよう修正（多問フォーム・
単一 multiSelect フォームは Right 経由でレビュー/提出ページに乗るので従来どおり必要）。

**表示側の別バグ（同時修正）**: 上記の decline は claude 側の正常な拒否応答なので、
Console はこれを「回答済み」ではなく明確に区別する必要がある。修正前は
`CollectInteractionAnswers`/`CollectTurns` が `is_error` を見ておらず、
拒否の定型文をそのまま「回答」として Console に渡し、`QuestionBlock` も
`answered=true` の場合は常に「回答済み」バッジを出し、定型文をパースできず
ラベルにマッチしない生テキストをそのままカードに出していた（ユーザー報告の
スクリーンショットそのもの）。claude.`InteractionAnswer{Text, Declined}` を追加し、
`Declined` は「(No answer provided)」を含む is_error な tool_result のときだけ立てる
（`isDeclinedAnswer`）。Console 側は `Part.declined` を見て「却下」バッジ＋固定の
短い注記を出し、定型文のパースは一切行わない。

**検証**: 実機（claude 2.1.232、tmux 直叩き）で①バグの再現、②修正後のキー列
（`n`, テキスト, `Enter`）が実際に自由入力として通ることの両方を確認済み
（本ドキュメントの diff と同じセッションで実施）。ユニットテストは
`questionKeys.test.ts`（"preview options drop the free-text row" ブロック）、
`transcript_test.go`（`TestCollectInteractionAnswers_Declined`）、
`QuestionBlock.dom.test.tsx`。

## 7. その `n` は Agent に届かない — キー列の検証は「配送層」も含めて初めて終わる（2026-08-18、claude 2.1.234 で実測・修正済み）

**症状**: preview 付き AUQ で自由入力してから「回答を送信」を押しても **何も起きない**。
トーストも出ず、質問カードもそのまま。§6 の修正が入った後に出た報告。

**根因（配送層）**: §6 の検証は **tmux 直叩き**で行ったため、Console の実際の経路
（`POST /sessions/{name}/input {seq}`）を一度も通っていなかった。Agent 側はこの
`{k}` を **名前付きキーのホワイトリスト**で検査する:

```go
// workspace/agent/session_io.go
func allowedKey(k string) bool {
	switch k {
	case "Up", "Down", "Left", "Right", "Enter", "Space", "Escape", "Tab", "BTab", "BSpace", "Home", "End":
```

`n` はここに無い。しかも検証は「半端にモーダルを駆動しないよう」**送信前に全ステップ**
に対して行われるので、1ステップが未知だと **400 `bad_key` で要求ごと落ち、打鍵は1つも
届かない**。つまりモーダルの契約（§6）は正しいのに、配送層で丸ごと消えていた。

**なぜ無言だったか**: `api()` は非 2xx を throw せず `{error:{code}}` を**戻り値**で返す。
`MirrorView` の `sendKeys`/`sendSeq` は `try { await … } catch {}` の形だったため catch すら
通らず、返ってきたエラーを誰も見ていなかった。回答経路は「沈黙＝成功」と見分けが付かない
面なので、ここだけは必ず失敗を喋らせること（managed の `sendRespond` は元からトースト表示）。

**修正**: `n` を `{t}`（`send-keys -l`）で送る。印字可能文字はペイン上で同じ 1 バイトなので
挙動は同じで、Agent を触らないぶん**版ズレに強い**（Console だけ更新されて Workspace の
Agent が古いピンでも動く）。あわせて `sendKeys`/`sendSeq` を `driveInput` に統合し、
失敗をトースト＋楽観的な「進行中」の巻き戻しにした。層またぎの回帰テストを
`questionKeys.test.ts` に追加（Go の `allowedKey` をソースから読み、ビルダーが吐く全
`{k}` が含まれることを固定 — `memoryTab.test.ts` の CP 許可リスト検査と同じ型）。

**多問フォームの実測（§6 では未検証だった）**: 3問（うち2問 preview）で1ステップずつ確認。

| 手順 | 画面 |
| --- | --- |
| `n` | ハイライト中の選択肢の `Notes:` が入力欄になる（フッタに `ctrl+g to edit in Vim` が増える） |
| テキスト | `Notes: <テキスト>` |
| `Enter` | **その質問が ☒ になり、次の質問タブへ自動で進む**（カーソルは選択肢0番） |
| 最後の質問の `Enter` | レビューページ（カーソルは "Submit answers"） |

→ `buildClaudeSeq` の前提（notes の Enter で次タブへ進む・多問は末尾 Enter でレビュー確定）
は多問でも成立。preview の有無が**質問ごと**に切り替わることも同時に再確認した
（preview 無しの3問目だけ `3. Type something.` / `4. Chat about this` の番号付きレイアウトに戻る）。
90ms 間隔（Agent の `{seq}` と同じペース）でも通ることを確認済み。

**表示側の別バグ（同時修正）**: notes 自由入力の tool_result は **値が引用符で囲まれない**唯一の形:

```
The user answered: "定義の表現形式は？"=(no option selected) notes: コスト優先で決めたい, "検証の入口は？"="UI" selected preview:
[検証] ボタン, "移行の順序は？"="先に基盤". Read the answers carefully — …
```

`questionAnswers.ts` の錨は `"<質問>"="` と**引用符まで**要求していたので、この形が1問でも
混ざるとカード全体が錨経路を諦め、旧ペア正規表現へ落ちる。旧経路は引用符の無いペアを
**丸ごと読み飛ばす**ため、以降の回答が1問ずつ**ズレて**表示される（実測: 3問中1問を自由入力
→ 1問目のカードに2問目の答え、2問目に3問目の答え、3問目は空）。錨の引用符を任意にし、
引用符が無い場合は `(no option selected) notes: ` を剥がして「自由入力」として解決する
（末尾の質問は閉じ引用符が無いので、claude の定型文 "Read the answers carefully" /
"You can now continue" で切る。未知の文言なら丸ごと残す＝嘘はつかない）。
テストは `questionAnswers.test.ts`（実データそのままの3例）。
