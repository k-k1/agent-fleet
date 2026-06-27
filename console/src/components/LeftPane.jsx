import SessionsSection from "./sections/SessionsSection.jsx";
import ReposSection from "./sections/ReposSection.jsx";
import FilesSection from "./sections/FilesSection.jsx";

// The left navigator: three stacked sections. Selecting an item in any of them
// drives what the main area shows (terminal / source control / file viewer).
export default function LeftPane() {
  return (
    <nav className="leftpane">
      <SessionsSection />
      <ReposSection />
      <FilesSection />
    </nav>
  );
}
