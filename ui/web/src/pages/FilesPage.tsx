import FilesPage from "../features/files/FilesPage";

// P2 / T6c (SPEC §4.3): render FilesPage directly without FilesPanel
// wrapper to isolate React #306 in production builds.
export function FilesPageRoute() {
  return <FilesPage />;
}

export { FilesPageRoute as FilesPage };
export default FilesPageRoute;
