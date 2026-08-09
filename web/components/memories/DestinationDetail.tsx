"use client";

import Link from "next/link";
import Image from "next/image";
import { useEffect, useState } from "react";
import { getSlot } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { listComments } from "@/lib/api/comments";
import { attachmentURL, listAttachments, type Attachment } from "@/lib/api/attachments";
import { useTripMembers } from "@/hooks/useTripMembers";
import { Media } from "@/components/ui/Media";
import { Skeleton } from "@/components/ui/Skeleton";
import { formatMoney } from "@/lib/format";
import type { Comment, Slot, SlotOption } from "@/lib/types";

interface Photo {
  attachment: Attachment;
  url: string;
}

/**
 * One destination: the place the group chose, its photos, and what was said about it.
 *
 * Photos are REAL attachments (Stage 2 Slice 3), fetched for the slot and its chosen option
 * and exchanged for a freshly signed URL per render — the storage key never leaves the server
 * and a signed URL is never persisted anywhere, because it is false by the time anything reads
 * it back (D90).
 *
 * There is no upload control here. Uploading is a three-party exchange (presign, direct PUT to
 * object storage, server-side confirm) and belongs to its own slice; building half of it would
 * be worse than pointing at where photos come from.
 */
export function DestinationDetail({ tripId, slotId }: { tripId: string; slotId: string }) {
  const { names } = useTripMembers(tripId);
  const [slot, setSlot] = useState<Slot | null>(null);
  const [option, setOption] = useState<SlotOption | null>(null);
  const [photos, setPhotos] = useState<Photo[] | null>(null);
  const [comments, setComments] = useState<Comment[]>([]);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      const [s, options, notes] = await Promise.all([
        getSlot(tripId, slotId),
        listOptions(tripId, slotId),
        listComments(tripId, slotId).catch(() => [] as Comment[]),
      ]);
      const chosen = options.find((o) => o.id === s.selected_option_id) ?? null;

      // Attachments hang off exactly one owner (the exclusive arc, D47), so a photo of this
      // place may be attached to the slot OR to the option that won. Both are "this place".
      const owners = [
        listAttachments(tripId, { slotId: s.id }).catch(() => [] as Attachment[]),
        chosen
          ? listAttachments(tripId, { slotOptionId: chosen.id }).catch(() => [] as Attachment[])
          : Promise.resolve([] as Attachment[]),
      ];
      const found = (await Promise.all(owners)).flat().filter((a) => a.status === "ready");

      const resolved = await Promise.all(
        found.map(async (attachment) => {
          if (attachment.kind === "link") {
            return { attachment, url: attachment.external_url ?? "" };
          }
          const url = await attachmentURL(tripId, attachment.id).catch(() => "");
          return { attachment, url };
        })
      );

      if (!cancelled) {
        setSlot(s);
        setOption(chosen);
        setComments(notes);
        setPhotos(resolved.filter((p) => p.url));
      }
    })().catch(() => {
      if (!cancelled) {
        setPhotos([]);
      }
    });

    return () => {
      cancelled = true;
    };
  }, [tripId, slotId]);

  if (!slot) {
    return (
      <div className="mx-auto w-full max-w-4xl px-6 py-12">
        <Skeleton className="h-72 w-full rounded-card" />
        <Skeleton className="mt-6 h-8 w-72" />
      </div>
    );
  }

  return (
    <main className="flex flex-1 flex-col">
      <Media seed={slot.id} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-4xl px-6 pb-12 pt-8 sm:px-8 sm:pb-16">
          <Link
            href={`/trips/${tripId}/memories`}
            className="rounded-sm text-ui-sm text-fg-on-media-dim transition-colors hover:text-fg-on-media"
          >
            ← All memories
          </Link>
          <h1 className="mt-16 font-display text-display-xl text-fg-on-media sm:mt-24">
            {option?.title ?? slot.title}
          </h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-ui-md text-fg-on-media-dim">
            {option?.place.name && <span>{option.place.name}</span>}
            {option?.estimated_cost_minor != null && (
              <span data-numeric>{formatMoney(option.estimated_cost_minor)}</span>
            )}
          </div>
        </div>
      </Media>

      <div className="mx-auto w-full max-w-4xl space-y-10 px-6 py-12 sm:px-8">
        {(option?.notes || slot.notes) && (
          <section>
            <h2 className="font-display text-display-sm text-fg">Notes</h2>
            <p className="mt-2 whitespace-pre-wrap text-ui-lg text-fg-muted">
              {option?.notes || slot.notes}
            </p>
          </section>
        )}

        <section>
          <div className="mb-3 flex items-baseline justify-between gap-4">
            <h2 className="font-display text-display-sm text-fg">Photos</h2>
            {photos && photos.length > 0 && (
              <span className="text-ui-xs text-fg-subtle" data-numeric>
                {photos.length}
              </span>
            )}
          </div>

          {photos === null && (
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="aspect-square rounded-card" />
              ))}
            </div>
          )}

          {photos !== null && photos.length === 0 && (
            <div className="rounded-card border border-dashed border-line bg-surface-raised px-6 py-12 text-center">
              <p className="text-ui-md font-medium text-fg">No photos yet</p>
              <p className="mx-auto mt-1 max-w-md text-ui-sm text-fg-muted">
                Photos attached to this place in Plan mode appear here. Uploading from the
                browser isn&rsquo;t built yet — it needs its own slice.
              </p>
            </div>
          )}

          {photos !== null && photos.length > 0 && (
            <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3">
              {photos.map(({ attachment, url }) => (
                <li key={attachment.id} className="overflow-hidden rounded-card bg-surface-sunken">
                  <div className="relative aspect-square">
                    <Image
                      src={url}
                      alt={attachment.original_name || "Trip photo"}
                      fill
                      unoptimized
                      sizes="(max-width: 640px) 50vw, 33vw"
                      className="object-cover"
                    />
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        {comments.length > 0 && (
          <section>
            <h2 className="font-display text-display-sm text-fg">What people said</h2>
            <ul className="mt-3 space-y-3">
              {comments.map((c) => (
                <li key={c.id} className="rounded-card border border-line-subtle bg-surface-raised px-4 py-3">
                  <p className="text-ui-xs text-fg-subtle">
                    {c.author_id ? (names[c.author_id] ?? c.author_id.slice(0, 8)) : "Unknown"}
                  </p>
                  <p className="mt-0.5 text-ui-md text-fg">{c.body}</p>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </main>
  );
}
