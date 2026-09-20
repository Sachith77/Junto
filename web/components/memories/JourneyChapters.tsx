"use client";

import { useState } from "react";
import Image from "next/image";
import type { MemoryItem } from "./MemoriesData";

interface JourneyChaptersProps {
  memories: MemoryItem[];
  onSelectMemory: (index: number) => void;
}

export function JourneyChapters({ memories, onSelectMemory }: JourneyChaptersProps) {
  const [playingAudioId, setPlayingAudioId] = useState<string | null>(null);

  const toggleAudio = (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (playingAudioId === id) {
      setPlayingAudioId(null);
    } else {
      setPlayingAudioId(id);
      // Auto stop after simulated snippet
      setTimeout(() => {
        setPlayingAudioId((curr) => (curr === id ? null : curr));
      }, 7000);
    }
  };

  return (
    <section className="mx-auto max-w-5xl space-y-16 py-10 sm:space-y-24">
      <div className="text-center">
        <span className="font-editorial text-ui-xs uppercase tracking-[0.2em] text-accent-text">
          Chronological Chronicle
        </span>
        <h2 className="mt-1 font-display text-display-xl font-normal text-fg tracking-tight">
          The Visual Journey
        </h2>
        <p className="mt-2 text-ui-sm text-fg-muted max-w-lg mx-auto">
          From first light on the southern bay to the golden hour along the northern headlands.
        </p>
      </div>

      <div className="space-y-20 sm:space-y-28">
        {memories.map((memory, index) => {
          const isEven = index % 2 === 0;
          const isPlaying = playingAudioId === memory.id;

          return (
            <article
              key={memory.id}
              className="group relative flex flex-col md:flex-row md:items-center gap-8 md:gap-12"
            >
              {/* Photo Frame Column */}
              <div className={`w-full md:w-1/2 ${isEven ? "md:order-1" : "md:order-2"}`}>
                <div
                  onClick={() => onSelectMemory(index)}
                  className="group/photo relative aspect-[4/3] cursor-pointer overflow-hidden rounded-2xl border border-line bg-surface-sunken shadow-xl transition-all duration-500 hover:-translate-y-1 hover:border-accent hover:shadow-2xl"
                >
                  <Image
                    src={memory.image}
                    alt={memory.alt}
                    fill
                    sizes="(max-width: 768px) 100vw, 50vw"
                    className="object-cover transition-transform duration-700 group-hover/photo:scale-105"
                  />

                  {/* Scrim */}
                  <div className="absolute inset-0 bg-gradient-to-t from-black/80 via-transparent to-black/20" />

                  {/* Top Day Badge */}
                  <div className="absolute left-4 top-4 flex items-center gap-2">
                    <span className="rounded-full border border-white/20 bg-black/40 px-3 py-1 text-ui-2xs font-semibold uppercase tracking-wider text-accent-on-dark backdrop-blur-md">
                      {memory.day}
                    </span>
                    <span className="rounded-full border border-white/20 bg-black/40 px-2.5 py-1 text-ui-2xs text-white/80 backdrop-blur-md">
                      {memory.time.split("·")[0].trim()}
                    </span>
                  </div>

                  {/* Bottom Photo Overlay */}
                  <div className="absolute bottom-4 left-4 right-4 flex items-center justify-between text-white">
                    <span className="text-ui-xs font-medium text-white/80">
                      📍 {memory.location}
                    </span>
                    <span className="text-[11px] font-medium text-accent-on-dark underline-offset-4 group-hover/photo:underline">
                      View Frame ↗
                    </span>
                  </div>
                </div>
              </div>

              {/* Editorial Notes & Audio Column */}
              <div
                className={`w-full md:w-1/2 space-y-4 ${
                  isEven ? "md:order-2 md:pl-2" : "md:order-1 md:pr-2"
                }`}
              >
                <div className="flex items-center gap-2 text-ui-xs text-fg-subtle">
                  <span className="font-semibold text-accent-text">{memory.day}</span>
                  <span>·</span>
                  <span>{memory.time}</span>
                  <span>·</span>
                  <span>Captured by {memory.author.name}</span>
                </div>

                <div>
                  <h3
                    onClick={() => onSelectMemory(index)}
                    className="cursor-pointer font-display text-display-lg leading-tight text-fg transition-colors hover:text-accent-text"
                  >
                    {memory.title}
                  </h3>
                  <p className="mt-1 text-ui-sm text-fg-muted">{memory.subtitle}</p>
                </div>

                {/* Primary Memory Quote */}
                <div className="rounded-xl border border-line-subtle bg-surface-raised p-4">
                  <p className="font-display text-ui-md italic leading-relaxed text-fg">
                    &ldquo;{memory.note}&rdquo;
                  </p>
                </div>

                {/* Story Context */}
                <p className="text-ui-sm leading-relaxed text-fg-muted">
                  {memory.story}
                </p>

                {/* Ambient Audio Snippet Player */}
                {memory.audioLabel && (
                  <div className="flex items-center justify-between rounded-xl border border-line bg-surface p-3 transition-colors">
                    <div className="flex items-center gap-3">
                      <button
                        onClick={(e) => toggleAudio(memory.id, e)}
                        className={`flex h-8 w-8 items-center justify-center rounded-full text-xs font-semibold transition-all ${
                          isPlaying
                            ? "bg-accent text-surface-sunken scale-105"
                            : "border border-line bg-surface-raised text-fg hover:border-line-strong hover:text-accent-text"
                        }`}
                        title={isPlaying ? "Pause audio" : "Play ambient sound"}
                      >
                        {isPlaying ? "⏸" : "▶"}
                      </button>
                      <div>
                        <p className="text-ui-xs font-medium text-fg">
                          {memory.audioLabel}
                        </p>
                        <p className="text-[10px] text-fg-subtle">
                          {isPlaying ? "Playing ambient audio clip..." : "Recorded on-site"}
                        </p>
                      </div>
                    </div>

                    <span className="text-[11px] font-mono text-fg-subtle">
                      {memory.audioDuration}
                    </span>
                  </div>
                )}
              </div>
            </article>
          );
        })}
      </div>
    </section>
  );
}
