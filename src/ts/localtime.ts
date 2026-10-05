// Server-rendered timestamps arrive as <time datetime="...Z" data-local> with a
// labelled UTC text ("5 Oct 2026 14:30 UTC"), so that with no script nobody
// misreads them by their offset. This rewrites them in the reader's own timezone.

/** Formats an ISO timestamp for display in timeZone (the browser's by default). */
export function formatLocal(iso: string, timeZone?: string): string | null {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    // 24-hour regardless of the browser locale: an en-US browser would
    // otherwise say "12:30 AM", which reads oddly beside SA bank data.
    hourCycle: "h23",
    timeZone,
  }).format(d);
}

/** Rewrites every <time data-local> under root. Safe to call repeatedly. */
export function localiseTimes(root: ParentNode): void {
  for (const el of root.querySelectorAll<HTMLTimeElement>("time[data-local]")) {
    const text = formatLocal(el.dateTime);
    if (text === null) continue;
    // The UTC original stays available on hover.
    if (!el.title) el.title = el.textContent ?? "";
    el.textContent = text;
  }
}
