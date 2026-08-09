import Link from "next/link";
import type { ReactNode } from "react";

/**
 * The frame every auth screen sits in.
 *
 * Outer-shell ADJACENT rather than outer-shell proper: the wordmark and heading are Fraunces
 * and the primary action is amber, so it is unmistakably the same product as the landing
 * page — but there is no full-bleed photography here. A form is a task, not a moment, and a
 * cinematic treatment would put a scrim between the reader and the two fields they came to
 * fill in. The card language (radius, border, shadow) is the same one every other card uses.
 */
export function AuthShell({
  title,
  blurb,
  children,
  footer,
}: {
  title: string;
  blurb?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <main className="flex flex-1 flex-col items-center justify-center px-6 py-12">
      <div className="w-full max-w-sm">
        <Link
          href="/"
          className="mx-auto mb-8 block w-fit rounded-sm font-display text-display-md text-fg"
        >
          Junto
        </Link>

        <div className="rounded-card border border-line-subtle bg-surface-raised p-6 shadow-sm sm:p-7">
          <h1 className="font-display text-display-md text-fg">{title}</h1>
          {blurb && <p className="mt-1.5 text-ui-md text-fg-muted">{blurb}</p>}
          <div className="mt-5">{children}</div>
        </div>

        {footer && <div className="mt-5 text-center text-ui-sm text-fg-muted">{footer}</div>}
      </div>
    </main>
  );
}

/** A labelled input. Exists so five screens cannot drift into five slightly different field
 *  styles, and so the label/id pairing (which is what makes them reachable by label at all)
 *  is impossible to forget. */
export function Field({
  id,
  label,
  hint,
  ...props
}: {
  id: string;
  label: string;
  hint?: string;
} & React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-ui-sm font-medium text-fg">
        {label}
      </label>
      <input
        id={id}
        className="w-full rounded-sm border border-line bg-surface px-3 py-2.5 text-ui-md text-fg placeholder:text-fg-subtle transition-colors focus:border-accent focus:outline-none"
        {...props}
      />
      {hint && <p className="text-ui-xs text-fg-subtle">{hint}</p>}
    </div>
  );
}

/** Error and success notices, so their colour and shape are decided once. */
export function Notice({
  tone,
  children,
}: {
  tone: "error" | "success" | "info";
  children: ReactNode;
}) {
  const styles = {
    error: "border-critical-600/25 bg-critical-50 text-critical-700",
    success: "border-positive-600/25 bg-positive-50 text-positive-700",
    info: "border-line bg-surface-sunken text-fg-muted",
  }[tone];

  return (
    <p
      role={tone === "error" ? "alert" : "status"}
      className={`rounded-md border px-3 py-2 text-ui-sm ${styles}`}
    >
      {children}
    </p>
  );
}
