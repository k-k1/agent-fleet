// An AskUserQuestion option's `preview` — the mockup / code snippet an agent attaches so
// the choices can be COMPARED before one is picked (two layouts, two implementations of
// the same function). It is the material the question is about, so the card shows it next
// to the label instead of only the CLI seeing it.
//
// The CLI renders it as markdown in a monospace box. The mirror keeps it verbatim in a
// monospace block, because for these previews the whitespace IS the content — an ASCII
// box drawing re-flowed as prose is garbage. The one markdown artifact worth removing is
// a fence wrapping the WHOLE preview: that's packaging (the agent reaching for a code
// block to get monospace), not part of the mockup. A preview with several fences keeps
// them — there the fences separate variants and dropping them would merge two snippets.

export function previewBody(raw: string | undefined): string {
  const text = (raw || "").replace(/\s+$/, "");
  if (!text.trim()) return "";
  // ```lang\n … \n``` covering everything — the capture keeps inner indentation intact.
  // The `$` anchor makes the lazy body backtrack out to the LAST fence, so a preview of
  // several blocks would otherwise match as one and lose its inner markers: a body that
  // still holds a fence line means this was never a single wrapper.
  const fenced = /^\s*```[^\n]*\n([\s\S]*?)\n?```$/.exec(text);
  const body = fenced && !/^\s*```/m.test(fenced[1]) ? fenced[1] : text;
  return body.replace(/\s+$/, "");
}
