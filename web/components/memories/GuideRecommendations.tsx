"use client";

import { useState } from "react";
import { GOA_RECOMMENDATIONS, type RecommendedPlace } from "./MemoriesData";

export function GuideRecommendations() {
  const [recommendations] = useState<RecommendedPlace[]>(GOA_RECOMMENDATIONS);
  const [copied, setCopied] = useState(false);

  const handleCopyGuide = () => {
    if (typeof window !== "undefined") {
      navigator.clipboard?.writeText(window.location.href);
      setCopied(true);
      setTimeout(() => setCopied(false), 2500);
    }
  };

  return (
    <section className="mx-auto max-w-5xl rounded-3xl border border-line-subtle bg-surface-raised p-6 shadow-xl sm:p-10">
      <div className="flex flex-col sm:flex-row sm:items-end justify-between gap-4 border-b border-line-subtle pb-6">
        <div>
          <span className="font-editorial text-ui-xs uppercase tracking-[0.2em] text-accent-text">
            Curated Recommendations
          </span>
          <h2 className="mt-1 font-display text-display-lg font-normal text-fg tracking-tight">
            The Group’s Tried & Tested Spots
          </h2>
          <p className="mt-1 text-ui-sm text-fg-muted max-w-md">
            Auto-compiled from our itinerary decisions and group verdicts. Shareable with friends heading to Goa.
          </p>
        </div>

        <button
          onClick={handleCopyGuide}
          className="inline-flex items-center gap-2 rounded-xl border border-line bg-surface px-4 py-2.5 text-ui-xs font-semibold text-fg transition-all hover:border-accent hover:text-accent-text shrink-0"
        >
          <span>{copied ? "✓ Copied to Clipboard!" : "📋 Share Recommendation List"}</span>
        </button>
      </div>

      <div className="mt-8 grid gap-4 sm:grid-cols-2">
        {recommendations.map((item) => (
          <div
            key={item.id}
            className="flex flex-col justify-between rounded-2xl border border-line bg-surface p-5 transition-colors hover:border-line-strong"
          >
            <div>
              <div className="flex items-start justify-between gap-2">
                <div>
                  <h3 className="font-display text-ui-lg font-medium text-fg">
                    {item.name}
                  </h3>
                  <p className="text-ui-xs text-fg-subtle">{item.area}</p>
                </div>
                <span className="rounded-full border border-line-subtle bg-surface-raised px-2.5 py-0.5 text-[10px] text-fg-muted">
                  {item.category}
                </span>
              </div>

              {/* Verdict Badges */}
              <div className="mt-3 flex flex-wrap gap-1.5">
                {item.verdicts.map((badge, idx) => (
                  <span
                    key={idx}
                    className="rounded-md border border-line-subtle bg-surface-raised px-2 py-0.5 text-[11px] font-medium text-accent-text"
                  >
                    ✓ {badge}
                  </span>
                ))}
              </div>

              {/* Highlight / Tip */}
              <p className="mt-3 text-ui-xs leading-relaxed text-fg-muted">
                {item.highlight}
              </p>
            </div>

            {item.bestTime && (
              <div className="mt-4 border-t border-line-subtle pt-2.5 text-[11px] text-fg-subtle">
                <span className="font-medium text-fg-muted">Best Time:</span> {item.bestTime}
              </div>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}
