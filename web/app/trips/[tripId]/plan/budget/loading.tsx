import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";

/** Instant fallback for the budget segment — see plan/loading.tsx for why these exist.
 *
 *  Shaped like BudgetPanel: heading, the total/balances summary band, then ledger rows. The
 *  point of matching the shape rather than showing a spinner is that the page does not reflow
 *  when the real content lands. */
export default function Loading() {
  return (
    <div className="space-y-8" data-testid="route-loading">
      <LoadingRegion label="Loading the budget" />
      <div className="space-y-2">
        <Skeleton className="h-9 w-40" />
        <Skeleton className="h-4 w-72" />
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Skeleton className="h-28 w-full rounded-card" />
        <Skeleton className="h-28 w-full rounded-card" />
      </div>
      <div className="space-y-2">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-14 w-full rounded-card" />
        ))}
      </div>
    </div>
  );
}
