import { FilesPanel } from "../features/files/FilesPanel";
import FilesPage from "../features/files/FilesPage";

// P2 / T6c (SPEC §4.3): wrap the existing <FilesPage> route in
// <FilesPanel mode="page">. The page-host is a transparent container
// (it renders a placeholder div with data-testid="files-panel-page-host")
// so the FilesPage header / body / tree / editor remain visually
// unchanged. Future commits can add a context provider or a hook
// inside <FilesPanel mode="page"> without touching FilesPage.tsx.
export default function FilesPageRoute() {
  return (
    <FilesPanel mode="page">
      <FilesPage />
    </FilesPanel>
  );
}
