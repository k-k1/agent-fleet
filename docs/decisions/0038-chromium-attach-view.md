# 0038. Attach to an externally owned Chromium over loopback CDP, and open it in a Console pane on a user click

English | [日本語](0038-chromium-attach-view.ja.md)

- Status: adopted; the implementation contract fixed by the P0 measurements (P1–P3 implemented. The
  direct route for interactive sessions was corrected on 2026-08-02. The broken entry route and the
  port collision were corrected on 2026-08-08 = decisions 18–20)
- See also: [53-chromium-attach-view.md](../log/53-chromium-attach-view.md) /
  [53, the P0 measurements](../log/53-chromium-attach-view-p0-verification.md) / [0018-container-browser-pane.md](0018-container-browser-pane.md) /
  [0035-session-report-v2-ledger.md](0035-session-report-v2-ledger.md)

## Context

In the existing browser pane the Workspace Agent owns the Chromium, the BrowserContext and the Page,
and displays a web app on the container's localhost. Refusing external top-level navigation and not
exposing raw CDP are the security boundary.

Separately, there is a use case where an external process such as Playwright owns a headless
Chromium, automates an external administration screen, and wants to hand only the final check or
confirmation to the user. In that case the business script must not be ported into AF; only the
rendering and input of an already existing Page need relaying to the Console. It is desirable for an
agent, following the instructions in CLAUDE.md / AGENTS.md, to be able to prepare that handover from
the Workspace Agent's local MCP.

## Decision

1. Add "Chromium Attach View" as a second mode of the existing browser pane. AF-owned Pages and
   externally owned Pages are separated by type in ownership, navigation and lifecycle.
2. The external owner starts Chromium with `--remote-debugging-address=127.0.0.1` and an explicit
   port. The Agent does not accept a host and connects only to loopback CDP.
3. The Agent attaches a CDP session to an existing target and relays to the Console only the same
   screencast and permitted Input operations as the existing foundation. Raw CDP, cookies, storage
   and response bodies are not exposed.
4. Detaching releases only AF's CDP session and viewer. It does not close the external owner's Page,
   BrowserContext, profile or Chromium process.
5. The loopback restriction on the ordinary browser pane is unchanged. An attachment can display an
   external HTTP(S) Page the owner opened, but it does not become a general browser into which
   arbitrary URLs can be typed from the Console.
6. Add target enumeration, attach, handoff request, state query and detach to the local AF MCP. On
   the assistant chat surface, target enumeration and state query are advertised as read, while
   attach/handoff/detach — which create a display and input route — are advertised only to
   `af_write`. On the interactive session surface, mcpreg's builtin `af` is started with
   `--self-report --chromium-attach` and advertises only `af_report` plus the seven Chromium tools.
   Fleet-wide read/write authority is not handed over.
7. An attach returns a short-lived opaque attachment ID and a Console action URL. The agent presents
   the URL to the user as a Markdown link, unchanged.
8. MCP or a server push alone never changes the Console layout. When the user clicks the action URL
   once, that Console client's layout store creates the pane by the normal route. That click is taken
   as the explicit user intent to display it.
9. "Completed" and "cancelled" on a handoff are the user's self-report and are not treated as
   evidence that the operation on the external site succeeded.
10. v1 targets Chromium/CDP only and is not generalised to Firefox/WebKit or arbitrary GUIs.
11. Based on the P0 measurements, the shared CDP core goes as far as request/response multiplexing
    and a bounded event queue. Only pipe/WebSocket framing and closing connection-owned resources are
    split into a transport adapter; discovery and Page ownership are not shared.
12. Screencasts from several CDP clients work independently per target session. AF starts, stops,
    ACKs and detaches only its own session, and v1's AF limits a target to one active attachment to
    avoid input contention.
13. An attachment's rendering and input are not mixed into the existing `/ws/browser` but separated
    into a dedicated `/ws/browser-attachments` namespace. The CP relay core may be shared, but the
    Agent handler and the permitted message set are separate.
14. An MCP tool result carries an `outputSchema`, short JSON text, and `structuredContent` with the
    same value. Because CLIs that do not pass structured values to the model actually exist, the text
    fallback is canonical and mandatory for every CLI, and there is no per-client branching.
15. An action link is placed in the order: an existing attachment's focus, a blank pane, a new slot.
    At the pane limit, another pane is not silently overwritten — the user chooses between replacing
    the active pane and cancelling, with cancel as the default focus.
16. On the interactive session surface too, the advertised set is the authorisation boundary for
    calls. `--self-report` alone preserves backward compatibility with the single `af_report` tool,
    and `--chromium-attach` is effective only in combination with `--self-report`. Permitting the
    Chromium change tools still rejects speculative calls to `list_my_sessions`, `send_to_session`
    and the like.
17. An interactive session is already an actor that can use the shell, Playwright and loopback CDP
    inside the same workspace, and displaying an attachment requires membership authentication plus a
    user click on the action link, so no additional Console opt-in is imposed. The `af_write` opt-in
    for the assistant, which performs broad fleet operations headlessly, is maintained as before.

18. An action link does not presume "navigating to a Console route". The route actually used most is
    clicking a Markdown link in the mirror, and there it is judged as an action link **before** file
    link resolution, opening the pane in place without navigating. Both routes go through the same
    function; there is no second implementation. The CP returns the Console shell on the action path
    (a static catch-all's 404 had wiped out the whole link route).
19. Chromium's remote-debugging port is not fixed. `--remote-debugging-port=0` plus
    `DevToolsActivePort` is the startup contract, and an attach identifies the individual by
    `browserId`. If several processes are listening on the same port, discovery fails with
    `cdp_port_ambiguous`. Chromium does not fail on a collision — it escapes to the other loopback
    family — so we do not depend on "the listen will fail" (§53.16). This check is advisory and
    passes when procfs cannot decide.
20. Listing attachments (`GET /api/browser/attachments`) is used only as the Console's recovery
    entrance. It is not made a second distribution route in place of the action link, and it is
    neither pushed nor polled.

## Options not taken

### Lift the external-navigation restriction on the existing browser pane

The existing Page is Agent-owned and is not the same as the external automation's Page, profile and
session. Lifting the restriction alone would not let it share the same Page as Playwright, and it
would lose the localhost boundary. The ownership modes are kept separate.

### Expose raw CDP to the Console or to MCP

CDP is a strong privilege, including arbitrary JS execution, cookies, network and target
management. What the Console needs is rendering and limited input, so the Agent degrades it to a
high-level wire.

### Xvfb + VNC/noVNC

It can display things other than Chromium, but it adds a daemon, whole-screen transport,
authorisation, resolution, clipboard and process ownership. It is overkill for AF, which already has
a CDP screencast foundation.

Re-examined on 2026-08-08 from the angle of "could we drop synthetic CDP input and use the OS's own
input handling?", and after measurement on the spot we decided to **maintain this judgement**. The
deciding factor is not cost but ownership: VNC requires the owner to start their own Chromium
headful on AF's virtual display, whereas §53.2 has the owner, not AF, starting Chromium. The
measurements and the discussion are in
[docs/53 §53.18](../log/53-chromium-attach-view.md#5318-rdp--vnc-転送の検討2026-08-08-実測).

### Connect to the Playwright protocol

Playwright's `launch_server` protocol is not CDP and is strongly coupled to a language and a
version. Chromium's standard loopback CDP is the minimal contract with the owner.

### Open the pane automatically on the MCP call

The layout is local state per user, per tenant and per browser client, and the Agent cannot decide
which client to display on. It would also be an unexpected change of screen. We return an
authenticated action link and open on a user click.

### Build the business script into AF

AF would end up owning site-specific selectors, credentials, terms and posting state. AF provides
only a generic human-operation surface and the handoff; the business logic stays in each project.

### Build a separate `af-browser` MCP server for interactive sessions

It uses the same Agent authentication and the same Chromium handler, but it adds a reserved name, a
materialisation ledger, per-CLI configuration, and one more server the user can see. The same
privilege separation is achievable with a startup flag on the existing builtin `af` plus a strict
advertised/callable set, so we do not split off a separate server.

### Extend `--self-report` to cover Chromium unconditionally

That would break the meaning and backward compatibility of an existing flag that means "one
self-report tool". An independent `--chromium-attach` is added, and only the combination of both
flags is materialised for the current interactive-session builtin.

## Consequences

- BrowserManager's request/response multiplexing, bounded event queue and screencast/input parts are
  reused, and pipe/WebSocket framing and resource ownership need splitting into a transport adapter.
  The `*pipeCDP` type leak in `browserCDPEvent` is fixed too.
- The Console layout gains `browserAttach` content and an action route, but the port, target and
  external URL are not persisted.
- The CP and the Agent gain `/ws/browser-attachments`. The lookup and wire contract of the existing
  `/ws/browser` are unchanged.
- Because an attachment may display an externally authenticated screen, frames, input, URLs, titles
  and console bodies are not persisted.
- Simultaneous input from the owner and a human contends destructively. Stopping the owner before
  `user-control` is a mandatory condition of the usage contract, and v1 displays and enforces
  `view-only/user-control/locked` but neither detects nor enforces the stop itself.
- MCP returns the text fallback and the structured result redundantly. Claude/Codex/Copilot can use
  structured values, while opencode/Cursor/Kiro pass only text to the model as of P0.
- The interactive session's builtin `af` is no longer `af_report`-only, but fleet tools other than
  the seven Chromium ones continue to be refused, both in advertising and in calls. ADR 0035's
  property that "the advertised set is the scope boundary" is maintained.
- Automatic completion reporting is possible in future, but the MVP is built on the state query, and
  a persistent handoff ledger is a later stage.
