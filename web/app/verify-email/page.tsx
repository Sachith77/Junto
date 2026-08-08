"use client";

import Link from "next/link";
import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { verifyEmail } from "@/lib/api/auth";

function VerifyEmailInner() {
  const params = useSearchParams();
  const token = params.get("token");
  const [state, setState] = useState<"pending" | "ok" | "error">(() =>
    token ? "pending" : "error"
  );

  useEffect(() => {
    if (!token) return;
    verifyEmail(token)
      .then(() => setState("ok"))
      .catch(() => setState("error"));
  }, [token]);

  return (
    <main className="flex flex-1 items-center justify-center p-8">
      <div className="max-w-sm space-y-2 text-center">
        {state === "pending" && <p>Verifying…</p>}
        {state === "ok" && (
          <>
            <h1 className="text-2xl font-semibold">Email verified</h1>
            <Link href="/login" className="text-blue-600 underline">
              Log in
            </Link>
          </>
        )}
        {state === "error" && (
          <p role="alert" className="text-red-700">
            That verification link is invalid or has expired.
          </p>
        )}
      </div>
    </main>
  );
}

export default function VerifyEmailPage() {
  return (
    <Suspense fallback={null}>
      <VerifyEmailInner />
    </Suspense>
  );
}
