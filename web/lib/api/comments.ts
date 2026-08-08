import { apiFetch } from "../http";
import type { Comment } from "../types";

// Posting and deleting go over the WebSocket as comment.create.v1 / comment.delete.v1 (D103 —
// comments are WS-native, unlike attachments), not through this module — see
// components/CommentsList.tsx. This is the read side only.
export async function listComments(tripId: string, slotId: string): Promise<Comment[]> {
  return apiFetch<Comment[]>(`/api/v1/trips/${tripId}/slots/${slotId}/comments`);
}
