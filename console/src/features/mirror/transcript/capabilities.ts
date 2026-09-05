// transcript/capabilities — what the surrounding view lets the reader DO.
//
// The same transcript is rendered in two very different situations:
//
//   mirror (MirrorView)        the session's owner, inside their own Workspace. Every
//                              coordinate is theirs to open: files, diffs, pasted images,
//                              plan panes, fork points.
//   shared view (SharedSessionView)
//                              a recipient reading someone else's session through the
//                              control-plane's allowlist DTO (docs/log/59 §3), which strips
//                              cwd / path / filePath and every structured coordinate.
//                              There is nothing local to open and nothing to drive.
//
// Rather than thread a `readOnly` flag (which invites "show the button but disable it"),
// each affordance is an OPTIONAL capability.
//
//   THE RULE: a capability that is absent means the affordance is NOT RENDERED.
//
// Never render a control that cannot work. A recipient must not be shown a fork button
// that 404s or a diff row that opens nothing — an inert control reads as a bug, and a
// disabled one invites a support question. Blocks degrade to a self-contained rendering
// instead (e.g. ToolTrace expands the diff inline when `openDiff` is missing).

import type { Group, Part, TurnTtsWiring } from "./types.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";

export interface TranscriptCaps {
  /** Display name of the agent answering (registry name, e.g. "Claude"). */
  agentName: string;
  /**
   * Display name for user-role turns. Absent → "You" (「あなた」), which is right only
   * when the reader IS the person who typed them (the mirror). A recipient is reading
   * somebody else's conversation, so the shared view passes the owner's login id.
   */
  userName?: string;

  // ── Content resolution ────────────────────────────────────────────────────────
  /** Repo the paths in Markdown resolve against, for click-to-open links. */
  repo?: string | null;
  /**
   * Fetch a pasted image's bytes by transcript name. Absent → no thumbnails
   * (the shared DTO drops attachment paths, and the bytes live in the owner's
   * Workspace, so there is nothing a recipient could fetch).
   */
  loadPastedImage?: (name: string) => Promise<Blob | null>;
  /** Direct <img src> for a shared file, full size. Absent → FileCard shows no picture. */
  fileURL?: (path: string) => string;
  /**
   * The same bytes downscaled for a card-sized <img>. Absent → the card falls back to
   * fileURL, which is correct but pulls the original (a shared render is megabytes to
   * paint 190x240 px). Never used for the lightbox: enlarging must show the real file.
   */
  thumbURL?: (path: string) => string;

  // ── Navigation ────────────────────────────────────────────────────────────────
  /** Open a file in its own pane. Absent → UserFileBlock is not rendered. */
  openFile?: (path: string, line?: number, column?: number) => void;
  /** Open an image in the lightbox. */
  openImage?: (url: string) => void;
  /** Open an edit's before/after in a diff pane. Absent → ToolTrace expands inline. */
  openDiff?: (p: Part) => void;
  /** Open a plan's full Markdown in a pane. Absent → PlanBlock expands inline. */
  openPlan?: (plan: string) => void;

  // ── Owner-only actions ────────────────────────────────────────────────────────
  /**
   * Session name, used to key per-session UI state (ToDo open/dismissed) and the plan
   * review comments. Absent → that state is not persisted and no comments are shown.
   */
  session?: string;
  /** Send the plan review comments collected in the doc pane. */
  sendPlanComments?: (plan: string) => void;
  /** Why sending is blocked ("" / undefined = allowed). */
  planSendDisabled?: string;
  /** Branch from a past user turn (docs/log/55). Absent → no turn offers it. */
  forkAt?: (turn: Group) => void;
  /**
   * Jump to Settings > Agents (「設定 > エージェント」) after an auth failure. Absent →
   * ErrorBlock shows the agent's own text without a fix-it link: a recipient cannot
   * re-authenticate somebody else's agent, so offering the route would be a dead end.
   */
  onReauth?: () => void;
  /** Karaoke read-aloud wiring (docs/log/24). Absent → no per-turn TTS buttons. */
  tts?: TurnTtsWiring;
  /**
   * Marks drawn over the conversation (docs/log/69 / ADR 0050). Absent → no marks are
   * painted and no selection pill appears. "May read but may not draw" (a read-only share
   * recipient) is carried by the wiring's own `canEdit`: a recipient still needs the marks
   * rendered, so that case cannot be split on the presence of this capability alone.
   */
  marks?: TranscriptMarksWiring;

  // ── Display preferences ───────────────────────────────────────────────────────
  /** Show the agent's chain-of-thought expanded (per-kind behaviour setting, default off). */
  expandThinking?: boolean;
  /** Was this plan optimistically rejected locally? Defaults to never. */
  isRejectedPlan?: (plan: string) => boolean;
  /** Heaviest turn's spend, used to scale the per-turn bar. 0 → no bars. */
  maxSpend?: number;
}
