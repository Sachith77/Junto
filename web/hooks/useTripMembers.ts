import { useEffect, useState } from "react";
import { listMembers } from "@/lib/api/members";

/** user_id -> display_name, for turning presence/vote/option payloads (which only carry ids)
 * into something a person can read. */
export function useTripMembers(tripId: string): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});

  useEffect(() => {
    let cancelled = false;
    listMembers(tripId)
      .then((members) => {
        if (cancelled) return;
        const map: Record<string, string> = {};
        for (const m of members) map[m.user_id] = m.display_name ?? m.user_id.slice(0, 8);
        setNames(map);
      })
      .catch(() => {
        /* Callers fall back to truncated ids when this fails. */
      });
    return () => {
      cancelled = true;
    };
  }, [tripId]);

  return names;
}
