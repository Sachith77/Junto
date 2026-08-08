"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { TripSocketProvider } from "@/context/TripSocketContext";
import { getTrip } from "@/lib/api/trips";
import type { Trip } from "@/lib/types";
import { PresenceBar } from "./PresenceBar";

function TripHeader({ tripId }: { tripId: string }) {
  const [trip, setTrip] = useState<Trip | null>(null);

  useEffect(() => {
    void getTrip(tripId).then(setTrip);
  }, [tripId]);

  return (
    <header className="flex items-center justify-between border-b border-neutral-200 bg-white px-6 py-4">
      <div>
        <Link href="/trips" className="text-sm text-neutral-400">
          ← Trips
        </Link>
        <h1 className="text-xl font-semibold">{trip?.name ?? "…"}</h1>
      </div>
      <PresenceBar tripId={tripId} />
    </header>
  );
}

export function TripShell({
  tripId,
  children,
}: {
  tripId: string;
  children: React.ReactNode;
}) {
  const { status } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

  if (status !== "authenticated") return null;

  return (
    <TripSocketProvider tripId={tripId}>
      <div className="flex min-h-full flex-1 flex-col">
        <TripHeader tripId={tripId} />
        <div className="mx-auto w-full max-w-2xl flex-1 p-6">{children}</div>
      </div>
    </TripSocketProvider>
  );
}
