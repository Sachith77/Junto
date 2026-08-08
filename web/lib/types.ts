export interface User {
  id: string;
  email: string;
  display_name: string;
  email_verified_at: string | null;
  created_at: string;
}

export interface Session {
  access_token: string;
  expires_at: string;
  token_type: "Bearer";
  user: User;
}

export interface Trip {
  id: string;
  name: string;
  description: string;
  time_zone: string;
  start_date: string | null;
  end_date: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Member {
  user_id: string;
  role: "owner" | "editor" | "viewer";
  invited_by?: string;
  joined_at: string;
  version: number;
  // Populated client-side from /me when the row belongs to the current user; the backend
  // does not join in a display name here (see membership_handler.go).
  display_name?: string;
}

export type SlotKind = "place" | "activity" | "transport" | "lodging" | "note";
export type SlotStatus = "planned" | "covered" | "skipped";

export interface Slot {
  id: string;
  day_id?: string;
  kind: SlotKind;
  title: string;
  notes: string;
  start_time?: string;
  end_time?: string;
  position: string;
  selected_option_id?: string;
  status: SlotStatus;
  status_changed_at?: string;
  status_changed_by?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Place {
  name: string;
  address: string;
  lat?: number;
  lng?: number;
  provider_id?: string;
}

export interface SlotOption {
  id: string;
  slot_id: string;
  title: string;
  notes: string;
  external_url: string;
  estimated_cost_minor?: number;
  place: Place;
  proposed_by?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Vote {
  user_id: string;
  option_id?: string;
  version: number;
  updated_at: string;
}

// --- WebSocket wire protocol (internal/transport/ws/protocol.go) ---

export interface OpFrame {
  type: "op";
  trip_id: string;
  seq: number;
  op_id: string;
  kind: string;
  entity_id: string;
  actor_id?: string;
  fields: string[];
  payload: { fields: Record<string, unknown>; meta: { version: number; updated_at: string } };
  client_op_id?: string;
  cause_op_id?: string;
  created_at: string;
}

export interface PresenceMember {
  user_id: string;
  conn_id: string;
  joined_at: string;
}

export interface SubscribedFrame {
  type: "subscribed";
  trip_id: string;
  seq: number;
  presence: PresenceMember[];
}

export interface PresenceFrame {
  type: "presence";
  trip_id: string;
  event: "joined" | "left";
  user_id: string;
  conn_id: string;
  at: string;
}

export interface ErrorFrame {
  type: "error";
  client_op_id?: string;
  code: string;
  message: string;
  violations?: { field: string; code: string; message: string }[];
}

export interface ResyncFrame {
  type: "resync_required";
  trip_id: string;
  reason?: string;
}

export interface SimpleFrame {
  type: "unsubscribed";
  trip_id: string;
}

export type ServerFrame =
  | OpFrame
  | SubscribedFrame
  | PresenceFrame
  | ErrorFrame
  | ResyncFrame
  | SimpleFrame;
