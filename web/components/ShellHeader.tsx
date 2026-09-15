"use client";

import Link from "next/link";
import { useAuth } from "@/context/AuthContext";

/** Header for authenticated OUTER-SHELL pages (trips list, create, mode select).
 *  Sits on paper rather than on media, so it stays legible above whatever
 *  cover art the page below happens to render. */
export function ShellHeader({ children }: { children?: React.ReactNode }) {
  const { user, logout } = useAuth();

  return (
    <header className="sticky top-0 z-30 border-b border-line-subtle bg-surface/80 backdrop-blur-xl">
      <div className="mx-auto flex h-[4.5rem] w-full max-w-[90rem] items-center justify-between px-5 sm:px-8 lg:px-10">
        <Link href="/trips" className="rounded-sm font-display text-display-sm text-fg transition-colors hover:text-white">
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
                className="rounded-full border border-line px-3 py-1.5 text-ui-sm text-fg-muted transition-colors hover:border-line-strong hover:text-fg"
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
