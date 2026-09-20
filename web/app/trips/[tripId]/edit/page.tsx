"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { ShellHeader } from "@/components/ShellHeader";
import { useTrip } from "@/components/TripShell";
import { CoverPhotoField } from "@/components/trips/CoverPhotoField";
import { Button, ButtonLink } from "@/components/ui/Button";
import { deleteTrip, updateTrip, uploadTripCover, type CoverSuggestion } from "@/lib/api/trips";
import { ApiError } from "@/lib/http";

const FIELD = "w-full rounded-sm border border-line bg-surface-raised px-3 py-2.5 text-ui-lg text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent";
const LABEL = "block text-ui-sm font-medium text-fg";

export default function EditTripPage({ params }: { params: Promise<{ tripId: string }> }) {
  const trip = useTrip();
  const router = useRouter();
  const [tripId, setTripId] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [startDate, setStartDate] = useState("");
  const [endDate, setEndDate] = useState("");
  const [timeZone, setTimeZone] = useState("UTC");
  const [coverFile, setCoverFile] = useState<File | null>(null);
  const [coverSuggestion, setCoverSuggestion] = useState<CoverSuggestion | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  useEffect(() => { params.then(({ tripId: id }) => setTripId(id)); }, [params]);
  useEffect(() => {
    if (!trip) return;
    setName(trip.name); setDescription(trip.description); setTimeZone(trip.time_zone);
    setStartDate(trip.start_date?.slice(0, 10) ?? ""); setEndDate(trip.end_date?.slice(0, 10) ?? "");
  }, [trip]);

  if (!trip || !tripId) return null;

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSubmitting(true); setError(null);
    try {
      let updated = await updateTrip(tripId, {
        name: name.trim(), description: description.trim(), timeZone,
        startDate: startDate || null, endDate: endDate || null, version: trip.version,
        coverSuggestionId: coverFile ? null : coverSuggestion?.id ?? null,
      });
      if (coverFile) updated = await uploadTripCover(tripId, coverFile);
      router.push(`/trips/${updated.id}`);
      router.refresh();
    } catch (err) {
      setError(err instanceof ApiError ? (err.violations[0]?.message ?? err.message) : "We couldn’t save the trip. Please try again.");
      setSubmitting(false);
    }
  };

  const onDeleteTrip = async () => {
    setDeleting(true);
    setDeleteError(null);
    try {
      await deleteTrip(tripId, trip.version);
      router.push("/trips");
      router.refresh();
    } catch (err) {
      setDeleteError(err instanceof ApiError ? (err.violations[0]?.message ?? err.message) : "Could not delete trip. Please try again.");
      setDeleting(false);
    }
  };

  return (
    <div className="flex min-h-full flex-1 flex-col bg-surface">
      <ShellHeader />
      <main className="mx-auto w-full max-w-5xl flex-1 px-6 py-10 sm:px-8 sm:py-14">
        <p className="text-ui-2xs font-semibold uppercase tracking-[.16em] text-accent-text">Trip settings</p>
        <h1 className="mt-2 font-display text-display-xl text-fg">Edit your trip</h1>
        <div className="mt-9 grid gap-10 lg:grid-cols-[minmax(0,1fr)_360px]">
          <form onSubmit={onSubmit} className="space-y-6">
            {error && <p role="alert" className="rounded-md border border-critical-600/25 bg-critical-50 px-4 py-3 text-ui-md text-critical-700">{error}</p>}
            <div className="space-y-1.5">
              <label className={LABEL} htmlFor="name">Trip name or destination</label>
              <input id="name" required value={name} onChange={(e) => setName(e.target.value)} className={FIELD} />
            </div>
            <div className="space-y-1.5">
              <label className={LABEL} htmlFor="description">Description <span className="font-normal text-fg-subtle">(optional)</span></label>
              <textarea id="description" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} className={`${FIELD} resize-y`} />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5"><label className={LABEL} htmlFor="start">Starts</label><input id="start" type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} className={FIELD} /></div>
              <div className="space-y-1.5"><label className={LABEL} htmlFor="end">Ends</label><input id="end" type="date" min={startDate || undefined} value={endDate} onChange={(e) => setEndDate(e.target.value)} className={FIELD} /></div>
            </div>
            <div className="space-y-1.5"><label className={LABEL} htmlFor="timezone">Time zone</label><input id="timezone" value={timeZone} onChange={(e) => setTimeZone(e.target.value)} className={FIELD} /></div>
            <div className="flex items-center gap-3 pt-2">
              <Button type="submit" size="lg" disabled={submitting || !name.trim()}>{submitting ? (coverFile ? "Saving and uploading…" : "Saving…") : "Save changes"}</Button>
              <ButtonLink href={`/trips/${tripId}`} variant="ghost" size="lg">Cancel</ButtonLink>
            </div>
          </form>
          <aside>
            <p className="mb-3 text-ui-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">Cover preview</p>
            <CoverPhotoField destination={name} file={coverFile} onFileChange={setCoverFile} onSuggestionChange={setCoverSuggestion} currentCover={trip.cover} autoSuggest={trip.cover.source !== "uploaded"}>
              <div className="absolute inset-x-0 bottom-0 p-6"><h2 className="line-clamp-2 font-display text-display-lg text-fg-on-media">{name || trip.name}</h2></div>
            </CoverPhotoField>
          </aside>
        </div>

        {/* Danger Zone: Delete Trip */}
        <section className="mt-16 rounded-card border border-critical-600/30 bg-critical-50/40 p-6 sm:p-8">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-ui-md font-semibold text-critical-700">Delete this trip</h2>
              <p className="mt-1 max-w-xl text-ui-sm text-fg-muted">
                Permanently delete this trip along with all itinerary slots, votes, comments, and budget splits. This action cannot be undone.
              </p>
            </div>
            <div>
              <Button
                type="button"
                variant="danger"
                size="md"
                onClick={() => setConfirmDeleteOpen(true)}
              >
                Delete trip
              </Button>
            </div>
          </div>
        </section>

        {/* Delete Confirmation Modal */}
        {confirmDeleteOpen && (
          <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm">
            <div className="w-full max-w-md rounded-card border border-line bg-surface p-6 shadow-2xl animate-in fade-in zoom-in-95 duration-150">
              <h3 className="font-display text-display-md text-fg">Delete &ldquo;{trip.name}&rdquo;?</h3>
              <p className="mt-3 text-ui-sm leading-relaxed text-fg-muted">
                Are you sure you want to delete this trip? All collaborative plans, votes, and records will be removed immediately for everyone in the group.
              </p>

              {deleteError && (
                <p className="mt-3 rounded-md bg-critical-50 p-2.5 text-ui-xs text-critical-700">
                  {deleteError}
                </p>
              )}

              <div className="mt-6 flex items-center justify-end gap-3">
                <Button
                  type="button"
                  variant="ghost"
                  size="md"
                  disabled={deleting}
                  onClick={() => {
                    setConfirmDeleteOpen(false);
                    setDeleteError(null);
                  }}
                >
                  Keep trip
                </Button>
                <Button
                  type="button"
                  variant="danger"
                  size="md"
                  disabled={deleting}
                  onClick={onDeleteTrip}
                >
                  {deleting ? "Deleting…" : "Yes, delete trip"}
                </Button>
              </div>
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

