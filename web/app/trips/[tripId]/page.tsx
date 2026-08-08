import { ModePicker } from "@/components/ModePicker";

export default async function TripPage({
  params,
}: {
  params: Promise<{ tripId: string }>;
}) {
  const { tripId } = await params;
  return <ModePicker tripId={tripId} />;
}
