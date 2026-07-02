import LayoutMap from "./LayoutMap.jsx";
import SessionsSection from "./sections/SessionsSection.jsx";
import ReposSection from "./sections/ReposSection.jsx";
import FilesSection from "./sections/FilesSection.jsx";

// The left navigator: a pinned pane map on top, then three stacked, scrolling
// sections. Selecting an item in any section drives what the main area shows
// (terminal / source control / file viewer).
//
// The pane map sits ABOVE the sections (not inside SESSIONS) because it charts every
// pane — sessions, source control, and files alike — not just sessions. It's outside
// the scroll region so it stays fixed at the top instead of scrolling away.
export default function LeftPane() {
  return (
    <nav className="leftpane">
      <LayoutMap />
      <div className="leftpane-scroll">
        <SessionsSection />
        <ReposSection />
        <FilesSection />
      </div>
    </nav>
  );
}
