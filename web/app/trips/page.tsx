"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { listTrips } from "@/lib/api/trips";
import { useAuth } from "@/context/AuthContext";
import { ShellHeader } from "@/components/ShellHeader";
import { TripCard } from "@/components/TripCard";
import { ButtonLink } from "@/components/ui/Button";
import { LoadingRegion, TripCardSkeleton } from "@/components/ui/Skeleton";
import { Media } from "@/components/ui/Media";
import type { Trip } from "@/lib/types";

type Load = { state: "loading" } | { state: "ready"; trips: Trip[] } | { state: "error" };

export default function TripsPage() {
  const { status } = useAuth();
  const router = useRouter();
  const [load, setLoad] = useState<Load>({ state: "loading" });

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

  useEffect(() => {
    if (status !== "authenticated") return;
    let cancelled = false;
    listTrips()
      .then((trips) => !cancelled && setLoad({ state: "ready", trips }))
      .catch(() => !cancelled && setLoad({ state: "error" }));
    return () => {
      cancelled = true;
    };
  }, [status]);

  if (status !== "authenticated") return null;

  return (
    <div className="flex min-h-full flex-1 flex-col">
      <ShellHeader>
        <ButtonLink href="/trips/new" variant="primary" size="sm">
          New trip
        </ButtonLink>
      </ShellHeader>

      <main className="mx-auto w-full max-w-6xl flex-1 px-6 py-12 sm:px-8 sm:py-16">
        <div className="mb-10">
          <h1 className="font-display text-display-xl text-fg">Your trips</h1>
          <p className="mt-2 text-ui-lg text-fg-muted">
            Pick up where the group left off.
          </p>
        </div>

        {load.state === "loading" && (
          <>
            <LoadingRegion label="Loading your trips" />
            <div className="grid gap-6 sm:grid-cols-2">
              <TripCardSkeleton />
              <TripCardSkeleton />
            </div>
          </>
        )}

        {load.state === "error" && (
          <div className="rounded-card border border-critical-600/25 bg-critical-50 px-6 py-5">
            <p className="text-ui-md font-medium text-critical-700">
              We couldn&rsquo;t load your trips.
            </p>
            <p className="mt-1 text-ui-sm text-fg-muted">
              Check that the API is running, then reload the page.
            </p>
          </div>
        )}

        {load.state === "ready" && load.trips.length === 0 && <EmptyTrips />}

        {load.state === "ready" && load.trips.length > 0 && (
          <div className="grid gap-6 sm:grid-cols-2">
            {load.trips.map((trip, i) => (
              // The first two get the taller treatment — an editorial grid should
              // have a focal point rather than reading as a uniform contact sheet.
              <TripCard key={trip.id} trip={trip} priority={i < 2} />
            ))}
          </div>
        )}
      </main>
    </div>
  );
}

/** Intentional empty state, not an absence. It gets the same cinematic treatment
 *  a real trip card would, so a new account's first screen still looks like the
 *  product rather than like something failed to load. */
function EmptyTrips() {
  return (
    <Media
      seed="alpine"
      className="rounded-card shadow-lg"
      data-testid="trips-empty"
    >
      <div className="relative px-8 py-20 text-center sm:px-16 sm:py-24">
        <h2 className="font-display text-display-lg text-fg-on-media">
          No trips yet
        </h2>
        <p className="mx-auto mt-3 max-w-md text-ui-lg text-fg-on-media-dim">
          Start one, invite the group, and let everyone throw ideas at it. Nothing gets planned
          alone.
        </p>
        <div className="mt-8 flex justify-center">
          <ButtonLink href="/trips/new" variant="primary" size="lg">
            Create your first trip
          </ButtonLink>
        </div>
      </div>
    </Media>
  );
}
