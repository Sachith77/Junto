"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { createTrip } from "@/lib/api/trips";
import { useAuth } from "@/context/AuthContext";
import { ApiError } from "@/lib/http";
import { ShellHeader } from "@/components/ShellHeader";
import { Button, ButtonLink } from "@/components/ui/Button";
import { Media } from "@/components/ui/Media";
import { formatDateRange } from "@/lib/format";
import { useBrowserTimeZone, useTimeZoneList } from "@/lib/useTimeZone";

const FIELD =
  "w-full rounded-sm border border-line bg-surface-raised px-3 py-2.5 text-ui-lg text-fg " +
  "placeholder:text-fg-subtle focus:border-accent focus:outline-none " +
  "focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent";

const LABEL = "block text-ui-sm font-medium text-fg";

export default function NewTripPage() {
  const { status } = useAuth();
  const router = useRouter();

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [startDate, setStartDate] = useState("");
  const [endDate, setEndDate] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // Defaults to where the reader actually is; an explicit pick wins once made.
  const browserZone = useBrowserTimeZone();
  const zones = useTimeZoneList();
  const [zoneOverride, setZoneOverride] = useState<string | null>(null);
  const timeZone = zoneOverride ?? browserZone;

  useEffect(() => {
    if (status === "anonymous") router.replace("/login");
  }, [status, router]);

  // The preview shows LAYOUT — how the name, dates and description will sit on a
  // trips-list card. It cannot show the real cover: covers are seeded from the
  // trip id, and the id does not exist until the server assigns it (trips are
  // one of the few entities a client does not name, unlike D4 entities). Seeding
  // the preview on the name keeps it stable while typing; the caption under it
  // says plainly that the final art is assigned on creation, because a preview
  // that quietly shows different art than you get is a small lie.
  const previewSeed = name.trim() || "junto-new-trip";

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setError(null);
    setSubmitting(true);
    try {
      const trip = await createTrip({
        name: name.trim(),
        description: description.trim(),
        timeZone,
        startDate: startDate || null,
        endDate: endDate || null,
      });
      router.push(`/trips/${trip.id}`);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.violations[0]?.message ?? err.message);
      } else {
        setError("Something went wrong creating the trip.");
      }
      setSubmitting(false);
    }
  };

  if (status !== "authenticated") return null;

  return (
    <div className="flex min-h-full flex-1 flex-col">
      <ShellHeader />
      <main className="mx-auto w-full max-w-5xl flex-1 px-6 py-12 sm:px-8 sm:py-16">
        <h1 className="font-display text-display-xl text-fg">Start a trip</h1>
        <p className="mt-2 text-ui-lg text-fg-muted">
          You can invite everyone and fill in the details later.
        </p>

        <div className="mt-10 grid gap-10 lg:grid-cols-[minmax(0,1fr)_360px]">
          <form onSubmit={onSubmit} className="space-y-6">
            {error && (
              <p
                role="alert"
                className="rounded-md border border-critical-600/25 bg-critical-50 px-4 py-3 text-ui-md text-critical-700"
              >
                {error}
              </p>
            )}

            <div className="space-y-1.5">
              <label className={LABEL} htmlFor="name">
                Trip name
              </label>
              <input
                id="name"
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Lisbon in autumn"
                className={FIELD}
              />
            </div>

            <div className="space-y-1.5">
              <label className={LABEL} htmlFor="description">
                Description <span className="font-normal text-fg-subtle">(optional)</span>
              </label>
              <textarea
                id="description"
                rows={3}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Tiled streets, long lunches, and one very contested dinner reservation."
                className={`${FIELD} resize-y`}
              />
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <label className={LABEL} htmlFor="start">
                  Starts <span className="font-normal text-fg-subtle">(optional)</span>
                </label>
                <input
                  id="start"
                  type="date"
                  value={startDate}
                  onChange={(e) => setStartDate(e.target.value)}
                  className={FIELD}
                />
              </div>
              <div className="space-y-1.5">
                <label className={LABEL} htmlFor="end">
                  Ends <span className="font-normal text-fg-subtle">(optional)</span>
                </label>
                <input
                  id="end"
                  type="date"
                  min={startDate || undefined}
                  value={endDate}
                  onChange={(e) => setEndDate(e.target.value)}
                  className={FIELD}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <label className={LABEL} htmlFor="tz">
                Time zone
              </label>
              <select
                id="tz"
                value={timeZone}
                onChange={(e) => setZoneOverride(e.target.value)}
                className={FIELD}
              >
                {/* The browser's own zone may not be in the platform list on
                    older runtimes; include it so `value` always has an option. */}
                {(zones.includes(timeZone) ? zones : [timeZone, ...zones]).map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </select>
              <p className="text-ui-xs text-fg-subtle">
                Every time on the itinerary is local to the trip, not to whoever is reading it.
              </p>
            </div>

            <div className="flex items-center gap-3 pt-2">
              <Button type="submit" size="lg" disabled={submitting || !name.trim()}>
                {submitting ? "Creating…" : "Create trip"}
              </Button>
              <ButtonLink href="/trips" variant="ghost" size="lg">
                Cancel
              </ButtonLink>
            </div>
          </form>

          <aside className="lg:pt-1">
            <p className="mb-3 text-ui-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
              Preview
            </p>
            <Media seed={previewSeed} className="h-72 rounded-card shadow-lg">
              <div className="absolute inset-x-0 bottom-0 p-6">
                <p className="text-ui-2xs font-medium uppercase tracking-[0.12em] text-accent-on-dark">
                  {formatDateRange(
                    startDate ? `${startDate}T00:00:00Z` : null,
                    endDate ? `${endDate}T00:00:00Z` : null
                  )}
                </p>
                <h2 className="mt-2 font-display text-display-lg text-fg-on-media">
                  {name.trim() || "Your trip"}
                </h2>
                {description.trim() && (
                  <p className="mt-1.5 line-clamp-2 text-ui-md text-fg-on-media-dim">
                    {description.trim()}
                  </p>
                )}
              </div>
            </Media>
            <p className="mt-3 text-ui-xs text-fg-subtle">
              Cover art is assigned when the trip is created, then stays the same for everyone.
            </p>
          </aside>
        </div>
      </main>
    </div>
  );
}
