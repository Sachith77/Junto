/** Dates arrive as RFC3339 from the API but are calendar dates, not instants —
 *  a trip starting "2026-09-14" starts on the 14th regardless of who is reading.
 *  Parsing the date part directly avoids the classic off-by-one where a UTC
 *  midnight renders as the 13th for anyone west of Greenwich. */
function calendarDate(iso: string): Date {
  const [y, m, d] = iso.slice(0, 10).split("-").map(Number);
  return new Date(y, (m ?? 1) - 1, d ?? 1);
}

export function formatDateRange(start: string | null, end: string | null): string {
  if (!start && !end) return "Dates not set";
  const fmt = (d: Date, withYear: boolean) =>
    d.toLocaleDateString("en-GB", {
      day: "numeric",
      month: "short",
      ...(withYear ? { year: "numeric" } : {}),
    });

  if (start && end) {
    const s = calendarDate(start);
    const e = calendarDate(end);
    const sameYear = s.getFullYear() === e.getFullYear();
    return `${fmt(s, !sameYear)} – ${fmt(e, true)}`;
  }
  const only = calendarDate((start ?? end) as string);
  return `${start ? "From" : "Until"} ${fmt(only, true)}`;
}

export function tripNights(start: string | null, end: string | null): number | null {
  if (!start || !end) return null;
  const ms = calendarDate(end).getTime() - calendarDate(start).getTime();
  const nights = Math.round(ms / 86_400_000);
  return nights > 0 ? nights : null;
}

/** Money arrives as bigint minor units (D43) and must never be reconstructed
 *  through a float — 45000/100 is fine, but the pattern is what matters. */
export function formatMoney(minor: number, currency = "EUR"): string {
  return new Intl.NumberFormat("en-GB", { style: "currency", currency }).format(minor / 100);
}
