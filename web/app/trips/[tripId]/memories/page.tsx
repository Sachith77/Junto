import { Memories } from "@/components/memories/Memories";

export default async function MemoriesPage({ params }: { params: Promise<{ tripId: string }> }) {
  const { tripId } = await params;
  return <Memories tripId={tripId} />;
}
