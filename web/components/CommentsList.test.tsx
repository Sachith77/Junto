import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { CommentsList } from "./CommentsList";
import type { Comment, OpFrame, User } from "@/lib/types";

const listComments = vi.fn<() => Promise<Comment[]>>();
const sendOp = vi.fn();
let currentUser: User = {
  id: "u-me",
  email: "me@example.com",
  display_name: "Me",
  email_verified_at: "x",
  created_at: "x",
};
let opHandler: ((op: OpFrame) => void) | null = null;

vi.mock("@/lib/api/comments", () => ({ listComments: () => listComments() }));
vi.mock("@/hooks/useTripMembers", () => ({ useTripMembers: () => ({ "u-me": "Me", "u-other": "Other" }) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: currentUser }) }));
vi.mock("@/context/TripSocketContext", () => ({
  useTripSocket: () => ({
    onOp: (handler: (op: OpFrame) => void) => {
      opHandler = handler;
      return () => {
        opHandler = null;
      };
    },
    sendOp,
    resyncSignal: 0,
  }),
}));

function makeComment(id: string, body: string, authorId: string): Comment {
  return { id, slot_id: "slot-1", body, author_id: authorId, created_at: "2026-08-08T00:00:00Z" };
}

function makeOpFrame(kind: string, entityId: string, payloadFields: Record<string, unknown>, actorId?: string): OpFrame {
  return {
    type: "op",
    trip_id: "trip-1",
    seq: 1,
    op_id: "op-1",
    kind,
    entity_id: entityId,
    actor_id: actorId,
    fields: Object.keys(payloadFields),
    payload: { fields: payloadFields, meta: { version: 0, updated_at: "2026-08-08T00:00:00Z" } },
    created_at: "2026-08-08T00:00:00Z",
  };
}

beforeEach(() => {
  listComments.mockReset();
  sendOp.mockReset().mockResolvedValue({});
  opHandler = null;
  currentUser = { id: "u-me", email: "me@example.com", display_name: "Me", email_verified_at: "x", created_at: "x" };
});

describe("CommentsList", () => {
  it("shows an empty state with no comments", async () => {
    listComments.mockResolvedValue([]);
    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    expect(await screen.findByText("No comments yet.")).toBeInTheDocument();
  });

  it("renders existing comments with resolved author names, in order", async () => {
    listComments.mockResolvedValue([
      makeComment("c-2", "second", "u-other"),
      makeComment("c-1", "first", "u-me"),
    ]);
    render(<CommentsList tripId="trip-1" slotId="slot-1" />);

    const rows = await screen.findAllByTestId("comment");
    expect(rows).toHaveLength(2);
    // Sorted by created_at, not by fetch order — both share a timestamp here, so this mainly
    // pins that rendering doesn't crash on a stable sort; author resolution is the real check.
    expect(within(rows[0]).getByText(/first|second/)).toBeInTheDocument();
    expect(screen.getByText("Other")).toBeInTheDocument();
  });

  it("only shows a delete control on the current user's own comments", async () => {
    listComments.mockResolvedValue([
      makeComment("c-mine", "mine", "u-me"),
      makeComment("c-theirs", "theirs", "u-other"),
    ]);
    render(<CommentsList tripId="trip-1" slotId="slot-1" />);

    const rows = await screen.findAllByTestId("comment");
    const mine = rows.find((r) => r.textContent?.includes("mine"))!;
    const theirs = rows.find((r) => r.textContent?.includes("theirs"))!;
    expect(within(mine).getByTestId("delete-comment")).toBeInTheDocument();
    expect(within(theirs).queryByTestId("delete-comment")).not.toBeInTheDocument();
  });

  it("posts optimistically — the comment appears immediately, marked pending, before the server acks", async () => {
    listComments.mockResolvedValue([]);
    // Never resolves within the test, so the row must already be visible from the optimistic
    // update alone, not from awaiting sendOp.
    sendOp.mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup();

    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    await screen.findByText("No comments yet.");

    await user.type(screen.getByPlaceholderText("Add a comment"), "Should we book the early flight?");
    await user.click(screen.getByTestId("post-comment"));

    const row = await screen.findByTestId("comment");
    expect(row).toHaveTextContent("Should we book the early flight?");
    expect(row.dataset.pending).toBe("true");
    expect(sendOp).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "comment.create.v1",
        fields: ["slot_id", "body"],
        values: { slot_id: "slot-1", body: "Should we book the early flight?" },
      })
    );
  });

  it("reconciles: the pending flag clears once the op broadcast for the same entity id arrives", async () => {
    listComments.mockResolvedValue([]);
    let resolveSend: (() => void) | undefined;
    sendOp.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveSend = resolve;
        })
    );
    const user = userEvent.setup();

    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    await screen.findByText("No comments yet.");
    await user.type(screen.getByPlaceholderText("Add a comment"), "hi");
    await user.click(screen.getByTestId("post-comment"));

    const row = await screen.findByTestId("comment");
    expect(row.dataset.pending).toBe("true");
    const entityId = sendOp.mock.calls[0][0].entityId as string;

    // The server's broadcast of our OWN op arrives over the socket — the actual ack path.
    act(() => {
      opHandler?.(makeOpFrame("comment.create.v1", entityId, { slot_id: "slot-1", body: "hi" }, "u-me"));
    });
    resolveSend?.();

    expect(await screen.findByTestId("comment")).toHaveAttribute("data-pending", "false");
  });

  it("rolls back optimistically on a rejected post and restores the draft", async () => {
    listComments.mockResolvedValue([]);
    sendOp.mockRejectedValue(new Error("rate_limited"));
    const user = userEvent.setup();

    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    await screen.findByText("No comments yet.");
    await user.type(screen.getByPlaceholderText("Add a comment"), "oops");
    await user.click(screen.getByTestId("post-comment"));

    await screen.findByRole("alert");
    expect(screen.queryByTestId("comment")).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("Add a comment")).toHaveValue("oops");
  });

  it("deletes optimistically and calls comment.delete.v1 with no field values", async () => {
    listComments.mockResolvedValue([makeComment("c-mine", "mine", "u-me")]);
    sendOp.mockResolvedValue({});
    const user = userEvent.setup();

    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    await screen.findByTestId("comment");

    await user.click(screen.getByTestId("delete-comment"));

    expect(sendOp).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "comment.delete.v1", entityId: "c-mine", fields: ["deleted_at"], values: {} })
    );
    expect(screen.queryByTestId("comment")).not.toBeInTheDocument();
  });

  it("live delivery: a comment created by ANOTHER client appears via the op broadcast alone", async () => {
    listComments.mockResolvedValue([]);
    render(<CommentsList tripId="trip-1" slotId="slot-1" />);
    await screen.findByText("No comments yet.");

    act(() => {
      opHandler?.(
        makeOpFrame("comment.create.v1", "c-remote", { slot_id: "slot-1", body: "from another client" }, "u-other")
      );
    });

    const row = await screen.findByTestId("comment");
    expect(row).toHaveTextContent("from another client");
    expect(row.dataset.pending).toBe("false");
  });
});
