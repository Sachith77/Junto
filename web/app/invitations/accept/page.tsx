"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { acceptInvitation } from "@/lib/api/members";
import { useAuth } from "@/context/AuthContext";
import { AuthShell } from "@/components/auth/AuthShell";
import { ButtonLink } from "@/components/ui/Button";

function AcceptInvitationInner() {
  const token = useSearchParams().get("token");
  const { status } = useAuth();
  const router = useRouter();
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (status !== "authenticated" || !token) return;
    acceptInvitation(token)
      .then((trip) => router.replace(`/trips/${trip.id}`))
      .catch(() => setFailed(true));
  }, [status, token, router]);

  if (!token) {
    return (
      <AuthShell title="Link not valid" blurb="This invitation link is missing its token.">
        <ButtonLink href="/trips" size="lg" className="w-full">
          Go to your trips
        </ButtonLink>
      </AuthShell>
    );
  }

  if (status === "loading") {
    return (
      <AuthShell title="One moment…">
        <span role="status" className="sr-only">
          Checking your session
        </span>
      </AuthShell>
    );
  }

  if (status === "anonymous") {
    return (
      <AuthShell
        title="You've been invited"
        // The invitation is checked against the invitee's own address (D58), so signing in
        // first is not incidental — it is what makes a targeted invite mean anything.
        blurb="Log in or sign up first, then open this link again to join the trip."
      >
        <div className="space-y-2">
          <ButtonLink href="/login" size="lg" className="w-full">
            Log in
          </ButtonLink>
          <ButtonLink href="/signup" variant="secondary" size="lg" className="w-full">
            Create an account
          </ButtonLink>
        </div>
      </AuthShell>
    );
  }

  if (failed) {
    return (
      <AuthShell
        title="Invitation not valid"
        blurb="It may have expired, been revoked, already been used, or been meant for a different address."
      >
        <ButtonLink href="/trips" size="lg" className="w-full">
          Go to your trips
        </ButtonLink>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="Joining trip…">
      <span role="status" className="sr-only">
        Accepting your invitation
      </span>
    </AuthShell>
  );
}

export default function AcceptInvitationPage() {
  return (
    <Suspense fallback={null}>
      <AcceptInvitationInner />
    </Suspense>
  );
}
