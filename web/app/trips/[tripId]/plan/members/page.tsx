import { MembersPanel } from "@/components/plan/MembersPanel";

export default async function MembersPage({ params }: { params: Promise<{ tripId: string }> }) {
  const { tripId } = await params;
  return <MembersPanel tripId={tripId} />;
}
