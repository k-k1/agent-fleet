// Agent memory > Import & export: a hub of cards, one per source, grouped Import / Export /
// Maintenance. Start opens that source's wizard in place of the hub; Cancel, or Back to the hub
// once it has finished, returns here. Only one wizard exists at a time, and leaving it unmounts
// it, so no wizard's state can reach another's.
//
// The project move is always offered (its wizard says when there is nothing to move) rather than
// loading the project list here just to decide whether to draw a card.
//
// A codex import (#1950, on hold) goes in as one more entry of IMPORTS below, with its wizard in
// WIZARDS; nothing else in this file changes.

import { useState, type ReactElement } from "react";
import { Button } from "../../../../ui/Button.tsx";
import { useT, type MsgKey } from "../../../../lib/i18n/index.ts";
import { ClaudeImportPanel } from "../memoryClaudeImport.tsx";
import { MuseImportPanel } from "../memoryMuseImport.tsx";
import { ClaudeExportPanel } from "../memoryClaudeExport.tsx";
import { ProjectMovePanel } from "../memoryProjectMove.tsx";
import { BundleImportWizard } from "../memoryBundleImport.tsx";

type Source = "claude" | "muse" | "bundle" | "writeback" | "move";
type Key = MsgKey;

interface Card {
  source: Source;
  title: Key;
  desc: Key;
}

const IMPORTS: Card[] = [
  { source: "claude", title: "mem.ci_title", desc: "mem.ie_desc_claude" },
  { source: "muse", title: "mem.mi_title", desc: "mem.ie_desc_muse" },
  { source: "bundle", title: "mem.bi_title", desc: "mem.ie_desc_bundle" },
];
const EXPORTS: Card[] = [{ source: "writeback", title: "mem.ce_title", desc: "mem.ie_desc_writeback" }];
const MAINTENANCE: Card[] = [{ source: "move", title: "mem.pm_title", desc: "mem.ie_desc_move" }];

export function ImportExportView({ reload, onChanged }: { reload: number; onChanged: () => void }) {
  const tr = useT();
  const [active, setActive] = useState<Source | null>(null);
  const close = () => setActive(null);

  const WIZARDS: Record<Source, ReactElement> = {
    claude: <ClaudeImportPanel reload={reload} onChanged={onChanged} onClose={close} />,
    muse: <MuseImportPanel reload={reload} onChanged={onChanged} onClose={close} />,
    bundle: <BundleImportWizard onChanged={onChanged} onClose={close} />,
    writeback: <ClaudeExportPanel reload={reload} onClose={close} />,
    move: <ProjectMovePanel reload={reload} onChanged={onChanged} onClose={close} />,
  };
  if (active) return WIZARDS[active];

  const group = (title: Key, cards: Card[]) => (
    <section className="mem-ie-group" aria-label={tr(title)}>
      <h3 className="mem-ie-group-title">{tr(title)}</h3>
      <div className="mem-ie-cards">
        {cards.map((c) => (
          <div key={c.source} className="mem-ie-card">
            <h4>{tr(c.title)}</h4>
            <p className="muted ds-hint">{tr(c.desc)}</p>
            <Button aria-label={`${tr("mem.ie_start")}: ${tr(c.title)}`} onClick={() => setActive(c.source)}>
              {tr("mem.ie_start")}
            </Button>
          </div>
        ))}
      </div>
    </section>
  );
  return (
    <>
      {group("mem.ie_group_import", IMPORTS)}
      {group("mem.ie_group_export", EXPORTS)}
      {group("mem.ie_group_maintenance", MAINTENANCE)}
    </>
  );
}
