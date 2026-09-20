"use client";

import { useState } from "react";
import Image from "next/image";
import type { MemoryItem } from "./MemoriesData";

interface ShareMemoryModalProps {
  isOpen: boolean;
  onClose: () => void;
  tripName: string;
  memories: MemoryItem[];
}

export function ShareMemoryModal({
  isOpen,
  onClose,
  tripName,
  memories,
}: ShareMemoryModalProps) {
  const [copied, setCopied] = useState(false);

  if (!isOpen) return null;

  const handleCopyLink = () => {
    if (typeof window !== "undefined") {
      navigator.clipboard?.writeText(window.location.href);
      setCopied(true);
      setTimeout(() => setCopied(false), 2500);
    }
  };

  const coverPhoto = memories[0]?.image || "/memories/goa/goa-dawn.jpg";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/85 p-4 backdrop-blur-xl">
      <div className="absolute inset-0" onClick={onClose} />

      <div
        className="relative z-10 w-full max-w-md rounded-2xl border border-line bg-surface p-6 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-line-subtle pb-4">
          <div>
            <p className="text-ui-2xs font-semibold uppercase tracking-wider text-accent-text">
              Share Recollection
            </p>
            <h2 className="font-display text-display-md text-fg">Trip Memory Book</h2>
          </div>
          <button
            onClick={onClose}
            className="grid h-8 w-8 place-items-center rounded-full border border-line text-ui-xs text-fg-muted transition-colors hover:border-line-strong hover:text-fg"
          >
            ✕
          </button>
        </div>

        {/* Postcard Preview Card */}
        <div className="mt-5 overflow-hidden rounded-xl border border-white/10 bg-surface-raised p-3.5 shadow-lg">
          <div className="relative aspect-[16/9] w-full overflow-hidden rounded-lg">
            <Image
              src={coverPhoto}
              alt={tripName}
              fill
              sizes="400px"
              className="object-cover"
            />
            <div className="absolute inset-0 bg-gradient-to-t from-black/80 via-transparent to-black/20" />
            <div className="absolute bottom-3 left-3 right-3 text-white">
              <span className="text-[10px] font-semibold uppercase tracking-widest text-accent-on-dark">
                Official Trip Story
              </span>
              <h3 className="font-display text-lg font-normal">{tripName}</h3>
              <p className="text-xs text-white/80">{memories.length} Captured Memories</p>
            </div>
          </div>

          <div className="mt-3 flex items-center justify-between px-1 text-ui-xs text-fg-muted">
            <span>✨ Includes photos, notes & voice echoes</span>
            <span className="text-accent-text">Ready to share</span>
          </div>
        </div>

        {/* Share actions */}
        <div className="mt-6 space-y-3">
          <button
            onClick={handleCopyLink}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-accent px-4 py-2.5 text-ui-sm font-semibold text-surface-sunken transition-colors hover:bg-accent-hover active:scale-[0.99]"
          >
            <span>{copied ? "✓ Link Copied to Clipboard!" : "📋 Copy Private Recap Link"}</span>
          </button>

          <div className="flex gap-2">
            <button
              onClick={() => {
                alert("Recap postcard downloaded! (Demo)");
              }}
              className="flex-1 rounded-lg border border-line bg-surface-raised py-2 text-ui-xs font-medium text-fg transition-colors hover:border-line-strong"
            >
              📥 Save Postcard
            </button>
            <button
              onClick={() => {
                alert("Exported full memory album as PDF! (Demo)");
              }}
              className="flex-1 rounded-lg border border-line bg-surface-raised py-2 text-ui-xs font-medium text-fg transition-colors hover:border-line-strong"
            >
              📖 Export Photo Book
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
