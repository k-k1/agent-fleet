// composerSend — what one composer send puts on the wire, and what the optimistic echo shows.
//
// Split out of MirrorView.send() so the order is testable without mounting the mirror: a TUI
// send weaves the attachment paths into the text (buildImagePrompt), and the image studio's
// signal (ADR 0100 decision 5) must come AFTER that, as the last line — the transcript strips
// only a last line. The echo never carries the signal: it shows the member's own words.
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { withStudioSignal } from "./transcript/model.ts";

export interface ComposerSend {
  /** The optimistic echo's text. */
  echo: string;
  /** The body POSTed to the session. */
  wire: string;
  /** Managed passes attachments as wire attachments; a TUI send has them in the text. */
  attachments?: string[];
}

export function composerSend(text: string, paths: string[], kind: string, managed: boolean, signal = ""): ComposerSend {
  const echo = (managed ? text : buildImagePrompt(text, paths, kind)).trim();
  return { echo, wire: withStudioSignal(echo, signal), ...(managed ? { attachments: paths } : {}) };
}
