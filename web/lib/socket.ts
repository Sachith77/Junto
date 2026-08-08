import { WS_URL } from "./config";
import { mintWsTicket } from "./api/wsTicket";
import type {
  ErrorFrame,
  OpFrame,
  PresenceFrame,
  PresenceMember,
  ResyncFrame,
  ServerFrame,
  SubscribedFrame,
} from "./types";

export type ConnectionStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";

interface Events {
  status: ConnectionStatus;
  op: OpFrame;
  "presence-snapshot": { tripId: string; members: PresenceMember[] };
  "presence-change": PresenceFrame;
  resync: { tripId: string; reason?: string };
  "session-revoked": void;
}

type Handler<K extends keyof Events> = (payload: Events[K]) => void;

interface PendingOp {
  resolve: (frame: OpFrame) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

const ACK_TIMEOUT_MS = 8000;
const MAX_BACKOFF_MS = 8000;

/**
 * One WebSocket per browser session, shared across every trip the user has open (a ticket is
 * user-scoped, not trip-scoped — see internal/transport/ws/ticket.go). Framework-agnostic on
 * purpose so it can be driven from a plain vitest test without React or jsdom's WebSocket
 * quirks getting in the way.
 */
export class TripSocket {
  private ws: WebSocket | null = null;
  private status: ConnectionStatus = "idle";
  private subscriptions = new Map<string, number | null>(); // tripId -> last known seq
  private pending = new Map<string, PendingOp>();
  private listeners: { [K in keyof Events]: Set<Handler<K>> } = {
    status: new Set(),
    op: new Set(),
    "presence-snapshot": new Set(),
    "presence-change": new Set(),
    resync: new Set(),
    "session-revoked": new Set(),
  };
  private reconnectAttempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private closedByCaller = false;

  on<K extends keyof Events>(event: K, handler: Handler<K>): () => void {
    this.listeners[event].add(handler as never);
    return () => this.listeners[event].delete(handler as never);
  }

  private emit<K extends keyof Events>(event: K, payload: Events[K]): void {
    for (const h of this.listeners[event]) (h as Handler<K>)(payload);
  }

  private setStatus(status: ConnectionStatus): void {
    this.status = status;
    this.emit("status", status);
  }

  getStatus(): ConnectionStatus {
    return this.status;
  }

  /** Ensures a connection exists, then subscribes to a trip. Idempotent. */
  async subscribe(tripId: string): Promise<void> {
    if (!this.subscriptions.has(tripId)) {
      this.subscriptions.set(tripId, null);
    }
    if (this.status === "idle" || this.status === "closed") {
      // connect()'s onopen handler sends a subscribe frame for every tracked trip, including
      // this one just added — sending it again below would double-subscribe the same
      // connection, which the server does not treat as a no-op (observed: it re-broadcasts a
      // spurious "left" for the connection's OWN presence entry, corrupting the room's
      // presence set for everyone already in it). Do not send from both places.
      await this.connect();
      return;
    }
    if (this.status === "open") {
      this.sendSubscribe(tripId);
    }
    // Otherwise a connect() from an earlier call is still in flight; its onopen handler will
    // send this subscription too, since it's already in `this.subscriptions` by now.
  }

  unsubscribe(tripId: string): void {
    this.subscriptions.delete(tripId);
    this.sendRaw({ type: "unsubscribe", trip_id: tripId });
  }

  close(): void {
    this.closedByCaller = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.setStatus("closed");
  }

  private sendSubscribe(tripId: string): void {
    if (this.status !== "open") return;
    const sinceSeq = this.subscriptions.get(tripId) ?? null;
    this.sendRaw({ type: "subscribe", trip_id: tripId, since_seq: sinceSeq });
  }

  private sendRaw(frame: Record<string, unknown>): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(frame));
    }
  }

  /** Submits one op and resolves with its broadcast (the ack — see protocol.go). */
  sendOp(input: {
    tripId: string;
    kind: string;
    entityId: string;
    fields: string[];
    values: Record<string, unknown>;
  }): Promise<OpFrame> {
    const clientOpId = crypto.randomUUID();
    return new Promise<OpFrame>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(clientOpId);
        reject(new Error("timed out waiting for server acknowledgement"));
      }, ACK_TIMEOUT_MS);
      this.pending.set(clientOpId, { resolve, reject, timer });
      this.sendRaw({
        type: "op",
        trip_id: input.tripId,
        client_op_id: clientOpId,
        kind: input.kind,
        entity_id: input.entityId,
        fields: input.fields,
        values: input.values,
      });
    });
  }

  private async connect(): Promise<void> {
    this.closedByCaller = false;
    this.setStatus(this.reconnectAttempt > 0 ? "reconnecting" : "connecting");

    let ticket: string;
    try {
      ticket = (await mintWsTicket()).ticket;
    } catch (err) {
      this.scheduleReconnect();
      throw err;
    }

    await new Promise<void>((resolve, reject) => {
      const ws = new WebSocket(`${WS_URL}?ticket=${encodeURIComponent(ticket)}`);
      this.ws = ws;

      ws.onopen = () => {
        this.reconnectAttempt = 0;
        this.setStatus("open");
        for (const tripId of this.subscriptions.keys()) this.sendSubscribe(tripId);
        resolve();
      };

      ws.onmessage = (event) => {
        this.handleFrame(JSON.parse(event.data as string) as ServerFrame);
      };

      ws.onerror = () => {
        // onclose follows; reconnect is scheduled there.
      };

      ws.onclose = () => {
        this.ws = null;
        if (this.closedByCaller) {
          this.setStatus("closed");
          return;
        }
        this.scheduleReconnect();
        resolve();
      };

      // Reject the connect() promise only if the socket never opens at all; a later drop is
      // handled by the reconnect loop, not by this promise (nothing awaits it by then).
      setTimeout(() => {
        if (ws.readyState !== WebSocket.OPEN) reject(new Error("connection timed out"));
      }, ACK_TIMEOUT_MS);
    }).catch(() => {
      this.scheduleReconnect();
    });
  }

  private scheduleReconnect(): void {
    if (this.closedByCaller) return;
    this.setStatus("reconnecting");
    const delay = Math.min(500 * 2 ** this.reconnectAttempt, MAX_BACKOFF_MS);
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      void this.connect();
    }, delay);
  }

  private handleFrame(frame: ServerFrame): void {
    switch (frame.type) {
      case "subscribed": {
        const f = frame as SubscribedFrame;
        this.subscriptions.set(f.trip_id, f.seq);
        this.emit("presence-snapshot", { tripId: f.trip_id, members: f.presence });
        break;
      }
      case "op": {
        const f = frame as OpFrame;
        if (this.subscriptions.has(f.trip_id)) {
          this.subscriptions.set(f.trip_id, f.seq);
        }
        this.emit("op", f);
        if (f.client_op_id) {
          const pending = this.pending.get(f.client_op_id);
          if (pending) {
            clearTimeout(pending.timer);
            this.pending.delete(f.client_op_id);
            pending.resolve(f);
          }
        }
        break;
      }
      case "presence": {
        this.emit("presence-change", frame as PresenceFrame);
        break;
      }
      case "error": {
        const f = frame as ErrorFrame;
        if (f.code === "session_revoked") {
          this.emit("session-revoked", undefined);
          break;
        }
        if (f.client_op_id) {
          const pending = this.pending.get(f.client_op_id);
          if (pending) {
            clearTimeout(pending.timer);
            this.pending.delete(f.client_op_id);
            pending.reject(new Error(f.message || f.code));
          }
        }
        break;
      }
      case "resync_required": {
        const f = frame as ResyncFrame;
        // Reset to "no prior state" so the next subscribe starts at the head (D74) — paired
        // with a REST refetch by the caller, which re-establishes the baseline this resumes.
        this.subscriptions.set(f.trip_id, null);
        this.emit("resync", { tripId: f.trip_id, reason: f.reason });
        this.sendSubscribe(f.trip_id);
        break;
      }
      case "unsubscribed":
        break;
    }
  }
}

let singleton: TripSocket | null = null;

export function getTripSocket(): TripSocket {
  if (!singleton) singleton = new TripSocket();
  return singleton;
}

export function resetTripSocket(): void {
  singleton?.close();
  singleton = null;
}
