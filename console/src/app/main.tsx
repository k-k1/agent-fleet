// Entry for the NEXT console (docs/22 rebuild) — served as next.html alongside the
// frozen current console (index.html) during the parallel-entry transition.
import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { PaneHoverProvider } from "../lib/panehover.tsx";
import { wireViewport } from "./viewport.ts";
import "@vscode/codicons/dist/codicon.css";
import "../styles/tokens.css";
import "../styles/base.css";
import "../ui/ui.css";
import "./app.css";
import "./topbar.css";
import "./wsbar.css";
import "../features/panes/panes.css";
import "../features/terminal/terminal.css";
import "../features/sessions/sessions.css";
import "../features/repos/repos.css";
import "../features/files/files.css";
import "../features/scm/scm.css";
import "../features/viewer/viewer.css";
import "../features/chat/chat.css";
import "../features/mirror/mirror.css";
import "../features/memo/memo.css";
import "../features/settings/settings.css";

// Pin the frame's bars above the mobile soft keyboard (iOS visual-viewport fit).
wireViewport();

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ToastProvider>
      <ConfirmProvider>
        <PaneHoverProvider>
          <App />
        </PaneHoverProvider>
      </ConfirmProvider>
    </ToastProvider>
  </React.StrictMode>,
);
