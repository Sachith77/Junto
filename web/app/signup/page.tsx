"use client";

import Link from "next/link";
import { ButtonLink } from "@/components/ui/Button";
import { useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { ApiError } from "@/lib/http";

export default function SignupPage() {
  const { signup } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState<string | null>(null);
  // null = not submitted. "verify" = check your inbox. "ready" = the server already
  // marked the address verified (AUTH_AUTO_VERIFY_EMAIL), so there is nothing to wait for.
  const [done, setDone] = useState<null | "verify" | "ready">(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const user = await signup(email, password, displayName);
      // Branch on what the server actually returned rather than on a client-side
      // copy of the server's configuration — the response is the source of truth.
      setDone(user.email_verified_at ? "ready" : "verify");
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.violations[0]?.message ?? err.message);
      } else {
        setError("Something went wrong.");
      }
    } finally {
      setSubmitting(false);
    }
  };

  if (done) {
    return (
      <main className="flex flex-1 items-center justify-center p-8">
        <div className="max-w-sm space-y-3 text-center">
          <h1 className="font-display text-display-lg text-fg">
            {done === "ready" ? "You're all set" : "Check your email"}
          </h1>
          <p className="text-ui-lg text-fg-muted">
            {done === "ready" ? (
              <>Your account is ready. Log in and start planning.</>
            ) : (
              <>We sent a verification link to {email}. Follow it, then log in.</>
            )}
          </p>
          <div className="pt-2">
            <ButtonLink href="/login" variant="primary" size="lg">
              {done === "ready" ? "Log in" : "Back to log in"}
            </ButtonLink>
          </div>
          {done === "verify" && (
            <p className="pt-2 text-ui-xs text-fg-subtle">
              Running locally? Mail is captured by Mailpit at{" "}
              <a
                href="http://localhost:8025"
                target="_blank"
                rel="noreferrer"
                className="text-accent-text underline"
              >
                localhost:8025
              </a>
              .
            </p>
          )}
        </div>
      </main>
    );
  }

  return (
    <main className="flex flex-1 items-center justify-center p-8">
      <form onSubmit={onSubmit} className="w-full max-w-sm space-y-4">
        <h1 className="text-2xl font-semibold">Sign up</h1>
        {error && (
          <p role="alert" className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
            {error}
          </p>
        )}
        <div className="space-y-1">
          <label className="text-sm font-medium" htmlFor="displayName">
            Name
          </label>
          <input
            id="displayName"
            required
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="w-full rounded-md border border-neutral-300 px-3 py-2"
          />
        </div>
        <div className="space-y-1">
          <label className="text-sm font-medium" htmlFor="email">
            Email
          </label>
          <input
            id="email"
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="w-full rounded-md border border-neutral-300 px-3 py-2"
          />
        </div>
        <div className="space-y-1">
          <label className="text-sm font-medium" htmlFor="password">
            Password (min. 12 characters)
          </label>
          <input
            id="password"
            type="password"
            minLength={12}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded-md border border-neutral-300 px-3 py-2"
          />
        </div>
        <button
          type="submit"
          disabled={submitting}
          className="w-full rounded-md bg-blue-600 px-4 py-2 text-white disabled:opacity-50"
        >
          {submitting ? "Signing up…" : "Sign up"}
        </button>
        <p className="text-sm text-neutral-500">
          Already have an account?{" "}
          <Link href="/login" className="text-blue-600 underline">
            Log in
          </Link>
        </p>
      </form>
    </main>
  );
}
