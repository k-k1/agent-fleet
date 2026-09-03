import { useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// ChatCopyButton copies the reply's RAW Markdown (not the rendered HTML) to the
// clipboard — same behavior as MirrorView's CopyButton.
export function ChatCopyButton({ text }: { text: string }) {
  const tr = useT();
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context / permission) — no-op */
    }
  };
  return (
    <button type="button" className="ghost cm-copy" title={tr("chat.copy_md_title")} onClick={copy}>
      <Icon name={done ? "check" : "copy"} /> {done ? tr("chat.copied") : tr("chat.copy")}
    </button>
  );
}
