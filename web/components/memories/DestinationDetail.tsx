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
import { GOA_MEMORIES_DATA, type MemoryItem } from "./MemoriesData";

interface Photo {
  attachment: Attachment;
  url: string;
}

export function DestinationDetail({ tripId, slotId }: { tripId: string; slotId: string }) {
  const isGoaDemo = slotId === "goa-demo" || GOA_MEMORIES_DATA.some((m) => m.id === slotId);

  if (isGoaDemo) {
    const memory = GOA_MEMORIES_DATA.find((m) => m.id === slotId) || GOA_MEMORIES_DATA[0];
    return <GoaFrameDetail tripId={tripId} memory={memory} />;
  }

  return <LiveDestinationDetail tripId={tripId} slotId={slotId} />;
}

function GoaFrameDetail({ tripId, memory }: { tripId: string; memory: MemoryItem }) {
  const [reactions, setReactions] = useState(memory.reactions);
  const [comments, setComments] = useState(memory.comments);
  const [commentInput, setCommentInput] = useState("");

  const handleReaction = (type: "heart" | "fire" | "sparkle") => {
    setReactions((prev) => ({
      ...prev,
      [type]: prev[type] + 1,
    }));
  };

  const handleAddComment = (e: React.FormEvent) => {
    e.preventDefault();
    if (!commentInput.trim()) return;
    setComments((prev) => [
      ...prev,
      {
        id: `c-${Date.now()}`,
        author: "You",
        avatar: "Y",
        time: "Just now",
        text: commentInput.trim(),
      },
    ]);
    setCommentInput("");
  };

  return (
    <main className="flex flex-1 flex-col bg-surface pb-24">
      {/* Hero with Photo */}
      <Media seed="sea" image={memory.image} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-5xl px-6 pb-12 pt-8 sm:px-8 sm:pb-16">
          <Link
            href={`/trips/${tripId}/memories`}
            className="inline-flex items-center gap-2 rounded-full border border-white/20 bg-black/30 px-3.5 py-1.5 text-ui-sm text-fg-on-media-dim backdrop-blur-md transition-colors hover:bg-black/50 hover:text-fg-on-media"
          >
            ← Back to all memories
          </Link>

          <div className="mt-12 sm:mt-16">
            <div className="flex items-center gap-2">
              <span className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs font-semibold uppercase tracking-wider text-accent-on-dark backdrop-blur-md">
                {memory.day}
              </span>
              <span className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs text-white/80 backdrop-blur-md">
                {memory.time}
              </span>
            </div>

            <h1 className="mt-4 max-w-3xl font-display text-[clamp(2.5rem,8vw,3.5rem)] leading-[1.05] tracking-[-0.025em] text-fg-on-media">
              {memory.title}
            </h1>
            <p className="mt-2 text-ui-lg text-fg-on-media-dim">{memory.subtitle}</p>
          </div>
        </div>
      </Media>

      {/* Detail Layout */}
      <div className="mx-auto w-full max-w-5xl px-6 pt-10 sm:px-8">
        <div className="grid gap-10 lg:grid-cols-3">
          {/* Main Column */}
          <div className="space-y-8 lg:col-span-2">
            {/* Primary Quote Box */}
            <div className="rounded-2xl border border-line-subtle bg-surface-raised p-6 shadow-md">
              <p className="font-display text-display-md italic leading-relaxed text-fg">
                &ldquo;{memory.note}&rdquo;
              </p>
              <div className="mt-4 flex items-center justify-between border-t border-line-subtle pt-3 text-ui-xs text-fg-subtle">
                <span>📍 {memory.location}</span>
                <span>Captured by {memory.author.name}</span>
              </div>
            </div>

            {/* Story Paragraph */}
            <div>
              <h2 className="text-ui-2xs font-semibold uppercase tracking-wider text-accent-text">
                The Story
              </h2>
              <p className="mt-2 text-ui-lg leading-relaxed text-fg-muted">
                {memory.story}
              </p>
            </div>

            {/* Gallery Frames */}
            <div>
              <h2 className="text-ui-2xs font-semibold uppercase tracking-wider text-accent-text mb-4">
                Full Frame
              </h2>
              <div className="relative aspect-[16/10] overflow-hidden rounded-2xl border border-line bg-surface-sunken shadow-xl">
                <Image
                  src={memory.image}
                  alt={memory.alt}
                  fill
                  sizes="(max-width: 1024px) 100vw, 66vw"
                  className="object-cover"
                />
              </div>
            </div>
          </div>

          {/* Sidebar Column: Reactions & Group Echoes */}
          <div className="space-y-6">
            {/* Reactions Box */}
            <div className="rounded-2xl border border-line-subtle bg-surface-raised p-5 shadow-sm">
              <h3 className="text-ui-2xs font-semibold uppercase tracking-wider text-fg-subtle">
                Group Reactions
              </h3>
              <div className="mt-4 flex gap-2">
                <button
                  onClick={() => handleReaction("heart")}
                  className="flex flex-1 items-center justify-center gap-1.5 rounded-xl border border-line bg-surface p-2.5 text-ui-xs text-fg hover:border-rose-500/50 hover:bg-rose-500/10 hover:text-rose-300 transition-colors"
                >
                  <span>❤️</span>
                  <span className="font-semibold">{reactions.heart}</span>
                </button>
                <button
                  onClick={() => handleReaction("fire")}
                  className="flex flex-1 items-center justify-center gap-1.5 rounded-xl border border-line bg-surface p-2.5 text-ui-xs text-fg hover:border-amber-500/50 hover:bg-amber-500/10 hover:text-amber-300 transition-colors"
                >
                  <span>🔥</span>
                  <span className="font-semibold">{reactions.fire}</span>
                </button>
                <button
                  onClick={() => handleReaction("sparkle")}
                  className="flex flex-1 items-center justify-center gap-1.5 rounded-xl border border-line bg-surface p-2.5 text-ui-xs text-fg hover:border-sky-500/50 hover:bg-sky-500/10 hover:text-sky-300 transition-colors"
                >
                  <span>✨</span>
                  <span className="font-semibold">{reactions.sparkle}</span>
                </button>
              </div>
            </div>

            {/* Comments Thread */}
            <div className="rounded-2xl border border-line-subtle bg-surface-raised p-5 shadow-sm">
              <h3 className="text-ui-2xs font-semibold uppercase tracking-wider text-fg-subtle">
                What People Said ({comments.length})
              </h3>

              <div className="mt-4 space-y-3">
                {comments.map((c) => (
                  <div key={c.id} className="rounded-xl border border-line-subtle bg-surface p-3 text-ui-xs">
                    <div className="flex items-center justify-between">
                      <span className="font-medium text-accent-text">{c.author}</span>
                      <span className="text-[10px] text-fg-subtle">{c.time}</span>
                    </div>
                    <p className="mt-1 leading-relaxed text-fg-muted">{c.text}</p>
                  </div>
                ))}
              </div>

              <form onSubmit={handleAddComment} className="mt-4 pt-3 border-t border-line-subtle">
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={commentInput}
                    onChange={(e) => setCommentInput(e.target.value)}
                    placeholder="Add a memory note..."
                    className="flex-1 rounded-lg border border-line bg-surface-sunken px-3 py-1.5 text-ui-xs text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
                  />
                  <button
                    type="submit"
                    disabled={!commentInput.trim()}
                    className="rounded-lg bg-accent px-3 py-1.5 text-ui-xs font-semibold text-surface-sunken transition-opacity hover:bg-accent-hover disabled:opacity-40"
                  >
                    Post
                  </button>
                </div>
              </form>
            </div>
          </div>
        </div>
      </div>
    </main>
  );
}

function LiveDestinationDetail({ tripId, slotId }: { tripId: string; slotId: string }) {
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
    <main className="flex flex-1 flex-col bg-surface pb-24">
      <Media seed={slot.id} scrim="hero" className="shrink-0">
        <div className="relative mx-auto w-full max-w-4xl px-6 pb-12 pt-8 sm:px-8 sm:pb-16">
          <Link
            href={`/trips/${tripId}/memories`}
            className="inline-flex items-center gap-2 rounded-full border border-white/20 bg-black/30 px-3.5 py-1.5 text-ui-sm text-fg-on-media-dim backdrop-blur-md transition-colors hover:bg-black/50 hover:text-fg-on-media"
          >
            ← Back to all memories
          </Link>
          <h1 className="mt-12 max-w-3xl font-display text-[clamp(2.25rem,8vw,2.75rem)] leading-[1.08] tracking-[-0.02em] text-fg-on-media sm:mt-16">
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
          <section className="rounded-2xl border border-line-subtle bg-surface-raised p-6">
            <h2 className="font-display text-display-sm text-fg">Notes</h2>
            <p className="mt-2 whitespace-pre-wrap text-ui-lg text-fg-muted">
              {option?.notes || slot.notes}
            </p>
          </section>
        )}

        <section>
          <div className="mb-4 flex items-baseline justify-between gap-4">
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
                Photos attached to this place in Plan mode appear here.
              </p>
            </div>
          )}

          {photos !== null && photos.length > 0 && (
            <ul className="grid grid-cols-2 gap-4 sm:grid-cols-3">
              {photos.map(({ attachment, url }) => (
                <li key={attachment.id} className="overflow-hidden rounded-2xl border border-line bg-surface-sunken shadow-md">
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
          <section className="rounded-2xl border border-line-subtle bg-surface-raised p-6">
            <h2 className="font-display text-display-sm text-fg">What people said</h2>
            <ul className="mt-4 space-y-3">
              {comments.map((c) => (
                <li key={c.id} className="rounded-xl border border-line-subtle bg-surface px-4 py-3">
                  <p className="text-ui-xs text-fg-subtle font-medium">
                    {c.author_id ? (names[c.author_id] ?? c.author_id.slice(0, 8)) : "Unknown"}
                  </p>
                  <p className="mt-1 text-ui-md text-fg">{c.body}</p>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </main>
  );
}
