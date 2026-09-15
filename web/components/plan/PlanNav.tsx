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
    { href: `${base}/members`, label: "People" },
  ];

  return (
    <nav className="fixed inset-x-4 bottom-4 z-40 flex items-center justify-around rounded-xl border border-line bg-surface-raised/95 p-1.5 shadow-xl backdrop-blur-xl sm:static sm:inset-auto sm:justify-start sm:rounded-none sm:border-0 sm:border-b sm:border-line-subtle sm:bg-transparent sm:p-0 sm:shadow-none" aria-label="Plan sections">
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
            className={`flex min-w-20 flex-col items-center gap-0.5 rounded-lg px-4 py-2 text-ui-xs font-medium transition-colors sm:-mb-px sm:min-w-0 sm:flex-row sm:gap-2 sm:rounded-none sm:border-b-2 sm:px-4 sm:py-3 sm:text-ui-md ${
              active ? "bg-accent-tint text-accent-text sm:border-accent sm:bg-transparent sm:text-fg" : "text-fg-muted hover:bg-surface-sunken hover:text-fg sm:border-transparent sm:hover:bg-transparent"
            }`}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}
