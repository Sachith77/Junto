import { TripShell } from "@/components/TripShell";

export default async function TripLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ tripId: string }>;
}) {
  const { tripId } = await params;
  return <TripShell tripId={tripId}>{children}</TripShell>;
}
