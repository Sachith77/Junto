import type { CSSProperties, ReactNode } from "react";
import { GRAIN_URL, VIGNETTE, coverFor } from "@/lib/cover";

// The scrim rule, made structural.
//
// tokens.css states that text over media always sits on a scrim. A rule written
// in a comment is a rule someone forgets at 2am, so this component is the only
// sanctioned way to put content over a cover: the scrim is not a prop that can
// be turned off, and `data-on-media` (which switches the focus ring to the
// on-dark accent step) is applied automatically.

export function Media({
  seed,
  children,
  className = "",
  scrim = "media",
  style,
  underlay,
}: {
  /** Stable id — the cover is derived from it, so it never changes for a trip. */
  seed: string;
  children?: ReactNode;
  className?: string;
  /** `media` = bottom-anchored caption gradient. `hero` = top-and-bottom. */
  scrim?: "media" | "hero" | "flat";
  style?: CSSProperties;
  /** Rendered between the cover wash and the grain — i.e. INSIDE the treated surface rather
   *  than on top of it. This is where a focal bloom belongs: passed as a child it would sit
   *  above the scrim and read as a light shining ON the card instead of coming FROM it. */
  underlay?: ReactNode;
}) {
  const cover = coverFor(seed);
  const scrimValue =
    scrim === "hero"
      ? "var(--scrim-hero)"
      : scrim === "flat"
        ? "var(--scrim-flat)"
        : "var(--scrim-media)";

  return (
    <div
      data-on-media
      data-cover={cover.name}
      className={`relative isolate overflow-hidden ${className}`}
      style={{ background: cover.gradient, ...style }}
    >
      {underlay}
      {/* Grain sits under the scrim so the scrim's own smoothness is not textured.
          Raised from 0.16 to 0.22: at the lower value it removed banding but contributed no
          perceptible texture, which is half of what makes a real photograph read as a surface
          rather than a fill. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-[0.22] mix-blend-overlay"
        style={{ backgroundImage: GRAIN_URL }}
      />
      {/* Corner falloff, under the scrim for the same reason as the grain. */}
      <div aria-hidden className="pointer-events-none absolute inset-0" style={{ background: VIGNETTE }} />
      <div aria-hidden className="pointer-events-none absolute inset-0" style={{ background: scrimValue }} />
      {children}
    </div>
  );
}
