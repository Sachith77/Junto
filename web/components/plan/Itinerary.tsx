"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { createDay, listDays, type Day } from "@/lib/api/days";
import { createSlot, listSlots, type SlotKind } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { useLiveFlash } from "@/hooks/useLiveFlash";
import { Button } from "@/components/ui/Button";
import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";
import { ApiError } from "@/lib/http";
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

/** A quiet tint per kind, so a day's shape is scannable before any of it is read.
 *
 *  The previous badge was one grey chip for every kind — uppercase, boxed, and the heaviest
 *  thing on a row whose actual subject is the title beside it. These are the same size and
 *  weight as before but sit at ~50-tint backgrounds with a 700-step label, which is the
 *  contrast pairing the rest of the palette already uses for quiet status text.
 *
 *  The tints are tokens (--color-kind-*), not literals, and two of the five reuse hues the
 *  palette already had. `critical` was deliberately NOT borrowed for a category: red means
 *  "something is wrong" everywhere else in this system, and a category is not wrong. Category
 *  is carried by the LABEL; the tint only makes the label findable, so nothing here depends
 *  on colour alone. */
const KIND_TINT: Record<string, string> = {
  lodging: "bg-kind-lodging text-kind-lodging-ink",
  activity: "bg-kind-activity text-kind-activity-ink",
  transport: "bg-kind-transport text-kind-transport-ink",
  place: "bg-kind-place text-kind-place-ink",
  note: "bg-kind-note text-kind-note-ink",
};

const KINDS = Object.keys(KIND_LABEL) as SlotKind[];

/** Last id in an ordered bucket, or null when it is empty.
 *
 *  Exists so every call site reads the same way: the anchor for "add to the end" is always
 *  the last thing currently there, and null is reserved for a genuinely empty bucket — where
 *  "insert before the first" and "append" happen to mean the same thing. */
function lastIdOf(items: { slot: Slot }[] | Day[] | undefined): string | null {
  if (!items || items.length === 0) return null;
  const last = items[items.length - 1];
  return "slot" in last ? last.slot.id : last.id;
}

interface SlotSummary {
  slot: Slot;
  options: SlotOption[];
}

/** Short enough to stay imperceptible next to the measured p99 delivery of 23–49ms, long
 *  enough to swallow a multi-op burst from one intent. */
const OP_REFETCH_COALESCE_MS = 120;

/** The itinerary: days in order, slots within each.
 *
 *  A sequential list rather than day-columns. Columns look tidier in a screenshot but a trip
 *  is read in time order, and a horizontal scroll for day 6 of 8 fights the one thing this
 *  screen is for. Slots that are still undecided are the interesting ones, so the resolution
 *  state is on every row rather than only inside the detail view. */
export function Itinerary({ tripId }: { tripId: string }) {
  const { onOp, resyncSignal } = useTripSocket();
  const { myRole } = useTripMembers(tripId);
  const [days, setDays] = useState<Day[] | null>(null);
  const [slots, setSlots] = useState<Record<string, SlotSummary[]> | null>(null);
  const [backlog, setBacklog] = useState<SlotSummary[]>([]);

  // CapEditSlots is granted to owner and editor, not viewer. Hiding the controls is a
  // courtesy, not the enforcement — the service refuses a viewer's write regardless.
  const canEdit = myRole === "owner" || myRole === "editor";

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

  // After our own write. The op we just caused is broadcast back to us too and would
  // eventually trigger the coalesced refetch below, but waiting ~120ms to see your own click
  // land reads as lag — and if the socket is down, the REST write must still show up.
  const refresh = useCallback(
    () => fetchAll().then(apply).catch(() => {}),
    [fetchAll, apply]
  );

  // One user INTENT commits as several ops linked by cause_op_id (D63) — selecting an option
  // that also clears another field, deleting a slot's selected option (D56), and so on. A
  // refetch per op therefore means N full itinerary reloads (days + slots + one options call
  // per slot) for a single click by someone else. Coalesce the burst into one trailing
  // refetch: the ops arrive milliseconds apart, and the fold they describe is only worth
  // reading once it has settled.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;
    const unsubscribe = onOp((op) => {
      if (!(SLOT_OPS.has(op.kind) || DAY_OPS.has(op.kind) || RESOLUTION_OPS.has(op.kind))) return;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        fetchAll()
          .then((d) => !cancelled && apply(d))
          .catch(() => {});
      }, OP_REFETCH_COALESCE_MS);
    });
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      unsubscribe();
    };
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
        {canEdit ? (
          <div className="mx-auto mt-6 max-w-sm text-left">
            <AddDay tripId={tripId} dayCount={0} afterDayId={null} onAdded={refresh} />
          </div>
        ) : (
          <p className="mt-6 text-ui-sm text-fg-subtle">
            You have view-only access to this trip.
          </p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-8">
      {days.map((day, index) => (
        <section key={day.id} data-testid="day-section" data-day-id={day.id}>
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
          {canEdit && (
            <AddSlot
              tripId={tripId}
              dayId={day.id}
              afterSlotId={lastIdOf(slots[day.id])}
              onAdded={refresh}
            />
          )}
        </section>
      ))}

      {backlog.length > 0 && (
        <section>
          <div className="mb-3 flex items-baseline gap-3">
            <h2 className="font-display text-display-sm text-fg">Unscheduled</h2>
            <span className="text-ui-xs text-fg-subtle">not yet placed on a day</span>
          </div>
          <SlotRows tripId={tripId} entries={backlog} flashProps={flashProps} />
          {canEdit && (
            <AddSlot
              tripId={tripId}
              dayId={null}
              afterSlotId={lastIdOf(backlog)}
              onAdded={refresh}
            />
          )}
        </section>
      )}

      {canEdit && (
        <div className="flex flex-wrap items-center gap-2 border-t border-line-subtle pt-6">
          <AddDay tripId={tripId} dayCount={days.length} afterDayId={lastIdOf(days)} onAdded={refresh} />
          {/* Offered here only while the backlog is empty — once it has rows it has its own
              section above, and two entry points to one form is one too many. */}
          {backlog.length === 0 && (
            <AddSlot tripId={tripId} dayId={null} afterSlotId={null} onAdded={refresh} trigger="inline" />
          )}
        </div>
      )}
    </div>
  );
}

/** A disclosure rather than a permanently open form. An itinerary is read far more often than
 *  it is edited, and eight always-visible input rows would make the schedule — the thing the
 *  screen is for — the minority of the page. */
function AddSlot({
  tripId,
  dayId,
  afterSlotId,
  onAdded,
  trigger = "block",
}: {
  tripId: string;
  dayId: string | null;
  /** The last slot currently in this bucket, so a new one lands after it rather than before
   *  the first (see createSlot). Null when the bucket is empty, which is then correct. */
  afterSlotId: string | null;
  onAdded: () => void | Promise<void>;
  /** "block" sits under a day's rows as a full-width dashed target; "inline" sits in a row
   *  of page-level actions, where a dashed block would read as an empty list. */
  trigger?: "block" | "inline";
}) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState<SlotKind>("activity");
  const [startTime, setStartTime] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await createSlot(tripId, { dayId, kind, title: title.trim(), startTime, afterSlotId });
      // Reset rather than close: adding one thing to a day is almost always adding three.
      setTitle("");
      setStartTime("");
      await onAdded();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? (err.violations[0]?.message ?? err.message)
          : "Couldn't add that."
      );
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    if (trigger === "inline") {
      return (
        <Button
          type="button"
          variant="secondary"
          onClick={() => setOpen(true)}
          // A distinct id from the per-day trigger: both are on screen at once whenever the
          // backlog is empty, and a test that picks "the first add-slot-open" would silently
          // be exercising whichever one the DOM happened to order first.
          data-testid="add-unscheduled-open"
        >
          + Add something unscheduled
        </Button>
      );
    }
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        data-testid="add-slot-open"
        className="mt-2 w-full rounded-card border border-dashed border-line px-4 py-2.5 text-ui-sm text-fg-subtle transition-colors hover:border-line-strong hover:text-fg"
      >
        + Add {dayId ? "to this day" : "something unscheduled"}
      </button>
    );
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      data-testid="add-slot-form"
      // w-full so the open form claims its own line when the trigger sat in a flex row of
      // page actions, rather than being squeezed beside the "Add a day" button.
      className="mt-2 w-full rounded-card border border-line bg-surface-raised p-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <input
          autoFocus
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="What needs deciding? e.g. Dinner in Alfama"
          aria-label="Title"
          data-testid="add-slot-title"
          className="min-w-56 flex-1 rounded-sm border border-line bg-surface px-3 py-2 text-ui-md text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
        />
        <input
          type="time"
          value={startTime}
          onChange={(e) => setStartTime(e.target.value)}
          aria-label="Start time (optional)"
          data-numeric
          className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md text-fg focus:border-accent focus:outline-none"
        />
        <select
          value={kind}
          onChange={(e) => setKind(e.target.value as SlotKind)}
          aria-label="Kind"
          className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md text-fg focus:border-accent focus:outline-none"
        >
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {KIND_LABEL[k]}
            </option>
          ))}
        </select>
        <Button type="submit" disabled={busy || !title.trim()} data-testid="add-slot-submit">
          Add
        </Button>
        <button
          type="button"
          onClick={() => {
            setOpen(false);
            setError(null);
          }}
          className="rounded-sm px-1 text-ui-sm text-fg-subtle transition-colors hover:text-fg"
        >
          Cancel
        </button>
      </div>
      <p className="mt-2 text-ui-xs text-fg-subtle">
        A slot is a decision, not an answer — add candidate options inside it for the group to
        vote on.
      </p>
      {error && (
        <p role="alert" className="mt-2 text-ui-xs text-critical-700">
          {error}
        </p>
      )}
    </form>
  );
}

function AddDay({
  tripId,
  dayCount,
  afterDayId,
  onAdded,
}: {
  tripId: string;
  dayCount: number;
  /** The last existing day, so a new one is appended rather than pushed to the front. */
  afterDayId: string | null;
  onAdded: () => void | Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [date, setDate] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // A day without a label is normal — it renders as "Day N" by position. Defaulting the
      // label here keeps that fallback from being the only thing anyone ever sees.
      await createDay(tripId, {
        label: label.trim() || `Day ${dayCount + 1}`,
        date: date || null,
        afterDayId,
      });
      setLabel("");
      setDate("");
      setOpen(false);
      await onAdded();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? (err.violations[0]?.message ?? err.message)
          : "Couldn't add that day."
      );
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return (
      <Button
        type="button"
        variant="secondary"
        onClick={() => setOpen(true)}
        data-testid="add-day-open"
      >
        + Add a day
      </Button>
    );
  }

  return (
    <form onSubmit={(e) => void submit(e)} data-testid="add-day-form">
      <div className="flex flex-wrap items-center gap-2">
        <input
          autoFocus
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={`Day ${dayCount + 1}`}
          aria-label="Day label"
          data-testid="add-day-label"
          className="min-w-40 flex-1 rounded-sm border border-line bg-surface-raised px-3 py-2 text-ui-md text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
        />
        <input
          type="date"
          value={date}
          onChange={(e) => setDate(e.target.value)}
          aria-label="Date (optional)"
          data-numeric
          className="rounded-sm border border-line bg-surface-raised px-3 py-2 text-ui-md text-fg focus:border-accent focus:outline-none"
        />
        <Button type="submit" disabled={busy} data-testid="add-day-submit">
          Add day
        </Button>
        <button
          type="button"
          onClick={() => {
            setOpen(false);
            setError(null);
          }}
          className="rounded-sm px-1 text-ui-sm text-fg-subtle transition-colors hover:text-fg"
        >
          Cancel
        </button>
      </div>
      {error && (
        <p role="alert" className="mt-2 text-ui-xs text-critical-700">
          {error}
        </p>
      )}
    </form>
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
        No slots on this day yet
      </p>
    );
  }

  return (
    <ul className="space-y-2">
      {entries.map(({ slot, options }) => {
        const chosen = options.find((o) => o.id === slot.selected_option_id);
        // The flash goes on the LINK, not the <li>.
        //
        // On the <li> it was invisible: the link paints an opaque bg-surface-raised over the
        // whole row, so the tint animated underneath it and never reached a pixel. Measured
        // before the move — the li animated to rgba(254,246,236,0.71) while the link sat at
        // rgb(255,255,255) on top of it. Putting it on the element that OWNS the background is
        // what BudgetPanel already did, which is why Budget's flash worked and this one did not.
        return (
          <li key={slot.id}>
            <Link
              {...flashProps(slot.id)}
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
                  {/* Sentence case, not uppercase, and a rounded-full pill rather than a box:
                      the previous chip was uppercase + tracked + boxed, which is the styling
                      this system reserves for things that matter more than the title they sit
                      next to. It read as a debug label. */}
                  <span
                    className={`shrink-0 rounded-full px-2 py-0.5 text-ui-2xs font-medium ${
                      KIND_TINT[slot.kind] ?? KIND_TINT.note
                    }`}
                  >
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
  // "Empty" was the one label in this family written in schema language rather than product
  // language — and it sat on the same row as "No options proposed yet", saying the same thing
  // twice in two registers. The decision states are now Chosen / Undecided / No options.
  return (
    <span className="shrink-0 rounded-xs border border-line px-2 py-1 text-ui-2xs font-medium text-fg-muted">
      {candidates > 0 ? "Undecided" : "No options"}
    </span>
  );
}
