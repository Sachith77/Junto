"use client";

import { useEffect, useState } from "react";
import { listMembers } from "@/lib/api/members";
import { useAuth } from "@/context/AuthContext";
import type { Member } from "@/lib/types";

export interface TripRoster {
  members: Member[];
  /** user_id -> display name, for rendering authors, voters and splits as people. */
  names: Record<string, string>;
  /** The current user's role on this trip, once known. Drives which controls are offered. */
  myRole: Member["role"] | null;
  loading: boolean;
}

/** The trip's roster. Every collaborative surface needs it, because ops and votes carry user
 *  ids and nothing else — the display name comes from the member read model. */
export function useTripMembers(tripId: string): TripRoster {
  const { user } = useAuth();
  const [members, setMembers] = useState<Member[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    listMembers(tripId)
      .then((m) => !cancelled && setMembers(m))
      .catch(() => !cancelled && setMembers([]));
    return () => {
      cancelled = true;
    };
  }, [tripId]);

  const names: Record<string, string> = {};
  for (const m of members ?? []) {
    names[m.user_id] = m.display_name || m.user_id.slice(0, 8);
  }

  return {
    members: members ?? [],
    names,
    myRole: user ? ((members ?? []).find((m) => m.user_id === user.id)?.role ?? null) : null,
    loading: members === null,
  };
}
