"use client";

import { GOA_MEMORIES_DATA } from "./MemoriesData";
import { Memories } from "./Memories";
import { DestinationDetail } from "./DestinationDetail";

export const GOA_DEMO_ID = "goa-demo";

export const GOA_MEMORIES = GOA_MEMORIES_DATA.map((m) => ({
  image: m.image,
  alt: m.alt,
  note: m.note,
}));

export function isGoaDemo(tripName: string | undefined): boolean {
  return tripName?.toLowerCase().includes("goa") ?? false;
}

export function GoaMemories({ tripId }: { tripId: string }) {
  return <Memories tripId={tripId} />;
}

export function GoaMemoryDetail({ tripId }: { tripId: string }) {
  return <DestinationDetail tripId={tripId} slotId={GOA_DEMO_ID} />;
}
