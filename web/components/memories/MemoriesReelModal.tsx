"use client";

import { useEffect, useState, useCallback, useRef } from "react";
import Image from "next/image";
import type { MemoryItem } from "./MemoriesData";

interface MemoriesReelModalProps {
  isOpen: boolean;
  onClose: () => void;
  memories: MemoryItem[];
  initialIndex?: number;
  onReaction?: (id: string, reactionType: "heart" | "fire" | "sparkle") => void;
}

export function MemoriesReelModal({
  isOpen,
  onClose,
  memories,
  initialIndex = 0,
  onReaction,
}: MemoriesReelModalProps) {
  const [currentIndex, setCurrentIndex] = useState(initialIndex);
  const [isPaused, setIsPaused] = useState(false);
  const [progress, setProgress] = useState(0);
  const [reactionBurst, setReactionBurst] = useState<string | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const duration = 6000; // 6 seconds per memory

  useEffect(() => {
    if (isOpen) {
      setCurrentIndex(initialIndex);
      setProgress(0);
      setIsPaused(false);
    }
  }, [isOpen, initialIndex]);

  const handleNext = useCallback(() => {
    setProgress(0);
    if (currentIndex < memories.length - 1) {
      setCurrentIndex((prev) => prev + 1);
    } else {
      onClose();
    }
  }, [currentIndex, memories.length, onClose]);

  const handlePrev = useCallback(() => {
    setProgress(0);
    if (currentIndex > 0) {
      setCurrentIndex((prev) => prev - 1);
    }
  }, [currentIndex]);

  // Handle keyboard navigation
  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight" || e.key === " ") {
        e.preventDefault();
        handleNext();
      }
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        handlePrev();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, handleNext, handlePrev, onClose]);

  // Story progress timer
  useEffect(() => {
    if (!isOpen || isPaused) return;

    const interval = 50; // update every 50ms
    timerRef.current = setInterval(() => {
      setProgress((prev) => {
        const next = prev + (interval / duration) * 100;
        if (next >= 100) {
          handleNext();
          return 0;
        }
        return next;
      });
    }, interval);

    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
    };
  }, [isOpen, isPaused, handleNext, duration]);

  if (!isOpen || memories.length === 0) return null;

  const current = memories[currentIndex];

  const triggerReaction = (type: "heart" | "fire" | "sparkle") => {
    setReactionBurst(type);
    onReaction?.(current.id, type);
    setTimeout(() => setReactionBurst(null), 1000);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/95 backdrop-blur-2xl">
      {/* Background ambient glow */}
      <div
        className="pointer-events-none absolute inset-0 opacity-15 blur-3xl transition-all duration-1000"
        style={{
          backgroundImage: `radial-gradient(circle at center, rgb(183 206 224 / 0.3) 0%, transparent 70%)`,
        }}
      />

      {/* Main Story Container (Phone/Card aspect ratio) */}
      <div
        className="relative mx-auto flex h-full max-h-[94vh] w-full max-w-lg flex-col overflow-hidden rounded-2xl bg-surface-sunken shadow-2xl ring-1 ring-white/10"
        onMouseEnter={() => setIsPaused(true)}
        onMouseLeave={() => setIsPaused(false)}
      >
        {/* Progress Bars Header */}
        <div className="absolute inset-x-0 top-0 z-30 flex gap-1.5 p-4 pt-3.5">
          {memories.map((_, idx) => (
            <div
              key={idx}
              className="h-1 flex-1 overflow-hidden rounded-full bg-white/20 backdrop-blur-sm"
            >
              <div
                className="h-full bg-white transition-all duration-75"
                style={{
                  width:
                    idx < currentIndex
                      ? "100%"
                      : idx === currentIndex
                        ? `${progress}%`
                        : "0%",
                }}
              />
            </div>
          ))}
        </div>

        {/* Top Info Bar */}
        <div className="absolute inset-x-0 top-6 z-30 flex items-center justify-between px-5 pt-2 text-white">
          <div className="flex items-center gap-3">
            <div className="flex h-8 w-8 items-center justify-center rounded-full border border-white/20 bg-black/40 text-xs font-semibold backdrop-blur-md">
              {current.author.avatar}
            </div>
            <div>
              <div className="flex items-center gap-2">
                <span className="text-ui-xs font-medium text-white">{current.author.name}</span>
                <span className="text-[10px] uppercase tracking-wider text-accent-on-dark/90">
                  {current.day}
                </span>
              </div>
              <p className="text-[11px] text-white/70">{current.time}</p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={() => setIsPaused(!isPaused)}
              className="flex h-8 w-8 items-center justify-center rounded-full bg-black/40 text-xs text-white/80 backdrop-blur-md transition-colors hover:bg-black/60 hover:text-white"
              title={isPaused ? "Play" : "Pause"}
            >
              {isPaused ? "▶" : "⏸"}
            </button>
            <button
              onClick={onClose}
              className="flex h-8 w-8 items-center justify-center rounded-full bg-black/40 text-sm font-light text-white/80 backdrop-blur-md transition-colors hover:bg-black/60 hover:text-white"
              title="Close (ESC)"
            >
              ✕
            </button>
          </div>
        </div>

        {/* Story Photo */}
        <div className="relative flex-1">
          <Image
            src={current.image}
            alt={current.alt}
            fill
            priority
            sizes="512px"
            className="object-cover transition-opacity duration-300"
          />

          {/* Scrim Overlay */}
          <div className="absolute inset-0 bg-gradient-to-b from-black/60 via-transparent to-black/90" />

          {/* Left / Right click touch navigation zones */}
          <div className="absolute inset-y-16 inset-x-0 z-20 flex">
            <div
              className="h-full w-1/3 cursor-pointer"
              onClick={handlePrev}
              title="Previous frame"
            />
            <div
              className="h-full w-2/3 cursor-pointer"
              onClick={handleNext}
              title="Next frame"
            />
          </div>

          {/* Burst Reaction Animation */}
          {reactionBurst && (
            <div className="pointer-events-none absolute inset-0 z-40 flex items-center justify-center animate-bounce">
              <span className="text-6xl drop-shadow-2xl">
                {reactionBurst === "heart" ? "❤️" : reactionBurst === "fire" ? "🔥" : "✨"}
              </span>
            </div>
          )}
        </div>

        {/* Bottom Story Content & Note Card */}
        <div className="relative z-30 space-y-3 bg-gradient-to-t from-black via-black/95 to-transparent p-6 pt-2 text-white">
          <div className="flex items-center justify-between gap-2">
            <span className="inline-flex items-center gap-1.5 rounded-full border border-white/15 bg-white/10 px-2.5 py-1 text-[11px] font-medium tracking-wide text-accent-on-dark backdrop-blur-md">
              📍 {current.location}
            </span>
          </div>

          <div>
            <h2 className="font-display text-display-md font-normal leading-tight text-white">
              {current.title}
            </h2>
            <p className="mt-2 text-ui-sm italic leading-relaxed text-fg-on-media-dim">
              &ldquo;{current.note}&rdquo;
            </p>
          </div>

          {/* Quick Reaction Bar */}
          <div className="flex items-center justify-between border-t border-white/10 pt-3">
            <div className="flex items-center gap-2">
              <button
                onClick={() => triggerReaction("heart")}
                className="flex items-center gap-1.5 rounded-full border border-white/15 bg-white/10 px-3 py-1.5 text-xs text-white backdrop-blur-md transition-transform hover:scale-105 active:scale-95"
              >
                <span>❤️</span>
                <span className="font-medium">{current.reactions.heart}</span>
              </button>
              <button
                onClick={() => triggerReaction("fire")}
                className="flex items-center gap-1.5 rounded-full border border-white/15 bg-white/10 px-3 py-1.5 text-xs text-white backdrop-blur-md transition-transform hover:scale-105 active:scale-95"
              >
                <span>🔥</span>
                <span className="font-medium">{current.reactions.fire}</span>
              </button>
              <button
                onClick={() => triggerReaction("sparkle")}
                className="flex items-center gap-1.5 rounded-full border border-white/15 bg-white/10 px-3 py-1.5 text-xs text-white backdrop-blur-md transition-transform hover:scale-105 active:scale-95"
              >
                <span>✨</span>
                <span className="font-medium">{current.reactions.sparkle}</span>
              </button>
            </div>

            <span className="text-[11px] text-white/50">
              {currentIndex + 1} of {memories.length}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
