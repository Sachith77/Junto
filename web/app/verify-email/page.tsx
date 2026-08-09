"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { verifyEmail } from "@/lib/api/auth";
import { AuthShell } from "@/components/auth/AuthShell";
import { ButtonLink } from "@/components/ui/Button";

function VerifyEmailInner() {
  const token = useSearchParams().get("token");
  const [state, setState] = useState<"pending" | "ok" | "error">(() =>
    token ? "pending" : "error"
  );

  useEffect(() => {
    if (!token) return;
    verifyEmail(token)
      .then(() => setState("ok"))
      .catch(() => setState("error"));
  }, [token]);

  if (state === "pending") {
    return (
      <AuthShell title="Verifying…" blurb="One moment.">
        <span role="status" className="sr-only">
          Verifying your email address
        </span>
      </AuthShell>
    );
  }

  if (state === "ok") {
    return (
      <AuthShell title="Email verified" blurb="Your account is ready to use.">
        <ButtonLink href="/login" size="lg" className="w-full">
          Log in
        </ButtonLink>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Link not valid"
      // Verification tokens are single-use, so the commonest cause of landing here is a
      // second click on an already-used link — worth saying, since the fix differs.
      blurb="That link is invalid, already used, or has expired."
    >
      <ButtonLink href="/login" size="lg" className="w-full">
        Back to log in
      </ButtonLink>
    </AuthShell>
  );
}

export default function VerifyEmailPage() {
  return (
    <Suspense fallback={null}>
      <VerifyEmailInner />
    </Suspense>
  );
}
