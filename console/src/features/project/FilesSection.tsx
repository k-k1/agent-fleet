// FilesSection — the rail-bottom global file browser. The primary tree stays
// rooted at "repos" (the working copies are the daily objects, one level deep),
// and a separate collapsed "home" disclosure below it lazily mounts a second
// tree rooted at home ("") for everything else the agent may browse (the
// backend denylists secrets). Section default collapsed (the trees are
// on-demand); a reveal request (a clone just landed, a repo row's
// フォルダを開く) opens the section so the target is visible — hence the
// controlled Section + own persistence (the old console's af-section-files key,
// so an existing collapse choice carries over).
import { useEffect, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useFilesStore } from "../files/store.ts";
import { ProjectFiles } from "./ProjectFiles.tsx";

const KEY = "af-section-files";
const HOME_KEY = "af-files-home";

export function FilesSection() {
  const reveal = useFilesStore((s) => s.reveal);
  const bump = useFilesStore((s) => s.bump);
  const [open, setOpen] = useState(() => localStorage.getItem(KEY) === "1");
  const set = (v: boolean) => {
    setOpen(v);
    try {
      localStorage.setItem(KEY, v ? "1" : "0");
    } catch {}
  };
  const [homeOpen, setHomeOpen] = useState(() => localStorage.getItem(HOME_KEY) === "1");
  const setHome = (v: boolean) => {
    setHomeOpen(v);
    try {
      localStorage.setItem(HOME_KEY, v ? "1" : "0");
    } catch {}
  };

  useEffect(() => {
    if (reveal.path) set(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal.n]);

  return (
    <Section
      title="ファイル"
      icon="files"
      open={open}
      onToggle={() => set(!open)}
      actions={<IconButton icon="refresh" label="更新" onClick={bump} />}
    >
      <ProjectFiles root="repos" />
      {/* home: the rest of ~ (repos/ shows again inside — harmless). Lazy:
          mounted only while open. */}
      <button
        type="button"
        className="files-home-btn"
        onClick={() => setHome(!homeOpen)}
        aria-expanded={homeOpen}
        title={homeOpen ? "home を折りたたむ" : "home を展開（~ 全体をブラウズ）"}
      >
        <Icon name={homeOpen ? "chevron-down" : "chevron-right"} />
        <Icon name="home" /> home
      </button>
      {homeOpen && <ProjectFiles root="" />}
    </Section>
  );
}
