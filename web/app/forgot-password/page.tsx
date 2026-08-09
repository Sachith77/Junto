"use client";

import Link from "next/link";
import { useState } from "react";
import { requestPasswordReset } from "@/lib/api/auth";
import { ApiError } from "@/lib/http";
import { AuthShell, Field, Notice } from "@/components/auth/AuthShell";
import { Button, ButtonLink } from "@/components/ui/Button";

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await requestPasswordReset(email);
      setSent(true);
    } catch (err) {
      // The endpoint succeeds whether or not the address exists, so a failure here is a
      // transport problem or a throttle — never "no such account", which it deliberately
      // refuses to tell anyone.
      if (err instanceof ApiError && err.status === 429) {
        setError("Too many attempts. Please wait a moment and try again.");
      } else {
        setError("Couldn't send that email. Please try again.");
      }
      setSubmitting(false);
    }
  };

  if (sent) {
    return (
      <AuthShell
        title="Check your email"
        // Worded so it says the same thing whether or not the account exists — the message
        // must not become the oracle the endpoint refuses to be.
        blurb={<>If an account exists for {email}, we&rsquo;ve sent a link to reset its password.</>}
      >
        <div className="space-y-4">
          <ButtonLink href="/login" size="lg" className="w-full">
            Back to log in
          </ButtonLink>
          <p className="text-ui-xs text-fg-subtle">
            The link expires in an hour. Running locally? Mail is captured by Mailpit at{" "}
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
        </div>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Reset your password"
      blurb="We'll email you a link to set a new one."
      footer={
        <>
          Remembered it?{" "}
          <Link href="/login" className="rounded-sm text-accent-text underline underline-offset-2">
            Log in
          </Link>
        </>
      }
    >
      <form onSubmit={onSubmit} className="space-y-4">
        {error && <Notice tone="error">{error}</Notice>}
        <Field
          id="email"
          label="Email"
          type="email"
          autoComplete="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <Button type="submit" size="lg" disabled={submitting} className="w-full">
          {submitting ? "Sending…" : "Send reset link"}
        </Button>
      </form>
    </AuthShell>
  );
}
