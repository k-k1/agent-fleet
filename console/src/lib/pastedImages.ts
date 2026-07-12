// Pasted-image helpers shared by the terminal mirror (MirrorView) and the assistant
// chat (ChatView) composers. All agents ride the same flow: none of the headless CLI
// agents has native image input in this path, so we upload the image, then reference
// its saved absolute path in the prompt and let the agent open it — claude via its
// Read tool, codex via view_image (fires on a plain path mention), opencode via its
// own tools (vision quality is model-dependent; a non-vision model declines honestly).

// Machine-facing instructions appended to a prompt when image(s) are attached. English
// (not Japanese) because they're CLI-agent instructions — hidden from the chat bubble
// by splitPastedImages, and English tokenizes cheaper. claude gets its Read-tool
// wording; other agents a tool-neutral one (codex has no Read tool).
export const IMG_PROMPT = "Open the following image(s) with the Read tool:";
export const IMG_PROMPT_GENERIC = "Look at the following image file(s):";
// Prior (Japanese) wording, still stripped from older turns so their bubbles stay clean.
export const IMG_PROMPT_LEGACY = "次の画像を Read ツールで開いて確認してください:";
const IMG_PROMPTS = [IMG_PROMPT, IMG_PROMPT_GENERIC, IMG_PROMPT_LEGACY];
// Matches our pasted-image paths (…/pasted/<key>/paste-<n>.<ext>), capturing the basename.
export const PASTE_PATH_RE = /\S*\/pasted\/[^\s/]+\/(paste-\d+\.(?:png|jpe?g|gif|webp))/g;

// splitPastedImages pulls the pasted-image basenames out of a user turn's text and returns
// the text with the appended image instruction removed (so the bubble shows the user's
// words + thumbnails, not the machine-facing paths).
export function splitPastedImages(text: string): { text: string; images: string[] } {
  const images: string[] = [];
  PASTE_PATH_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = PASTE_PATH_RE.exec(text))) images.push(m[1]);
  if (!images.length) return { text, images };
  // Trim the instruction (any known wording) plus the trailing paths; fall back to
  // stripping the paths alone.
  const idx = IMG_PROMPTS.map((p) => text.indexOf(p)).filter((i) => i >= 0)[0] ?? -1;
  const cleaned = (idx >= 0 ? text.slice(0, idx) : text.replace(PASTE_PATH_RE, "")).trim();
  return { text: cleaned, images };
}

// buildImagePrompt composes the sent prompt: the user's words + the kind's instruction +
// the space-joined absolute paths (kept on ONE line so send-keys can't submit early).
export function buildImagePrompt(text: string, paths: string[], kind = "claude"): string {
  if (!paths.length) return text;
  const instr = (kind === "claude" ? IMG_PROMPT : IMG_PROMPT_GENERIC) + " " + paths.join(" ");
  return text ? text + " " + instr : instr;
}
