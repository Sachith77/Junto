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

const COVERS: Cover[] = [
  {
    name: "dusk",
    gradient:
      "linear-gradient(152deg, #241536 0%, #4a2440 32%, #8f4327 62%, #d98324 84%, #f4c76b 100%)",
  },
  {
    name: "sea",
    gradient:
      "linear-gradient(152deg, #06202e 0%, #0d3f52 34%, #176b76 64%, #3aa39c 86%, #93d8cd 100%)",
  },
  {
    name: "forest",
    gradient:
      "linear-gradient(152deg, #0a1a13 0%, #17301f 32%, #2f5133 62%, #5c7f45 84%, #a8bd76 100%)",
  },
  {
    name: "desert",
    gradient:
      "linear-gradient(152deg, #2a1112 0%, #5c2318 34%, #99442247 60%, #c2672c 82%, #efb765 100%)",
  },
  {
    name: "alpine",
    gradient:
      "linear-gradient(152deg, #121c2e 0%, #263a57 34%, #4a6485 64%, #7d97b8 86%, #c3d4e4 100%)",
  },
  {
    name: "night",
    gradient:
      "linear-gradient(152deg, #0b0a14 0%, #1d1836 34%, #37275c 64%, #5c3f7d 86%, #9a7bb0 100%)",
  },
];

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
