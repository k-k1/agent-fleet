# 92 — TUI モーダル駆動の実測検証プレイブック（AUQ ほか）

正: コード（本書は検証手法と実測記録）/ 主な更新トリガ: claude CLI のバージョン更新・AUQ 駆動経路（`PendingQuestions` / `handleSessionInput`）の変更 / 最終確認: 2026-07（claude v2.1.204）

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
