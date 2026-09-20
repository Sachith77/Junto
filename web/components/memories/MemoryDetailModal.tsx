"use client";

import { useEffect, useState } from "react";
import Image from "next/image";
import type { MemoryItem } from "./MemoriesData";

interface MemoryDetailModalProps {
  memory: MemoryItem | null;
  onClose: () => void;
  onPrev?: () => void;
  onNext?: () => void;
  hasPrev?: boolean;
  hasNext?: boolean;
  onReaction?: (id: string, reactionType: "heart" | "fire" | "sparkle") => void;
  onAddComment?: (id: string, text: string) => void;
}

export function MemoryDetailModal({
  memory,
  onClose,
  onPrev,
  onNext,
  hasPrev,
  hasNext,
  onReaction,
  onAddComment,
}: MemoryDetailModalProps) {
  const [commentInput, setCommentInput] = useState("");
  const [activeReaction, setActiveReaction] = useState<string | null>(null);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft" && hasPrev && onPrev) onPrev();
      if (e.key === "ArrowRight" && hasNext && onNext) onNext();
    };

    if (memory) {
      window.addEventListener("keydown", handleKeyDown);
    }
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [memory, hasPrev, hasNext, onPrev, onNext, onClose]);

  if (!memory) return null;

  const handlePostComment = (e: React.FormEvent) => {
    e.preventDefault();
    if (!commentInput.trim()) return;
    onAddComment?.(memory.id, commentInput.trim());
    setCommentInput("");
  };

  const handleReactionClick = (type: "heart" | "fire" | "sparkle") => {
    setActiveReaction(type);
    onReaction?.(memory.id, type);
    setTimeout(() => setActiveReaction(null), 800);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/85 p-3 backdrop-blur-xl sm:p-6 lg:p-8">
      {/* Backdrop click to close */}
      <div className="absolute inset-0" onClick={onClose} />

      {/* Main Modal Card */}
      <div
        className="relative z-10 flex max-h-[92vh] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-line bg-surface-raised shadow-2xl lg:flex-row"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Left Side: Photo Frame */}
        <div className="relative flex min-h-[300px] flex-1 items-center justify-center bg-black/60 sm:min-h-[420px] lg:min-h-[600px]">
          <Image
            src={memory.image}
            alt={memory.alt}
            fill
            sizes="(max-width: 1024px) 100vw, 60vw"
            className="object-cover"
            priority
          />

          <div className="absolute inset-0 bg-gradient-to-t from-black/70 via-transparent to-black/30 pointer-events-none" />

          {/* Top badges */}
          <div className="absolute left-4 top-4 flex items-center gap-2">
            <span className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs font-medium uppercase tracking-wider text-accent-on-dark backdrop-blur-md">
              {memory.day}
            </span>
            <span className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs text-white/80 backdrop-blur-md">
              {memory.time}
            </span>
          </div>

          {/* Navigation Arrows */}
          {hasPrev && onPrev && (
            <button
              onClick={onPrev}
              className="absolute left-4 top-1/2 -translate-y-1/2 rounded-full border border-white/20 bg-black/50 p-2.5 text-white/90 backdrop-blur-md transition-all hover:scale-110 hover:bg-black/80 hover:text-white"
              title="Previous Memory (Left Arrow)"
            >
              ←
            </button>
          )}

          {hasNext && onNext && (
            <button
              onClick={onNext}
              className="absolute right-4 top-1/2 -translate-y-1/2 rounded-full border border-white/20 bg-black/50 p-2.5 text-white/90 backdrop-blur-md transition-all hover:scale-110 hover:bg-black/80 hover:text-white"
              title="Next Memory (Right Arrow)"
            >
              →
            </button>
          )}

          {/* Bottom Photo Pill */}
          <div className="absolute bottom-4 left-4 right-4 flex items-center justify-between text-white">
            <span className="text-ui-xs font-light text-white/80">
              📍 {memory.location}
            </span>
            {memory.vibe && (
              <span className="text-[11px] text-accent-on-dark/90">
                {memory.vibe}
              </span>
            )}
          </div>
        </div>

        {/* Right Side: Editorial Journal, Notes, Reactions & Comments */}
        <div className="flex w-full flex-col justify-between overflow-y-auto bg-surface p-6 lg:w-[420px] lg:p-7">
          <div>
            {/* Header & Close */}
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-center gap-2.5">
                <div className="flex h-8 w-8 items-center justify-center rounded-full bg-accent-tint text-ui-xs font-semibold text-accent-text ring-1 ring-accent/30">
                  {memory.author.avatar}
                </div>
                <div>
                  <p className="text-ui-xs font-medium text-fg">
                    {memory.author.name}
                  </p>
                  <p className="text-ui-2xs text-fg-subtle">
                    {memory.author.role || "Trip member"}
                  </p>
                </div>
              </div>

              <button
                onClick={onClose}
                className="grid h-8 w-8 place-items-center rounded-full border border-line text-ui-xs text-fg-muted transition-colors hover:border-line-strong hover:text-fg"
                title="Close"
              >
                ✕
              </button>
            </div>

            {/* Title & Core Note */}
            <div className="mt-5">
              <h2 className="font-display text-display-md leading-tight text-fg">
                {memory.title}
              </h2>
              <p className="mt-1 text-ui-xs font-medium text-accent-text">
                {memory.subtitle}
              </p>

              <blockquote className="mt-4 rounded-lg border-l-2 border-accent bg-surface-raised p-3.5 text-ui-sm italic leading-relaxed text-fg">
                &ldquo;{memory.note}&rdquo;
              </blockquote>

              {memory.story && (
                <div className="mt-4">
                  <h3 className="text-ui-2xs font-semibold uppercase tracking-wider text-fg-subtle">
                    The Story
                  </h3>
                  <p className="mt-1.5 text-ui-sm leading-relaxed text-fg-muted">
                    {memory.story}
                  </p>
                </div>
              )}
            </div>

            {/* Reaction Bar */}
            <div className="mt-6 flex items-center gap-2 border-y border-line-subtle py-3">
              <button
                onClick={() => handleReactionClick("heart")}
                className={`flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-ui-xs transition-all ${
                  activeReaction === "heart"
                    ? "scale-110 border-rose-500/50 bg-rose-500/10 text-rose-300"
                    : "border-line bg-surface-raised text-fg-muted hover:border-line-strong hover:text-fg"
                }`}
              >
                <span>❤️</span>
                <span className="font-medium" data-numeric>
                  {memory.reactions.heart}
                </span>
              </button>

              <button
                onClick={() => handleReactionClick("fire")}
                className={`flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-ui-xs transition-all ${
                  activeReaction === "fire"
                    ? "scale-110 border-amber-500/50 bg-amber-500/10 text-amber-300"
                    : "border-line bg-surface-raised text-fg-muted hover:border-line-strong hover:text-fg"
                }`}
              >
                <span>🔥</span>
                <span className="font-medium" data-numeric>
                  {memory.reactions.fire}
                </span>
              </button>

              <button
                onClick={() => handleReactionClick("sparkle")}
                className={`flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-ui-xs transition-all ${
                  activeReaction === "sparkle"
                    ? "scale-110 border-sky-500/50 bg-sky-500/10 text-sky-300"
                    : "border-line bg-surface-raised text-fg-muted hover:border-line-strong hover:text-fg"
                }`}
              >
                <span>✨</span>
                <span className="font-medium" data-numeric>
                  {memory.reactions.sparkle}
                </span>
              </button>
            </div>

            {/* Comments Thread */}
            <div className="mt-5">
              <div className="flex items-center justify-between">
                <h3 className="text-ui-2xs font-semibold uppercase tracking-wider text-fg-subtle">
                  Group Notes & Reactions ({memory.comments.length})
                </h3>
              </div>

              <div className="mt-3 max-h-48 space-y-2.5 overflow-y-auto pr-1">
                {memory.comments.map((c) => (
                  <div
                    key={c.id}
                    className="rounded-lg border border-line-subtle bg-surface-raised p-2.5 text-ui-xs"
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-medium text-accent-text">{c.author}</span>
                      <span className="text-[10px] text-fg-subtle">{c.time}</span>
                    </div>
                    <p className="mt-1 text-fg-muted leading-relaxed">{c.text}</p>
                  </div>
                ))}
              </div>
            </div>
          </div>

          {/* Add Comment Input Form */}
          <form onSubmit={handlePostComment} className="mt-6 pt-4 border-t border-line-subtle">
            <div className="flex gap-2">
              <input
                type="text"
                value={commentInput}
                onChange={(e) => setCommentInput(e.target.value)}
                placeholder="Add a memory note or quote..."
                className="flex-1 rounded-md border border-line bg-surface-sunken px-3 py-2 text-ui-sm text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
              />
              <button
                type="submit"
                disabled={!commentInput.trim()}
                className="rounded-md bg-accent px-3.5 py-2 text-ui-xs font-semibold text-surface-sunken transition-opacity hover:bg-accent-hover disabled:opacity-40"
              >
                Post
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
