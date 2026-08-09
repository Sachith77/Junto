import { apiFetch } from "../http";

export interface BudgetSplit {
  user_id: string;
  amount_minor: number;
}

export interface BudgetEntry {
  id: string;
  label: string;
  category: string;
  amount_minor: number;
  slot_option_id?: string;
  paid_by?: string;
  incurred_on?: string;
  splits: BudgetSplit[];
  created_by?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export const BUDGET_CATEGORIES = [
  "lodging",
  "transport",
  "food",
  "activity",
  "shopping",
  "other",
] as const;

export async function listBudget(tripId: string): Promise<BudgetEntry[]> {
  return apiFetch<BudgetEntry[]>(`/api/v1/trips/${tripId}/budget`);
}

export async function createBudgetEntry(
  tripId: string,
  input: {
    label: string;
    category: string;
    amountMinor: number;
    paidBy: string | null;
    splits: BudgetSplit[];
  }
): Promise<BudgetEntry> {
  return apiFetch<BudgetEntry>(`/api/v1/trips/${tripId}/budget`, {
    method: "POST",
    body: {
      label: input.label,
      category: input.category,
      amount_minor: input.amountMinor,
      slot_option_id: null,
      paid_by: input.paidBy,
      incurred_on: null,
      splits: input.splits,
      version: null,
    },
  });
}

/** PUT, not PATCH — a budget entry is replaced whole together with its complete split set
 *  (D44), and the version is REQUIRED because the entry cannot merge (D85). */
export async function deleteBudgetEntry(
  tripId: string,
  entryId: string,
  version: number
): Promise<void> {
  await apiFetch<void>(`/api/v1/trips/${tripId}/budget/${entryId}`, {
    method: "DELETE",
    body: { version },
  });
}
