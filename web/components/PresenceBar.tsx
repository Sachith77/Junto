"use client";

import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { avatarColor, initials } from "@/lib/avatar";

/** Who is connected to this trip right now.
 *
 *  The accent ring is the "live" language from the token system — it means "here now", which
 *  is a different fact from "is a member" (that one is a row on the Members screen). */
export function PresenceBar({ tripId }: { tripId: string }) {
  const { presence, status } = useTripSocket();
  const { names } = useTripMembers(tripId);

  // Collapsed by user, not by connection: two tabs is one person, and rendering them twice
  // would make presence look like the trip is busier than it is.
  const users = [...new Set(presence.map((p) => p.user_id))];
  const shown = users.slice(0, 5);
  const overflow = users.length - shown.length;

  return (
    <div className="flex items-center gap-2.5" data-testid="presence-bar">
      <div className="flex -space-x-1.5">
        {shown.map((userId) => {
          const label = names[userId] ?? userId.slice(0, 8);
          return (
            <span
              key={userId}
              title={`${label} — here now`}
              data-testid="presence-avatar"
              className="flex h-7 w-7 items-center justify-center rounded-full text-ui-2xs font-semibold text-fg-inverse ring-2 ring-live"
              style={{ backgroundColor: avatarColor(userId) }}
            >
              {initials(label)}
            </span>
          );
        })}
        {overflow > 0 && (
          <span
            className="flex h-7 w-7 items-center justify-center rounded-full bg-surface-sunken text-ui-2xs font-semibold text-fg-muted ring-2 ring-surface"
            title={`${overflow} more here`}
            data-numeric
          >
            +{overflow}
          </span>
        )}
        {users.length === 0 && (
          <span className="text-ui-xs text-fg-subtle">Only you here</span>
        )}
      </div>
      <span
        aria-hidden
        title={`connection: ${status}`}
        className={`h-1.5 w-1.5 rounded-full ${status === "open" ? "bg-live" : "bg-line-strong"}`}
      />
      <span className="sr-only" role="status">
        {status === "open" ? "Connected" : `Connection ${status}`}
      </span>
    </div>
  );
}
