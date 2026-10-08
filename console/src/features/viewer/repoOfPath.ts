// repoOfFilePath names the working copy a home-relative file path lives in ("repos/<name>/…" →
// "<name>"), or null for a file outside ~/repos. The name is what the Markdown renderer looks up
// in the repository list to find the origin a `#N` is read against; a path it cannot place gets
// no ticket links at all rather than links against a guessed repository.
export function repoOfFilePath(filePath: string): string | null {
  const m = /^(?:\.\/)?repos\/([^/]+)\/./.exec(filePath);
  return m ? m[1] : null;
}
