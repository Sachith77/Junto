import Link from "next/link";
import { Media } from "@/components/ui/Media";
import { formatDateRange, tripNights } from "@/lib/format";
import type { Trip } from "@/lib/types";

// The outer shell's primary object. Photo-forward, serif title, caption on a
// scrim. The whole card is one link rather than a card containing a link, so
// keyboard users get a single tab stop and the focus ring wraps the real target.

export function TripCard({ trip, priority }: { trip: Trip; priority?: boolean }) {
  const nights = tripNights(trip.start_date, trip.end_date);

  return (
    <Link
      href={`/trips/${trip.id}`}
      data-testid="trip-card"
      className="group block rounded-card focus-visible:outline-2 focus-visible:outline-offset-2"
    >
      <Media
        seed={trip.id}
        className={`rounded-card shadow-lg transition-transform duration-300 ease-[cubic-bezier(.22,1,.36,1)] group-hover:-translate-y-1 ${
          priority ? "h-80 sm:h-96" : "h-64 sm:h-72"
        }`}
      >
        <div className="absolute inset-x-0 bottom-0 p-6 sm:p-7">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-ui-2xs font-medium uppercase tracking-[0.12em] text-accent-on-dark">
            <span>{formatDateRange(trip.start_date, trip.end_date)}</span>
            {nights !== null && (
              <>
                <span aria-hidden className="opacity-50">
                  ·
                </span>
                <span>
                  {nights} {nights === 1 ? "night" : "nights"}
                </span>
              </>
            )}
          </div>
          <h3
            className={`mt-2 font-display text-fg-on-media ${
              priority ? "text-display-xl" : "text-display-lg"
            }`}
          >
            {trip.name}
          </h3>
          {trip.description && (
            <p className="mt-1.5 line-clamp-2 max-w-md text-ui-md text-fg-on-media-dim">
              {trip.description}
            </p>
          )}
        </div>
      </Media>
    </Link>
  );
}
