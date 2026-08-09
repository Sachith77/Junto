"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { ApiError } from "@/lib/http";
import { AuthShell, Field, Notice } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/Button";

export default function LoginPage() {
  const { login } = useAuth();
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await login(email, password);
      router.push("/trips");
    } catch (err) {
      // Every credential failure collapses to one message (D32) — distinguishing unknown
      // account from wrong password would turn this form into an enumeration oracle. The two
      // exceptions are the ones the user can actually act on.
      if (err instanceof ApiError && err.status === 403) {
        setError("Please verify your email before logging in.");
      } else if (err instanceof ApiError && err.status === 429) {
        setError("Too many attempts. Please wait a moment and try again.");
      } else {
        setError("Invalid email or password.");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AuthShell
      title="Welcome back"
      blurb="Pick up where the group left off."
      footer={
        <>
          No account?{" "}
          <Link href="/signup" className="rounded-sm text-accent-text underline underline-offset-2">
            Sign up
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

        <div className="space-y-1.5">
          <div className="flex items-baseline justify-between gap-3">
            <label htmlFor="password" className="block text-ui-sm font-medium text-fg">
              Password
            </label>
            <Link
              href="/forgot-password"
              className="rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 hover:text-fg"
            >
              Forgot?
            </Link>
          </div>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded-sm border border-line bg-surface px-3 py-2.5 text-ui-md text-fg placeholder:text-fg-subtle transition-colors focus:border-accent focus:outline-none"
          />
        </div>

        <Button type="submit" size="lg" disabled={submitting} className="w-full">
          {submitting ? "Logging in…" : "Log in"}
        </Button>
      </form>
    </AuthShell>
  );
}
