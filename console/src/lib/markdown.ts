import { load } from "js-yaml";

export interface YamlFrontMatter {
  attributes: Record<string, unknown>;
  body: string;
}

// Read a YAML front matter block at the start of a Markdown document. Marked
// does not consume front matter by default, so the viewer handles it before
// rendering the body.
//
// Only a complete, mapping-shaped block at byte zero is recognized. This leaves
// thematic breaks, invalid YAML, and incomplete front matter (while a chat
// message is streaming) as ordinary Markdown.
export function splitYamlFrontMatter(source: string): YamlFrontMatter | null {
  const match = source.match(/^\uFEFF?---[\t ]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)/);
  if (!match) return null;
  const yaml = match[0]
    .replace(/^\uFEFF?---[\t ]*\r?\n/, "")
    .replace(/\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)$/, "");
  try {
    const attributes = load(yaml);
    if (!attributes || Array.isArray(attributes) || typeof attributes !== "object") return null;
    return { attributes: attributes as Record<string, unknown>, body: source.slice(match[0].length) };
  } catch {
    return null;
  }
}
