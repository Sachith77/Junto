import { Itinerary } from "@/components/plan/Itinerary";

export default async function PlanPage({ params }: { params: Promise<{ tripId: string }> }) {
  const { tripId } = await params;
  return <Itinerary tripId={tripId} />;
}
