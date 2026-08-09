"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { TripSocketProvider } from "@/context/TripSocketContext";
import { getTrip } from "@/lib/api/trips";
import type { Trip } from "@/lib/types";
import { PresenceBar } from "./PresenceBar";
import { PlanNav } from "./plan/PlanNav";

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

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

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

  return (
    <TripSocketProvider tripId={tripId}>
      <div className="flex min-h-full flex-1 flex-col">{children}</div>
    </TripSocketProvider>
  );
}

/** Chrome for the DENSE inner app (Plan mode). Compact bar, serif title as the
 *  thread back to the outer shell, live presence on the right. */
export function PlanChrome({ tripId, children }: { tripId: string; children: React.ReactNode }) {
  const [trip, setTrip] = useState<Trip | null>(null);

  useEffect(() => {
    let cancelled = false;
    getTrip(tripId)
      .then((t) => !cancelled && setTrip(t))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [tripId]);

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
