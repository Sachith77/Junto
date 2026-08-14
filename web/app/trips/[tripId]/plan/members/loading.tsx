import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";

/** Instant fallback for the members segment — see plan/loading.tsx for why these exist.
 *
 *  Shaped like MembersPanel: heading, then roster rows of an avatar beside two lines of text. */
export default function Loading() {
  return (
    <div className="space-y-10" data-testid="route-loading">
      <LoadingRegion label="Loading members" />
      <section>
        <Skeleton className="h-9 w-36" />
        <Skeleton className="mt-2 h-4 w-64" />
        <ul className="mt-5 divide-y divide-line-subtle rounded-card border border-line-subtle bg-surface-raised">
          {[0, 1, 2].map((i) => (
            <li key={i} className="flex items-center gap-3 px-4 py-3">
              <Skeleton className="h-9 w-9 rounded-full" />
              <div className="flex-1 space-y-1.5">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-3 w-56" />
              </div>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
