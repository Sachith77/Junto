"use client";

import Link from "next/link";
import { useCallback, useRef } from "react";
import { Media } from "@/components/ui/Media";
import { formatDateRange, tripNights } from "@/lib/format";
import type { Trip } from "@/lib/types";

// The outer shell's primary object, and the most-looked-at surface in the product.
//
// Three things separate this from the label-stack card it replaced, and all three are aimed at
// the same problem: a generated cover has no subject, so the eye had nothing to land on and
// the card read as a coloured rectangle with text parked in the corner.
//
//   1. A FOCAL BLOOM near the centre. The per-palette light direction (dusk from the right
//      horizon, alpine from the left) is ambient and stays — it sets mood but gives the eye no
//      target. The bloom is the target. It blends with `screen`, so it brightens whatever hue
//      is under it rather than painting its own colour on top, which is what keeps six
//      palettes looking like six places instead of six cards with the same white blob.
//   2. PARALLAX on pointer move. The bloom tracks the cursor further than the type does, and
//      in the opposite direction, so cover and caption separate into two planes. This is the
//      difference between a card that moves and a card that feels lit from within — and it is
//      cheap: two CSS variables, written from a rAF-throttled pointer handler.
//   3. EDITORIAL type. Meta moved to a top eyebrow and the title dropped to the bottom at
//      display sizes, so the composition is one large serif against a considered background —
//      a magazine cover rather than a UI card.
//
// The whole card is one link so keyboard users get a single tab stop and the focus ring wraps
// the real target.

export function TripCard({ trip }: { trip: Trip }) {
  const nights = tripNights(trip.start_date, trip.end_date);
  const ref = useRef<HTMLAnchorElement>(null);
  const frame = useRef<number | null>(null);

  // Pointer position as two -0.5..0.5 variables the CSS below multiplies into translations.
  //
  // rAF-throttled because pointermove fires far faster than the screen refreshes, and writing
  // a style property per event is how a hover effect turns into jank on a list of cards.
  const onPointerMove = useCallback((e: React.PointerEvent<HTMLAnchorElement>) => {
    // Coarse pointers have no hover, and reduced-motion users asked not to have this.
    if (e.pointerType !== "mouse") return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const el = ref.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const mx = (e.clientX - rect.left) / rect.width - 0.5;
    const my = (e.clientY - rect.top) / rect.height - 0.5;
    if (frame.current !== null) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = null;
      el.style.setProperty("--mx", mx.toFixed(3));
      el.style.setProperty("--my", my.toFixed(3));
    });
  }, []);

  const reset = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    el.style.setProperty("--mx", "0");
    el.style.setProperty("--my", "0");
  }, []);

  return (
    <Link
      ref={ref}
      href={`/trips/${trip.id}`}
      data-testid="trip-card"
      onPointerMove={onPointerMove}
      onPointerLeave={reset}
      onBlur={reset}
      className="group block rounded-card [--mx:0] [--my:0] focus-visible:outline-2 focus-visible:outline-offset-2"
    >
      <Media
        seed={trip.id}
        className="h-80 rounded-card shadow-lg transition-[transform,box-shadow] duration-[220ms] ease-[cubic-bezier(.22,1,.36,1)] group-hover:-translate-y-1.5 group-hover:shadow-2xl group-focus-visible:-translate-y-1.5 sm:h-[24rem]" 
        underlay={
          <>
            {/* The focal bloom — ONE layer, many stops.
                Two earlier attempts failed in instructive ways. A single wide two-stop radial
                read as fog: it lightened a region without giving the eye anywhere to land. A
                tight second core on top of it read WORSE — a small hard ellipse that
                screen-blending desaturated into a grey smudge, which looks like dust on the
                screen rather than light behind it.
                What a real glow has is no edge anywhere: intensity falls off continuously, and
                fastest near the middle. These stops approximate that curve, which is why it
                reads as luminous at 0.44 where the two-layer version looked dirty at 0.55.
                Sits high-centre so the eye lands on the light and falls to the title. */}
            <div
              aria-hidden
              className="pointer-events-none absolute inset-0 mix-blend-screen transition-[opacity,transform] duration-[420ms] ease-out group-hover:opacity-100"
              style={{
                background:
                  "radial-gradient(circle at 50% 36%, rgb(255 247 232 / 0.44) 0%, rgb(255 244 226 / 0.30) 14%, rgb(255 238 214 / 0.165) 28%, rgb(255 233 205 / 0.08) 44%, rgb(255 230 200 / 0.03) 60%, transparent 76%)",
                opacity: 0.86,
                transform:
                  "translate3d(calc(var(--mx) * 34px), calc(var(--my) * 26px), 0) scale(1.02)",
              }}
            />
          </>
        }
      >
        {/* Eyebrow: meta at the TOP, magazine masthead position, leaving the whole lower
            third to the title. */}
        <div
          className="absolute inset-x-0 top-0 flex flex-wrap items-center gap-x-2.5 gap-y-1 p-6 text-ui-2xs font-medium uppercase tracking-[0.16em] text-accent-on-dark transition-transform duration-[420ms] ease-out sm:p-7"
          style={{ transform: "translate3d(calc(var(--mx) * -6px), calc(var(--my) * -4px), 0)" }}
        >
          <span>{formatDateRange(trip.start_date, trip.end_date)}</span>
          {nights !== null && (
            <>
              <span aria-hidden className="opacity-50">
                ·
              </span>
              <span>
                {nights} {nights === 1 ? "night" : "nights"}
              </span>
            </>
          )}
        </div>

        {/* Title plane. Moves LESS than the bloom and in the opposite direction — that
            difference is the whole parallax; matching speeds would just be a card sliding. */}
        <div
          className="absolute inset-x-0 bottom-0 p-6 transition-transform duration-[420ms] ease-out sm:p-7"
          style={{ transform: "translate3d(calc(var(--mx) * -12px), calc(var(--my) * -9px), 0)" }}
        >
          <h3 className="max-w-[14ch] font-display text-display-xl text-fg-on-media">
            {trip.name}
          </h3>
          {trip.description && (
            <p className="mt-2 line-clamp-1 max-w-md text-ui-md text-fg-on-media-dim">
              {trip.description}
            </p>
          )}
        </div>
      </Media>
    </Link>
  );
}
