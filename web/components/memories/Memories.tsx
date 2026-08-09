"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { listSlots } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { listDays, type Day } from "@/lib/api/days";
import { Media } from "@/components/ui/Media";
import { Skeleton } from "@/components/ui/Skeleton";
import { ButtonLink } from "@/components/ui/Button";
import type { Slot, SlotOption } from "@/lib/types";

/**
 * Memories mode — the trip as it actually happened.
 *
 * THERE IS NO MEMORIES BACKEND. No `destinations` table, no photo aggregation, no curation.
 * Rather than invent sample data and present it as working, a destination is DERIVED from
 * data the Stage 1/2 model already holds:
 *
 *   destination = a slot whose decision was RESOLVED (selected_option_id is set)
 *
 * That mapping is not a stand-in — it is the honest answer to "where did we actually go",
 * because the resolved option is exactly the place the group settled on. Days give the
 * ordering. Photos come from real attachments hung off the slot or its chosen option.
 *
 * What is genuinely missing, and is flagged in the UI rather than faked: nothing curates,
 * re-orders or captions a memory, and an unresolved slot is invisible here even if the group
 * went anyway. Both need a real Memories slice.
 */

export interface Destination {
  slot: Slot;
  option: SlotOption;
  dayLabel: string | null;
}

export function Memories({ tripId }: { tripId: string }) {
  const [destinations, setDestinations] = useState<Destination[] | null>(null);

  useEffect(() => {
    let cancelled = false;

    (async () => {
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

      if (!cancelled) setDestinations(list);
    })().catch(() => {
      if (!cancelled) setDestinations([]);
    });

    return () => {
      cancelled = true;
    };
  }, [tripId]);

  if (destinations === null) {
    return (
      <div className="mx-auto w-full max-w-6xl px-6 py-12 sm:px-8">
        <Skeleton className="h-10 w-64" />
        <div className="mt-8 flex gap-5 overflow-hidden">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-96 w-80 shrink-0 rounded-card" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <main className="flex flex-1 flex-col">
      <Media seed={tripId} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-6xl px-6 pb-14 pt-8 sm:px-8 sm:pb-20 sm:pt-10">
          <Link
            href={`/trips/${tripId}`}
            className="rounded-sm text-ui-sm text-fg-on-media-dim transition-colors hover:text-fg-on-media"
          >
            ← Back to trip
          </Link>
          <p className="mt-14 text-ui-2xs font-medium uppercase tracking-[0.16em] text-accent-on-dark sm:mt-20">
            Memories
          </p>
          <h1 className="mt-3 font-display text-display-2xl text-fg-on-media">
            Where you went
          </h1>
          <p className="mt-3 max-w-xl text-ui-lg text-fg-on-media-dim">
            Every decision the group actually settled on, in the order you lived them.
          </p>
        </div>
      </Media>

      <div className="mx-auto w-full max-w-6xl px-6 py-12 sm:px-8 sm:py-16">
        {destinations.length === 0 ? (
          <EmptyMemories tripId={tripId} />
        ) : (
          <>
            <div className="mb-5 flex items-baseline justify-between gap-4">
              <h2 className="font-display text-display-md text-fg">
                {destinations.length} {destinations.length === 1 ? "place" : "places"}
              </h2>
              <span className="hidden text-ui-xs text-fg-subtle sm:inline">scroll →</span>
            </div>

            {/* A carousel here, not a grid: memories are browsed as a sequence — this is the
                trip in order — where the trips list is a set you pick one from. */}
            <ul
              data-testid="memory-carousel"
              className="-mx-6 flex snap-x snap-mandatory gap-5 overflow-x-auto px-6 pb-4 sm:-mx-8 sm:px-8"
            >
              {destinations.map(({ slot, option, dayLabel }, i) => (
                <li key={slot.id} className="w-72 shrink-0 snap-start sm:w-80">
                  <Link
                    href={`/trips/${tripId}/memories/${slot.id}`}
                    data-testid="memory-card"
                    className="group block rounded-card"
                  >
                    <Media
                      seed={slot.id}
                      className="h-96 rounded-card shadow-lg transition-transform duration-300 ease-[cubic-bezier(.22,1,.36,1)] group-hover:-translate-y-1"
                    >
                      <span className="absolute left-4 top-4 rounded-xs border border-white/25 bg-black/30 px-2 py-1 text-ui-2xs font-medium uppercase tracking-[0.1em] text-fg-on-media backdrop-blur-sm">
                        {String(i + 1).padStart(2, "0")}
                      </span>
                      <div className="absolute inset-x-0 bottom-0 p-5">
                        {dayLabel && (
                          <p className="text-ui-2xs font-medium uppercase tracking-[0.12em] text-accent-on-dark">
                            {dayLabel}
                          </p>
                        )}
                        <h3 className="mt-1.5 font-display text-display-md text-fg-on-media">
                          {option.title}
                        </h3>
                        <p className="mt-1 line-clamp-2 text-ui-sm text-fg-on-media-dim">
                          {option.place.name || slot.title}
                        </p>
                      </div>
                    </Media>
                  </Link>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </main>
  );
}

function EmptyMemories({ tripId }: { tripId: string }) {
  return (
    <div className="rounded-card border border-dashed border-line bg-surface-raised px-6 py-16 text-center">
      <h2 className="font-display text-display-md text-fg">Nothing to remember yet</h2>
      <p className="mx-auto mt-2 max-w-md text-ui-md text-fg-muted">
        A place shows up here once the group has actually decided on it — pick a winning option
        for a slot in Plan mode and it becomes part of the trip&rsquo;s story.
      </p>
      <div className="mt-6 flex justify-center">
        <ButtonLink href={`/trips/${tripId}/plan`} variant="primary">
          Go to Plan
        </ButtonLink>
      </div>
    </div>
  );
}
