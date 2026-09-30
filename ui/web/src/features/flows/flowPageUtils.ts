export function runStatusColor(status: string): string {
  switch (status) {
    case "completed":
      return "green";
    case "running":
      return "blue";
    case "pending":
      return "default";
    case "canceled":
      return "orange";
    case "error":
      return "red";
    default:
      return "default";
  }
}

export function versionStatusColor(status: string): string {
  switch (status) {
    case "published":
      return "green";
    case "gray":
      return "orange";
    case "draft":
      return "blue";
    default:
      return "default";
  }
}

/** Unified timestamp display across the flows UI (local date + HH:mm). */
export function formatTime(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.toLocaleDateString()} ${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}
