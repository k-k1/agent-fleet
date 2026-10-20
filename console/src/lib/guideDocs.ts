// Opens a page of the shipped markdown guide as a file pane.
//
// The container receives the guide/ tree and nothing else, so the shipped layout starts at
// the shelves (member/, admin/, …) with no docs/ level. English is canonical (`name.md`); the
// Japanese page sits beside it as `name.ja.md`. The file viewer has no heading-anchor
// target, so callers open the page, not a section.
// Native runtime without a rootfs stages the guide elsewhere (AGENT_DOCS_DIR) and the agent
// refuses the fixed path there; the workspace payload carries that root as `docsRoot`, and
// every other runtime leaves it empty so the fixed path stands.
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useLayoutStore } from "../layout/store.ts";
import { getLocale } from "./i18n/index.ts";

const GUIDE_ROOT = "/usr/local/share/agent-fleet/docs";

// `page` is the path under the guide root without extension, e.g. "README" or "member/10-integrations".
export function openGuidePage(page: string): void {
  const root = useWorkspaceStore.getState().docsRoot.replace(/\/+$/, "") || GUIDE_ROOT;
  useLayoutStore.getState().openTargetInNew(
    { content: { kind: "file", filePath: `${root}/${page}${getLocale() === "ja" ? ".ja" : ""}.md` } },
    true,
  );
}
