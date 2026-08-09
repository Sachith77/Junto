import { SlotDetail } from "@/components/plan/SlotDetail";

export default async function SlotPage({
  params,
}: {
  params: Promise<{ tripId: string; slotId: string }>;
}) {
  const { tripId, slotId } = await params;
  return <SlotDetail tripId={tripId} slotId={slotId} />;
}
