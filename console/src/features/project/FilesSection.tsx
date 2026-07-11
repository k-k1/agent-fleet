// FilesSection — the rail-bottom global file browser: one ProjectFiles tree
// rooted at "repos", so the top level is the working copies themselves. Default
// collapsed (the tree is on-demand); a reveal request (a clone just landed, a
// repo row's フォルダを開く) opens the section so the target is visible — hence
// the controlled Section + own persistence (the old console's af-section-files
// key, so an existing collapse choice carries over).
import { useEffect, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useFilesStore } from "../files/store.ts";
import { ProjectFiles } from "./ProjectFiles.tsx";

const KEY = "af-section-files";

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
    </Section>
  );
}
