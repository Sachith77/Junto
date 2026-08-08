import { TripShell } from "@/components/TripShell";

// Socket + auth boundary only. Visual chrome belongs to each mode, because the
// cinematic mode picker and the dense Plan surfaces want completely different
// framing — see components/TripShell.tsx.
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
