# 125. 作業中の Managed セッション宛ての peer メッセージは積まれていた — 中断で捨てず、送信側に queued を返す

- 依頼: #1244（priority: high）。残件は #1255 / #1256 / #1257 / #1258。
- 関連: ADR 0041（Addendum 2026-09-30）、docs/log/58（セッション間メッセージ）、docs/log/27 §12.2-4（interrupt はキューも捨てる）。

## 1. 起きたこと（2026-09-29、codex Managed）

- レビュー役の codex 子 2 本に「書き手から PR 番号が届くまで待て」と書いたら、2 本とも codex 組み込みの
  `wait_agent`（`timeout_ms: 3600000`）で待った。`wait_agent` はサブエージェントを待つ道具で、新しい入力では戻らない。
- 書き手の `send_to_peer_session` は `delivered: true` を返した。子は読めないまま 1 時間以上「作業中」に居続け、
  利用者が止めるとメッセージは消えた（`GET /sessions/{name}/messages` に無い）。再起動後の子は、もう存在しない
  メッセージを待って黙った。送った側にも受けた側にも何も伝わらない。
- issue は「codex ドライバが `turn/steer` で配達した」と書いたが、コードを読むと違った（§2）。

## 2. 真因（コードで確認）

1. **steer ではなく積まれていた。** `send_to_peer_session` → `AgentSendToSession`（`mcpx/mcp_stdio.go`）→
   `POST /sessions/{name}/input`（`confirm` 付き）→ Managed なら `handleManagedInputPrompt` → `h.Send()`。
   `Steer` を呼ぶのは Console の `/turn op=steer` だけ（`sessionx/session_turn.go`）。
2. **積んだ瞬間に成功が返っていた。** 7 ドライバの `Send`（= `accept`）は、実行中のターンがあるとメモリ上の
   `h.queue` に積んで即 nil を返す。これが 200 になり `delivered: true` になる。Managed 経路は `confirm` を
   見ない（docs/log/58 §58.3 の「ターン開始の証拠までブロック」は TUI の claude だけの話）。
   → Managed では、**どう待っていても今のターンが終わるまで読めない**。issue の「ポーリングしていた 4 本は次の
   ツール境界で読めた」は、一度ターンを終えたところでキューが次ターンとして流れた、と読むのが整合的
   （該当セッションは削除済みで確かめられない）。
3. **捨てる経路は 7 ドライバ全部にあった**（すべて `h.queue = nil`）。

   | 経路 | 呼び出し元 | 修正後 |
   |---|---|---|
   | `Interrupt()` | チャットの「実行を停止」（`/turn interrupt`）、codex の質問 cancel / deny | peer は残して次ターン |
   | `AbortManaged` → `Interrupt` | Agent の graceful shutdown | 全部捨てる（`interruptAll`） |
   | codex の `drain` → `Interrupt` | app-server の再起動 | 全部捨てる（`interruptAll`） |
   | `DropHandle` | 行の「停止」（halt）・アーカイブ・再作成・実行方式の切替 | 全部捨てる（#1255） |
   | lcpp の `dropHandle` → `Interrupt` | 同上 | 全部捨てる（`interruptAll`。自前でキューを消していない） |
   | opencode の `drain`（`abortSession` を直接） | serve の再起動 | 変更なし。キューは残り、ポンプが次の項目を止まりかけの daemon へ送る（#1255） |
   | プロセスの再起動 | Agent の再起動 | キューはメモリだけ（#1255） |

   docs/log/27 §12.2-4 の「interrupt はキューも破棄する（停止の意思はキューに及ぶ）」は、peer 機能より前に
   利用者自身の追い打ち入力を想定して決めたもの。lcpp の `dropHandle` は `Interrupt` にキューの消去を任せていた。
   `Interrupt` が印付きを残すようになると、map から外れたハンドルのポンプが、閉じかけの store の上で残りの
   ターンを走らせる（ポンプは生存も ctx も見ない）。
4. **表示。** 待っている間は受信側ミラーに「キュー済み」の吹き出し（`queuedPrompts`、working の間だけ）で
   出ていたが、中断で跡形なく消える。転写には一度も入らない。

調査中に見つけた既存の穴（#1258）: muse は、ホストの死亡や idle 通知でターンが終わったあとにキューが止まる。
`startTurn` の失敗では 1 件落ち、`turn/started` までの隙間の送信はホスト側のキューへ入る。copilot / cursor / kiro は、
子プロセスが死ぬとポンプが終わり、`spawn` が再開しない。

## 3. 直したこと

- `agents.TurnInput.KeepOnInterrupt` と `agents.KeptOnInterrupt`。印は `handleManagedInputPrompt` が peer 送信の
  ときだけ立てる。7 ドライバの `Interrupt()` は印付きだけを残し（`interrupt(true)`）、片付け（`AbortManaged`・
  codex の `drain`・lcpp の `dropHandle`）は `interruptAll()` で全部捨てる。
- 残した項目は、中断したターンが落ち着いた直後にポンプが次のターンとして流す。7 ドライバとも経路を確認した
  （codex: `turn/completed` → `runTurn` 復帰 → pump のループ／opencode: blocking `/message` の復帰／ACP:
  `session/prompt` の復帰／lcpp: ctx の cancel／muse: `turn/completed` → `finishTurn` → `pump`）。
- `agents.QueueingSender`（`SendQueued`）。各ドライバの `accept` が「実行中のターン（か先に積まれた入力）の後ろに
  積んだ」を返す。条件は既存の `TurnQueued` と同じ `running || len(queue) > 1`（muse は `running && !steer`）。
  Managed の `/input` は積んだとき `held: true` を返す。キー名を `queued` にしなかったのは、同じ `/input` の
  when_ready 応答が `queued` をセッション名（文字列）に使っているから。`testdata/wiremap.golden` を取り直した。
- `send_to_peer_session` は `held` のとき `delivered: false, queued: true` と注記（再送しない）を返す。
  ツール説明の delivered の定義も直した。
- レビュー 1 巡目（codex / gpt-6-sol）で直した 2 点:
  - muse は `running` を非同期の `turn/started` で初めて立てるので、`turn/start` の受理からそこまでの隙間の送信は
    ホストへ直接行き、ホスト側のキューに入る（`ifBusy` の既定は `queue`）。`turn/start` の応答の
    `disposition: "queued"` を読んで積まれたと返すようにした。MSP の定義では `turn/interrupt` は 1 ターンだけを
    止め、積まれたターンを外すのは別コマンドの `turn/unqueue` なので、ホスト側に積まれた peer メッセージは停止
    では消えない（仕様から読んだもので、実測はしていない）。
  - opencode は、このハンドルが始めていないターン（手で付けた TUI、前の Agent が残したターン）が serve で
    走っていると、ポンプが `waitIdle` で最大 60 秒待ってから送る。`accept` の前に serve の状態を見て、忙しければ
    積まれたと返すようにした（`accept` の後に聞くと、自分の入力で忙しく見える）。
- 据え置いた 1 点: opencode の `drain` はタイムアウトで `abortSession` を直接呼び、キューを消さない。指摘の案
  （キューを消す）は、今は古い daemon の保存領域に届くかもしれない項目を、確実に黙って消すことになる。正しい直し方は
  再起動のあいだキューを持ち越し、`Resume` に再開させる（daemon の死亡では既にそうしている、§31）ことで、#1255 で扱う。
- 指針: `workspace/notes/agent-fleet.md`（待つならターンを終える。`wait_agent` はメッセージを待たない）と、
  `create_session` の `initial_prompt` の説明。
- `guide/member/02-sessions`（英日）: マネージドの作業中はターンの後に届く。実行の停止では捨てない。
  セッションやワークスペースの停止では失われる。

## 4. 採らなかった案と残件

- **作業中の codex / muse へはネイティブの `turn/steer` で届ける**（次のツール境界で読める）: kind ごとに意味が
  変わる。1 回のツール呼び出しで塞がっている相手には効かない。codex が中断時に未消費の steer をどう扱うかも
  測っていない。
- **中断後は自動で流さず、次の入力まで保留する**: 保留の状態と、解除・破棄の UI が要る。事件で要ったのは逆で、
  止めたら届くこと。
- **捨てたら送信側へ知らせる**: 新しいメッセージの種類と受け方の規則が要る。消える経路そのものを塞ぐほうが先。
- 残件:
  - halt・アーカイブ・Agent の再起動では、まだ消える #1255
  - Terminal（CLI）の各 CLI が中断時に自分の待ち行列をどうするか。claude の配達確認は enqueue も配達と数える #1256
  - operator（`report_to`）とスケジュール実行の注入も、中断で同じように消える #1257
    🟢 2026-10-03 追記: held の仕組みを operator とスケジュールにも広げ、捨てたものは「実行されなかった」と報告する
    ようにした（ADR 0105・0035 の 2026-10-03 の追記）。
  - キューが止まる（muse・ACP の 3 種）#1258。送り出し途中の入力に停止が届かない件も同じ issue で扱う
    （レビュー 2 巡目）。muse ではホスト側に積まれた利用者自身の追い打ちが停止のあとに始まる。opencode では
    別のクライアントのターンを `waitIdle` で待つあいだ、キューから出した入力を `Interrupt` が捨てられない。
    どちらもこの変更の前からあり、窓は広げていない。

## 5. 検証

- 各ドライバに 2 本（lcpp は `dropHandle` を足して 3 本）: 印付きは中断を生き残って次のターンになり、利用者の
  追い打ちは消える。片付けでは全部消える。モックには、中断したターンを保留する口を足した（`holdInterrupt` /
  `holdAbort` / `holdCancel`）。ポンプが拾ったあとでは「キューが空」は何も証明しないため。
- 陽性対照: `Interrupt` を「全部捨てる」「全部残す」に、片付けを「印付きを残す」に変えると各テストが落ちることを
  確かめてから戻した。1 回目の ACP の片付けテストは、変異の下でターンを走らせたまま後始末に入り、テストの HOME が
  戻ったあとにポンプが状態を書いて、実 HOME の `~/.local/state/agent-fleet/session-status/slot-1.json` を作った。
  テストを「変異の下でも残りのターンに応答してポンプを空にする」形に直し、ファイルは消した。
- sessionx: 作業中の Managed 宛ての peer 送信は `held: true` と `KeepOnInterrupt` が付き、利用者の送信はどちらも
  付かない。mcpx: `held` のとき `delivered: false, queued: true, note`。
- `go test ./...`（workspace/agent）は緑。wiremap.golden の差分は `handleManagedInputPrompt {held,sent} cond{held}`
  の 1 行だけ。

## 6. 実機確認（2026-09-30、マージ後の配備）

PR #1268 のマージ（f906a77cf）後に配備した Agent で確かめた。証拠には Agent が返す JSON（ツールの返り値、
`/messages` の転写と `queuedPrompts`）だけを使い、プローブ役のセッションの自己申告は使っていない。

- **配備の確認**: `/usr/local/bin/workspace-agent` の mtime は 08:37:28、マージは 08:00:33（どちらも JST）。
  `grep -a` で、今回足したツール説明・`peerQueuedNote`・`KeepOnInterrupt`・`create_session` に足した一文が
  それぞれ 1 件見つかった。陽性対照の既存文字列（`メッセージを届けられませんでした`）も 1 件、コメントにしか無い
  文字列は 0 件。セッションに配られたツール説明も新しい文言になっていた。
- **codex Managed**（gpt-6-luna。`sleep 180` を 1 回だけ実行させたプローブ）:
  - モデルは `exec_command` で `sleep` を裏で走らせ、`wait` ツールで待った。事件と同じ「ツールの中で待つ」形になった。
  - そこへ `send_to_peer_session` を送ると `delivered: false, queued: true`（Agent の応答は `held: true`）と注記が
    返った。`/messages` では封筒付きの本文が `queuedPrompts` に出て、転写には無かった。
  - `POST /sessions/{name}/turn {"op":"interrupt"}`（チャットの「実行を停止」と同じ）で止めると、`wait` は
    「aborted by user after 3.0s」になった。peer メッセージは停止と同じ秒（00:36:41.459Z）に、**別の anchor の
    新しい user ターン**として `source: peer` 付きで転写に入った。
- **opencode Managed**（既定モデル。`bash` で `sleep 180` を実行させたプローブ）:
  - 作業中に、利用者自身の追い打ちを `/turn` の steer（作業中に Console の入力欄が送るもの）で送り、続けて peer
    メッセージを送った。`queuedPrompts` は 2 件になった。
  - 停止すると peer メッセージだけが次のターンになり（`source: peer`）、`queuedPrompts` は空になった。追い打ちは
    転写のどこにも無く（同じ検索で peer の本文は見つかる）、その後 40 秒 idle のままだった。
