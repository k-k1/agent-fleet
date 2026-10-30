// PathMenuItems — the actions on a file or folder that do not depend on where the path was
// found. The Files rail menu and the Markdown path-link menu (PathLinkMenu.tsx) both render
// them, so the item list exists once; what stays with each caller is what only it can do
// (the rail's new file / rename / delete / SVN log, the link menu's open / reveal).
import type { MouseEvent as RMouseEvent, ReactNode } from "react";
import { useCallback } from "react";
import { downloadURL, getTenant } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { imageFormat } from "../../lib/filemeta.ts";
import { downloadFolderZip } from "../files/folderZip.ts";
import { chatCreate } from "../chat/api.ts";
import { openChat } from "../chat/open.ts";
import { useFilesStore } from "../files/store.ts";
import { openGallery } from "../gallery/open.ts";
import { useReposStore } from "../repos/store.ts";
import { needsRepoList, shellLaunchTarget } from "./shellTarget.ts";
import { useStartWork } from "../repos/useStartWork.ts";
import { kindIcon } from "../../lib/sessionkind.ts";

const baseName = (p: string) => p.split("/").pop() || p;
const parentOf = (p: string) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
};

/** Copy `text` to the clipboard and say so; `label` names what was copied. */
export function usePathCopy() {
  const toast = useToast();
  return useCallback(
    (text: string, label: string) => {
      if (navigator.clipboard?.writeText)
        navigator.clipboard.writeText(text).then(
          () => toast(t("proj.copied", { label }), { kind: "success" }),
          () => toast(t("common.copy_failed")),
        );
      else toast(t("common.copy_failed"));
    },
    [toast],
  );
}

// Translate / summarize: a new assistant chat that attaches the file (and, server-side, its
// dir as knowledge) and returns a seed prompt; openChat prefills the composer, never sends —
// the user reviews and hits Enter.
export function useVerbChat() {
  const toast = useToast();
  return useCallback(
    async (filePath: string, verb: "translate" | "summarize") => {
      try {
        const title =
          verb === "translate"
            ? t("proj.verb_title.translate", { name: baseName(filePath) })
            : t("proj.verb_title.summarize", { name: baseName(filePath) });
        const c = await chatCreate("", title, { attachPath: filePath, seedVerb: verb });
        if (c && c.id) {
          autoAddToActiveWorkingSet("convs", c.id); // docs/log/52 §1
          openChat(c.id, c.seed);
        } else toast(t("send.chat_create_failed"));
      } catch {
        toast(t("send.chat_create_failed"));
      }
    },
    [toast],
  );
}

interface PathMenuItemsProps {
  /** Home-relative path, or absolute ("/…") for a place outside home. */
  path: string;
  type: "file" | "dir";
  /** Runs `fn` after closing the menu. */
  runMenu: (fn: () => void) => void;
  /** Closes the menu without an action of its own (the download anchor). */
  closeMenu: () => void;
  /** Extra items between the copy items and "open in gallery" (the rail's SVN log). */
  afterCopy?: ReactNode;
}

export function PathMenuItems({ path, type, runMenu, closeMenu, afterCopy }: PathMenuItemsProps) {
  const tr = useT();
  const toast = useToast();
  const copyText = usePathCopy();
  const openVerbChat = useVerbChat();
  const startWork = useStartWork();
  const openTarget = useLayoutStore((s) => s.openTarget);
  // Outside home (scratch, the staged docs mount) only what works there is offered: the reader,
  // the gallery, translate / summarize and the folder zip all address home-relative paths.
  const outside = path.startsWith("/");
  const isFile = type === "file";
  // "Open in gallery" is offered for a folder and for an image file — never for anything
  // else, and `imageFormat` is the only thing allowed to decide that (ADR 0080 decision 3):
  // a second extension table here would make a file the viewer shows as an image invisible
  // in the gallery. A file opens its parent folder focused on itself.
  const showGallery = !outside && (type === "dir" || !!imageFormat(path));
  const galleryDir = type === "dir" ? path : parentOf(path);
  // The containing folder of a file, for the two "folder" items. Empty for a file directly in
  // home: that folder is the whole tree, nothing worth opening.
  const folder = isFile && !outside ? parentOf(path) : "";
  // A non-image file has no "Open in gallery" of its own; its folder's gallery is offered
  // instead (an image already opens that folder, focused on itself).
  const showFolderGallery = !!folder && !showGallery;
  const openGalleryFromMenu = (e: RMouseEvent) => {
    // Modifier / middle click lands in another pane, the same rule the tree rows follow.
    const newPane = e.ctrlKey || e.metaKey || e.button === 1;
    const focus = isFile ? path : undefined;
    runMenu(() => openGallery(galleryDir, { focus, newPane }));
  };
  // A plain shell whose cwd is the folder (target rules: shellTarget.ts). A launch outside a
  // working copy is added to the active working set here, because useStartWork only does that
  // for a home session.
  const openShellHere = () =>
    runMenu(() => {
      // The tenant the click was made in: a switch while the list or the create is in flight must
      // not carry this path into the other tenant's workspace (the fetch wrapper stamps the
      // CURRENT tenant on each request) or its working sets.
      const tenant = getTenant();
      void (async () => {
        // An unread list is not "no working copies": launching on it would file a folder of a
        // working copy as a plain path, with the wrong Meta.Repo for good. Read it first.
        const store = useReposStore.getState();
        if (needsRepoList(path) && store.loadState !== "ok") await store.refresh();
        if (getTenant() !== tenant) return;
        if (needsRepoList(path) && useReposStore.getState().loadState !== "ok") {
          toast(tr("rp.launch_failed", { err: tr("err.network") }));
          return;
        }
        const { dir, subdir, inCopy } = shellLaunchTarget(path, useReposStore.getState().repos.map((r) => r.name));
        const r = await startWork({ dir, repo: "" }, { kind: "shell", driver: "", model: "", effort: "", startMode: "normal", prompt: "", title: tr("proj.shell_title", { name: baseName(path) }), images: [], worktree: false, subdir, base: "", newBranch: "" });
        if (r.ok && r.name && !inCopy && getTenant() === tenant) autoAddToActiveWorkingSet("sessions", r.name);
      })().catch(() => toast(tr("rp.launch_failed", { err: tr("err.network") })));
    });
  const openFolderGalleryFromMenu = (e: RMouseEvent) => {
    const newPane = e.ctrlKey || e.metaKey || e.button === 1;
    runMenu(() => openGallery(folder, { newPane }));
  };

  return (
    <>
      {!outside && (
        <li>
          <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(baseName(path), t("proj.name")))}>
            <Icon name="copy" /> {tr("proj.copy_name")}
          </button>
        </li>
      )}
      <li>
        <button
          type="button"
          className="ui-menu-item"
          onClick={() => runMenu(() => copyText(path, outside ? t("view.path") : t("proj.rel_path")))}
        >
          <Icon name="copy" /> {outside ? tr("view.copy_path") : tr("proj.copy_rel_path")}
        </button>
      </li>
      {afterCopy}
      {folder && (
        <li>
          <button
            type="button"
            className="ui-menu-item"
            onClick={() => runMenu(() => useFilesStore.getState().revealInFiles(folder, { focus: true }))}
          >
            <Icon name="folder" /> {tr("gallery.open_folder")}
          </button>
        </li>
      )}
      {type === "dir" && (
        <li>
          <button type="button" className="ui-menu-item" onClick={openShellHere}>
            <Icon name={kindIcon("shell")} /> {tr("proj.open_shell_here")}
          </button>
        </li>
      )}
      {showFolderGallery && (
        <li>
          <button
            type="button"
            className="ui-menu-item"
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => e.button === 1 && openFolderGalleryFromMenu(e)}
            onClick={openFolderGalleryFromMenu}
          >
            <Icon name="file-media" /> {tr("proj.open_folder_gallery")}
          </button>
        </li>
      )}
      {showGallery && (
        <li>
          <button
            type="button"
            className="ui-menu-item"
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => e.button === 1 && openGalleryFromMenu(e)}
            onClick={openGalleryFromMenu}
          >
            <Icon name="file-media" /> {tr("proj.open_gallery")}
          </button>
        </li>
      )}
      {isFile && !outside && (
        <li>
          <button
            type="button"
            className="ui-menu-item"
            onClick={() => runMenu(() => openTarget({ content: { kind: "read", filePath: path } }))}
          >
            <Icon name="book" /> {tr("proj.open_reader")}
          </button>
        </li>
      )}
      {isFile && (
        <li>
          <a className="ui-menu-item files-ctx-a" href={downloadURL(path)} download onClick={closeMenu}>
            <Icon name="cloud-download" /> {tr("proj.download")}
          </a>
        </li>
      )}
      {type === "dir" && !outside && (
        <li>
          <button
            type="button"
            className="ui-menu-item"
            title={tr("proj.download_zip_title")}
            onClick={() => runMenu(() => void downloadFolderZip(path, toast))}
          >
            <Icon name="cloud-download" /> {tr("proj.download_zip")}
          </button>
        </li>
      )}
      {isFile && !outside && (
        <li>
          <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void openVerbChat(path, "translate"))}>
            <Icon name="globe" /> {tr("proj.translate")}
          </button>
        </li>
      )}
      {isFile && !outside && (
        <li>
          <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void openVerbChat(path, "summarize"))}>
            <Icon name="list-selection" /> {tr("proj.summarize")}
          </button>
        </li>
      )}
    </>
  );
}
