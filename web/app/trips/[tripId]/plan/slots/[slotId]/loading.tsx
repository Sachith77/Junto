import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";

/** Instant fallback for a slot's decision page — see plan/loading.tsx for why these exist.
 *
 *  This is the most-clicked navigation in the app: every itinerary row leads here, so it is
 *  the transition where a frozen screen is noticed most often. */
export default function Loading() {
  return (
    <div className="space-y-8" data-testid="route-loading">
      <LoadingRegion label="Loading this decision" />
      <Skeleton className="h-8 w-64" />
      <div className="space-y-3">
        <Skeleton className="h-28 w-full rounded-card" />
        <Skeleton className="h-28 w-full rounded-card" />
      </div>
    </div>
  );
}
