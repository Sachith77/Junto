"use client";

import { useEffect, useMemo, useState } from "react";
import { suggestTripCover, type CoverSuggestion } from "@/lib/api/trips";
import type { TripCover } from "@/lib/types";
import { Media } from "@/components/ui/Media";

const ACCEPTED = ["image/jpeg", "image/png", "image/webp", "image/avif"];
const MAX_BYTES = 12 * 1024 * 1024;

export function CoverPhotoField({
  destination, file, onFileChange, onSuggestionChange, currentCover,
  autoSuggest = true, children,
}: {
  destination: string;
  file: File | null;
  onFileChange: (file: File | null) => void;
  onSuggestionChange: (suggestion: CoverSuggestion | null) => void;
  currentCover?: TripCover;
  autoSuggest?: boolean;
  children?: React.ReactNode;
}) {
  const [suggestion, setSuggestion] = useState<CoverSuggestion | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const localURL = useMemo(() => (file ? URL.createObjectURL(file) : null), [file]);

  useEffect(() => () => { if (localURL) URL.revokeObjectURL(localURL); }, [localURL]);

  useEffect(() => {
    if (file || !autoSuggest || destination.trim().length < 2) {
      if (!file) { setSuggestion(null); onSuggestionChange(null); }
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setLoading(true);
      suggestTripCover(destination.trim(), controller.signal)
        .then((next) => { setSuggestion(next); onSuggestionChange(next); })
        .catch((err) => { if (err?.name !== "AbortError") { setSuggestion(null); onSuggestionChange(null); } })
        .finally(() => setLoading(false));
    }, 450);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [destination, file, autoSuggest, onSuggestionChange]);

  const choose = (next: File | null) => {
    setError(null);
    if (next && !ACCEPTED.includes(next.type)) { setError("Choose a JPEG, PNG, WebP, or AVIF image."); return; }
    if (next && next.size > MAX_BYTES) { setError("Cover photos must be 12 MB or smaller."); return; }
    onFileChange(next);
    if (next) onSuggestionChange(null);
  };

  const image = localURL ?? suggestion?.url ?? currentCover?.url;
  const shownSuggestion = !file && suggestion?.source === "suggested" ? suggestion : null;

  return (
    <div>
      <Media seed={suggestion?.source === "neutral" ? `neutral:${destination}` : (destination.trim() || "junto-new-trip")} image={image} className="h-72 rounded-card shadow-lg">
        {children}
        {loading && <span className="absolute right-3 top-3 rounded-full bg-black/45 px-2.5 py-1 text-[10px] text-white/80 backdrop-blur-sm">Finding a photo…</span>}
      </Media>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <label className="inline-flex h-9 cursor-pointer items-center rounded-md border border-line bg-surface-raised px-3 text-ui-sm font-medium text-fg transition-colors hover:border-line-strong hover:bg-surface-sunken">
          <input type="file" accept={ACCEPTED.join(",")} className="sr-only" onChange={(event) => choose(event.target.files?.[0] ?? null)} />
          {file ? "Choose another photo" : "Upload cover photo"}
        </label>
        {file && <button type="button" onClick={() => choose(null)} className="h-9 rounded-md px-3 text-ui-sm text-fg-muted hover:bg-surface-sunken hover:text-fg">Remove</button>}
        <span className="text-ui-xs text-fg-subtle">JPEG, PNG, WebP or AVIF · max 12 MB</span>
      </div>
      {error && <p role="alert" className="mt-2 text-ui-xs text-critical-700">{error}</p>}
      {shownSuggestion?.photographer_name ? (
        <p className="mt-2 text-ui-xs text-fg-subtle">
          Suggested photo by <a href={shownSuggestion.photographer_url} target="_blank" rel="noreferrer" className="underline underline-offset-2">{shownSuggestion.photographer_name}</a>{" "}
          on <a href={shownSuggestion.photo_url} target="_blank" rel="noreferrer" className="underline underline-offset-2">Unsplash</a>.
        </p>
      ) : (
        <p className="mt-2 text-ui-xs text-fg-subtle">{file ? "Your photo will be used everywhere this trip appears." : "We’ll suggest a destination photo as you type. You can always replace it."}</p>
      )}
    </div>
  );
}
