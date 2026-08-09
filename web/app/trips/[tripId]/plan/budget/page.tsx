import { BudgetPanel } from "@/components/plan/BudgetPanel";

export default async function BudgetPage({ params }: { params: Promise<{ tripId: string }> }) {
  const { tripId } = await params;
  return <BudgetPanel tripId={tripId} />;
}
