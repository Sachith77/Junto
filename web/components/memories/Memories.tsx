"use client";

import Link from "next/link";
import { useState, useEffect } from "react";
import { Media } from "@/components/ui/Media";
import { useTrip } from "@/components/TripShell";
import { coverSeedForTrip } from "@/lib/cover";
import { listSlots } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { listDays, type Day } from "@/lib/api/days";
import type { Slot, SlotOption } from "@/lib/types";
import {
  GOA_MEMORIES_DATA,
  TRIP_RECAP_STATS,
  type MemoryItem,
} from "./MemoriesData";
import { CollageView } from "./CollageView";
import { JourneyChapters } from "./JourneyChapters";
import { GuideRecommendations } from "./GuideRecommendations";
import { BudgetSummary } from "./BudgetSummary";
import { MemoriesReelModal } from "./MemoriesReelModal";
import { MemoryDetailModal } from "./MemoryDetailModal";
import { ShareMemoryModal } from "./ShareMemoryModal";

export interface Destination {
  slot: Slot;
  option: SlotOption;
  dayLabel: string | null;
}

export function Memories({ tripId }: { tripId: string }) {
  const trip = useTrip();
  const [memoriesList, setMemoriesList] = useState<MemoryItem[]>(GOA_MEMORIES_DATA);
  const [reelOpen, setReelOpen] = useState(false);
  const [reelStartIndex, setReelStartIndex] = useState(0);
  const [selectedMemoryIndex, setSelectedMemoryIndex] = useState<number | null>(null);
  const [shareModalOpen, setShareModalOpen] = useState(false);
  const [toastMessage, setToastMessage] = useState<string | null>(null);

  const [liveDestinations, setLiveDestinations] = useState<Destination[] | null>(null);
  const [useDemoView, setUseDemoView] = useState(true);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const [days, slots] = await Promise.all([listDays(tripId), listSlots(tripId)]);
        const dayById = new Map<string, Day>(days.map((d) => [d.id, d]));
        const dayOrder = new Map<string, number>(days.map((d, i) => [d.id, i]));

        const resolved = slots.filter((s) => s.selected_option_id);
        const built = await Promise.all(
          resolved.map(async (slot) => {
            const options = await listOptions(tripId, slot.id).catch(() => [] as SlotOption[]);
            const option = options.find((o) => o.id === slot.selected_option_id);
            if (!option) return null;
            const day = slot.day_id ? dayById.get(slot.day_id) : undefined;
            return {
              slot,
              option,
              dayLabel: day?.label ?? null,
              _order: slot.day_id ? (dayOrder.get(slot.day_id) ?? 999) : 999,
            };
          })
        );

        const list = built
          .filter((d): d is Destination & { _order: number } => d !== null)
          .sort((a, b) => a._order - b._order || a.slot.position.localeCompare(b.slot.position))
          .map(({ slot, option, dayLabel }) => ({ slot, option, dayLabel }));

        if (!cancelled) {
          setLiveDestinations(list);
          const isGoa = trip?.name?.toLowerCase().includes("goa");
          if (!isGoa && list.length > 0) {
            setUseDemoView(false);
          }
        }
      } catch {
        if (!cancelled) setLiveDestinations([]);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [tripId, trip?.name]);

  const showToast = (msg: string) => {
    setToastMessage(msg);
    setTimeout(() => setToastMessage(null), 3000);
  };

  const handleReaction = (id: string, type: "heart" | "fire" | "sparkle") => {
    setMemoriesList((prev) =>
      prev.map((item) => {
        if (item.id === id) {
          return {
            ...item,
            reactions: {
              ...item.reactions,
              [type]: item.reactions[type] + 1,
            },
          };
        }
        return item;
      })
    );
    showToast("Reaction added!");
  };

  const handleAddComment = (id: string, text: string) => {
    const newComment = {
      id: `c-${Date.now()}`,
      author: "You",
      avatar: "Y",
      time: "Just now",
      text,
    };

    setMemoriesList((prev) =>
      prev.map((item) => {
        if (item.id === id) {
          return {
            ...item,
            comments: [...item.comments, newComment],
          };
        }
        return item;
      })
    );
    showToast("Note added to memory!");
  };

  const openReelAt = (index: number) => {
    setReelStartIndex(index);
    setReelOpen(true);
  };

  const tripTitle = trip?.name || "Goa";
  const heroImage = memoriesList[0]?.image || "/memories/goa/goa-dawn.jpg";

  return (
    <main className="flex flex-1 flex-col bg-surface pb-28">
      {/* Toast */}
      {toastMessage && (
        <div className="fixed bottom-6 right-6 z-50 animate-rise-in rounded-xl border border-line bg-surface-raised px-4 py-3 text-ui-sm font-medium text-fg shadow-2xl backdrop-blur-xl">
          {toastMessage}
        </div>
      )}

      {/* Hero Header */}
      <Media
        seed={trip ? coverSeedForTrip(trip) : tripId}
        image={heroImage}
        scrim="hero"
        className="shrink-0"
      >
        <div className="relative mx-auto w-full max-w-6xl px-6 pb-12 pt-7 sm:px-8 sm:pb-16 sm:pt-8">
          {/* Top Bar Navigation */}
          <div className="flex items-center justify-between">
            <Link
              href={`/trips/${tripId}`}
              className="group inline-flex items-center gap-2 rounded-full border border-white/20 bg-black/30 px-3.5 py-1.5 text-ui-sm text-fg-on-media-dim backdrop-blur-md transition-colors hover:border-white/40 hover:bg-black/50 hover:text-fg-on-media"
            >
              <span>←</span>
              <span>Back to trip</span>
            </Link>

            {liveDestinations && liveDestinations.length > 0 && (
              <button
                onClick={() => setUseDemoView(!useDemoView)}
                className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs font-medium text-accent-on-dark backdrop-blur-md transition-colors hover:bg-black/60"
              >
                {useDemoView ? "Showing Demo Album (Switch to Live)" : "Showing Live Slots (Switch to Demo Album)"}
              </button>
            )}
          </div>

          {/* Title Block */}
          <div className="mt-12 sm:mt-16">
            <div className="inline-flex items-center gap-2 rounded-full border border-white/20 bg-white/10 px-3 py-1 text-ui-2xs font-semibold uppercase tracking-[0.16em] text-accent-on-dark backdrop-blur-md">
              <span>✦ Memories Archive</span>
              <span>·</span>
              <span>{memoriesList.length} Frames Captured</span>
            </div>

            <h1 className="mt-4 font-display text-[clamp(2.75rem,8vw,3.75rem)] leading-[1.02] tracking-[-0.03em] text-fg-on-media">
              {tripTitle}, Remembered
            </h1>

            <p className="mt-3 max-w-2xl text-ui-lg text-fg-on-media-dim">
              {trip?.description ||
                "Salt air, late lunches, and the places that stayed with us. The trip as it actually happened."}
            </p>

            {/* Members & Action Toolbar */}
            <div className="mt-8 flex flex-wrap items-center justify-between gap-4 border-t border-white/15 pt-6">
              {/* Friends Avatars */}
              <div className="flex items-center gap-3">
                <div className="flex -space-x-2">
                  {TRIP_RECAP_STATS.friends.map((f) => (
                    <div
                      key={f.name}
                      className="flex h-8 w-8 items-center justify-center rounded-full border-2 border-surface bg-surface-raised text-xs font-bold text-fg ring-1 ring-white/20"
                      title={f.name}
                    >
                      {f.avatar}
                    </div>
                  ))}
                </div>
                <span className="text-ui-xs text-white/80 font-medium">
                  4 Contributors · 100% Consensus
                </span>
              </div>

              {/* Action Buttons */}
              <div className="flex flex-wrap items-center gap-3">
                <button
                  onClick={() => openReelAt(0)}
                  className="group flex items-center gap-2 rounded-xl bg-accent px-5 py-2.5 text-ui-sm font-semibold text-surface-sunken shadow-lg transition-all hover:bg-accent-hover hover:scale-105 active:scale-95"
                >
                  <span className="text-base transition-transform group-hover:scale-110">▶</span>
                  <span>Relive Trip Reel</span>
                </button>

                <button
                  onClick={() => setShareModalOpen(true)}
                  className="flex items-center gap-2 rounded-xl border border-white/25 bg-black/35 px-4 py-2.5 text-ui-sm font-medium text-white backdrop-blur-md transition-colors hover:bg-white/20 hover:border-white/40"
                >
                  <span>📤</span>
                  <span>Share Album</span>
                </button>
              </div>
            </div>
          </div>
        </div>
      </Media>

      {/* Main Single Page Streamlined Body */}
      <div className="mx-auto w-full max-w-6xl px-6 pt-12 space-y-20 sm:px-8 sm:space-y-28">
        {/* 1. Scrapbook Centerpiece (Polaroid & Frame Board) */}
        <CollageView
          memories={memoriesList}
          onSelectMemory={(idx) => setSelectedMemoryIndex(idx)}
          tripName={tripTitle}
        />

        {/* 2. The Visual Story Chapters (Photography + Notes + Soundbite) */}
        <JourneyChapters
          memories={memoriesList}
          onSelectMemory={(idx) => setSelectedMemoryIndex(idx)}
        />

        {/* 3. The Curated Recommendations ("The Black Book") */}
        <GuideRecommendations />

        {/* 4. Planned vs. Spent Financial Ledger (At the very end) */}
        <BudgetSummary />
      </div>

      {/* Fullscreen Story Reel Modal */}
      <MemoriesReelModal
        isOpen={reelOpen}
        onClose={() => setReelOpen(false)}
        memories={memoriesList}
        initialIndex={reelStartIndex}
        onReaction={handleReaction}
      />

      {/* Deep-dive Lightbox Detail Modal */}
      {selectedMemoryIndex !== null && (
        <MemoryDetailModal
          memory={memoriesList[selectedMemoryIndex]}
          onClose={() => setSelectedMemoryIndex(null)}
          onPrev={() => setSelectedMemoryIndex((prev) => (prev !== null && prev > 0 ? prev - 1 : prev))}
          onNext={() =>
            setSelectedMemoryIndex((prev) =>
              prev !== null && prev < memoriesList.length - 1 ? prev + 1 : prev
            )
          }
          hasPrev={selectedMemoryIndex > 0}
          hasNext={selectedMemoryIndex < memoriesList.length - 1}
          onReaction={handleReaction}
          onAddComment={handleAddComment}
        />
      )}

      {/* Share Memory Modal */}
      <ShareMemoryModal
        isOpen={shareModalOpen}
        onClose={() => setShareModalOpen(false)}
        tripName={tripTitle}
        memories={memoriesList}
      />
    </main>
  );
}
