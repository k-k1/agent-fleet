// Return the Markdown document body after a YAML front matter block.  Markdown
// renderers normally treat front matter as document metadata, not prose, but
// marked does not consume it by default.
//
// Only a complete block at byte zero is removed. This deliberately leaves
// thematic breaks elsewhere (and incomplete front matter while a chat message
// is streaming) untouched.
export function withoutYamlFrontMatter(source: string): string {
  const match = source.match(/^\uFEFF?---[\t ]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)/);
  return match ? source.slice(match[0].length) : source;
}
