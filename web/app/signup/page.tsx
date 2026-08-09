"use client";

import Link from "next/link";
import { useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { ApiError } from "@/lib/http";
import { AuthShell, Field, Notice } from "@/components/auth/AuthShell";
import { Button, ButtonLink } from "@/components/ui/Button";

export default function SignupPage() {
  const { signup } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState<string | null>(null);
  // null = not submitted. "verify" = check your inbox. "ready" = the server already marked the
  // address verified (AUTH_AUTO_VERIFY_EMAIL, D105), so there is nothing to wait for.
  const [done, setDone] = useState<null | "verify" | "ready">(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const user = await signup(email, password, displayName);
      // Branch on what the server actually returned rather than on a client-side copy of the
      // server's configuration — the response is the source of truth.
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
      <AuthShell
        title={done === "ready" ? "You're all set" : "Check your email"}
        blurb={
          done === "ready" ? (
            "Your account is ready. Log in and start planning."
          ) : (
            <>We sent a verification link to {email}. Follow it, then log in.</>
          )
        }
      >
        <div className="space-y-4">
          <ButtonLink href="/login" size="lg" className="w-full">
            {done === "ready" ? "Log in" : "Back to log in"}
          </ButtonLink>
          {done === "verify" && (
            <p className="text-ui-xs text-fg-subtle">
              Running locally? Mail is captured by Mailpit at{" "}
              <a
                href="http://localhost:8025"
                target="_blank"
                rel="noreferrer"
                className="rounded-sm text-accent-text underline underline-offset-2"
              >
                localhost:8025
              </a>
              .
            </p>
          )}
        </div>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Start planning"
      blurb="One account, however many trips."
      footer={
        <>
          Already have an account?{" "}
          <Link href="/login" className="rounded-sm text-accent-text underline underline-offset-2">
            Log in
          </Link>
        </>
      }
    >
      <form onSubmit={onSubmit} className="space-y-4">
        {error && <Notice tone="error">{error}</Notice>}

        <Field
          id="displayName"
          label="Name"
          autoComplete="name"
          required
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder="Alice Moreau"
        />
        <Field
          id="email"
          label="Email"
          type="email"
          autoComplete="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <Field
          id="password"
          label="Password"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          // Length-only by design; the compensating controls are Argon2id and the rate
          // limiter (D35), so the form asks for length rather than character classes.
          hint="At least 12 characters. A short sentence works well."
        />

        <Button type="submit" size="lg" disabled={submitting} className="w-full">
          {submitting ? "Creating account…" : "Create account"}
        </Button>
      </form>
    </AuthShell>
  );
}
