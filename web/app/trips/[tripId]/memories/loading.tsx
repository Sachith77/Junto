import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";

/** Instant fallback for Memories — see plan/loading.tsx for why these exist.
 *
 *  Memories is the outer-shell visual language rather than the dense inner app: a full-bleed
 *  cover with a title over it, then a grid of destination cards. The fallback mirrors that,
 *  because a dense-app skeleton here would flash the wrong layout for a few hundred
 *  milliseconds and then jump. */
export default function Loading() {
  return (
    <div className="space-y-10" data-testid="route-loading">
      <LoadingRegion label="Loading memories" />
      <Skeleton className="h-64 w-full rounded-card sm:h-80" />
      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <div key={i} className="space-y-2">
            <Skeleton className="h-48 w-full rounded-card" />
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-3.5 w-1/3" />
          </div>
        ))}
      </div>
    </div>
  );
}
