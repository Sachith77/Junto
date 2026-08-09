"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { listDays, type Day } from "@/lib/api/days";
import { listSlots } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { useTripSocket } from "@/context/TripSocketContext";
import { useLiveFlash } from "@/hooks/useLiveFlash";
import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";
import type { OpFrame, Slot, SlotOption } from "@/lib/types";

const SLOT_OPS = new Set(["slot.create.v1", "slot.edit.v1", "slot.move.v1", "slot.delete.v1"]);
const DAY_OPS = new Set(["day.create.v1", "day.edit.v1", "day.move.v1", "day.delete.v1"]);
const RESOLUTION_OPS = new Set(["slot.select_option.v1", "option.create.v1", "option.delete.v1", "vote.set.v1"]);

const KIND_LABEL: Record<string, string> = {
  place: "Place",
  activity: "Activity",
  transport: "Transport",
  lodging: "Lodging",
  note: "Note",
};

interface SlotSummary {
  slot: Slot;
  options: SlotOption[];
}

/** The itinerary: days in order, slots within each.
 *
 *  A sequential list rather than day-columns. Columns look tidier in a screenshot but a trip
 *  is read in time order, and a horizontal scroll for day 6 of 8 fights the one thing this
 *  screen is for. Slots that are still undecided are the interesting ones, so the resolution
 *  state is on every row rather than only inside the detail view. */
export function Itinerary({ tripId }: { tripId: string }) {
  const { onOp, resyncSignal } = useTripSocket();
  const [days, setDays] = useState<Day[] | null>(null);
  const [slots, setSlots] = useState<Record<string, SlotSummary[]> | null>(null);
  const [backlog, setBacklog] = useState<SlotSummary[]>([]);

  // A slot flashes when someone else changes IT, or changes the decision inside it — an
  // option added or a vote cast shows up here as "this slot moved", which is the level of
  // detail this screen is about.
  const { flashProps } = useLiveFlash((op: OpFrame) => {
    if (SLOT_OPS.has(op.kind)) return op.entity_id;
    if (op.kind === "slot.select_option.v1") return op.entity_id;
    const fields = op.payload.fields as { slot_id?: string };
    if (RESOLUTION_OPS.has(op.kind)) return fields.slot_id ?? null;
    return null;
  });

  // Returns data rather than setting state, so the effect below can set state inside a
  // promise callback. A loader that setStates directly is reached synchronously from the
  // effect body, which is the cascading-render pattern React warns about.
  const fetchAll = useCallback(async () => {
    const [freshDays, freshSlots] = await Promise.all([listDays(tripId), listSlots(tripId)]);
    // Options per slot, so the itinerary can say "3 candidates, undecided" without opening
    // each one. Fetched in parallel — the alternative is an N+1 waterfall on every render.
    const withOptions = await Promise.all(
      freshSlots.map(async (slot) => ({
        slot,
        options: await listOptions(tripId, slot.id).catch(() => [] as SlotOption[]),
      }))
    );

    const grouped: Record<string, SlotSummary[]> = {};
    const loose: SlotSummary[] = [];
    for (const entry of withOptions) {
      if (entry.slot.day_id) {
        (grouped[entry.slot.day_id] ??= []).push(entry);
      } else {
        loose.push(entry);
      }
    }
    return { days: freshDays, grouped, loose };
  }, [tripId]);

  const apply = useCallback((d: { days: Day[]; grouped: Record<string, SlotSummary[]>; loose: SlotSummary[] }) => {
    setDays(d.days);
    setSlots(d.grouped);
    setBacklog(d.loose);
  }, []);

  useEffect(() => {
    let cancelled = false;
    fetchAll()
      .then((d) => {
        if (!cancelled) apply(d);
      })
      .catch(() => {
        if (!cancelled) {
          setDays([]);
          setSlots({});
        }
      });
    return () => {
      cancelled = true;
    };
  }, [fetchAll, apply, resyncSignal]);

  useEffect(() => {
    return onOp((op) => {
      if (SLOT_OPS.has(op.kind) || DAY_OPS.has(op.kind) || RESOLUTION_OPS.has(op.kind)) {
        void fetchAll().then(apply).catch(() => {});
      }
    });
  }, [onOp, fetchAll, apply]);

  if (days === null || slots === null) {
    return (
      <>
        <LoadingRegion label="Loading the itinerary" />
        <div className="space-y-6">
          {[0, 1].map((i) => (
            <div key={i} className="space-y-2">
              <Skeleton className="h-5 w-40" />
              <Skeleton className="h-16 w-full rounded-card" />
              <Skeleton className="h-16 w-full rounded-card" />
            </div>
          ))}
        </div>
      </>
    );
  }

  const empty = days.length === 0 && backlog.length === 0;
  if (empty) {
    return (
      <div className="rounded-card border border-dashed border-line bg-surface-raised px-6 py-14 text-center">
        <h2 className="font-display text-display-md text-fg">Nothing planned yet</h2>
        <p className="mx-auto mt-2 max-w-md text-ui-md text-fg-muted">
          Add a day, then add the decisions you need to make within it — where to stay, what to
          eat, how to get there. Each one collects candidate options the group can vote on.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      {days.map((day, index) => (
        <section key={day.id}>
          <div className="mb-3 flex items-baseline gap-3">
            <h2 className="font-display text-display-sm text-fg">
              {day.label || `Day ${index + 1}`}
            </h2>
            {day.date && (
              <span className="text-ui-xs text-fg-subtle">
                {new Date(day.date.slice(0, 10)).toLocaleDateString("en-GB", {
                  weekday: "short",
                  day: "numeric",
                  month: "short",
                })}
              </span>
            )}
          </div>
          <SlotRows tripId={tripId} entries={slots[day.id] ?? []} flashProps={flashProps} />
        </section>
      ))}

      {backlog.length > 0 && (
        <section>
          <div className="mb-3 flex items-baseline gap-3">
            <h2 className="font-display text-display-sm text-fg">Unscheduled</h2>
            <span className="text-ui-xs text-fg-subtle">not yet placed on a day</span>
          </div>
          <SlotRows tripId={tripId} entries={backlog} flashProps={flashProps} />
        </section>
      )}
    </div>
  );
}

function SlotRows({
  tripId,
  entries,
  flashProps,
}: {
  tripId: string;
  entries: SlotSummary[];
  flashProps: (id: string) => Record<string, unknown>;
}) {
  if (entries.length === 0) {
    return (
      <p className="rounded-card border border-dashed border-line-subtle px-4 py-6 text-center text-ui-sm text-fg-subtle">
        No slots on this day yet.
      </p>
    );
  }

  return (
    <ul className="space-y-2">
      {entries.map(({ slot, options }) => {
        const chosen = options.find((o) => o.id === slot.selected_option_id);
        return (
          <li key={slot.id} {...flashProps(slot.id)}>
            <Link
              href={`/trips/${tripId}/plan/slots/${slot.id}`}
              data-testid="slot-row"
              className="flex items-center gap-4 rounded-card border border-line-subtle bg-surface-raised px-4 py-3 transition-colors hover:border-line-strong"
            >
              <div className="w-14 shrink-0 text-ui-sm text-fg-subtle" data-numeric>
                {slot.start_time ?? "—"}
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-ui-md font-medium text-fg">{slot.title}</span>
                  <span className="shrink-0 rounded-xs bg-surface-sunken px-1.5 py-0.5 text-ui-2xs font-medium uppercase tracking-[0.08em] text-fg-muted">
                    {KIND_LABEL[slot.kind] ?? slot.kind}
                  </span>
                </div>
                <p className="mt-0.5 truncate text-ui-xs text-fg-subtle">
                  {chosen ? (
                    <>Chosen: {chosen.title}</>
                  ) : options.length > 0 ? (
                    <>
                      {options.length} {options.length === 1 ? "option" : "options"} · undecided
                    </>
                  ) : (
                    <>No options proposed yet</>
                  )}
                </p>
              </div>
              <ResolutionPill decided={Boolean(chosen)} candidates={options.length} />
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

/** The D41 distinction, compressed to one glyph for the list view: a filled accent badge for
 *  a resolved decision, a neutral outline for one still open. Never colour alone — the label
 *  says which it is. */
function ResolutionPill({ decided, candidates }: { decided: boolean; candidates: number }) {
  if (decided) {
    return (
      <span className="shrink-0 rounded-xs bg-accent px-2 py-1 text-ui-2xs font-semibold text-fg-inverse">
        ✓ Chosen
      </span>
    );
  }
  return (
    <span className="shrink-0 rounded-xs border border-line px-2 py-1 text-ui-2xs font-medium text-fg-muted">
      {candidates > 0 ? "Undecided" : "Empty"}
    </span>
  );
}
