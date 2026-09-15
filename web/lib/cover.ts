// Generated cover art for trips and destinations.
//
// The outer shell is a photography-forward direction, but `trips` has no cover
// image column — there is nowhere for a photo to come from, and inventing a
// schema change to serve a styling slice would be the tail wagging the dog.
//
// So covers are GENERATED, deterministically, from the trip id. Three properties
// make this read as art direction rather than as a missing image:
//
//   1. Deterministic — a trip keeps the same cover forever, across reloads,
//      across devices, across users. A random gradient would feel broken the
//      second two members compared screens.
//   2. Multi-stop and off-axis — real photographs have a light source. Flat
//      two-stop gradients read as "placeholder"; these have a warm horizon and
//      a dark corner, which is what makes the scrim look intentional over them.
//   3. Grained — a faint turbulence overlay kills the banding that gives CSS
//      gradients away on large surfaces.
//
// When a cover_image_url column eventually exists, this becomes the fallback for
// trips that have not set one, which is exactly what it should be anyway.

export interface Cover {
  /** CSS background value for the media surface. */
  gradient: string;
  image?: string;
  position?: string;
  /** Palette name, useful for debugging and for tests that assert determinism. */
  name: string;
}

// Each cover is TWO layers: a radial light source over a directional wash.
//
// The first version used one linear-gradient per palette, all at 152deg with the same stop
// positions. Six palettes, one composition — which is precisely what made them read as
// generated rather than art-directed: side by side they are the same picture in different
// colours, and no photograph has a light source in the same place as the one next to it.
//
// So the angle, the position of the light and its spread now vary per palette. The radial is
// listed first because CSS paints background layers front-to-back, so it sits ON the wash.
const COVERS: Cover[] = [
  {
    name: "dusk",
    image: "/trip-covers/lisbon.png",
    position: "center 45%",
    // Low sun at the right horizon — the warmest of the set.
    gradient:
      "radial-gradient(105% 75% at 82% 88%, rgb(201 173 130 / 0.20) 0%, transparent 66%), " +
      "linear-gradient(152deg, #171319 0%, #34252a 38%, #61402f 72%, #84633f 100%)",
  },
  {
    name: "sea",
    image: "/trip-covers/coast.png",
    position: "center 48%",
    // High, diffuse light from the upper left — water reads as lit from above.
    gradient:
      "radial-gradient(95% 70% at 22% 12%, rgb(182 202 192 / 0.20) 0%, transparent 72%), " +
      "linear-gradient(168deg, #101719 0%, #1b3033 40%, #345151 74%, #60716a 100%)",
  },
  {
    name: "forest",
    image: "/junto-valley.png",
    position: "center 48%",
    // Light broken through canopy: tight, high, off to one side.
    gradient:
      "radial-gradient(70% 55% at 68% 8%, rgb(205 208 174 / 0.18) 0%, transparent 66%), " +
      "linear-gradient(135deg, #111712 0%, #222e22 40%, #3d4d38 74%, #656a50 100%)",
  },
  {
    name: "desert",
    image: "/trip-covers/coast.png",
    position: "center 55%",
    // Overhead glare, wide and bleaching. Note the fixed alpha typo in the old third stop
    // (#99442247 was an 8-digit hex in a list of 6-digit ones, so it rendered semi-transparent).
    gradient:
      "radial-gradient(120% 85% at 50% 4%, rgb(223 205 174 / 0.18) 0%, transparent 70%), " +
      "linear-gradient(160deg, #1b1512 0%, #3d2a20 42%, #65452f 76%, #8a6d49 100%)",
  },
  {
    name: "alpine",
    image: "/trip-covers/alpine.png",
    position: "center 48%",
    // Cold light raking from the left, snow-bright at the edge.
    gradient:
      "radial-gradient(85% 100% at 6% 42%, rgb(214 220 222 / 0.18) 0%, transparent 70%), " +
      "linear-gradient(122deg, #14181d 0%, #29333b 42%, #48565e 76%, #737d7d 100%)",
  },
  {
    name: "night",
    image: "/junto-valley.png",
    position: "center 38%",
    // Moon: small, high, cool, and the only light in the frame.
    gradient:
      "radial-gradient(52% 42% at 76% 16%, rgb(202 194 210 / 0.17) 0%, transparent 74%), " +
      "linear-gradient(178deg, #111014 0%, #24212b 42%, #40394a 76%, #61586a 100%)",
  },
];

/** A corner falloff, applied to every cover.
 *
 *  Photographs darken at the edges; flat CSS surfaces do not, and the difference is most of
 *  what makes a gradient look like a swatch. Kept very weak — this should be felt at the
 *  corners of a large card, never seen as a ring. */
export const VIGNETTE =
  "radial-gradient(120% 100% at 50% 42%, transparent 52%, rgb(0 0 0 / 0.16) 84%, rgb(0 0 0 / 0.30) 100%)";

/** FNV-1a — small, fast, and stable across runtimes, which is the only property
 *  that matters here. `id.length` alone would cluster every UUID onto one cover. */
function hash(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

export type CoverName = (typeof COVERS)[number]["name"];

/** Hashing is right for USER content — a trip's cover should be stable and
 *  varied without anyone choosing it. It is wrong for fixed marketing surfaces,
 *  where "whatever the hash of this string happens to give" is not art
 *  direction. Those pass an explicit name. */
export function coverFor(idOrName: string): Cover {
  const named = COVERS.find((c) => c.name === idOrName);
  if (named) return named;
  if (idOrName.startsWith("neutral:")) {
    const h = hash(idOrName);
    const hue = 188 + (h % 42);
    const warmHue = 24 + ((h >>> 8) % 24);
    const x = 22 + ((h >>> 16) % 58);
    return {
      name: "neutral",
      gradient:
        `radial-gradient(90% 75% at ${x}% 18%, hsl(${warmHue} 28% 63% / .20) 0%, transparent 68%), ` +
        `linear-gradient(145deg, hsl(${hue} 24% 9%) 0%, hsl(${hue} 22% 17%) 45%, hsl(${hue - 18} 18% 31%) 100%)`,
    };
  }
  return COVERS[hash(idOrName) % COVERS.length];
}

export function coverSeedForTrip(trip: { id: string; name: string; description?: string | null; cover?: { source?: string } }): string {
  if (trip.cover?.source === "neutral") return `neutral:${trip.id}`;
  if (trip.cover?.source && trip.cover.source !== "legacy") return trip.id;
  const words = `${trip.name} ${trip.description ?? ""}`.toLowerCase();
  if (/lisbon|sintra|portugal/.test(words)) return "dusk";
  if (/goa|beach|coast|island|summer/.test(words)) return "sea";
  if (/alpine|mountain|lake|snow|ski/.test(words)) return "alpine";
  return trip.id;
}

/** Faint film grain. Inlined as a data URI so it costs no request and cannot be
 *  blocked; opacity is low enough to remove banding without being visible as texture. */
export const GRAIN_URL =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='140' height='140'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='3'/%3E%3C/filter%3E%3Crect width='140' height='140' filter='url(%23n)' opacity='0.38'/%3E%3C/svg%3E\")";
