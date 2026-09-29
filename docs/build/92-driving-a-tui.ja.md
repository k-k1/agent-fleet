---
audience: "CLI の対話画面を機械から駆動する人"
source_of_truth: "コード（本書はそれを実機で検証する方法）"
updated: "2026-09"
---

# 92. CLI の対話画面を機械から駆動する — 検証プレイブック

[English](92-driving-a-tui.md) | 日本語

Terminal (CLI) のセッションでは、Console はエージェントのモーダル画面——質問・プラン承認・
許可プロンプト——に**セッションの tmux ペインへキーを送って代理回答する**。この結合は
**CLI 側の UI 変更で黙って壊れる**。エラーにならず、「**違う選択肢が回答される**」という形で
現れる。だから、挙動が怪しいとき・駆動経路を直すとき・CLI を更新したときは、**実機の TUI に
対して再検証する**。

マネージドのセッションには本書は当てはまらない。キーを送るペインが無く、Console は保留中の
対話に Agent の構造化された経路で答える（たとえば質問なら `POST /sessions/{name}/respond`）。どの kind に Terminal の
経路があり、それぞれどのモーダル状態を出すかは [エージェント機能表](../../guide/ref/agents.ja.md)
にある——たとえば lcpp と muse には経路そのものが無い（`BuildLaunch` が `ErrNoTerminalRoute`
を返す）。

**このプレイブックを生んだ日付つきの実測と事件記録は凍結アーカイブにある**——特定の CLI 版に
紐づいた記録で、現役の棚には置けない（寿命が違う）。ここに置くのは、**古びない方法**だけ。

## 92.1 プレイブック

使い捨てのセッションを立て、**Agent が送るのと同じキーとタイミングを再現**し、pane を観察する。
例は claude の質問モーダル（`AskUserQuestion`）を駆動する。方法は他の kind にもそのまま使えるが、
キー列は使い回せない（[92.4](#924-駆動コードの所在) を参照）。

> フリートの生きたセッションには触らないこと。作業ディレクトリはスクラッチに。
> **質問 1 回＝実際に 1 ターン分のコストがかかる。**

```bash
# 1) 使い捨てセッションを起動。スクラッチのディレクトリは Agent が事前に信頼済みにした
#    場所ではない（claude のフォルダ信頼プロンプトを事前承認するのは自分の起動ディレクトリ
#    だけ）ので、初回はそのプロンプトで止まる。Enter を押す前に pane を読むこと。
tmux new-session -d -s auqtest -x 140 -y 50 "claude '<質問を1つだけさせるプロンプト>'"

# 2) モーダル表示を待って観察
tmux capture-pane -p -t auqtest | tail -30

# 3) Agent と同じ入力を再現。-l はリテラルの打鍵で {seq} のテキスト手順に当たる。
#    キー名だけなら {keys} / {seq} のキー手順。Agent は 1 回の {keys} / {seq} 要求の
#    手順の間に 90ms 空ける（Enter の前も同じ）。
tmux send-keys -t auqtest -l 'テキスト'
sleep 0.09
tmux send-keys -t auqtest Down

# 4) 何が回答されたかを読み戻す（claude は「User answered …」行を出す。
#    pane で折り返されていたら -J がつなぐ）
tmux capture-pane -p -J -t auqtest -S -60 | grep -A3 "answered"

# 5) 後片付け
tmux kill-session -t auqtest
```

打鍵したプロンプト（`{prompt}`）はキー手順と間合いが違う。Agent は本文を打ち込み、
`inputSubmitDelay`——claude は 20ms、それ以外の kind は 200ms、`AGENT_INPUT_SUBMIT_DELAY_MS`
で上書き可——待ってから Enter を送る。codex・opencode・copilot・cursor・kiro には本文を
`send-keys -l` ではなく bracketed paste（`load-buffer` ＋ `paste-buffer -p`）で渡す
（`typePromptText`）。

Agent 自身の画面読み取り（`internal/tmuxx/tmuxx.go` の `tmuxx.CapturePane`・`ReadPane`・
状態プローブ）は `-J` を**付けずに**取る。pane の幅を超える行は、それらが見るのと同じ形で
分かれている。状態検出に何が見えているかを確かめるときは同じ取り方で読み、折り返された 1 行を
丸ごと欲しいときだけ `-J` を足す。

### 92.1.1 プローブの隔離 — 実際に踏んだ罠 2 つ

上の素の形は**セッションの中から実行すると危ない**。2 つ直しておくこと。

1. **専用ソケットで立てる**（`tmux -L probe …`）。本番の Agent は tmux コマンドをすべて
   既定ソケットで打つ（`tmuxx.Cmd`。動かすのは `AF_TMUX_SOCKET` だけ）ので、既定ソケットは
   **Agent が所有する tmux サーバーそのもの**。そこで `kill-server` を打てば**ワークスペースの
   全セッションが死ぬ**し、Agent の `claude_` 接頭辞を付けたプローブは meta の無い孤児
   セッションとして一覧に出る。`-L` なら完全に別サーバー。
2. **セッション名の env と CLI 自身の env を落としてから起動する。**Agent が起動するペインは
   どれも `AF_SESSION_NAME` を持ち、claude の状態フックはユーザーの `settings.json` に
   入っているので、セッションの中から起動したプローブもそのフックを鳴らす——そして
   `NormalizeHookSID`（`internal/agents/claude/sid.go`）がそれを**呼び出し元のセッションに
   付け替える**。実測での症状: プローブの質問状態が**計測者自身のセッションに書かれ**
   （ありもしない質問カードが Console に出て、コンポーザが「質問保留」で塞がれる）、会話 ID の
   台帳がプローブの会話を指した（**次の再開で別の会話が復元されうる**）。今回はホスト側が
   自分のフックを撃つたびに自己修復されて実害に至らなかったが、**アイドルなセッションから
   測っていたら残っていた**。CLI 自身の env の継承はもっと悪く（実測）、子セッション扱いに
   なって**フックがそもそも鳴らない**。

```bash
mkdir -p "$AF_WORK_DIR/probe"
env -u AF_SESSION_NAME -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_ENTRYPOINT \
    -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_EXECPATH -u CLAUDE_PID -u AI_AGENT \
  tmux -L probe new-session -d -s p1 -x 200 -y 50 -c "$AF_WORK_DIR/probe" \
  "claude --session-id $(uuidgen) --model sonnet --dangerously-skip-permissions"
```

`AF_SESSION_NAME` が無ければフックは claude が報告する会話 ID で状態を記録するので、
session-id を明示すると状態ファイルと保留ファイルの所在が確定する。そのまま観測でき、
後片付けもその id だけで済む。

### 92.1.2 各パターンを出させるプロンプト

まず質問を 1 つ出させ、同じセッションで追い質問すると、1 起動で複数の形を回せる——
単一選択・複数選択・1 回の呼び出しに 2 問・選択肢に preview が付いた単一選択。

**ラベルには必ず「非 ASCII の文字＋数字混じり」を入れる**（記録に残るプローブは日本語を
使った）。テキスト無視と数字キー即確定の両方のリスクを一度に検出できる。

### 92.1.3 判定の要点

- ラベル全文を打鍵して**モーダルが無反応**であること。反応したらフィルタが復活＝挙動が変わった合図。
- `Down×i, Enter` で**意図した行**が回答されること。
- 複数選択で Enter が**送信でなくトグル**であること。
- Type 行でテキストが登録されること。複数選択では打鍵でその行にチェックが入り、Enter を
  押すとまた外れる。
- 質問の選択肢のどれかに preview が付いていると、その質問には **Type 行が無い**。自由入力は
  ハイライト中の選択肢の notes に入る（`n`・本文・Enter）。この配置は質問ごとに決まり、
  フォーム全体では決まらない。Type 行がある前提のキー列は代わりに「Chat about this」に着地し、
  打鍵は捨てられ、claude は続く Enter を質問の辞退として受け取る。

## 92.2 CLI 更新時の回帰チェック

最低この 5 つ: ①単一選択をキーで、②ラベル全文を打鍵して**何も起きない**ことの確認、
③複数選択のトグル→送信、④Type 行の自由入力、⑤preview 付き単一選択の自由入力
（notes。Type 行は無い）。

**5 つのどれかが変わっていたら、キー列の生成側と本書を同じ変更で更新する。**

## 92.3 ここから得た不変条件

修正の形そのもので、書き直しても保つべきもの:

- **選択肢の回答は必ずキー列で送る。ラベルをテキストとして送らない。**
  ラベル打鍵こそが「黙って違う選択肢を答える」原因だった。
- **保留中の対話がある間、素のテキスト入力は拒否する**——送信元が Console のコンポーザでも、
  予約実行でも、セッションを駆動する外部クライアントでも。エラーコードは状態を名指す:
  `question_pending`・`plan_pending`・`permission_pending`・`auth_expired`（claude の
  ログインが切れている）、それ以外は `interaction_pending`。ゲート（`promptBlocker`）は
  whitelist 方式（idle / working 以外を全部塞ぐ）。Enter がハイライト行を無音で確定する事故は、
  プランでも許可プロンプトでも同型だから。ゲートに見えるのは kind ごとに読むものだけ——状態
  ストア、状態フックの無い kind ならその kind 自身のプローブ——で、現在は codex・opencode・
  cursor の Terminal セッションの保留モーダルが見えていない（#1227）。
- **却下は「いいえ」の行へのキー移動ではない**。プラン承認メニューの行数は claude の版で
  変わり、固定の `Down×3` が「Yes」の行へ回り込んで、却下するはずのプランを承認した。承認は
  既定行での Enter、却下は割り込み（Escape）（`planDecision.ts`）。
- **クリックは選択、送信は別ボタン**。クリック即送信は取り消せず、比較していた選択肢の
  preview も消えてしまう。
- **検証はキー列で終わらない。配送層まで通して初めて終わる**。プローブでは正しい
  キー列が、Agent に届いていないことがある。たとえば Agent は `{k}` の手順をすべて名前付き
  キーの whitelist（`allowedKey`）で検査し、知らないものが 1 つでもあれば要求ごと断るので、
  `{k: "n"}` で送った notes のキーは pane に届かなかった（今はテキスト手順で送る）。失敗した
  回答は画面でそう言わなければならない。沈黙は成功に見える。

## 92.4 駆動コードの所在

このプレイブックで変化が見つかったときに読み直すもの:

- **キー列** — `console/src/features/mirror/questionKeys.ts`: claude のタブ付きモーダルは
  `buildClaudeSeq` / `buildClaudeSubmit`、1 問 1 ページのメニュー（codex・opencode・agy。
  agy の書き込み行は Enter で入ってから本文を受ける）は `buildMenuSeq`、マネージドのセッションは
  `buildRespondAnswers`。プランと許可のボタンは `MirrorView.tsx` で配線している。
- **配送** — `workspace/agent/internal/sessionx/session_io.go` の
  `POST /sessions/{name}/input`: `{keys}`（`sendNamedKeys`）、`{seq}`（キー手順とテキスト
  手順）、`{prompt}`（`submitPromptTUI` → `typeLineAndSubmit`）、`allowedKey` の whitelist、
  `promptBlocker` のゲート。
- **何が固定しているか** — `questionKeys.test.ts` は各ビルダーの出力を固定し、Go のソースから
  `allowedKey` を読んで、ビルダーが出す `{k}` がすべて Agent に受け付けられることを確かめる。
  これらは単体テストで、実物の CLI には触れない。手動起動の `claude-tui-contract.yml` は claude を
  実機で動かし、状態検出（`TestClaudeTUIContractLive`。`internal/tmuxx/testdata/footers` の
  キャプチャ集に対する単体テストの実機版）とプラン承認の既定行（`TestClaudePlanApprovalContractLive`）を
  確かめる。本物の質問モーダルに Console のキー列で答える自動テストは無い——そのために
  このプレイブックがある。
