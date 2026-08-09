"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { useSearchParams } from "next/navigation";
import { resetPassword } from "@/lib/api/auth";
import { ApiError } from "@/lib/http";
import { AuthShell, Field, Notice } from "@/components/auth/AuthShell";
import { Button, ButtonLink } from "@/components/ui/Button";

function ResetPasswordInner() {
  const token = useSearchParams().get("token");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [done, setDone] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (!token) {
    return (
      <AuthShell title="Link not valid" blurb="This reset link is missing its token.">
        <ButtonLink href="/forgot-password" size="lg" className="w-full">
          Request a new link
        </ButtonLink>
      </AuthShell>
    );
  }

  if (done) {
    return (
      <AuthShell
        title="Password changed"
        // Worth saying explicitly: resetting a password revokes every session (D91/D95), so
        // the user's other devices really are signed out and that is not a bug.
        blurb="You've been signed out everywhere else. Log in with your new password."
      >
        <ButtonLink href="/login" size="lg" className="w-full">
          Log in
        </ButtonLink>
      </AuthShell>
    );
  }

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setError("Those passwords don't match.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await resetPassword(token, password);
      setDone(true);
    } catch (err) {
      if (err instanceof ApiError && err.violations.length > 0) {
        setError(err.violations[0].message);
      } else if (err instanceof ApiError && err.status === 429) {
        setError("Too many attempts. Please wait a moment and try again.");
      } else {
        setError("That link is invalid or has expired. Request a new one.");
      }
      setSubmitting(false);
    }
  };

  return (
    <AuthShell
      title="Set a new password"
      footer={
        <Link href="/login" className="rounded-sm text-accent-text underline underline-offset-2">
          Back to log in
        </Link>
      }
    >
      <form onSubmit={onSubmit} className="space-y-4">
        {error && <Notice tone="error">{error}</Notice>}
        <Field
          id="password"
          label="New password"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          hint="At least 12 characters."
        />
        <Field
          id="confirm"
          label="Confirm new password"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        <Button type="submit" size="lg" disabled={submitting} className="w-full">
          {submitting ? "Saving…" : "Change password"}
        </Button>
      </form>
    </AuthShell>
  );
}

export default function ResetPasswordPage() {
  return (
    <Suspense fallback={null}>
      <ResetPasswordInner />
    </Suspense>
  );
}
