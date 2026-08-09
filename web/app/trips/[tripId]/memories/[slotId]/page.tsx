import { DestinationDetail } from "@/components/memories/DestinationDetail";

export default async function DestinationPage({
  params,
}: {
  params: Promise<{ tripId: string; slotId: string }>;
}) {
  const { tripId, slotId } = await params;
  return <DestinationDetail tripId={tripId} slotId={slotId} />;
}
