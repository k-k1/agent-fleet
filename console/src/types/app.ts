// App-level shared types: the identity/tenant/workspace shapes and the full
// AppContext value (AppState) exposed by state.tsx via useApp(). Kept here so
// components can import a precise context type as they migrate to TSX.
import type { Layout, Pane, PaneKind } from "./layout.ts";
import type { Session } from "./session.ts";

// GET /api/whoami — the signed-in identity. email/user are the fields the UI reads.
// scheduler_enabled is a deployment capability flag (AF_SCHEDULER_INTERVAL is set): the
// left-rail schedules section is hidden when it is false, since nothing can ever fire.
export interface Whoami {
  email?: string;
  user?: string;
  scheduler_enabled?: boolean;
  [k: string]: unknown;
}

// A tenant membership from GET /api/tenants.
export interface Tenant {
  slug: string;
  name?: string;
  role?: string;
}

// A partial pane descriptor describing an open target (which view + its identity).
export type PanePatch = Partial<Pane>;

// Files-tree reveal request: a home-relative path plus a monotonically increasing
// counter so repeat clicks on the same path still re-trigger the effect.
export interface Reveal {
  path: string | null;
  n: number;
}

// The full AppContext value. Everything shared across the top bar, WS bar, left
// pane, main area, and dialogs. Consumed via useApp() in state.tsx.
export interface AppState {
  // identity / tenant
  whoami: Whoami | null;
  tenants: Tenant[];
  tenant: string;
  showPicker: boolean;
  superAdmin: boolean;
  selectTenant: (slug: string) => void;
  // workspace
  wsState: string;
  refreshWs: () => Promise<void>;
  startWs: () => Promise<void>;
  stopWs: () => Promise<void>;
  recreateWs: () => Promise<void>;
  // active-pane projection (back-compat for not-yet-pane-aware components)
  mode: PaneKind;
  scmRepo: string | null;
  commitSha: string | null;
  filePath: string | null;
  session: string | null;
  // pane layout
  layout: Layout;
  activePaneId: string;
  splitRight: () => void;
  splitDown: (paneId: string) => void;
  closePane: (paneId: string) => void;
  closeSessionPanes: (name: string) => void;
  resetToTerminal: () => void;
  setActivePane: (id: string) => void;
  setColRatios: (ratios: number[]) => void;
  setRowRatio: (colId: string, r: number) => void;
  swapPanes: (aId: string, bId: string) => void;
  dropSplit: (srcId: string, refId: string, dir: "right" | "down") => void;
  showTerminal: (sess?: string) => void;
  showTerminalSplit: (sess: string) => void;
  showChat: (sess: string) => void;
  showChatSplit: (sess: string) => void;
  openInNewPane: (patch: PanePatch, force?: boolean) => void;
  showSCM: (repo: string) => void;
  showSCMSplit: (repo: string) => void;
  showChanges: (repo: string) => void;
  showChangesSplit: (repo: string) => void;
  showCommit: (repo: string, sha: string) => void;
  showCommitSplit: (repo: string, sha: string) => void;
  showFileDiff: (repo: string, path: string, staged: boolean) => void;
  showFile: (path: string) => void;
  showFileSplit: (path: string) => void;
  showDoc: (title: string, content: string) => void;
  showDiff: (title: string, edits: unknown, tool: string) => void;
  openChat: (conversationId: string, seed?: string) => void;
  openAssistantDraft: (assistantId: string) => void;
  promoteDraft: (paneId: string, conversationId: string) => void;
  chatListKey: number;
  bumpChatList: () => void;
  chatBusy: Record<string, boolean>;
  markChatBusy: (id: string, busy: boolean) => void;
  setPaneWrap: (id: string, wrap: boolean | null) => void;
  // dialogs
  settingsOpen: boolean;
  settingsSection: string;
  openSettings: (section?: string) => void;
  closeSettings: () => void;
  adminOpen: boolean;
  openAdmin: () => void;
  closeAdmin: () => void;
  // AdminTab keeps its current drill-down depth here (0=tenants,1=tenant,2=member) so
  // the X/backdrop close can pop all its history entries at once.
  adminDepthRef: { current: number };
  // mobile nav drawer
  navOpen: boolean;
  toggleNav: () => void;
  closeNav: () => void;
  // desktop left-pane collapse: leftOpen = shown; leftMode = "push" | "overlay"
  leftOpen: boolean;
  leftMode: string;
  toggleLeft: () => void;
  closeLeft: () => void;
  toggleLeftMode: () => void;
  // shared data + refresh signals
  sessions: Session[];
  sessionsKey: number;
  reposKey: number;
  connKey: number;
  filesKey: number;
  memosKey: number;
  bumpSessions: () => void;
  bumpRepos: () => void;
  bumpConn: () => void;
  bumpFiles: () => void;
  bumpMemos: () => void;
  newSessionTick: number;
  openNewSession: () => void;
  reveal: Reveal;
  revealInFiles: (path: string) => void;
}
