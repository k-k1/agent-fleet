// Which session made a picture — the one thing a generated image's own record cannot say.
//
// The sidecar's `agent` field is the AGENT BUILD that wrote the graph (imagegen/props.go), not
// the session, and nothing else in the record names one. What does name it is WHERE the picture
// lives: generate_image files every session's output under a folder of its own, and the session
// list carries that folder as `generatedImagesPath` (ADR 0080 decision 8).
//
// So the match is folder-to-folder, and the folder comes off the wire rather than being derived
// here: it is named by a uuidV5 over the Agent's own inputs, and a second copy of that rule on
// this side would drift silently the day the Agent's changes (types/session.ts says the same).
//
// The pure half, kept free of the stores so the two rules that are easy to get quietly wrong —
// whole folder, never the empty one — are covered by node tests. The hook and the jump live in
// useGeneratingSession.ts.
import type { Session } from "../../types/session.ts";

/** The folder a browse-root-relative file path sits in ("" for a file at the root). */
export function folderOf(filePath: string): string {
  const at = filePath.lastIndexOf("/");
  return at < 0 ? "" : filePath.slice(0, at);
}

/**
 * The session whose generated-images folder IS `dir`, or undefined.
 *
 * Equality, not a prefix test: a prefix would hand a session every picture in every folder
 * beneath it. And `generatedImagesPath` is absent on a session that has generated nothing, so
 * it is guarded explicitly — without that, a file at the browse root (`dir` = "") would match
 * every such session at once.
 *
 * A picture whose session has been deleted or archived resolves to nothing, and that is the
 * honest answer: the callers draw no control at all rather than one that leads nowhere.
 */
export function sessionOfGeneratedFolder(sessions: Session[], dir: string): Session | undefined {
  if (!dir) return undefined;
  return sessions.find((s) => !!s.generatedImagesPath && s.generatedImagesPath === dir);
}
