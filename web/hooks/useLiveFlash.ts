"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useTripSocket } from "@/context/TripSocketContext";
import type { OpFrame } from "@/lib/types";

const FLASH_MS = 1200; // matches --animate-live-flash in tokens.css

/**
 * Turns "another member just changed this" into a visible acknowledgement.
 *
 * This project's whole claim is a real-time sync engine, and state that mutates silently
 * gives a user no evidence any of that exists — so every WS-delivered change gets a brief
 * accent tint (see the live-flash keyframe in tokens.css).
 *
 * Two rules make it informative rather than noisy:
 *   - Only REMOTE ops flash. Highlighting your own edit teaches nothing and would make every
 *     interaction blink; `self` comes from the socket, which knows which client op ids it
 *     minted.
 *   - The flash is keyed per entity, so three members editing three different things produce
 *     three quiet highlights rather than one repainting screen.
 *
 * `resolve` maps an op to the id of the thing on screen that should react — often the op's
 * own entity, but a vote should flash the OPTION it was cast for, not the invisible vote row.
 * Returning null ignores the op.
 */
export function useLiveFlash(resolve: (op: OpFrame) => string | null | undefined) {
  const { onOp } = useTripSocket();
  const [flashing, setFlashing] = useState<Record<string, number>>({});
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  // Held in a ref so changing the mapping does not tear down the subscription — callers
  // routinely pass an inline arrow, which would otherwise resubscribe on every render.
  const resolveRef = useRef(resolve);
  useEffect(() => {
    resolveRef.current = resolve;
  });

  useEffect(() => {
    return onOp((op, { self }) => {
      if (self) return;
      const id = resolveRef.current(op);
      if (!id) return;

      // The value is a nonce, not a boolean: re-flashing an already-flashing element has to
      // restart the CSS animation, and React will not re-run it if the key's value is equal.
      setFlashing((prev) => ({ ...prev, [id]: (prev[id] ?? 0) + 1 }));

      const existing = timers.current.get(id);
      if (existing) clearTimeout(existing);
      timers.current.set(
        id,
        setTimeout(() => {
          timers.current.delete(id);
          setFlashing((prev) => {
            const next = { ...prev };
            delete next[id];
            return next;
          });
        }, FLASH_MS)
      );
    });
  }, [onOp]);

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const t of pending.values()) clearTimeout(t);
      pending.clear();
    };
  }, []);

  /** Spread onto the element that should react. The changing `key` restarts the animation. */
  const flashProps = useCallback(
    (id: string) => {
      const nonce = flashing[id];
      if (!nonce) return {};
      return { key: `${id}-flash-${nonce}`, "data-live": true as const };
    },
    [flashing]
  );

  const isFlashing = useCallback((id: string) => Boolean(flashing[id]), [flashing]);

  return { flashProps, isFlashing };
}
