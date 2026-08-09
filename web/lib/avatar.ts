// One avatar identity, used by the presence bar, comment thread, members list and anywhere
// else a person appears.
//
// Centralised because three components had grown their own copy of this, and two of them had
// already drifted onto different palettes — so the same person was a different colour in the
// presence bar than in their own comments, which quietly undoes the point of colour-coding
// people at all.
//
// The palette is drawn from the cover art rather than from Tailwind's defaults, so a row of
// avatars sits inside the product's colour world instead of looking like a chart legend.
const AVATAR_COLORS = [
  "#7c3f2f", // dusk clay
  "#14596b", // sea
  "#2f5133", // forest
  "#8f4a0b", // accent-700
  "#37275c", // night
  "#4a6485", // alpine
];

export function avatarColor(userId: string): string {
  let hash = 0;
  for (let i = 0; i < userId.length; i++) hash = (hash * 31 + userId.charCodeAt(i)) >>> 0;
  return AVATAR_COLORS[hash % AVATAR_COLORS.length];
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = parts[0]?.[0] ?? "?";
  const last = parts.length > 1 ? parts[parts.length - 1][0] : "";
  return (first + last).toUpperCase();
}
