/** formatBytes renders a byte count for humans.
 *
 *  Kept here rather than inline in each card so the metering views agree on units:
 *  two formatters would eventually disagree on the rounding, and the reconciliation
 *  total would stop matching the sum a reader computed by hand. */
export function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

/** formatCount renders an integer with thousands separators. */
export function formatCount(value?: number): string {
  if (!value || value <= 0) {
    return "0";
  }
  return value.toLocaleString();
}
