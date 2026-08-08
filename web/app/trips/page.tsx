"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { createTrip, listTrips } from "@/lib/api/trips";
import { useAuth } from "@/context/AuthContext";
import type { Trip } from "@/lib/types";

export default function TripsPage() {
  const { status, user, logout } = useAuth();
  const router = useRouter();
  const [trips, setTrips] = useState<Trip[]>([]);
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

  useEffect(() => {
    if (status === "authenticated") void listTrips().then(setTrips);
  }, [status]);

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setCreating(true);
    try {
      const trip = await createTrip({ name, description: "", timeZone: "UTC" });
      setTrips((prev) => [trip, ...prev]);
      setName("");
    } finally {
      setCreating(false);
    }
  };

  if (status !== "authenticated") return null;

  return (
    <main className="mx-auto w-full max-w-2xl flex-1 p-8">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Trips</h1>
        <div className="flex items-center gap-3 text-sm text-neutral-500">
          <span>{user?.display_name}</span>
          <button onClick={() => void logout()} className="underline">
            Log out
          </button>
        </div>
      </div>

      <form onSubmit={onCreate} className="mb-6 flex gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="New trip name"
          className="flex-1 rounded-md border border-neutral-300 px-3 py-2"
        />
        <button
          type="submit"
          disabled={creating}
          className="rounded-md bg-blue-600 px-4 py-2 text-white disabled:opacity-50"
        >
          Create
        </button>
      </form>

      <ul className="space-y-2">
        {trips.map((trip) => (
          <li key={trip.id}>
            <Link
              href={`/trips/${trip.id}`}
              className="block rounded-md border border-neutral-200 bg-white px-4 py-3 hover:border-neutral-300"
            >
              {trip.name}
            </Link>
          </li>
        ))}
        {trips.length === 0 && <p className="text-neutral-400">No trips yet.</p>}
      </ul>
    </main>
  );
}
