import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";

/** Instant fallback for the itinerary segment.
 *
 *  Without a loading.tsx, clicking a tab leaves the PREVIOUS panel on screen, frozen, until
 *  the new segment's payload arrives — measured at 337–453ms for a client-side tab click, and
 *  far longer the first time a route is compiled in dev. Nothing on screen changes in that
 *  window, so the click reads as ignored rather than as in progress, which is the whole of the
 *  "not smooth" complaint.
 *
 *  This is the fallback Next renders immediately on navigation (it is prefetched with the
 *  route), so the response to a click is instant even when the data is not. The panels' own
 *  Skeletons stay where they are: those cover "route mounted, data still loading", which is a
 *  different window from "segment still arriving" and neither one covers the other. */
export default function Loading() {
  return (
    <div data-testid="route-loading">
      <LoadingRegion label="Loading the itinerary" />
      <div className="space-y-6">
        {[0, 1].map((i) => (
          <div key={i} className="space-y-2">
            <Skeleton className="h-5 w-40" />
            <Skeleton className="h-16 w-full rounded-card" />
            <Skeleton className="h-16 w-full rounded-card" />
          </div>
        ))}
      </div>
    </div>
  );
}
