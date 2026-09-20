"use client";

import Link from "next/link";
import { useState } from "react";
import { Media } from "@/components/ui/Media";
import { formatDateRange, tripNights } from "@/lib/format";
import { coverSeedForTrip } from "@/lib/cover";
import { Button } from "@/components/ui/Button";
import type { Trip } from "@/lib/types";

/** A quiet editorial cover. Motion is limited to a small lift: the trip itself,
 * not a cursor effect, is the focal point. */
export function TripCard({
  trip,
  onDelete,
}: {
  trip: Trip;
  onDelete?: (tripId: string, version: number) => Promise<void>;
}) {
  const nights = tripNights(trip.start_date, trip.end_date);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const handleDelete = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (!onDelete) return;
    setDeleting(true);
    try {
      await onDelete(trip.id, trip.version);
    } catch {
      setDeleting(false);
      setConfirmOpen(false);
    }
  };

  return (
    <>
      <div className="group relative rounded-card">
        <Link
          href={`/trips/${trip.id}`}
          data-testid="trip-card"
          className="block rounded-card focus-visible:outline-2 focus-visible:outline-offset-2"
        >
          <Media
            seed={coverSeedForTrip(trip)}
            image={trip.cover?.url}
            className="h-72 rounded-card border border-white/15 shadow-lg transition-[transform,border-color,box-shadow] duration-200 ease-out group-hover:-translate-y-1 group-hover:border-white/30 group-hover:shadow-xl group-focus-visible:-translate-y-1 sm:h-96"
          >
            <div className="absolute inset-x-0 top-0 flex items-center justify-between p-6 text-ui-2xs font-medium uppercase tracking-[0.15em] text-accent-on-dark sm:p-7">
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                <span>{formatDateRange(trip.start_date, trip.end_date)}</span>
                {nights !== null && (
                  <>
                    <span aria-hidden className="opacity-40">·</span>
                    <span>{nights} {nights === 1 ? "night" : "nights"}</span>
                  </>
                )}
              </div>

              {onDelete && (
                <button
                  type="button"
                  title="Delete trip"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setConfirmOpen(true);
                  }}
                  className="relative z-20 flex h-8 w-8 items-center justify-center rounded-full bg-black/40 text-white/70 opacity-0 backdrop-blur-md transition-all hover:scale-110 hover:bg-critical-600 hover:text-white group-hover:opacity-100"
                >
                  <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                  </svg>
                </button>
              )}
            </div>

            <div className="absolute inset-x-0 bottom-0 min-w-0 p-6 sm:p-7">
              <h3 className="line-clamp-2 max-w-[16ch] font-display text-[clamp(2rem,7vw,2.75rem)] leading-[1.05] tracking-[-0.02em] text-fg-on-media">{trip.name}</h3>
              {trip.description && (
                <p className="mt-2 line-clamp-2 max-w-md break-words text-ui-md leading-relaxed text-fg-on-media-dim">{trip.description}</p>
              )}
            </div>
          </Media>
        </Link>
        {trip.cover?.source === "suggested" && trip.cover.photographer_name && (
          <span className="absolute bottom-3 right-4 z-10 text-[10px] text-white/65">
            Photo by <a href={trip.cover.photographer_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">{trip.cover.photographer_name}</a>{" "}
            on <a href={trip.cover.photo_url} target="_blank" rel="noreferrer" className="underline-offset-2 hover:text-white hover:underline">Unsplash</a>
          </span>
        )}
      </div>

      {/* Delete Confirmation Modal */}
      {confirmOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
          }}
        >
          <div
            className="w-full max-w-md rounded-card border border-line bg-surface p-6 shadow-2xl animate-in fade-in zoom-in-95 duration-150"
            onClick={(e) => e.stopPropagation()}
          >
            <h3 className="font-display text-display-md text-fg">Delete &ldquo;{trip.name}&rdquo;?</h3>
            <p className="mt-3 text-ui-sm leading-relaxed text-fg-muted">
              Are you sure you want to delete this trip? All collaborative plans, votes, comments, and budget splits will be permanently removed.
            </p>

            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                type="button"
                variant="ghost"
                size="md"
                disabled={deleting}
                onClick={() => setConfirmOpen(false)}
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="danger"
                size="md"
                disabled={deleting}
                onClick={handleDelete}
              >
                {deleting ? "Deleting…" : "Delete trip"}
              </Button>

            </div>
          </div>
        </div>
      )}
    </>
  );
}

