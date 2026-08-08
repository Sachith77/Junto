"use client";

import Link from "next/link";
import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { acceptInvitation } from "@/lib/api/members";
import { useAuth } from "@/context/AuthContext";

function AcceptInvitationInner() {
  const params = useSearchParams();
  const token = params.get("token");
  const { status } = useAuth();
  const router = useRouter();
  const [state, setState] = useState<"pending" | "error">("pending");

  useEffect(() => {
    if (status !== "authenticated" || !token) return;
    acceptInvitation(token)
      .then((trip) => router.replace(`/trips/${trip.id}`))
      .catch(() => setState("error"));
  }, [status, token, router]);

  if (!token) {
    return (
      <p role="alert" className="text-red-700">
        This invitation link is missing its token.
      </p>
    );
  }

  if (status === "loading") return <p>Loading…</p>;

  if (status === "anonymous") {
    return (
      <p>
        <Link href="/login" className="text-blue-600 underline">
          Log in
        </Link>{" "}
        (or{" "}
        <Link href="/signup" className="text-blue-600 underline">
          sign up
        </Link>
        ), then open this invitation link again to join the trip.
      </p>
    );
  }

  if (state === "error") {
    return (
      <p role="alert" className="text-red-700">
        That invitation is invalid, expired, or already used.
      </p>
    );
  }

  return <p>Joining trip…</p>;
}

export default function AcceptInvitationPage() {
  return (
    <main className="flex flex-1 items-center justify-center p-8">
      <div className="max-w-sm space-y-2 text-center">
        <Suspense fallback={null}>
          <AcceptInvitationInner />
        </Suspense>
      </div>
    </main>
  );
}
