"use client";

import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";

const AVATAR_COLORS = [
  "#f97316",
  "#ec4899",
  "#8b5cf6",
  "#06b6d4",
  "#22c55e",
  "#eab308",
  "#ef4444",
  "#3b82f6",
];

function colorFor(userId: string): string {
  let hash = 0;
  for (let i = 0; i < userId.length; i++) hash = (hash * 31 + userId.charCodeAt(i)) >>> 0;
  return AVATAR_COLORS[hash % AVATAR_COLORS.length];
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  const first = parts[0]?.[0] ?? "?";
  const second = parts.length > 1 ? parts[parts.length - 1][0] : "";
  return (first + second).toUpperCase();
}

export function PresenceBar({ tripId }: { tripId: string }) {
  const { presence, status } = useTripSocket();
  const names = useTripMembers(tripId);

  const uniqueUsers = new Map<string, string>();
  for (const p of presence) uniqueUsers.set(p.user_id, p.joined_at);

  return (
    <div className="flex items-center gap-3" data-testid="presence-bar">
      <div className="flex -space-x-2">
        {[...uniqueUsers.keys()].map((userId) => {
          const label = names[userId] ?? userId.slice(0, 8);
          return (
            <div
              key={userId}
              title={label}
              data-testid="presence-avatar"
              className="flex h-8 w-8 items-center justify-center rounded-full border-2 border-white text-xs font-semibold text-white shadow-sm"
              style={{ backgroundColor: colorFor(userId) }}
            >
              {initials(label)}
            </div>
          );
        })}
        {uniqueUsers.size === 0 && (
          <span className="text-sm text-neutral-400">No one else here yet</span>
        )}
      </div>
      <span
        className={`h-2 w-2 rounded-full ${status === "open" ? "bg-emerald-500" : "bg-neutral-300"}`}
        title={`connection: ${status}`}
      />
    </div>
  );
}
