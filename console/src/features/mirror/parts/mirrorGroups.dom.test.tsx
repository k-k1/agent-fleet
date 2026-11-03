// Runs in the dom project only because the module graph touches localStorage at import.
import { describe, expect, it } from "vitest";
import { splitPastedImages } from "../../../lib/pastedImages.ts";
import { queueEntries } from "../stopQueue.ts";
import { STUDIO_SIGNAL_PREFIX, STUDIO_SIGNAL_SUFFIX } from "../transcript/model.ts";
import { groupWithQueue } from "./mirrorGroups.ts";

const path = (n: number) => `/home/dev/.cache/agent-fleet/pasted/k/paste-${n}.png`;
const echoText = (n: number) => `look Open the following file(s) with the Read tool: ${path(n)}`;
// What claude's queue holds for the same send: the Agent typed the path in backticks.
const typedText = (n: number) => `look Open the following file(s) with the Read tool: \`${path(n)}\``;

describe("groupWithQueue pairs an echo with its queue entry", () => {
  it("matches a queue entry whose attachment paths claude received in backticks", () => {
    const g = groupWithQueue([], [{ id: 1, text: echoText(1), sinceIdx: 0 }], queueEntries(null, [typedText(1)]));
    expect(g).toHaveLength(1);
    expect(g[0].folded).toBe(1);
    expect(g[0].queued).toBe(true);
    expect(splitPastedImages(g[0].text)).toEqual({ text: "look", images: ["paste-1.png"], files: [] });
  });

  it("matches a queue entry that carries the image studio signal line", () => {
    const signal = `${STUDIO_SIGNAL_PREFIX}x ${STUDIO_SIGNAL_SUFFIX}`;
    // No attachment: with one, the instruction cut in splitPastedImages would drop the line anyway.
    const g = groupWithQueue([], [{ id: 1, text: "draw a cat", sinceIdx: 0 }], queueEntries(null, ["draw a cat\n" + signal]));
    expect(g).toHaveLength(1);
    expect(g[0].folded).toBe(1);
  });

  it("does not pair prompts with the same words but different attachments", () => {
    const g = groupWithQueue([], [{ id: 1, text: echoText(1), sinceIdx: 0 }], queueEntries(null, [typedText(2)]));
    const images = g.flatMap((b) => splitPastedImages(b.text).images);
    expect(images.sort()).toEqual(["paste-1.png", "paste-2.png"]);
  });

  it("does not pair prompts whose words differ after an instruction wording they quote", () => {
    const att = (lang: string) =>
      `Translate "Look at the following file(s):" into ${lang}. Open the following file(s) with the Read tool: ${path(1)}`;
    const quoted = (lang: string) => att(lang).replace(path(1), `\`${path(1)}\``);
    const g = groupWithQueue([], [{ id: 1, text: att("French"), sinceIdx: 0 }], queueEntries(null, [quoted("Japanese"), quoted("French")]));
    const texts = g.map((b) => b.text);
    expect(texts.some((t) => t.includes("Japanese"))).toBe(true);
    // The French echo took the French entry: one French text, not the echo plus a leftover row.
    expect(texts.join("\n").split("French").length - 1).toBe(1);
  });

  it("keeps a path the member backticked in their own words apart from the bare one", () => {
    const script = "/home/dev/.cache/agent-fleet/pasted/k/paste-2-check.sh";
    const bare = `Run this shell command:\necho ${script}`;
    const quoted = `Run this shell command:\necho \`${script}\``;
    const g = groupWithQueue([], [{ id: 1, text: bare, sinceIdx: 0 }], queueEntries(null, [quoted, bare]));
    const all = g.map((b) => b.text).join("\n");
    expect(all).toContain(quoted);
    expect(all.split(script).length - 1).toBe(2);
  });
});
