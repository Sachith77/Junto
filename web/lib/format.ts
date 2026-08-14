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

/** Grouping is a property of the CURRENCY's home locale, not of the reader's.
 *
 *  Rupees group in lakhs and crores — ₹12,34,567.89, not ₹1,234,567.89 — and formatting them
 *  with en-GB produces a number that is technically parseable and visibly foreign. Anything
 *  not listed falls back to en-GB, which is what the rest of the app already uses for dates. */
const CURRENCY_LOCALE: Record<string, string> = {
  INR: "en-IN",
};

/** Money arrives as bigint minor units (D43) and must never be reconstructed
 *  through a float — 45000/100 is fine, but the pattern is what matters.
 *
 *  The default is INR. Note that `trips.base_currency` exists in the schema (default 'USD')
 *  but is carried by neither `domain.Trip` nor the trip wire type, so there is currently no
 *  per-trip currency to read — this default IS the currency the product displays. When that
 *  column is wired through, this argument is where it lands, and the schema default should be
 *  brought into line at the same time rather than leaving two disagreeing defaults. */
export function formatMoney(minor: number, currency = "INR"): string {
  const locale = CURRENCY_LOCALE[currency] ?? "en-GB";
  return new Intl.NumberFormat(locale, { style: "currency", currency }).format(minor / 100);
}
