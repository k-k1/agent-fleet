// prompthelp — layer B of decision 7: one message the Console composes, one `askAssistant()`
// call the user pressed, one proposal shown before anything lands.
//
// The message is built HERE, not in the modal, for the same reason MemoTidyModal's prompt is
// a pure function: it is the part with a correct answer (which facts the model needs, and in
// what order), and it is the part a test can hold. The modal owns only the preview.
//
// Two rules the ADR states and this file enforces by shape:
//   - nothing is applied on its own — `parseProposal` returns data, never a draft;
//   - no model call happens without a press, so this module never fetches on import.
import type { ImagegenLora, ImagegenModel } from "./wire.ts";
import { loraTriggers } from "./wire.ts";
import { familyCard } from "./families.ts";

export interface PromptHelpContext {
  /** What the person typed in their own language — never translated before sending. */
  intent: string;
  model: ImagegenModel | null;
  /** The LoRAs currently selected, so their triggers are part of the brief. */
  loras: ImagegenLora[];
  /** The administrator's negative for this row, and the deployment-wide one. */
  rowNegative?: string;
  alwaysNegative?: string;
  /** Whether the family samples with a negative branch at all (status `knobs`). */
  negativeReaches: boolean;
}

export interface PromptProposal {
  prompt: string;
  negative: string;
  note: string;
}

/**
 * The single message sent to `POST api/chat/ask`.
 *
 * English, because it is an instruction to a model and the user's own words are quoted
 * inside it in whatever language they typed — the model answers in the prompt dialect the
 * checkpoint wants, which is not the user's language either way.
 */
export function buildPromptHelpMessage(ctx: PromptHelpContext): string {
  const card = familyCard(ctx.model?.family);
  const lines: string[] = [];
  lines.push(
    "You are helping compose a text-to-image prompt for a specific Stable Diffusion / Flux checkpoint.",
    "Answer with JSON only, no prose and no code fence, in the shape",
    '{"prompt": "...", "negative": "...", "note": "..."}.',
    "",
  );
  if (ctx.model) {
    lines.push(`Checkpoint: ${ctx.model.id}`);
    if (ctx.model.description) lines.push(`Checkpoint description: ${ctx.model.description}`);
    if (ctx.model.family) lines.push(`Family: ${ctx.model.family}`);
  }
  if (card) {
    lines.push(
      card.dialect === "tags"
        ? "Prompt dialect: comma-separated danbooru-style tags, most important first. Do not write sentences."
        : "Prompt dialect: one or two natural-language sentences describing the picture. Do not write a tag list.",
    );
    if (card.quality.length) {
      lines.push(`Quality prefixes this dialect uses (include one only if it fits): ${card.quality.join(" | ")}`);
    }
  }
  const triggers = ctx.loras.flatMap((l) => loraTriggers(l)).filter(Boolean);
  if (ctx.loras.length) {
    lines.push(`LoRAs in use: ${ctx.loras.map((l) => l.name).join(", ")}`);
    if (triggers.length) {
      lines.push(`Their trigger words MUST appear in the prompt verbatim: ${triggers.join(", ")}`);
    }
  }
  if (ctx.negativeReaches) {
    if (ctx.rowNegative) lines.push(`A negative prompt is already applied by the administrator: ${ctx.rowNegative}`);
    if (ctx.alwaysNegative) lines.push(`And deployment-wide: ${ctx.alwaysNegative}`);
    lines.push("Put in `negative` only what those do not already cover; leave it empty if nothing is needed.");
  } else {
    lines.push("This family ignores negative prompts. Return an empty string for `negative`.");
  }
  lines.push(
    "",
    "`note` is one short sentence for the person, in the language they wrote in, saying what you assumed.",
    "",
    "What they want:",
    ctx.intent.slice(0, 4000),
  );
  return lines.join("\n");
}

/** Strip a ```json fence, which every model adds back however firmly it is told not to. */
function unfence(reply: string): string {
  const m = /```(?:json)?\s*([\s\S]*?)```/i.exec(reply);
  return (m ? m[1] : reply).trim();
}

/**
 * Parse the answer into a proposal, or null.
 *
 * Null means "show the parse failure and offer to retry" — never "apply what could be
 * read". A half-parsed prompt silently replacing the user's text is exactly the silent edit
 * the ADR forbids. An object with no usable `prompt` is also null: a proposal whose only
 * content is a note has nothing to accept.
 */
export function parseProposal(reply: string): PromptProposal | null {
  const body = unfence(reply || "");
  // A model that ignored "JSON only" wraps the object in a sentence; take the outermost
  // braces rather than failing on the wrapper.
  const start = body.indexOf("{");
  const end = body.lastIndexOf("}");
  if (start < 0 || end <= start) return null;
  let obj: Record<string, unknown>;
  try {
    obj = JSON.parse(body.slice(start, end + 1)) as Record<string, unknown>;
  } catch {
    return null;
  }
  if (!obj || typeof obj !== "object") return null;
  const s = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
  const prompt = s(obj.prompt);
  if (!prompt) return null;
  return { prompt, negative: s(obj.negative), note: s(obj.note) };
}
