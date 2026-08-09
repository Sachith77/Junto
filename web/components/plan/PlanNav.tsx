"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

/** Dense-app tab bar. Sans labels, compact spacing, one amber underline for the active tab —
 *  the accent doing the same "this is the chosen one" job it does everywhere else. */
export function PlanNav({ tripId }: { tripId: string }) {
  const pathname = usePathname();
  const base = `/trips/${tripId}/plan`;

  const tabs = [
    { href: base, label: "Itinerary" },
    { href: `${base}/budget`, label: "Budget" },
    { href: `${base}/members`, label: "Members" },
  ];

  return (
    <nav className="flex items-center gap-1 border-b border-line-subtle" aria-label="Plan sections">
      {tabs.map((tab) => {
        // Exact match for the index tab, prefix match for the rest, so a nested slot page
        // still shows Itinerary as the current section.
        const active =
          tab.href === base
            ? pathname === base || pathname.startsWith(`${base}/slots`)
            : pathname.startsWith(tab.href);
        return (
          <Link
            key={tab.href}
            href={tab.href}
            aria-current={active ? "page" : undefined}
            className={`-mb-px border-b-2 px-3 py-2 text-ui-md font-medium transition-colors ${
              active ? "border-accent text-fg" : "border-transparent text-fg-muted hover:text-fg"
            }`}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}
