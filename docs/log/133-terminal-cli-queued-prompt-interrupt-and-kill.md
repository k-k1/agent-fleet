# 133. What each Terminal CLI does with a queued prompt on interrupt (Esc) and on pane kill

- Request: #1256. It completes the measurement that docs/log/127 started (claude, codex and opencode on Esc).
- Related: docs/log/125 §4 (the Managed-route fix, #1244), docs/log/127, ADR 0105.
- Follow-up filed: #2288.

## 1. Result

"Queued" means: the CLI accepted a second prompt typed while its first turn was still running, and shows it as
pending. One run per cell, on the versions below, on a text-only turn (plus a 30 s shell tool for codex). These are
observations, not a statement of what each CLI guarantees.

| CLI (version) | Esc while the prompt is queued | Pane killed (`tmux kill-session`) while it is queued |
|---|---|---|
| claude 2.1.302 | **Runs.** The turn is interrupted and the queued prompt is sent at once as the next request, prefixed `[Request interrupted by user]` | **Dropped.** The transcript holds only a `queue-operation` `enqueue` line; `--continue` restores the first prompt, the composer is empty, no request is sent. Lost (this one rests on the transcript and the fake server's request log) |
| codex 0.163.0 | **Runs.** `Model interrupted to submit steer instructions.`, then the queued prompt is sent | **Runs, without the pane.** codex's app-server daemon (PPID 1) outlives the pane, finishes the tool and sends the queued prompt 30 s later |
| opencode 2.0.32 | **Runs.** Two Esc within about a second interrupt (one Esc only arms "esc again to interrupt"); the queued prompt is sent as the next request. Nothing is sent before the interrupt | **Runs, without the pane.** opencode's `serve --service` daemon (PPID 1) outlives the pane, the stream ends, and the queued prompt is sent with no TUI attached |
| cursor 2026.10.16-e3cb207 | **Gone from the panel, and the turn is not interrupted** (pane observation: it never ran). Esc cancels the queued follow-up and the turn runs to the end. Ctrl+C (the key the CLI advertises) interrupts and leaves the follow-up held in its panel (`↑ to edit`), not run | **Not seen again.** No process survives; `--resume <chat id>` opens an empty chat (the first prompt is gone too), so a lost queue cannot be told from a lost turn. Whether it reached the backend is unknown |
| kiro-cli 2.31.0 | **Runs.** Esc interrupts and the queued prompt runs as the next turn | **Not seen again.** No process survives; `chat --resume` in the same directory opens a fresh, empty chat; not restored, not run in the pane, whether it reached the backend is unknown |
| copilot 1.0.98 | **Not interrupted.** With a prompt pending, one Esc and two Esc both left the stream running to its end; the pending prompt then ran. Ctrl+C: `Operation cancelled by user`; the pending prompt is gone from the display and the composer and never ran (pane observation) | **Not seen again.** No process survives; `--continue` restores the first prompt only; the queued one is not restored or run in the pane, whether it reached the backend is unknown |
| agy | not measured: the binary aborts on start (`CRNGT failed.` / `SIGABRT: abort`) even for `--help`, so neither its version nor its behaviour could be read in this container | not measured: same |

Two behaviours that decide how to read the table:

- **codex, this build: in this text-only trial a second prompt was sent at once.** It cut the stream (18:34:28,
  `n_user=3`, the `SLOW one` request then disconnected). The `Messages to be submitted after next tool call` queue
  was observed only while a tool was running (18:36:26). One trial each; this is not a rule for every turn.
- **codex and opencode keep the turn, and the queue, outside the pane.** Killing the pane ends only the TUI.

Changes against docs/log/127: opencode 1.18.33 did not send a queued prompt on its own after an interrupt; 2.0.32
does (18:38:35). The Esc results of claude and codex agree with 127. The condition under which codex queues does not: 127 §4 recorded a
queue in a text-only turn on 0.159.2; here a text-only turn on 0.163.0 took the prompt at once. Version or setup
difference is not resolved.

## 2. Method

### 2.1 claude, codex, opencode: fake LLM server, isolated config

No account is involved. A small local server (Appendix) speaks Anthropic `/v1/messages`, OpenAI `/v1/responses`
and `/v1/chat/completions`. A last user message containing `SLOW` is answered with a 90 s stream, one `tickN` per
second; `TOOL` on `/v1/responses` is answered with an `exec_command` call (`sleep 40`). Every request is logged
with the user-message count and the tail of the last user message; a client that goes away logs `DISCONNECT`.

Each CLI ran under its own `HOME` and config directory, in its own tmux session on the default socket
(`afprobe1256-<kind>`), killed afterwards:

| CLI | Isolation |
|---|---|
| claude | `CLAUDE_CONFIG_DIR=<dir>`, `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>`, a fake `ANTHROPIC_API_KEY`; `.claude.json` with `hasCompletedOnboarding`; the workspace-trust and API-key prompts answered once |
| codex | `CODEX_HOME=<dir>` with `config.toml`: `model_provider="fake"`, `wire_api="responses"`, `base_url` of the fake, `sandbox_mode="danger-full-access"` (read-only made `exec_command` return at once), the work directory under `[projects]` as trusted |
| opencode | XDG directories under the isolated `HOME`; `opencode.json` with an `@ai-sdk/openai-compatible` provider pointing at the fake |

Steps for one cell (send text with `tmux send-keys -l`, wait 1 s, then `Enter`; codex needs the pause or the
Enter is swallowed into a paste burst):

1. Start the CLI, wait for the composer.
2. Send `SLOW one` (codex: `TOOL one`), wait 5 s, confirm the turn is running.
3. Send `QUEUED two`, wait 2 s, capture the pane (`capture-pane -p -J`).
4. Esc cell: press Esc (opencode: Esc, 0.7 s, Esc), wait 4 s, capture the pane, read the server log.
   Kill cell: `tmux kill-session -t <name>`; read the server log; relaunch (`claude --continue`,
   `codex resume --last`, `opencode --session <id>`) and read the pane; for opencode and codex keep reading the server log for
   60 s and look for the processes still running under the isolated `HOME`.

### 2.2 cursor, kiro, copilot: the logged-in CLI, tiny prompts

These have no configurable endpoint, so the CLI's existing login was used (never a login flow). Working directory: an
empty scratch git repository under `$AF_WORK_DIR`, trusted once when the CLI asked. First prompt: `Write the numbers 1
to 1000, one per line, nothing else.` (copilot: `Do not use any tools. Write the numbers 1 to 3000 in your answer, one
per line, nothing else.`; without the sentence it proposed a `seq` shell call and the next input went into the
permission dialog). Queued prompt: `Reply with exactly the word QUEUEDTWO and nothing else.`, sent 4 s after the first.
cursor was started with `--model auto` (the account only allows Auto). For `--resume` cursor's chat id came from
`cursor-agent create-chat`. "Ran" means `QUEUEDTWO` and the answer appear in the pane afterwards; "dropped" means
they never do and the CLI is idle.

## 3. Evidence

All times are the workspace's local time, 2026-10-17.

### claude 2.1.302

Esc:

```
18:32:01 REQ n_user=1 ... SLOW one
18:32:07 pane: "❯ QUEUED two / ctrl+x ctrl+s to send now"   "❯ Press up to edit queued messages"
18:32:08 REQ n_user=2 last='[Request interrupted by user]  QUEUED two'
18:32:08 DISCONNECT ... SLOW one
18:32:11 pane: "⎿  Interrupted · What should Claude do instead?" / "❯ QUEUED two" / "● ok"
```

Kill (transcript lines of the killed session, in file order):

```
2026-10-17T09:32:22.882Z user  "SLOW one"
2026-10-17T09:32:28.376Z queue-operation enqueue "QUEUED two"
18:32:30 kill-session        18:32:32 DISCONNECT ... SLOW one
18:32:45 claude --continue -> pane: "❯ SLOW one", empty composer; no request in the server log
```

No `dequeue`/`remove` record exists, and nothing restores the enqueue on resume. This is the record that
`session_delivery.go` counts as delivered.

### codex 0.163.0

Esc, tool in flight:

```
18:36:26 pane: "• Messages to be submitted after next tool call (press esc to interrupt and send immediately)"
              "  ↳ QUEUED two"                         (Esc pressed here)
18:36:26 REQ /v1/responses last='QUEUED two'
18:36:30 pane: "• Model interrupted to submit steer instructions." / "› QUEUED two" / "• ok"
```

Kill, tool in flight (second run, server log watched for 60 s):

```
18:55:22 REQ last='TOOL one'                       (answered with the exec_command call)
18:55:30 pane shows the queued line; kill-session
18:55:52 REQ /v1/responses n_user=3 last='QUEUED two'       (the tool returned after about 30 s)
         processes 1655051, 1655103: PPID 1, started 18:55:13 (codex app-server-daemon); stopped by hand afterwards
```

An earlier kill run, resumed at once with `codex resume --last` (18:37:09), showed `Working` and no queued line;
that run was too short to see the 30 s tool end and is superseded by the one above. The queued text is also in
`history.jsonl` (the input history, `↑` recall), not in the rollout.

No tool in flight (so no queue):

```
18:34:22 REQ last='SLOW one'    18:34:28 REQ n_user=3 last='QUEUED two'    18:34:29 DISCONNECT ... SLOW one
```

### opencode 2.0.32

```
18:38:09 REQ SLOW one
18:38:14 queued "QUEUED two" shown as a user message;  18:38:23 still no request (8 s idle)
18:38:23 Esc: status "esc again to interrupt";  5 s later the arm has lapsed, nothing sent
18:38:34 Esc, Esc (0.5 s apart)
18:38:35 REQ n_user=2 last='QUEUED two'      18:38:36 DISCONNECT ... SLOW one
```

Kill: `18:38:53 SLOW one`, queued at about 18:38:59, `kill-session` 18:39:01. The sqlite store then held the queued text in
`session_inbox` with `delivery='steer'` and not in `session_message`. The TUI was relaunched with `--session <id>` at 18:39:31 (pane read at 18:39:41) and killed
again at about 18:39:50. The stream (a `serve --service` process, PPID 1, started 18:37:22) ended on its own:

```
18:40:23 DONE ... SLOW one
18:40:24 REQ /v1/chat/completions n_user=2 last='QUEUED two'   18:40:24 DONE
session_message: seq 13 user "QUEUED two" at 1792230024052, seq 19 idle outcome=succeeded
```

### cursor 2026.10.16-e3cb207

```
18:42:57 Esc with "┌─ follow-ups ─ ○ Reply with exactly the word QUEUEDTWO ... enter steer · ↑ select/edit · esc cancel"
18:42:59 panel gone, "Working" continues;  Esc again (18:42:59): still Working;  by 18:43:30 it reached 1000, idle; QUEUEDTWO never appears
18:43:46 Esc with no queue: still Working at 18:43:49, finishes 1000 at 18:44:04 (Esc does not interrupt)
18:44:15 Ctrl+C with the follow-up queued -> 18:44:18 turn stopped, composer holds the first prompt,
         "┌─ follow-ups ─ ○ Reply with exactly the word QUEUEDTWO ... ↑ to edit"; still the same at 18:44:33 (held, not run)
18:45:39 kill-session -> `cursor-agent --resume <id>` (18:45:55): "→ Plan, search, build anything", no history
18:56:58 kill-session again: no cursor process left
```

### kiro-cli 2.31.0

```
18:46:34 pane: "◇ 1 message queued · ctrl+x expand" / "Kiro is working · 6s · Type to steer · Ctrl+S to queue"   (Esc pressed)
18:46:37 "› Reply with exactly the word QUEUEDTWO and nothing else." / "• QUEUEDTWO" / "▸ Credits: turn 0.08 ... Time: 1s"
18:47:14 kill-session (queued shown) -> 18:47:27 `kiro-cli chat --resume`: banner and empty composer, no QUEUEDTWO
18:57:34 kill-session again: no kiro process left
```

### copilot 1.0.98

```
18:49:10 pane: "❯ Reply with exactly the word QUEUEDTWO ... └ [pending · ↑ recall]", status "Working · esc interrupt"
18:50:11 Esc: 18:50:12 ... 18:50:23 still "Working · esc interrupt", "pending" still shown; stream reached 3000
18:50:24 "Working ... esc edit prompt";  later "● QUEUEDTWO"
18:50:43 Esc, 18:50:45 Esc (second round): same, streamed on to the end, then the pending prompt ran
18:51:20 Ctrl+C: 18:51:21 "● Operation cancelled by user", the pending line is gone; composer empty; idle 8 s later; no QUEUEDTWO
18:53:51 kill-session (pending shown) -> 18:54:09 `copilot --continue` (after "Resume anyway"): the first prompt only
```

Esc with nothing queued was inconclusive: the one run pressed Esc before the stream began (status `esc edit prompt`)
and was not repeated.

## 4. What it means for Agent Fleet

- **A queued prompt can be lost.** After a pane kill the prompt was not seen again for claude, cursor, kiro and copilot (certain for claude, from
  the transcript and the request log; for the other three only the resumed pane was observed, and delivery to the
  backend is unknown). In cursor's Esc and copilot's Ctrl+C runs it vanished from the pane and never ran. A pane kill is what stop-and-archive, a recreate and an Agent restart do to a
  Terminal session.
- **claude is the one that matters for peer messages.** `confirmPromptDelivery` / `deliveryEvidenced`
  (`workspace/agent/internal/sessionx/session_delivery.go`) call the prompt delivered once
  `claude.PromptAcceptedSince` (`workspace/agent/internal/agents/claude/program.go`) sees the needle in the appended
  transcript bytes, and a `queue-operation` `enqueue` line carries it. `send_to_peer_session` then answers
  `delivered: true` for a message that exists only in claude's memory. Esc runs it, a pane kill loses it.
- **The code change (not made here):** make `PromptAcceptedSince` tell "enqueue line only" from "a `user` line (the
  turn started)"; for the enqueue-only case let the send response carry `held: true`. `mcp_stdio.go` already maps
  `held` to `queued: true, delivered: false` with the Managed route's note, so the sender sees the same word on both
  routes. The prompt is still delivered later in the common case, so the note stays "queued", not "failed".
  Filed as #2288.
- codex and opencode did not lose it in these runs because their turn and queue live in a process that outlives the
  pane; a host-level kill of that daemon was not measured.
- Stopping cursor with Esc (`workspace/agent/internal/agents/cursor/stop.go` sends it before the exit keys) did not
  interrupt a streaming text turn here; Ctrl+C did. One run on one prompt, so it is only a pointer.

## 5. Not measured

- agy (see the table).
- A queue of two or more prompts, a second interrupt of the turn that the first interrupt started, or of what is still queued, and interrupts during a tool call (except codex).
- kiro, cursor and copilot ran on real backends, so the evidence is the pane only; whether the CLI sent the queued
  prompt to its backend before a kill is not observable.
- After a kill, `--resume` / `--continue` of cursor and kiro opened an empty chat, so those two cells cannot say
  whether the queue alone was discarded or the whole unfinished turn was.
- A host-level kill (the daemon of codex and opencode), and the other kinds of queue entries (`ctrl+x ctrl+s`,
  `ctrl+q enqueue`).

## Appendix: the fake LLM server

`python3 fake.py <port> <logfile>`; Python 3 standard library only.

```python
import json, sys, time, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
LOG = sys.argv[2]
def log(s):
    with open(LOG, 'a') as f: f.write(time.strftime('%H:%M:%S ') + s + '\n')
def texts(content):
    if isinstance(content, str): return content
    out = []
    for b in content or []:
        if isinstance(b, dict) and b.get('type') in ('text', 'input_text'): out.append(b.get('text', ''))
    return ' '.join(out)
def users(body):
    items = body.get('messages') or body.get('input') or []
    if isinstance(items, str): return [items]
    return [texts(m.get('content')) for m in items if isinstance(m, dict) and m.get('role') == 'user']
class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *a): pass
    def sse(self, ev, data):
        self.wfile.write(f'event: {ev}\ndata: {json.dumps(data)}\n\n'.encode()); self.wfile.flush()
    def do_GET(self):
        b = b'{"data":[]}'
        self.send_response(200); self.send_header('content-type','application/json'); self.send_header('content-length',str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        n = int(self.headers.get('content-length') or 0)
        raw = self.rfile.read(n)
        try: body = json.loads(raw or b'{}')
        except Exception: body = {}
        us = users(body)
        last = (us[-1] if us else '')
        # skip system-reminder style blocks: report only the tail
        last = last.replace('\n', ' ')[-80:]
        slow = 'SLOW' in last
        log(f'REQ {self.path} model={body.get("model")} n_user={len(us)} last={last!r} slow={slow}')
        if not body.get('stream'):
            b = json.dumps({'id':'m','type':'message','role':'assistant','model':body.get('model','x'),'content':[{'type':'text','text':'ok'}],'stop_reason':'end_turn','usage':{'input_tokens':1,'output_tokens':1}}).encode()
            self.send_response(200); self.send_header('content-type','application/json'); self.send_header('content-length',str(len(b))); self.end_headers(); self.wfile.write(b); return
        self.send_response(200); self.send_header('content-type','text/event-stream'); self.send_header('cache-control','no-cache'); self.send_header('connection','close'); self.end_headers()
        chunks = [f'tick{i} ' for i in range(90)] if slow else ['ok']
        try:
            if self.path.startswith('/v1/messages'):
                self.sse('message_start', {'type':'message_start','message':{'id':'m1','type':'message','role':'assistant','model':body.get('model','x'),'content':[],'stop_reason':None,'usage':{'input_tokens':1,'output_tokens':1}}})
                self.sse('content_block_start', {'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}})
                for c in chunks:
                    self.sse('content_block_delta', {'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':c}})
                    if slow: time.sleep(1)
                self.sse('content_block_stop', {'type':'content_block_stop','index':0})
                self.sse('message_delta', {'type':'message_delta','delta':{'stop_reason':'end_turn','stop_sequence':None},'usage':{'output_tokens':1}})
                self.sse('message_stop', {'type':'message_stop'})
            elif self.path.startswith('/v1/responses') and 'TOOL' in last and isinstance(body.get('input'), list) and body['input'][-1].get('type') != 'function_call_output' and 'User prompt' not in last and body.get('tools'):
                rid='resp_t'
                item={'id':'fc_1','type':'function_call','status':'completed','call_id':'call_1','name':'exec_command','arguments':json.dumps({'cmd':'sleep 40','yield_time_ms':60000})}
                self.sse('response.created', {'type':'response.created','response':{'id':rid,'object':'response','status':'in_progress','output':[]}})
                self.sse('response.output_item.added', {'type':'response.output_item.added','output_index':0,'item':item})
                self.sse('response.output_item.done', {'type':'response.output_item.done','output_index':0,'item':item})
                self.sse('response.completed', {'type':'response.completed','response':{'id':rid,'object':'response','status':'completed','output':[item],'usage':{'input_tokens':1,'output_tokens':1,'total_tokens':2}}})
            elif self.path.startswith('/v1/responses'):
                rid='resp_1'
                self.sse('response.created', {'type':'response.created','response':{'id':rid,'object':'response','status':'in_progress','output':[]}})
                self.sse('response.output_item.added', {'type':'response.output_item.added','output_index':0,'item':{'id':'msg_1','type':'message','role':'assistant','status':'in_progress','content':[]}})
                self.sse('response.content_part.added', {'type':'response.content_part.added','item_id':'msg_1','output_index':0,'content_index':0,'part':{'type':'output_text','text':'','annotations':[]}})
                full=''
                for c in chunks:
                    full+=c
                    self.sse('response.output_text.delta', {'type':'response.output_text.delta','item_id':'msg_1','output_index':0,'content_index':0,'delta':c})
                    if slow: time.sleep(1)
                item={'id':'msg_1','type':'message','role':'assistant','status':'completed','content':[{'type':'output_text','text':full,'annotations':[]}]}
                self.sse('response.output_item.done', {'type':'response.output_item.done','output_index':0,'item':item})
                self.sse('response.completed', {'type':'response.completed','response':{'id':rid,'object':'response','status':'completed','output':[item],'usage':{'input_tokens':1,'output_tokens':1,'total_tokens':2}}})
            else:
                def ch(delta, fin=None):
                    d={'id':'c1','object':'chat.completion.chunk','created':int(time.time()),'model':body.get('model','x'),'choices':[{'index':0,'delta':delta,'finish_reason':fin}]}
                    self.wfile.write(f'data: {json.dumps(d)}\n\n'.encode()); self.wfile.flush()
                ch({'role':'assistant','content':''})
                for c in chunks:
                    ch({'content':c})
                    if slow: time.sleep(1)
                ch({}, 'stop')
                self.wfile.write(b'data: [DONE]\n\n'); self.wfile.flush()
            log(f'DONE {self.path} last={last!r}')
        except (BrokenPipeError, ConnectionResetError):
            log(f'DISCONNECT {self.path} last={last!r}')
        self.close_connection = True
ThreadingHTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
```
