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

        {/* One size for every card.
            The first two used to get a taller treatment on the theory that an editorial grid
            wants a focal point. In a two-column grid that only works when the count is even:
            at three trips it produced two tall cards and one short one, which does not read
            as emphasis — it reads as a layout bug, and it was reported as one. A "hero" row
            would need to span the full width to be legible as a choice, and that is a
            different design; uniform is the honest version of this one. */}
        {load.state === "ready" && load.trips.length > 0 && (
          <div className="grid gap-6 sm:grid-cols-2">
            {load.trips.map((trip) => (
              <TripCard key={trip.id} trip={trip} />
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
        {/* Joining a trip requires the emailed link, because a targeted invitation is checked
            against the invitee's own address when it is redeemed (D58). There is deliberately
            no list of "invitations addressed to me" — so an invited user who lands here sees
            an empty account and no sign the invitation exists. Saying so is the whole fix:
            without it the honest state of the system reads as a bug. */}
        <p className="mx-auto mt-6 max-w-md text-ui-sm text-fg-on-media-dim">
          Been invited? Open the link in your invitation email to join — it has to be opened
          while you&apos;re signed in as the address it was sent to.
        </p>
      </div>
    </Media>
  );
}
