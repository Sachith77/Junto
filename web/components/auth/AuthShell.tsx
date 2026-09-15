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
    <main className="grid min-h-screen flex-1 bg-surface lg:grid-cols-[minmax(0,1.08fr)_minmax(28rem,.92fr)]">
      <aside className="relative m-4 hidden overflow-hidden rounded-[1.25rem] bg-[url('/trip-covers/coast.png')] bg-cover bg-center lg:block">
        <div className="absolute inset-0 bg-[linear-gradient(180deg,rgba(7,13,14,.16),rgba(7,13,14,.7))]" />
        <div className="absolute inset-x-0 bottom-0 p-10 text-white xl:p-14">
          <p className="text-ui-2xs font-semibold uppercase tracking-[.16em] text-white/70">A better way to make the plan</p>
          <p className="mt-4 max-w-[9ch] font-editorial text-[clamp(4rem,7vw,6.5rem)] font-semibold uppercase leading-[.82] tracking-[-.045em]">
            Less chat.<br />More trip.
          </p>
        </div>
      </aside>

      <section className="flex items-center justify-center px-6 py-12 sm:px-10">
        <div className="w-full max-w-md">
        <Link
          href="/"
          className="mb-10 block w-fit rounded-sm font-display text-display-md text-fg"
        >
          Junto
        </Link>

        <div>
          <h1 className="font-editorial text-[clamp(2.75rem,12vw,3.5rem)] font-semibold uppercase leading-[.94] tracking-[-.035em] text-fg">{title}</h1>
          {blurb && <p className="mt-3 max-w-sm text-ui-md leading-relaxed text-fg-muted">{blurb}</p>}
          <div className="mt-8">{children}</div>
        </div>

        {footer && <div className="mt-5 text-center text-ui-sm text-fg-muted">{footer}</div>}
        </div>
      </section>
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
        className="h-12 w-full rounded-md border border-line bg-surface-sunken px-3.5 text-ui-md text-fg placeholder:text-fg-subtle transition-colors focus:border-accent focus:bg-surface focus:outline-none"
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
