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
    // Low sun at the right horizon — the warmest of the set.
    gradient:
      "radial-gradient(120% 85% at 82% 88%, rgb(255 214 138 / 0.55) 0%, rgb(217 131 36 / 0.22) 38%, transparent 68%), " +
      "linear-gradient(152deg, #241536 0%, #4a2440 32%, #8f4327 62%, #d98324 84%, #f4c76b 100%)",
  },
  {
    name: "sea",
    // High, diffuse light from the upper left — water reads as lit from above.
    gradient:
      "radial-gradient(95% 70% at 22% 12%, rgb(190 245 236 / 0.42) 0%, rgb(58 163 156 / 0.16) 44%, transparent 72%), " +
      "linear-gradient(168deg, #06202e 0%, #0d3f52 34%, #176b76 64%, #3aa39c 86%, #93d8cd 100%)",
  },
  {
    name: "forest",
    // Light broken through canopy: tight, high, off to one side.
    gradient:
      "radial-gradient(70% 55% at 68% 8%, rgb(214 233 168 / 0.40) 0%, rgb(92 127 69 / 0.16) 40%, transparent 66%), " +
      "linear-gradient(135deg, #0a1a13 0%, #17301f 32%, #2f5133 62%, #5c7f45 84%, #a8bd76 100%)",
  },
  {
    name: "desert",
    // Overhead glare, wide and bleaching. Note the fixed alpha typo in the old third stop
    // (#99442247 was an 8-digit hex in a list of 6-digit ones, so it rendered semi-transparent).
    gradient:
      "radial-gradient(130% 90% at 50% 4%, rgb(255 233 190 / 0.46) 0%, rgb(194 103 44 / 0.18) 42%, transparent 70%), " +
      "linear-gradient(160deg, #2a1112 0%, #5c2318 34%, #994422 60%, #c2672c 82%, #efb765 100%)",
  },
  {
    name: "alpine",
    // Cold light raking from the left, snow-bright at the edge.
    gradient:
      "radial-gradient(85% 100% at 6% 42%, rgb(226 238 250 / 0.44) 0%, rgb(125 151 184 / 0.16) 40%, transparent 70%), " +
      "linear-gradient(122deg, #121c2e 0%, #263a57 34%, #4a6485 64%, #7d97b8 86%, #c3d4e4 100%)",
  },
  {
    name: "night",
    // Moon: small, high, cool, and the only light in the frame.
    gradient:
      "radial-gradient(52% 42% at 76% 16%, rgb(214 200 240 / 0.38) 0%, rgb(92 63 125 / 0.18) 46%, transparent 74%), " +
      "linear-gradient(178deg, #0b0a14 0%, #1d1836 34%, #37275c 64%, #5c3f7d 86%, #9a7bb0 100%)",
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
  return COVERS[hash(idOrName) % COVERS.length];
}

/** Faint film grain. Inlined as a data URI so it costs no request and cannot be
 *  blocked; opacity is low enough to remove banding without being visible as texture. */
export const GRAIN_URL =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='140' height='140'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='3'/%3E%3C/filter%3E%3Crect width='140' height='140' filter='url(%23n)' opacity='0.38'/%3E%3C/svg%3E\")";
