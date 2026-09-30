# 127. 中断したとき、各 CLI は積まれた入力をどうするか — claude・codex・opencode の実測

- 依頼: #1282（停止の意味を決める ADR の前提）。#1256 の一部（claude・codex・opencode の中断。kill と残り 4 種は未測定）。
- 関連: ADR [0105](../decisions/0105-stop-continues-into-the-queue.ja.md)、docs/log/125 §4、docs/log/126 §4。

## 1. 測り方

- 手元に偽の LLM サーバを立てた（Anthropic `/v1/messages`、OpenAI `/v1/responses`・`/v1/chat/completions`）。
  最後の利用者メッセージに `SLOW` を含むリクエストは 90 秒かけて 1 秒ごとに文字を流し、それ以外はすぐ `ok` を返す。
  届いたリクエストはすべて、利用者メッセージの件数と最後の本文を記録した。
- 各 CLI は隔離した HOME と設定ディレクトリで起動した（claude: `CLAUDE_CONFIG_DIR` と偽の `ANTHROPIC_API_KEY`／
  codex: `CODEX_HOME` にカスタムプロバイダ／opencode: XDG 一式に openai-compatible のプロバイダ）。
  利用者のログイン情報や履歴には触れていない。課金は無い。tmux は専用ソケット。
- 手順: `SLOW one` を送る → 4 秒後、実行中に 2 件目を送って積ませる → Esc。以下「送った」はサーバの記録で判定した。
  画面は `capture-pane -J` で読んだ。
- 各場面は **1 回ずつ**しか走らせていない。以下は、この版・この条件で観測したことであって、CLI の仕様の断定ではない。

## 2. 結果

| CLI | 積んだときの表示 | 中断すると、積まれた入力は | 2 回目の Esc |
|---|---|---|---|
| claude 2.1.285 | 入力の下に積まれ、「Press up to edit queued messages」「ctrl+x ctrl+s to send now」 | **その場で次のリクエストとして送られた**。先頭に `[Request interrupted by user]` が付く。2 件積んだ 1 回の場面では、2 件が **1 つのリクエストに入って**送られた（CLI の中のターン境界までは見ていない） | 続いたリクエストを止めた。0.3 秒で 2 回押しても同じで、巻き戻しメニューは開かなかった（ターン実行中だったため、と読んでいる） |
| codex 0.159.2 | 「Messages to be submitted after next tool call (press esc to interrupt and send immediately)」 | **その場で送られた**。画面に「Model interrupted to submit steer instructions.」 | 続いたリクエストを止めた（画面に「Conversation interrupted」。CLI の中のターン境界は見ていない） |
| opencode 1.18.33 | 会話に `QUEUED` 印付きの利用者メッセージとして出る | 中断には Esc が 2 回要る（1 回目で「esc again to interrupt」）。中断しても **単独では送られない**。履歴には印なしで残る。次に送った入力と一緒に、文脈として次のリクエストに載った（利用者メッセージ 3 件） | — |

記録の抜粋（時刻、利用者メッセージ数、最後の本文）:

```
claude   11:50:15  n_user=2  '[Request interrupted by user] | QUEUED two'
claude   11:50:59  n_user=6  '[Request interrupted by user] | SLOW QUEUED six | SLOW QUEUED seven'
claude   11:51:00  client disconnected  (the request carrying both, 2nd Esc 0.3 s later)
codex    11:52:00  n_user=4  'QUEUED two'
opencode 11:53:26  client disconnected  'SLOW one'   (nothing sent for QUEUED two)
opencode 11:53:52  n_user=3  'NEXT three'           (SLOW one, QUEUED two, NEXT three)
```

## 3. 読み取れること

- 測った場面では、主要な 2 つの CLI の中断は「今のターンを止めて、積んだものへ進む」操作で、積んだ入力は捨てなかった。
  codex は画面でそう案内している。積んだ入力を取り消したい利用者は、中断の前に編集へ戻す（claude の ↑）。
- Console の Terminal（CLI）セッションの「実行を停止（Esc）」は CLI に Esc を送るので、claude と codex では既にこの動きになっている。
  Managed だけが、利用者の入力を捨て、peer は流す（docs/log/27 §12.2-4 と ADR 0041 補遺）。
- 私が #1258 の議論で「claude と codex は中断すると入力欄に戻すはず」と書いたのは記憶で、誤りだった。
- opencode の「履歴に残して次に一緒に載せる」は、捨てるとも流すとも違う第 3 の型。

## 4. 残り

- pane を kill したときの扱い、cursor・kiro・agy・copilot の中断は測っていない（#1256）。
- 測ったのは道具を使わない文字だけのターン。ツール実行中の中断で振る舞いが変わるかは見ていない。
- 1 回目の Esc のあとに積まれたものが残る場面（codex で 2 件以上積む、1 回目の Esc の後に積み足す）は作っていない。
  したがって、2 回目の Esc が残りのキューも捨てるかは分からない。
