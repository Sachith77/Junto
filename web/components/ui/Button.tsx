import Link from "next/link";
import type { ComponentProps, ReactNode } from "react";

// One button, both languages. The `onMedia` variant exists because a button
// sitting on full-bleed photography cannot use the light-ground accent step
// without failing contrast — it is the same decision the accent ramp encodes
// (accent-600 on light, accent-400 on dark), surfaced as a variant so call
// sites cannot get it wrong by picking a colour.

type Variant = "primary" | "secondary" | "ghost" | "onMedia";
type Size = "sm" | "md" | "lg";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-accent text-fg-inverse hover:bg-accent-hover shadow-xs",
  secondary:
    "border border-line bg-surface-raised text-fg hover:bg-surface-sunken hover:border-line-strong",
  ghost: "text-fg-muted hover:bg-surface-sunken hover:text-fg",
  onMedia:
    "border border-white/25 bg-white/10 text-fg-on-media backdrop-blur-sm hover:bg-white/20 hover:border-white/40",
};

const SIZES: Record<Size, string> = {
  sm: "h-8 px-3 text-ui-sm rounded-sm gap-1.5",
  md: "h-10 px-4 text-ui-md rounded-md gap-2",
  lg: "h-12 px-6 text-ui-lg rounded-md gap-2",
};

const BASE =
  "inline-flex items-center justify-center font-medium transition-colors duration-150 " +
  "disabled:cursor-not-allowed disabled:opacity-50 whitespace-nowrap";

function classes(variant: Variant, size: Size, className?: string) {
  return `${BASE} ${VARIANTS[variant]} ${SIZES[size]} ${className ?? ""}`;
}

export function Button({
  variant = "primary",
  size = "md",
  className,
  ...props
}: { variant?: Variant; size?: Size } & ComponentProps<"button">) {
  return <button className={classes(variant, size, className)} {...props} />;
}

export function ButtonLink({
  variant = "primary",
  size = "md",
  className,
  children,
  ...props
}: { variant?: Variant; size?: Size; children: ReactNode } & ComponentProps<typeof Link>) {
  return (
    <Link className={classes(variant, size, className)} {...props}>
      {children}
    </Link>
  );
}
