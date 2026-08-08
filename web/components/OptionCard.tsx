"use client";

import type { SlotOption } from "@/lib/types";

export function OptionCard({
  option,
  voteCount,
  isSelected,
  hasMyVote,
  proposerName,
  disabled,
  onVote,
  onRetract,
}: {
  option: SlotOption;
  voteCount: number;
  isSelected: boolean;
  hasMyVote: boolean;
  proposerName?: string;
  disabled?: boolean;
  onVote: () => void;
  onRetract: () => void;
}) {
  const cost =
    option.estimated_cost_minor != null ? (option.estimated_cost_minor / 100).toFixed(2) : null;

  return (
    <div
      data-testid="option-card"
      data-selected={isSelected}
      className={`rounded-lg border p-4 shadow-sm transition-colors ${
        isSelected ? "border-emerald-500 bg-emerald-50" : "border-neutral-200 bg-white"
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h3 className="font-medium text-neutral-900">{option.title}</h3>
            {isSelected && (
              <span
                data-testid="selected-badge"
                className="rounded-full bg-emerald-600 px-2 py-0.5 text-xs font-semibold text-white"
              >
                Selected
              </span>
            )}
          </div>
          {proposerName && (
            <p className="text-xs text-neutral-500">proposed by {proposerName}</p>
          )}
          {option.notes && <p className="mt-1 text-sm text-neutral-600">{option.notes}</p>}
          {cost !== null && (
            <p className="mt-1 text-sm font-medium text-neutral-700">est. {cost}</p>
          )}
          {option.external_url && (
            <a
              href={option.external_url}
              target="_blank"
              rel="noreferrer"
              className="mt-1 inline-block text-sm text-blue-600 underline"
            >
              link
            </a>
          )}
        </div>

        <div className="flex flex-col items-end gap-2">
          <span
            data-testid="vote-count"
            className="rounded-full bg-neutral-100 px-2.5 py-1 text-sm font-semibold text-neutral-700"
          >
            {voteCount} {voteCount === 1 ? "vote" : "votes"}
          </span>
          <button
            type="button"
            disabled={disabled}
            onClick={hasMyVote ? onRetract : onVote}
            data-testid={hasMyVote ? "retract-vote" : "cast-vote"}
            className={`rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:opacity-50 ${
              hasMyVote
                ? "bg-neutral-800 text-white hover:bg-neutral-700"
                : "bg-blue-600 text-white hover:bg-blue-500"
            }`}
          >
            {hasMyVote ? "Voted ✓ (retract)" : "Vote"}
          </button>
        </div>
      </div>
    </div>
  );
}
