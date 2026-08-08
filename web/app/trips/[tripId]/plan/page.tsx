import { SlotList } from "@/components/SlotList";

// Plan mode. Group A moved it here from /trips/[tripId] so the trip root could
// become the three-mode picker; its styling is deliberately untouched and is
// Group B's job.
export default async function PlanPage({
  params,
}: {
  params: Promise<{ tripId: string }>;
}) {
  const { tripId } = await params;
  return <SlotList tripId={tripId} />;
}
