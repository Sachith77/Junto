"use client";

import { Button } from "@/components/ui/Button";
import { formatMoney } from "@/lib/format";
import type { SlotOption } from "@/lib/types";

/**
 * A candidate under a decision — and the screen where D41 has to land.
 *
 * D41 says the group's RESOLUTION is stored, not derived from the tally, because groups
 * routinely override their own vote ("we voted A, but B had availability"). The interface has
 * to make those two facts separable at a glance, so:
 *
 *   CHOSEN  — filled accent badge + accent border + tinted surface + a left rail.
 *   VOTES   — a neutral grey counter, never accent, never competing for the eye.
 *
 * The most-voted option is deliberately NOT styled as chosen. If it were, the two facts would
 * collapse back into one and the override case — the entire reason the column is stored —
 * would be invisible.
 *
 * Colour is never the only carrier: "Chosen" is a word, the count says "votes", and the
 * chosen card gets a border and rail that survive greyscale.
 */
export function OptionCard({
  option,
  voteCount,
  totalVotes,
  isChosen,
  isMostVoted,
  hasMyVote,
  proposerName,
  disabled,
  canResolve,
  onVote,
  onRetract,
  onChoose,
  onClearChoice,
  flash,
}: {
  option: SlotOption;
  voteCount: number;
  totalVotes: number;
  isChosen: boolean;
  isMostVoted: boolean;
  hasMyVote: boolean;
  proposerName?: string;
  disabled?: boolean;
  canResolve: boolean;
  onVote: () => void;
  onRetract: () => void;
  onChoose: () => void;
  onClearChoice: () => void;
  flash?: Record<string, unknown>;
}) {
  const share = totalVotes > 0 ? Math.round((voteCount / totalVotes) * 100) : 0;

  return (
    <div
      {...flash}
      data-testid="option-card"
      data-chosen={isChosen}
      className={`relative overflow-hidden rounded-card border transition-colors ${
        isChosen
          ? "border-accent-300 bg-accent-tint"
          : "border-line-subtle bg-surface-raised hover:border-line"
      }`}
    >
      {/* The rail is the greyscale-safe half of the chosen treatment. */}
      {isChosen && <span aria-hidden className="absolute inset-y-0 left-0 w-1 bg-accent" />}

      <div className="flex items-start justify-between gap-4 p-4 pl-5">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-ui-lg font-medium text-fg">{option.title}</h3>
            {isChosen && (
              <span
                data-testid="chosen-badge"
                className="rounded-xs bg-accent px-2 py-0.5 text-ui-2xs font-semibold uppercase tracking-[0.08em] text-fg-inverse"
              >
                ✓ Chosen
              </span>
            )}
            {/* Named explicitly rather than styled like the resolution — this is the fact
                D41 says must NOT be mistaken for the decision. */}
            {!isChosen && isMostVoted && totalVotes > 0 && (
              <span className="rounded-xs border border-line px-2 py-0.5 text-ui-2xs font-medium text-fg-muted">
                Most votes
              </span>
            )}
          </div>

          {proposerName && (
            <p className="mt-1 text-ui-xs text-fg-subtle">proposed by {proposerName}</p>
          )}
          {option.notes && <p className="mt-2 text-ui-md text-fg-muted">{option.notes}</p>}

          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-ui-sm text-fg-muted">
            {option.estimated_cost_minor != null && (
              <span data-numeric className="font-medium text-fg">
                {formatMoney(option.estimated_cost_minor)}
              </span>
            )}
            {option.place.name && <span>{option.place.name}</span>}
            {option.external_url && (
              <a
                href={option.external_url}
                target="_blank"
                rel="noreferrer"
                className="text-accent-text underline underline-offset-2"
              >
                Link
              </a>
            )}
          </div>
        </div>

        <div className="flex w-32 shrink-0 flex-col items-end gap-2">
          {/* The tally: neutral, quiet, and quantitative. */}
          <div className="w-full text-right">
            <span
              data-testid="vote-count"
              data-numeric
              className="text-ui-sm font-semibold text-fg-muted"
            >
              {voteCount} {voteCount === 1 ? "vote" : "votes"}
            </span>
            <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-surface-sunken">
              <div
                className="h-full rounded-full bg-line-strong transition-[width] duration-300"
                style={{ width: `${share}%` }}
              />
            </div>
          </div>

          <Button
            size="sm"
            variant={hasMyVote ? "secondary" : "primary"}
            disabled={disabled}
            onClick={hasMyVote ? onRetract : onVote}
            data-testid={hasMyVote ? "retract-vote" : "cast-vote"}
            className="w-full"
          >
            {hasMyVote ? "Voted ✓" : "Vote"}
          </Button>

          {canResolve && (
            <button
              type="button"
              disabled={disabled}
              onClick={isChosen ? onClearChoice : onChoose}
              data-testid={isChosen ? "clear-choice" : "choose-option"}
              className="rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 transition-colors hover:text-fg disabled:opacity-50"
            >
              {isChosen ? "Un-choose" : "Choose this"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
