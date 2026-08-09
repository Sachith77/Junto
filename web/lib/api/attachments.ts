import { apiFetch } from "../http";

export interface Attachment {
  id: string;
  kind: "file" | "link";
  status: "pending" | "ready" | "failed";
  external_url?: string;
  content_type?: string;
  size_bytes?: number;
  original_name?: string;
  slot_option_id?: string;
  slot_id?: string;
  uploaded_by?: string;
  created_at: string;
}

/** Attachments are owner-scoped; exactly one owner may be named (the exclusive arc, D47). */
export async function listAttachments(
  tripId: string,
  owner: { slotId?: string; slotOptionId?: string }
): Promise<Attachment[]> {
  const q = new URLSearchParams();
  if (owner.slotId) q.set("slot_id", owner.slotId);
  if (owner.slotOptionId) q.set("slot_option_id", owner.slotOptionId);
  return apiFetch<Attachment[]>(`/api/v1/trips/${tripId}/attachments?${q}`);
}

/** The storage key never leaves the server (D90) — clients exchange an attachment id for a
 *  freshly signed URL per request, because a signed URL written anywhere durable is false by
 *  the time anything reads it back. */
export async function attachmentURL(tripId: string, attachmentId: string): Promise<string> {
  const res = await apiFetch<{ url: string }>(
    `/api/v1/trips/${tripId}/attachments/${attachmentId}/url`
  );
  return res.url;
}
