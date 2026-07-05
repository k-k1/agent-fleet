import React from "react";
import { createRoot } from "react-dom/client";
import { AppProvider } from "./state.jsx";
import { PaneHoverProvider } from "./lib/panehover.jsx";
import ToastProvider from "./components/ToastProvider.jsx";
import App from "./App.jsx";
import { wireViewport } from "./viewport.js";
import "@vscode/codicons/dist/codicon.css";
import "./styles.css";

// Pin the frame's bars above the mobile soft keyboard (iOS visual-viewport fit).
wireViewport();

// ToastProvider wraps AppProvider (not just App) so the app-state layer in state.tsx
// can raise toasts too, not only the view components below it.
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ToastProvider>
      <AppProvider>
        <PaneHoverProvider>
          <App />
        </PaneHoverProvider>
      </AppProvider>
    </ToastProvider>
  </React.StrictMode>,
);
