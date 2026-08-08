// Loading placeholders.
//
// The brief's rule: "no data yet" must never render as a blank screen. These
// mirror the SHAPE of what is coming — a trip card skeleton is card-shaped and
// card-sized — so the page does not reflow when real data lands. A centred
// spinner would be less work and would tell the user less.

export function Skeleton({ className = "" }: { className?: string }) {
  return (
    <div
      aria-hidden
      className={`animate-pulse rounded-sm bg-ink-200/70 ${className}`}
    />
  );
}

export function TripCardSkeleton() {
  return (
    <div className="overflow-hidden rounded-card border border-line-subtle bg-surface-raised">
      <Skeleton className="h-56 rounded-none" />
      <div className="space-y-2 p-5">
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-3.5 w-1/3" />
      </div>
    </div>
  );
}

/** Announced to assistive tech once, rather than each skeleton being read out. */
export function LoadingRegion({ label }: { label: string }) {
  return (
    <span role="status" aria-live="polite" className="sr-only">
      {label}
    </span>
  );
}
