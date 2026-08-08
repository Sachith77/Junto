import { PlanChrome } from "@/components/TripShell";

export default async function PlanLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ tripId: string }>;
}) {
  const { tripId } = await params;
  return <PlanChrome tripId={tripId}>{children}</PlanChrome>;
}
