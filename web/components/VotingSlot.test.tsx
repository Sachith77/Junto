import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { VotingSlot } from "./VotingSlot";
import type { Slot, SlotOption, Vote, User } from "@/lib/types";

const listOptions = vi.fn<() => Promise<SlotOption[]>>();
const listVotes = vi.fn<() => Promise<Vote[]>>();
const sendOp = vi.fn();
let currentUser: User = { id: "u-me", email: "me@example.com", display_name: "Me", email_verified_at: "x", created_at: "x" };

vi.mock("@/lib/api/options", () => ({ listOptions: () => listOptions() }));
vi.mock("@/lib/api/votes", () => ({ listVotes: () => listVotes() }));
vi.mock("@/hooks/useTripMembers", () => ({ useTripMembers: () => ({}) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: currentUser }) }));
vi.mock("@/context/TripSocketContext", () => ({
  useTripSocket: () => ({
    onOp: () => () => {},
    sendOp,
    resyncSignal: 0,
  }),
}));

function makeOption(id: string, title: string): SlotOption {
  return {
    id,
    slot_id: "slot-1",
    title,
    notes: "",
    external_url: "",
    place: { name: "", address: "" },
    version: 1,
    created_at: "x",
    updated_at: "x",
  };
}

function makeSlot(selectedOptionId?: string): Slot {
  return {
    id: "slot-1",
    kind: "activity",
    title: "Dinner",
    notes: "",
    position: "a",
    status: "planned",
    selected_option_id: selectedOptionId,
    version: 1,
    created_at: "x",
    updated_at: "x",
  };
}

beforeEach(() => {
  listOptions.mockReset();
  listVotes.mockReset();
  sendOp.mockReset().mockResolvedValue({});
  currentUser = { id: "u-me", email: "me@example.com", display_name: "Me", email_verified_at: "x", created_at: "x" };
});

describe("VotingSlot", () => {
  it("renders every option with a zero tally when nobody has voted", async () => {
    listOptions.mockResolvedValue([makeOption("opt-a", "Option A"), makeOption("opt-b", "Option B")]);
    listVotes.mockResolvedValue([]);

    render(<VotingSlot tripId="trip-1" slot={makeSlot()} />);

    const cards = await screen.findAllByTestId("option-card");
    expect(cards).toHaveLength(2);
    for (const card of cards) {
      expect(within(card).getByTestId("vote-count")).toHaveTextContent("0 votes");
      expect(within(card).queryByTestId("selected-badge")).not.toBeInTheDocument();
      expect(within(card).getByTestId("cast-vote")).toBeInTheDocument();
    }
  });

  it("renders a tied tally accurately with no option marked selected", async () => {
    listOptions.mockResolvedValue([makeOption("opt-a", "Option A"), makeOption("opt-b", "Option B")]);
    listVotes.mockResolvedValue([
      { user_id: "u1", option_id: "opt-a", version: 1, updated_at: "x" },
      { user_id: "u2", option_id: "opt-b", version: 1, updated_at: "x" },
    ]);

    render(<VotingSlot tripId="trip-1" slot={makeSlot()} />);

    const cardA = (await screen.findAllByTestId("option-card")).find((c) => c.textContent?.includes("Option A"))!;
    const cardB = (await screen.findAllByTestId("option-card")).find((c) => c.textContent?.includes("Option B"))!;

    expect(within(cardA).getByTestId("vote-count")).toHaveTextContent("1 vote");
    expect(within(cardB).getByTestId("vote-count")).toHaveTextContent("1 vote");
    expect(within(cardA).queryByTestId("selected-badge")).not.toBeInTheDocument();
    expect(within(cardB).queryByTestId("selected-badge")).not.toBeInTheDocument();
  });

  it("shows the resolved selection distinctly from the raw tally (D41)", async () => {
    listOptions.mockResolvedValue([makeOption("opt-a", "Option A"), makeOption("opt-b", "Option B")]);
    // The group voted mostly for A, but the resolved pick is B — a deliberate override.
    listVotes.mockResolvedValue([
      { user_id: "u1", option_id: "opt-a", version: 1, updated_at: "x" },
      { user_id: "u2", option_id: "opt-a", version: 1, updated_at: "x" },
      { user_id: "u3", option_id: "opt-b", version: 1, updated_at: "x" },
    ]);

    render(<VotingSlot tripId="trip-1" slot={makeSlot("opt-b")} />);

    const cardA = (await screen.findAllByTestId("option-card")).find((c) => c.textContent?.includes("Option A"))!;
    const cardB = (await screen.findAllByTestId("option-card")).find((c) => c.textContent?.includes("Option B"))!;

    expect(within(cardA).getByTestId("vote-count")).toHaveTextContent("2 votes");
    expect(within(cardB).getByTestId("vote-count")).toHaveTextContent("1 vote");
    expect(within(cardA).queryByTestId("selected-badge")).not.toBeInTheDocument();
    expect(within(cardB).getByTestId("selected-badge")).toBeInTheDocument();
  });

  it("casts a vote over the sync engine's vote.set.v1 op, not a REST call", async () => {
    listOptions.mockResolvedValue([makeOption("opt-a", "Option A")]);
    listVotes.mockResolvedValue([]);
    const user = userEvent.setup();

    render(<VotingSlot tripId="trip-1" slot={makeSlot()} />);
    const card = await screen.findByTestId("option-card");
    await user.click(within(card).getByTestId("cast-vote"));

    expect(sendOp).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "vote.set.v1",
        fields: ["slot_id", "user_id", "option_id"],
        values: { slot_id: "slot-1", option_id: "opt-a" },
      })
    );
  });

  it("retracts by sending option_id: null, mapping to the existing register semantics (D42)", async () => {
    listOptions.mockResolvedValue([makeOption("opt-a", "Option A")]);
    listVotes.mockResolvedValue([{ user_id: "u-me", option_id: "opt-a", version: 1, updated_at: "x" }]);
    const user = userEvent.setup();

    render(<VotingSlot tripId="trip-1" slot={makeSlot()} />);
    const card = await screen.findByTestId("option-card");
    expect(within(card).getByTestId("retract-vote")).toBeInTheDocument();

    await user.click(within(card).getByTestId("retract-vote"));

    expect(sendOp).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "vote.set.v1",
        values: { slot_id: "slot-1", option_id: null },
      })
    );
  });
});
