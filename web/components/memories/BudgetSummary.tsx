"use client";

import { TRIP_BUDGET_RECAP } from "./MemoriesData";

export function BudgetSummary() {
  const { currency, plannedBudget, actualSpent, savingsTotal, perPersonActual, perPersonSaved, breakdown } =
    TRIP_BUDGET_RECAP;

  return (
    <section className="mx-auto max-w-5xl rounded-3xl border border-line-subtle bg-surface-raised p-6 shadow-xl sm:p-10">
      <div className="flex flex-col sm:flex-row sm:items-baseline justify-between gap-2 border-b border-line-subtle pb-5">
        <div>
          <span className="font-editorial text-ui-xs uppercase tracking-[0.2em] text-accent-text">
            Financial Ledger
          </span>
          <h2 className="mt-1 font-display text-display-lg font-normal text-fg tracking-tight">
            Planned vs. Actual Spent
          </h2>
        </div>
        <p className="text-ui-xs text-fg-subtle">
          Final reconciled split for 4 members
        </p>
      </div>

      {/* Main Totals Cards */}
      <div className="mt-6 grid gap-4 sm:grid-cols-3">
        <div className="rounded-2xl border border-line bg-surface p-5">
          <span className="text-ui-xs text-fg-subtle">Planned Target</span>
          <p className="mt-1 font-display text-3xl text-fg" data-numeric>
            {currency}{plannedBudget.toLocaleString()}
          </p>
          <span className="mt-1 block text-[11px] text-fg-subtle">Initial group budget</span>
        </div>

        <div className="rounded-2xl border border-line bg-surface p-5">
          <span className="text-ui-xs text-fg-subtle">Actual Group Spend</span>
          <p className="mt-1 font-display text-3xl text-accent-text" data-numeric>
            {currency}{actualSpent.toLocaleString()}
          </p>
          <span className="mt-1 block text-[11px] text-fg-subtle">{currency}{perPersonActual.toLocaleString()} per person</span>
        </div>

        <div className="rounded-2xl border border-line bg-surface p-5">
          <span className="text-ui-xs text-fg-subtle">Under Budget By</span>
          <p className="mt-1 font-display text-3xl text-emerald-400" data-numeric>
            {currency}{savingsTotal.toLocaleString()}
          </p>
          <span className="mt-1 block text-[11px] text-emerald-400/80">Saved {currency}{perPersonSaved.toLocaleString()} each</span>
        </div>
      </div>

      {/* Category Breakdown Rows */}
      <div className="mt-6 rounded-2xl border border-line bg-surface p-5">
        <h3 className="text-ui-xs font-semibold uppercase tracking-wider text-fg-subtle mb-4">
          Category Summary
        </h3>

        <div className="space-y-3">
          {breakdown.map((item) => {
            const isUnder = item.actual <= item.planned;

            return (
              <div
                key={item.category}
                className="flex items-center justify-between text-ui-sm border-b border-line-subtle/50 pb-2.5 last:border-0 last:pb-0"
              >
                <span className="text-fg">{item.category}</span>
                <div className="flex items-center gap-4 text-ui-xs" data-numeric>
                  <span className="text-fg-subtle">Target: {currency}{item.planned.toLocaleString()}</span>
                  <span className="font-medium text-fg">{currency}{item.actual.toLocaleString()}</span>
                  <span className={`text-[11px] ${isUnder ? "text-emerald-400" : "text-rose-400"}`}>
                    {isUnder ? "✓ Under" : "Over"}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}
