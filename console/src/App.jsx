import { useApp } from "./state.jsx";
import TopBar from "./components/TopBar.jsx";
import WsBar from "./components/WsBar.jsx";
import LeftPane from "./components/LeftPane.jsx";
import TerminalView from "./views/TerminalView.jsx";
import SourceControlView from "./views/SourceControlView.jsx";
import FileView from "./views/FileView.jsx";
import SettingsDialog from "./settings/SettingsDialog.jsx";
import AdminDialog from "./settings/AdminDialog.jsx";

// App lays out the persistent frame: top bar (identity / tenant / settings), WS
// bar (workspace state + start/stop), the left navigator pane (Sessions / Repos /
// Files), and the main detail area. The terminal stays mounted regardless of mode
// so its WebSocket and scrollback survive switching to the SCM / file views.
export default function App() {
  const { mode, settingsOpen, adminOpen, navOpen, closeNav } = useApp();
  return (
    <div className="app">
      <TopBar />
      <WsBar />
      <div className={"body" + (navOpen ? " nav-open" : "")}>
        <LeftPane />
        {/* Mobile-only: dims the main area and dismisses the navigator drawer. */}
        <div className="nav-backdrop" onClick={closeNav} />
        <main className="main">
          {/* Terminal is always mounted; just hidden when another mode is active. */}
          <div className="view" hidden={mode !== "terminal"}>
            <TerminalView active={mode === "terminal"} />
          </div>
          {mode === "scm" && <SourceControlView />}
          {mode === "file" && <FileView />}
        </main>
      </div>
      {settingsOpen && <SettingsDialog />}
      {adminOpen && <AdminDialog />}
    </div>
  );
}
