import { apiFetchRaw } from "../http";

export interface WsTicket {
  ticket: string;
  expires_at: string;
}

// Bypasses the {data:...} envelope by design — see ws/ticket.go.
export async function mintWsTicket(): Promise<WsTicket> {
  return apiFetchRaw<WsTicket>("/api/v1/ws/ticket", { method: "POST" });
}
