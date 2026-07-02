import React from "react";
import { createRoot } from "react-dom/client";
import { AppProvider } from "./state.jsx";
import { PaneHoverProvider } from "./lib/panehover.jsx";
import ToastProvider from "./components/ToastProvider.jsx";
import App from "./App.jsx";
import "@vscode/codicons/dist/codicon.css";
import "./styles.css";

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
