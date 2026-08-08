"use client";

import Link from "next/link";
import { useAuth } from "@/context/AuthContext";

/** Header for authenticated OUTER-SHELL pages (trips list, create, mode select).
 *  Sits on paper rather than on media, so it stays legible above whatever
 *  cover art the page below happens to render. */
export function ShellHeader({ children }: { children?: React.ReactNode }) {
  const { user, logout } = useAuth();

  return (
    <header className="border-b border-line-subtle bg-surface/85 backdrop-blur-sm">
      <div className="mx-auto flex h-16 w-full max-w-6xl items-center justify-between px-6 sm:px-8">
        <Link href="/trips" className="font-display text-display-sm text-fg">
          Junto
        </Link>
        <div className="flex items-center gap-4">
          {children}
          {user && (
            <div className="flex items-center gap-3">
              <span className="hidden text-ui-sm text-fg-muted sm:inline">
                {user.display_name}
              </span>
              <button
                onClick={() => void logout()}
                className="rounded-sm text-ui-sm text-fg-subtle transition-colors hover:text-fg"
              >
                Log out
              </button>
            </div>
          )}
        </div>
      </div>
    </header>
  );
}
