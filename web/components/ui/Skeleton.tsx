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

/** Matches TripCard's real shape: ONE media block with the caption overlaid, at the same
 *  height. It previously described the older card — a 14rem image with a text block beneath
 *  it — so the list visibly re-laid-out the moment trips arrived, which is the exact reflow
 *  the note above says these exist to prevent. */
export function TripCardSkeleton() {
  return (
    <div className="relative h-80 overflow-hidden rounded-card bg-ink-200/70 sm:h-[24rem]">
      <div className="absolute inset-x-0 top-0 p-6 sm:p-7">
        <Skeleton className="h-3 w-44 bg-ink-300/70" />
      </div>
      <div className="absolute inset-x-0 bottom-0 space-y-3 p-6 sm:p-7">
        <Skeleton className="h-9 w-3/5 bg-ink-300/70" />
        <Skeleton className="h-4 w-2/5 bg-ink-300/70" />
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
