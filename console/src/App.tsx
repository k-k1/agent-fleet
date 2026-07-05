import { useApp } from "./state.jsx";
import TopBar from "./components/TopBar.jsx";
import WsBar from "./components/WsBar.jsx";
import LeftPane from "./components/LeftPane.jsx";
import PaneHost from "./components/PaneHost.jsx";
import SettingsDialog from "./settings/SettingsDialog.jsx";
import AdminDialog from "./settings/AdminDialog.jsx";
import ConfirmProvider from "./components/ConfirmProvider.jsx";

// App lays out the persistent frame: top bar (identity / tenant / settings), WS
// bar (workspace state + start/stop), the left navigator pane (Sessions / Repos /
// Files), and the main detail area. The main area is a PaneHost of one or two panes
// (split), each independently showing a terminal / source-control / file view.
export default function App() {
  const { settingsOpen, adminOpen, navOpen, closeNav, leftOpen, leftMode, closeLeft } = useApp();
  const bodyClass =
    "body" +
    (navOpen ? " nav-open" : "") +
    (leftOpen ? "" : " left-collapsed") +
    (leftMode === "overlay" ? " left-overlay" : "");
  return (
    <ConfirmProvider>
      <div className="app">
        <TopBar />
        <WsBar />
        <div className={bodyClass}>
          <LeftPane />
          {/* Dims the main area and dismisses the pane: the mobile drawer, and the
              desktop overlay-mode left pane. */}
          <div className="nav-backdrop" onClick={() => { closeNav(); closeLeft(); }} />
          <main className="main">
            <PaneHost />
          </main>
        </div>
        {settingsOpen && <SettingsDialog />}
        {adminOpen && <AdminDialog />}
      </div>
    </ConfirmProvider>
  );
}
