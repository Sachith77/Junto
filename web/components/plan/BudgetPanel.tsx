"use client";

import { useCallback, useEffect, useState } from "react";
import {
  BUDGET_CATEGORIES,
  createBudgetEntry,
  deleteBudgetEntry,
  listBudget,
  type BudgetEntry,
  type BudgetSplit,
} from "@/lib/api/budget";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { useLiveFlash } from "@/hooks/useLiveFlash";
import { ApiError } from "@/lib/http";
import { Button } from "@/components/ui/Button";
import { Skeleton } from "@/components/ui/Skeleton";
import { formatMoney } from "@/lib/format";
import type { OpFrame } from "@/lib/types";

const BUDGET_OPS = new Set(["budget.set.v1", "budget.delete.v1"]);

/** Same window, and the same reasoning, as Itinerary's OP_REFETCH_COALESCE_MS: short enough to
 *  stay imperceptible next to the measured p99 delivery of 23–49ms, long enough to swallow a
 *  burst.
 *
 *  The burst here is not a single intent fanning out into several ops the way D63 describes —
 *  a budget write is atomic by construction (D44/D83), so one intent is one op. It is several
 *  PEOPLE: settling up at the end of a trip is half a dozen members entering their expenses at
 *  once, and each remote op currently triggers its own full listBudget. Coalescing turns a
 *  flurry into one read of the state after it settles, which is the only version of it anyone
 *  can actually read. */
const OP_REFETCH_COALESCE_MS = 120;

/**
 * Splits an amount evenly in MINOR UNITS, giving the remainder to the earliest members.
 *
 * D45's example: 1000 across three is 333/333/334, and the extra unit has to belong to
 * someone deterministically. Doing this in the client the same way the server would means the
 * split the user previews is the split that gets stored — and because a deferred constraint
 * trigger refuses an unbalanced ledger from any writer, a rounding bug here surfaces as a
 * rejected write rather than as money quietly going missing.
 */
function splitEvenly(totalMinor: number, userIds: string[]): BudgetSplit[] {
  if (userIds.length === 0) return [];
  const base = Math.floor(totalMinor / userIds.length);
  let remainder = totalMinor - base * userIds.length;
  return userIds.map((user_id) => {
    const extra = remainder > 0 ? 1 : 0;
    remainder -= extra;
    return { user_id, amount_minor: base + extra };
  });
}

export function BudgetPanel({ tripId }: { tripId: string }) {
  const { user } = useAuth();
  const { onOp, resyncSignal } = useTripSocket();
  const { members, names, myRole } = useTripMembers(tripId);

  const [entries, setEntries] = useState<BudgetEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);

  const [label, setLabel] = useState("");
  const [category, setCategory] = useState<string>("food");
  const [amount, setAmount] = useState("");
  const [paidBy, setPaidBy] = useState("");

  const { flashProps } = useLiveFlash((op: OpFrame) =>
    BUDGET_OPS.has(op.kind) ? op.entity_id : null
  );

  const canManage = myRole === "owner" || myRole === "editor";

  const load = useCallback(
    () =>
      listBudget(tripId)
        .then((rows) => {
          setEntries(rows);
        })
        .catch((err: unknown) => {
          // The budget routes only mount when the API has them enabled; a 404 means the
          // deployment has no ledger, which is different from an empty one.
          setEntries([]);
          if (!(err instanceof ApiError && err.status === 404)) {
            setError("Couldn't load the budget.");
          }
        }),
    [tripId]
  );

  useEffect(() => {
    let cancelled = false;
    listBudget(tripId)
      .then((rows) => {
        if (!cancelled) setEntries(rows);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setEntries([]);
        if (!(err instanceof ApiError && err.status === 404)) {
          setError("Couldn't load the budget.");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [tripId, resyncSignal]);

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;
    const unsubscribe = onOp((op) => {
      if (!BUDGET_OPS.has(op.kind)) return;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        if (!cancelled) void load();
      }, OP_REFETCH_COALESCE_MS);
    });
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      unsubscribe();
    };
  }, [onOp, load]);

  // Default the payer to whoever is entering the expense — the common case by a distance.
  const payerDefault = paidBy || user?.id || "";

  const list = entries ?? [];
  const total = list.reduce((sum, e) => sum + e.amount_minor, 0);

  // Net position per member: what they paid, minus what they owe. Positive = owed money.
  const net = new Map<string, number>();
  for (const m of members) net.set(m.user_id, 0);
  for (const e of list) {
    if (e.paid_by) net.set(e.paid_by, (net.get(e.paid_by) ?? 0) + e.amount_minor);
    for (const s of e.splits) net.set(s.user_id, (net.get(s.user_id) ?? 0) - s.amount_minor);
  }
  const balances = [...net.entries()].sort((a, b) => b[1] - a[1]);
  const largest = Math.max(1, ...balances.map(([, v]) => Math.abs(v)));

  // Settle-up: greedily match the biggest creditor to the biggest debtor. Minimal transfers
  // are what a person actually wants ("pay Mira ₹40"), not a matrix of pairwise debts.
  const settlements: { from: string; to: string; amount: number }[] = [];
  {
    const debtors = balances.filter(([, v]) => v < 0).map(([id, v]) => [id, -v] as [string, number]);
    const creditors = balances.filter(([, v]) => v > 0).map(([id, v]) => [id, v] as [string, number]);
    let i = 0;
    let j = 0;
    while (i < debtors.length && j < creditors.length) {
      const pay = Math.min(debtors[i][1], creditors[j][1]);
      if (pay > 0) settlements.push({ from: debtors[i][0], to: creditors[j][0], amount: pay });
      debtors[i][1] -= pay;
      creditors[j][1] -= pay;
      if (debtors[i][1] === 0) i++;
      if (creditors[j][1] === 0) j++;
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const minor = Math.round(parseFloat(amount || "0") * 100);
    if (!label.trim() || !Number.isFinite(minor) || minor <= 0) return;
    setBusy(true);
    setError(null);
    try {
      await createBudgetEntry(tripId, {
        label: label.trim(),
        category,
        amountMinor: minor,
        paidBy: payerDefault || null,
        splits: splitEvenly(
          minor,
          members.map((m) => m.user_id)
        ),
      });
      setLabel("");
      setAmount("");
      setAdding(false);
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError ? (err.violations[0]?.message ?? err.message) : "Couldn't save that expense."
      );
    } finally {
      setBusy(false);
    }
  };

  const remove = async (entry: BudgetEntry) => {
    setBusy(true);
    setError(null);
    try {
      // Version is REQUIRED for a budget write (D85) — the entry is replaced whole, so the
      // caller has to state what it believes it is deleting.
      await deleteBudgetEntry(tripId, entry.id, entry.version);
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 409
          ? "Someone else changed this entry — reload and try again."
          : "Couldn't delete that expense."
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-8">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-display-lg text-fg">Budget</h1>
          <p className="mt-1 text-ui-md text-fg-muted">
            What the trip has cost, and who owes whom.
          </p>
        </div>
        {canManage && (
          <Button onClick={() => setAdding((v) => !v)} variant={adding ? "secondary" : "primary"}>
            {adding ? "Cancel" : "Add expense"}
          </Button>
        )}
      </header>

      {error && (
        <p role="alert" className="rounded-md border border-critical-600/25 bg-critical-50 px-4 py-2.5 text-ui-md text-critical-700">
          {error}
        </p>
      )}

      {adding && canManage && (
        <form
          onSubmit={(e) => void submit(e)}
          className="grid gap-3 rounded-card border border-line-subtle bg-surface-raised p-4 sm:grid-cols-[2fr_1fr_1fr_1fr_auto]"
        >
          <input
            required
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="Dinner at Ramiro"
            aria-label="What was it for"
            className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md focus:border-accent focus:outline-none"
          />
          <input
            required
            type="number"
            min="0.01"
            step="0.01"
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            placeholder="0.00"
            aria-label="Amount"
            className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md focus:border-accent focus:outline-none"
          />
          <select
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            aria-label="Category"
            className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md focus:border-accent focus:outline-none"
          >
            {BUDGET_CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
          <select
            value={payerDefault}
            onChange={(e) => setPaidBy(e.target.value)}
            aria-label="Paid by"
            className="rounded-sm border border-line bg-surface px-3 py-2 text-ui-md focus:border-accent focus:outline-none"
          >
            {members.map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.display_name || m.user_id.slice(0, 8)}
              </option>
            ))}
          </select>
          <Button type="submit" disabled={busy}>
            Save
          </Button>
          <p className="text-ui-xs text-fg-subtle sm:col-span-5">
            Split equally between all {members.length} members. The remainder goes to the
            earliest joiners, so the parts always add up to the total exactly.
          </p>
        </form>
      )}

      {/* ---- Summary: the trustworthiness of these numbers is the point ---- */}
      <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="rounded-card border border-line-subtle bg-surface-raised p-5">
          <p className="text-ui-2xs font-medium uppercase tracking-[0.12em] text-fg-subtle">
            Total spent
          </p>
          <p data-numeric className="mt-1 font-display text-display-lg text-fg">
            {formatMoney(total)}
          </p>
          <p className="mt-1 text-ui-xs text-fg-subtle">
            across {list.length} {list.length === 1 ? "expense" : "expenses"}
          </p>

          <div className="mt-5 space-y-2.5">
            <p className="text-ui-sm font-medium text-fg-muted">Net position</p>
            {entries === null &&
              [0, 1].map((i) => <Skeleton key={i} className="h-6 w-full" />)}
            {balances.map(([id, value]) => (
              <div key={id} className="flex items-center gap-3">
                <span className="w-32 shrink-0 truncate text-ui-sm text-fg">
                  {names[id] ?? id.slice(0, 8)}
                  {user?.id === id && <span className="text-fg-subtle"> (you)</span>}
                </span>
                <div className="relative h-4 flex-1">
                  {/* Centre line = settled. Bars grow right if owed, left if owing — the
                      shape reads before the number does. */}
                  <span aria-hidden className="absolute inset-y-0 left-1/2 w-px bg-line" />
                  <span
                    aria-hidden
                    className={`absolute inset-y-0.5 rounded-xs ${
                      value >= 0 ? "left-1/2 bg-positive-600/70" : "right-1/2 bg-critical-600/60"
                    }`}
                    style={{ width: `${(Math.abs(value) / largest) * 50}%` }}
                  />
                </div>
                <span
                  data-numeric
                  className={`w-24 shrink-0 text-right text-ui-sm font-semibold ${
                    value > 0 ? "text-positive-600" : value < 0 ? "text-critical-600" : "text-fg-subtle"
                  }`}
                >
                  {value === 0 ? "settled" : value > 0 ? `+${formatMoney(value)}` : formatMoney(value)}
                </span>
              </div>
            ))}
          </div>
        </div>

        <div className="rounded-card border border-line-subtle bg-surface-raised p-5">
          <p className="text-ui-2xs font-medium uppercase tracking-[0.12em] text-fg-subtle">
            Settle up
          </p>
          {settlements.length === 0 ? (
            <p className="mt-3 text-ui-md text-fg-muted">
              {list.length === 0 ? "Nothing spent yet." : "Everyone is square."}
            </p>
          ) : (
            <ul className="mt-3 space-y-2">
              {settlements.map((s, i) => (
                <li
                  key={i}
                  className="flex items-center justify-between gap-3 rounded-md bg-surface-sunken px-3 py-2"
                >
                  <span className="min-w-0 truncate text-ui-md text-fg">
                    <strong className="font-medium">{names[s.from] ?? s.from.slice(0, 8)}</strong>
                    <span className="text-fg-subtle"> pays </span>
                    <strong className="font-medium">{names[s.to] ?? s.to.slice(0, 8)}</strong>
                  </span>
                  <span data-numeric className="shrink-0 text-ui-md font-semibold text-fg">
                    {formatMoney(s.amount)}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-3 text-ui-xs text-fg-subtle">
            The fewest transfers that clear every balance.
          </p>
        </div>
      </section>

      {/* ---- The underlying entries ---- */}
      <section>
        <h2 className="mb-3 font-display text-display-sm text-fg">Expenses</h2>

        {entries === null && (
          <div className="space-y-2">
            <Skeleton className="h-16 w-full rounded-card" />
            <Skeleton className="h-16 w-full rounded-card" />
          </div>
        )}

        {/* Panel-level empty states share one treatment across the app — display heading,
            py-14, a supporting line under it — because they are the same moment on three
            screens. This one was the odd one out at body-weight and py-12; Itinerary
            ("Nothing planned yet") and Memories ("Nothing to remember yet") were already the
            heading form, and a heading that shrinks depending on which tab you are on reads
            as a different app. Section-level empties (a day with no slots, a slot with no
            options) stay deliberately quieter — they sit under a heading already. */}
        {entries !== null && list.length === 0 && (
          <div className="rounded-card border border-dashed border-line bg-surface-raised px-6 py-14 text-center">
            <h2 className="font-display text-display-md text-fg">Nothing recorded yet</h2>
            <p className="mx-auto mt-2 max-w-md text-ui-md text-fg-muted">
              Add what people have paid for and the split works itself out.
            </p>
          </div>
        )}

        <ul className="space-y-2">
          {list.map((entry) => (
            <BudgetRow
              key={entry.id}
              entry={entry}
              names={names}
              canManage={canManage}
              busy={busy}
              onDelete={() => void remove(entry)}
              flash={flashProps(entry.id)}
            />
          ))}
        </ul>
      </section>
    </div>
  );
}

function BudgetRow({
  entry,
  names,
  canManage,
  busy,
  onDelete,
  flash,
}: {
  entry: BudgetEntry;
  names: Record<string, string>;
  canManage: boolean;
  busy: boolean;
  onDelete: () => void;
  flash?: Record<string, unknown>;
}) {
  const [open, setOpen] = useState(false);
  const splitTotal = entry.splits.reduce((s, x) => s + x.amount_minor, 0);
  // Always true — a deferred constraint trigger refuses a ledger where it is not (D45). Shown
  // rather than assumed because "these numbers are trustworthy" is the whole claim of the
  // atomic-write work, and a claim the interface never displays is one the user cannot use.
  const balances = splitTotal === entry.amount_minor;

  return (
    <li {...flash} className="rounded-card border border-line-subtle bg-surface-raised">
      <div className="flex items-center gap-4 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-ui-md font-medium text-fg">{entry.label}</span>
            <span className="shrink-0 rounded-xs bg-surface-sunken px-1.5 py-0.5 text-ui-2xs font-medium uppercase tracking-[0.08em] text-fg-muted">
              {entry.category}
            </span>
          </div>
          <p className="mt-0.5 text-ui-xs text-fg-subtle">
            paid by {entry.paid_by ? (names[entry.paid_by] ?? entry.paid_by.slice(0, 8)) : "—"} ·{" "}
            {entry.splits.length} way split
          </p>
        </div>
        <span data-numeric className="shrink-0 text-ui-lg font-semibold text-fg">
          {formatMoney(entry.amount_minor)}
        </span>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="shrink-0 rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 hover:text-fg"
        >
          {open ? "Hide" : "Split"}
        </button>
      </div>

      {open && (
        <div className="border-t border-line-subtle px-4 py-3">
          <ul className="space-y-1">
            {entry.splits.map((s) => (
              <li key={s.user_id} className="flex items-center justify-between text-ui-sm">
                <span className="text-fg-muted">{names[s.user_id] ?? s.user_id.slice(0, 8)}</span>
                <span data-numeric className="text-fg">
                  {formatMoney(s.amount_minor)}
                </span>
              </li>
            ))}
          </ul>
          {/* The arithmetic, shown adding up. */}
          <div
            className={`mt-2 flex items-center justify-between border-t pt-2 text-ui-sm font-semibold ${
              balances ? "border-line" : "border-critical-600"
            }`}
          >
            <span className={balances ? "text-positive-600" : "text-critical-600"}>
              {balances ? "✓ Splits add up" : "✗ Splits do not add up"}
            </span>
            <span data-numeric className={balances ? "text-fg" : "text-critical-600"}>
              {formatMoney(splitTotal)}
            </span>
          </div>

          {canManage && (
            <button
              type="button"
              onClick={onDelete}
              disabled={busy}
              className="mt-3 rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 transition-colors hover:text-critical-600 disabled:opacity-50"
            >
              Delete this expense
            </button>
          )}
        </div>
      )}
    </li>
  );
}
