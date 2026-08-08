import { SlotList } from "@/components/SlotList";

export default async function TripPage({
  params,
}: {
  params: Promise<{ tripId: string }>;
}) {
  const { tripId } = await params;
  return <SlotList tripId={tripId} />;
}
