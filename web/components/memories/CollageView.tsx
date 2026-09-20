"use client";

import Image from "next/image";
import type { MemoryItem } from "./MemoriesData";

interface CollageViewProps {
  memories: MemoryItem[];
  onSelectMemory: (index: number) => void;
  tripName: string;
}

export function CollageView({ memories, onSelectMemory, tripName }: CollageViewProps) {
  const rotations = [
    "-rotate-1 hover:rotate-0",
    "rotate-2 hover:rotate-0",
    "-rotate-1.5 hover:rotate-0",
    "rotate-1 hover:rotate-0",
    "-rotate-2 hover:rotate-0",
    "rotate-1.5 hover:rotate-0",
  ];

  return (
    <section className="relative mx-auto max-w-5xl rounded-3xl border border-line-subtle bg-surface-raised p-6 shadow-xl sm:p-10">
      {/* Subtle header */}
      <div className="mb-8 text-center">
        <span className="font-editorial text-ui-xs uppercase tracking-[0.2em] text-accent-text">
          Scrapbook Archive
        </span>
        <h2 className="mt-1 font-display text-display-lg font-normal text-fg tracking-tight">
          {tripName}, in Six Moments
        </h2>
        <p className="mt-1 text-ui-sm text-fg-muted">
          Click any frame to view in high resolution and read the group notes.
        </p>
      </div>

      {/* Polaroid Cluster Grid */}
      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        {memories.map((memory, index) => {
          const rot = rotations[index % rotations.length];

          return (
            <div
              key={memory.id}
              onClick={() => onSelectMemory(index)}
              className={`group relative cursor-pointer transition-all duration-300 hover:scale-[1.03] hover:z-20 ${rot}`}
            >
              {/* Subtle tape strip */}
              <div
                className={`absolute -top-2.5 left-1/2 -translate-x-1/2 z-20 h-4 w-16 bg-white/10 backdrop-blur-sm border border-white/15 shadow-xs ${
                  index % 2 === 0 ? "rotate-2" : "-rotate-2"
                }`}
              />

              {/* Polaroid Frame */}
              <div className="overflow-hidden rounded-lg border border-line-subtle bg-surface p-3 pb-4 shadow-lg transition-all duration-300 group-hover:border-line group-hover:shadow-2xl">
                {/* Photo frame */}
                <div className="relative aspect-[4/3] w-full overflow-hidden rounded-md bg-surface-sunken">
                  <Image
                    src={memory.image}
                    alt={memory.alt}
                    fill
                    sizes="(max-width: 640px) 100vw, 33vw"
                    className="object-cover transition-transform duration-500 group-hover:scale-105"
                  />
                  <span className="absolute bottom-2 right-2 rounded-xs border border-white/20 bg-black/60 px-2 py-0.5 text-[10px] font-medium text-white backdrop-blur-xs">
                    {memory.day}
                  </span>
                </div>

                {/* Caption area */}
                <div className="mt-3 px-1">
                  <div className="flex items-baseline justify-between gap-2">
                    <p className="font-display text-ui-sm font-medium text-fg group-hover:text-accent-text transition-colors">
                      {memory.title}
                    </p>
                    <span className="text-[10px] text-fg-subtle shrink-0">
                      {memory.time.split("·")[0]}
                    </span>
                  </div>

                  <p className="mt-1 line-clamp-1 text-ui-xs italic text-fg-muted">
                    &ldquo;{memory.note}&rdquo;
                  </p>

                  <div className="mt-2.5 flex items-center justify-between border-t border-line-subtle pt-2 text-[11px] text-fg-subtle">
                    <span>📍 {memory.location.split(",")[0]}</span>
                    <span className="text-fg-muted font-medium">💬 {memory.comments.length}</span>
                  </div>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}
