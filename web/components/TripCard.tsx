import Link from "next/link";
import { Media } from "@/components/ui/Media";
import { formatDateRange, tripNights } from "@/lib/format";
import { coverSeedForTrip } from "@/lib/cover";
import type { Trip } from "@/lib/types";

/** A quiet editorial cover. Motion is limited to a small lift: the trip itself,
 * not a cursor effect, is the focal point. */
export function TripCard({ trip }: { trip: Trip }) {
  const nights = tripNights(trip.start_date, trip.end_date);

  return (
    <div className="group relative rounded-card">
      <Link
        href={`/trips/${trip.id}`}
        data-testid="trip-card"
        className="block rounded-card focus-visible:outline-2 focus-visible:outline-offset-2"
      >
      <Media
        seed={coverSeedForTrip(trip)}
        image={trip.cover?.url}
        className="h-72 rounded-card border border-white/15 shadow-lg transition-[transform,border-color,box-shadow] duration-200 ease-out group-hover:-translate-y-1 group-hover:border-white/30 group-hover:shadow-xl group-focus-visible:-translate-y-1 sm:h-96"
      >
        <div className="absolute inset-x-0 top-0 flex flex-wrap items-center gap-x-2.5 gap-y-1 p-6 text-ui-2xs font-medium uppercase tracking-[0.15em] text-accent-on-dark sm:p-7">
          <span>{formatDateRange(trip.start_date, trip.end_date)}</span>
          {nights !== null && (
            <>
              <span aria-hidden className="opacity-40">·</span>
              <span>{nights} {nights === 1 ? "night" : "nights"}</span>
            </>
          )}
        </div>

        <div className="absolute inset-x-0 bottom-0 min-w-0 p-6 sm:p-7">
          <h3 className="line-clamp-2 max-w-[16ch] font-display text-[clamp(2rem,7vw,2.75rem)] leading-[1.05] tracking-[-0.02em] text-fg-on-media">{trip.name}</h3>
          {trip.description && (
            <p className="mt-2 line-clamp-2 max-w-md break-words text-ui-md leading-relaxed text-fg-on-media-dim">{trip.description}</p>
          )}
        </div>
      </Media>
      </Link>
      {trip.cover?.source === "suggested" && trip.cover.photographer_name && (
        <span className="absolute bottom-3 right-4 z-10 text-[10px] text-white/65">
          Photo by <a href={trip.cover.photographer_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">{trip.cover.photographer_name}</a>{" "}
          on <a href={trip.cover.photo_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">Unsplash</a>
        </span>
      )}
    </div>
  );
}
