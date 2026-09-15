"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { getTrip } from "@/lib/api/trips";
import { useTripSocket } from "@/context/TripSocketContext";
import { Media } from "@/components/ui/Media";
import { Skeleton } from "@/components/ui/Skeleton";
import { formatDateRange } from "@/lib/format";
import { coverSeedForTrip } from "@/lib/cover";
import type { Trip } from "@/lib/types";

// The trip entry point: three modes, one per phase of a trip's life.
//
// Live and Memories render as real cards rather than being hidden, because the
// three-mode structure IS the product's shape — concealing two-thirds of it
// until they are built would misrepresent what this screen is. They are visibly
// unavailable instead, which is honest and costs nothing.

interface Mode {
  key: string;
  name: string;
  tagline: string;
  blurb: string;
  href: string | null;
  seed: string;
  status?: string;
}

const MODES: Mode[] = [
  {
    key: "plan",
    name: "Plan",
    tagline: "Before you go",
    blurb: "Build the itinerary together. Propose options, vote, split the budget, argue in the comments.",
    href: "plan",
    seed: "sea",
  },
  {
    key: "live",
    name: "Live",
    tagline: "While you're there",
    blurb: "Tick off what you actually did, and keep the plan honest as the day changes around you.",
    href: null,
    seed: "alpine",
    status: "Not built yet",
  },
  {
    key: "memories",
    name: "Memories",
    tagline: "After you're home",
    blurb: "The trip as it happened — photos and notes gathered against the places you went.",
    href: "memories",
    seed: "dusk",
  },
];

export function ModePicker({ tripId }: { tripId: string }) {
  const [trip, setTrip] = useState<Trip | null>(null);
  const { presence, status: socket } = useTripSocket();

  useEffect(() => {
    let cancelled = false;
    getTrip(tripId)
      .then((t) => !cancelled && setTrip(t))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [tripId]);

  const here = new Set(presence.map((p) => p.user_id)).size;

  return (
    <main className="flex flex-1 flex-col">
      {/* Hero band — the trip's own cover, so this screen is unmistakably about
          THIS trip rather than a generic menu. */}
      <Media seed={trip ? coverSeedForTrip(trip) : tripId} image={trip?.cover?.url} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-6xl px-6 pb-10 pt-7 sm:px-8 sm:pb-12 sm:pt-8">
          <div className="flex items-center justify-between">
            <Link
              href="/trips"
              className="rounded-sm text-ui-sm text-fg-on-media-dim transition-colors hover:text-fg-on-media"
            >
              ← All trips
            </Link>
            <div className="flex items-center gap-3">
            <Link href={`/trips/${tripId}/edit`} className="rounded-full border border-white/20 bg-black/20 px-3 py-1 text-ui-xs text-fg-on-media backdrop-blur-sm transition-colors hover:bg-white/15">
              Edit trip
            </Link>
            {socket === "open" && here > 0 && (
              <span className="flex items-center gap-2 rounded-full border border-white/20 bg-white/10 px-3 py-1 text-ui-xs text-fg-on-media backdrop-blur-sm">
                <span className="relative flex h-1.5 w-1.5">
                  <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-accent-on-dark" />
                </span>
                {here} {here === 1 ? "person" : "people"} here now
              </span>
            )}
            </div>
          </div>

          <div className="mt-14 sm:mt-14">
            {trip ? (
              <>
                <p className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-accent-on-dark">
                  {formatDateRange(trip.start_date, trip.end_date)}
                </p>
                <h1 className="mt-3 max-w-4xl font-display text-[clamp(2.75rem,8vw,3.5rem)] leading-[1.05] tracking-[-0.025em] text-fg-on-media">
                  {trip.name}
                </h1>
                {trip.description && (
                  <p className="mt-3 max-w-xl text-ui-lg text-fg-on-media-dim">
                    {trip.description}
                  </p>
                )}
              </>
            ) : (
              <div className="space-y-3">
                <Skeleton className="h-3 w-32 bg-white/25" />
                <Skeleton className="h-12 w-80 bg-white/25" />
              </div>
            )}
          </div>
          {trip?.cover?.source === "suggested" && trip.cover.photographer_name && (
            <span className="absolute bottom-3 right-8 text-[10px] text-white/65">
              Photo by <a href={trip.cover.photographer_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">{trip.cover.photographer_name}</a>{" "}
              on <a href={trip.cover.photo_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">Unsplash</a>
            </span>
          )}
        </div>
      </Media>

      <div className="mx-auto w-full max-w-7xl px-6 py-9 sm:px-8 sm:py-10">
        <h2 className="max-w-[20ch] font-editorial text-[clamp(2.75rem,8vw,3.25rem)] font-semibold uppercase leading-[.94] tracking-[-0.035em] text-fg">Choose the shape of the trip</h2>
        <div className="mt-6 grid gap-5 md:grid-cols-3">
          {MODES.map((mode) => (
            <ModeCard key={mode.key} mode={mode} tripId={tripId} />
          ))}
        </div>
      </div>
    </main>
  );
}

function ModeCard({ mode, tripId }: { mode: Mode; tripId: string }) {
  const inner = (
    // Hover: a small lift, a barely-there scale, and a deeper shadow, at 180ms.
    //
    // Three deliberate choices. The duration comes down from 300ms to 180ms because a hover
    // is a response to the cursor, not an entrance — at 300ms the card is still arriving after
    // the pointer has settled. The scale is 1.015, which is under the threshold where the
    // cover's grain visibly resamples but enough to read as "coming forward" alongside the
    // lift. And the shadow deepens with it, because a card that rises without its shadow
    // changing reads as sliding rather than lifting.
    //
    // transition-[transform,box-shadow] rather than transition-all: `all` would also animate
    // the background layers of the cover underneath, which is work for no visual gain.
    <Media
      seed={mode.seed}
      className={`h-72 rounded-xl border border-white/10 shadow-lg transition-[transform,box-shadow,border-color] duration-[180ms] ease-[cubic-bezier(.22,1,.36,1)] sm:h-[19rem] ${
        mode.href
          ? "group-hover:-translate-y-1.5 group-hover:scale-[1.015] group-hover:border-white/25 group-hover:shadow-xl group-focus-visible:-translate-y-1.5 group-focus-visible:scale-[1.015]"
          : ""
      }`}
    >
      <div className="absolute inset-0 flex min-w-0 flex-col justify-end p-6">
        <p className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-accent-on-dark">
          {mode.tagline}
        </p>
        <h3 className="mt-2 line-clamp-2 font-editorial text-[clamp(2.35rem,7vw,2.75rem)] font-semibold uppercase leading-[.95] tracking-[-0.03em] text-fg-on-media">{mode.name}</h3>
        <p className="mt-2 line-clamp-3 break-words text-ui-md leading-relaxed text-fg-on-media-dim">{mode.blurb}</p>
      </div>
      {mode.status && (
        <span className="absolute right-4 top-4 rounded-xs border border-white/25 bg-black/35 px-2 py-1 text-ui-2xs font-medium uppercase tracking-[0.1em] text-fg-on-media backdrop-blur-sm">
          {mode.status}
        </span>
      )}
    </Media>
  );

  if (!mode.href) {
    return (
      <div
        data-testid="mode-card"
        data-mode={mode.key}
        aria-disabled
        className="cursor-not-allowed opacity-55 grayscale-[0.35]"
      >
        {inner}
      </div>
    );
  }

  return (
    <Link
      href={`/trips/${tripId}/${mode.href}`}
      data-testid="mode-card"
      data-mode={mode.key}
      className="group block rounded-card"
    >
      {inner}
    </Link>
  );
}
