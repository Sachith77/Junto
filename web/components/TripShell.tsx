"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { createContext, useContext, useEffect, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { TripSocketProvider } from "@/context/TripSocketContext";
import { getTrip } from "@/lib/api/trips";
import type { Trip } from "@/lib/types";
import { PresenceBar } from "./PresenceBar";
import { PlanNav } from "./plan/PlanNav";

/** The trip TripShell already loaded, shared with everything it wraps.
 *
 *  It exists to stop the same GET /trips/{id} being issued twice per screen. The access
 *  check below has to fetch the trip anyway, and PlanChrome needs nothing more than the
 *  name — but it used to fetch it a second time, and the two could not overlap: PlanChrome
 *  only mounts once the access check has resolved, so the duplicate was a whole extra
 *  round trip in series, paid on every mode switch. Handing the first result down removes
 *  the request rather than merely deduplicating it in a cache. */
const TripContext = createContext<Trip | null>(null);

/** The current trip, or null while the access check is still in flight.
 *
 *  Only valid under TripShell. Returning null rather than throwing outside it is
 *  deliberate — the one consumer renders a placeholder title for the loading case anyway,
 *  so a missing provider degrades to that instead of crashing the screen. */
export function useTrip(): Trip | null {
  return useContext(TripContext);
}

/** Auth guard + socket subscription, with NO visual chrome.
 *
 *  It wraps every screen under /trips/[tripId] — including the cinematic mode
 *  picker and the dense Plan surfaces — precisely because those two want
 *  completely different chrome but the same live connection. Keeping the
 *  provider at this level also means switching modes does not tear down and
 *  re-establish the WebSocket, so presence does not flicker as you navigate. */
export function TripShell({ tripId, children }: { tripId: string; children: React.ReactNode }) {
  const { status } = useAuth();
  const router = useRouter();
  // Tagged with the trip it describes rather than reset synchronously when tripId changes:
  // a bare setAccess("checking") in the effect body is a cascading render, and deriving
  // "checking" from a stale tag gets the same result without one.
  // Carries the trip itself, not just the verdict: the access check has to fetch it, and
  // throwing the body away only forced PlanChrome to ask for it again a moment later.
  const [checked, setChecked] = useState<{
    id: string;
    state: "ok" | "denied";
    trip: Trip | null;
  } | null>(null);
  const access = checked?.id === tripId ? checked.state : "checking";
  // Tag-matched for the same reason as `access` — a trip from the previous tripId must not
  // be handed to the new one's children for the render before the effect re-runs.
  const trip = checked?.id === tripId ? checked.trip : null;

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

  // Confirm the trip is readable BEFORE rendering any of its screens.
  //
  // A non-member gets 404 from every trip-scoped route by design (D53 — confirming a trip
  // exists to someone with no access is itself a disclosure). Each screen used to swallow
  // that and fall back to its empty state, so the whole of Plan mode rendered as a trip with
  // no days, under a title stuck at "…" — indistinguishable from a trip that simply hasn't
  // been filled in yet, and reported as "Plan mode doesn't load". The 404 is correct; showing
  // it as emptiness was not.
  useEffect(() => {
    if (status !== "authenticated") return;
    let cancelled = false;
    getTrip(tripId)
      .then((t) => !cancelled && setChecked({ id: tripId, state: "ok", trip: t }))
      .catch(() => !cancelled && setChecked({ id: tripId, state: "denied", trip: null }));
    return () => {
      cancelled = true;
    };
  }, [tripId, status]);

  // A hard navigation drops the in-memory access token (D30) and has to restore it via
  // /auth/refresh, which takes a round trip. Returning null there paints a completely blank
  // page for as long as that takes — the exact "no data rendered as an empty screen" failure
  // the design brief calls out, and it is worst on the cinematic screens where the blank is
  // full-bleed. Render the frame instead, and let each screen show its own skeleton.
  if (status === "loading") {
    return (
      <div className="flex min-h-full flex-1 items-center justify-center bg-surface">
        <span role="status" className="text-ui-sm text-fg-subtle">
          Loading trip…
        </span>
      </div>
    );
  }
  if (status !== "authenticated") return null;

  if (access === "denied") {
    return (
      <div className="flex min-h-full flex-1 flex-col items-center justify-center gap-4 bg-surface px-5 text-center">
        <h1 className="font-display text-display-md text-fg">This trip isn&apos;t available</h1>
        <p className="max-w-sm text-ui-sm text-fg-muted">
          It may have been deleted, or you may not be a member of it. If someone invited you,
          open the link in your invitation email to join.
        </p>
        <Link
          href="/trips"
          className="rounded-sm text-ui-sm text-accent underline-offset-4 hover:underline"
        >
          Go to your trips
        </Link>
      </div>
    );
  }

  // Don't open a socket or mount a screen until access is confirmed: subscribing to a trip the
  // caller can't read just fails on the server side and leaves the UI in a fake live state.
  if (access === "checking") {
    return (
      <div className="flex min-h-full flex-1 items-center justify-center bg-surface">
        <span role="status" className="text-ui-sm text-fg-subtle">
          Loading trip…
        </span>
      </div>
    );
  }

  return (
    <TripContext.Provider value={trip}>
      <TripSocketProvider tripId={tripId}>
        <div className="flex min-h-full flex-1 flex-col">{children}</div>
      </TripSocketProvider>
    </TripContext.Provider>
  );
}

/** Chrome for the DENSE inner app (Plan mode). Compact bar, serif title as the
 *  thread back to the outer shell, live presence on the right. */
export function PlanChrome({ tripId, children }: { tripId: string; children: React.ReactNode }) {
  // Read from TripShell rather than fetched again. PlanChrome renders only after the access
  // check upstream has resolved, so its own request could never have started until that one
  // finished — it was a second round trip in series for a body already in memory, on every
  // navigation into and between Plan screens. The title is available immediately now.
  const trip = useTrip();

  return (
    <>
      <header className="sticky top-0 z-10 border-b border-line-subtle bg-surface/90 backdrop-blur-sm">
        <div className="mx-auto flex h-14 w-full max-w-5xl items-center justify-between gap-4 px-5">
          <div className="flex min-w-0 items-center gap-3">
            <Link
              href={`/trips/${tripId}`}
              className="shrink-0 rounded-sm text-ui-sm text-fg-subtle transition-colors hover:text-fg"
              aria-label="Back to trip modes"
            >
              ←
            </Link>
            <h1 className="truncate font-display text-display-sm text-fg">
              {trip?.name ?? "…"}
            </h1>
            <span className="hidden shrink-0 rounded-xs bg-surface-sunken px-2 py-0.5 text-ui-2xs font-medium uppercase tracking-[0.1em] text-fg-muted sm:inline">
              Plan
            </span>
          </div>
          <PresenceBar tripId={tripId} />
        </div>
      </header>
      <div className="mx-auto w-full max-w-5xl px-5">
        <PlanNav tripId={tripId} />
      </div>
      <main className="mx-auto w-full max-w-5xl flex-1 px-5 py-8">{children}</main>
    </>
  );
}
