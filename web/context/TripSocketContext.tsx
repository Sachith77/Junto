"use client";

import { createContext, useContext, useEffect, useRef, useState } from "react";
import { getTripSocket } from "@/lib/socket";
import type { ConnectionStatus } from "@/lib/socket";
import type { OpFrame, PresenceMember } from "@/lib/types";

interface TripSocketState {
  tripId: string;
  status: ConnectionStatus;
  presence: PresenceMember[];
  /** Registers a listener for every committed op on this trip. Returns an unsubscribe fn.
   *  `self` says whether this client caused the op — see DeliveredOp. */
  onOp: (handler: (op: OpFrame, meta: { self: boolean }) => void) => () => void;
  sendOp: (input: {
    kind: string;
    entityId: string;
    fields: string[];
    values: Record<string, unknown>;
  }) => Promise<OpFrame>;
  resyncSignal: number;
}

const TripSocketCtx = createContext<TripSocketState | null>(null);

/**
 * Owns the subscription lifecycle for one trip: subscribes on mount, unsubscribes on unmount,
 * and fans presence + op frames out to whatever's rendered underneath (PresenceBar,
 * VotingSlot, ...) so they share one connection instead of each racing to open their own.
 */
export function TripSocketProvider({
  tripId,
  children,
}: {
  tripId: string;
  children: React.ReactNode;
}) {
  const [status, setStatus] = useState<ConnectionStatus>(() => getTripSocket().getStatus());
  const [presence, setPresence] = useState<PresenceMember[]>([]);
  const [resyncSignal, setResyncSignal] = useState(0);
  const opHandlers = useRef(new Set<(op: OpFrame, meta: { self: boolean }) => void>());
  const presenceRef = useRef(new Map<string, PresenceMember>());

  useEffect(() => {
    const socket = getTripSocket();
    presenceRef.current = new Map();

    const offStatus = socket.on("status", setStatus);
    const offSnapshot = socket.on("presence-snapshot", ({ tripId: t, members }) => {
      if (t !== tripId) return;
      presenceRef.current = new Map(members.map((m) => [m.conn_id, m]));
      setPresence([...presenceRef.current.values()]);
    });
    const offChange = socket.on("presence-change", (frame) => {
      if (frame.trip_id !== tripId) return;
      if (frame.event === "joined") {
        presenceRef.current.set(frame.conn_id, {
          user_id: frame.user_id,
          conn_id: frame.conn_id,
          joined_at: frame.at,
        });
      } else {
        presenceRef.current.delete(frame.conn_id);
      }
      setPresence([...presenceRef.current.values()]);
    });
    const offOp = socket.on("op", ({ op, self }) => {
      if (op.trip_id !== tripId) return;
      for (const h of opHandlers.current) h(op, { self });
    });
    const offResync = socket.on("resync", ({ tripId: t }) => {
      if (t !== tripId) return;
      setResyncSignal((n) => n + 1);
    });

    void socket.subscribe(tripId);

    return () => {
      offStatus();
      offSnapshot();
      offChange();
      offOp();
      offResync();
      socket.unsubscribe(tripId);
    };
  }, [tripId]);

  const value: TripSocketState = {
    tripId,
    status,
    presence,
    onOp: (handler) => {
      opHandlers.current.add(handler);
      return () => opHandlers.current.delete(handler);
    },
    sendOp: (input) => getTripSocket().sendOp({ tripId, ...input }),
    resyncSignal,
  };

  return <TripSocketCtx.Provider value={value}>{children}</TripSocketCtx.Provider>;
}

export function useTripSocket(): TripSocketState {
  const ctx = useContext(TripSocketCtx);
  if (!ctx) throw new Error("useTripSocket must be used within TripSocketProvider");
  return ctx;
}
