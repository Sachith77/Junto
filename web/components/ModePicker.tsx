"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { getTrip } from "@/lib/api/trips";
import { useTripSocket } from "@/context/TripSocketContext";
import { Media } from "@/components/ui/Media";
import { Skeleton } from "@/components/ui/Skeleton";
import { formatDateRange } from "@/lib/format";
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
    href: null,
    seed: "dusk",
    status: "Coming next",
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
      <Media seed={tripId} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-6xl px-6 pb-12 pt-8 sm:px-8 sm:pb-16 sm:pt-10">
          <div className="flex items-center justify-between">
            <Link
              href="/trips"
              className="rounded-sm text-ui-sm text-fg-on-media-dim transition-colors hover:text-fg-on-media"
            >
              ← All trips
            </Link>
            {socket === "open" && here > 0 && (
              <span className="flex items-center gap-2 rounded-full border border-white/20 bg-white/10 px-3 py-1 text-ui-xs text-fg-on-media backdrop-blur-sm">
                <span className="relative flex h-1.5 w-1.5">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-accent-on-dark opacity-70" />
                  <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-accent-on-dark" />
                </span>
                {here} {here === 1 ? "person" : "people"} here now
              </span>
            )}
          </div>

          <div className="mt-16 sm:mt-24">
            {trip ? (
              <>
                <p className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-accent-on-dark">
                  {formatDateRange(trip.start_date, trip.end_date)}
                </p>
                <h1 className="mt-3 font-display text-display-2xl text-fg-on-media">
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
        </div>
      </Media>

      <div className="mx-auto w-full max-w-6xl px-6 py-12 sm:px-8 sm:py-16">
        <h2 className="font-display text-display-md text-fg">What are you doing today?</h2>
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
    <Media
      seed={mode.seed}
      className={`h-72 rounded-card shadow-lg transition-transform duration-300 ease-[cubic-bezier(.22,1,.36,1)] ${
        mode.href ? "group-hover:-translate-y-1" : ""
      }`}
    >
      <div className="absolute inset-0 flex flex-col justify-end p-6">
        <p className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-accent-on-dark">
          {mode.tagline}
        </p>
        <h3 className="mt-2 font-display text-display-lg text-fg-on-media">{mode.name}</h3>
        <p className="mt-2 text-ui-md text-fg-on-media-dim">{mode.blurb}</p>
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
