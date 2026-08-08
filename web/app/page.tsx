"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/context/AuthContext";
import { ButtonLink } from "@/components/ui/Button";
import { Media } from "@/components/ui/Media";

export default function Home() {
  const { status } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (status === "authenticated") router.replace("/trips");
  }, [status, router]);

  // A signed-in visitor is mid-redirect; rendering the marketing hero first
  // would flash it for a frame. A signed-out visitor sees the hero immediately
  // because AuthContext skips session restore on public routes.
  if (status !== "anonymous") {
    return <div className="flex-1" style={{ background: "var(--color-ink-950)" }} />;
  }

  return (
    <main className="flex flex-1 flex-col">
      {/* Explicitly art-directed rather than hashed: a fixed marketing surface
          should not inherit whatever cover a string happens to hash to. */}
      <Media seed="dusk" scrim="hero" className="flex flex-1 flex-col">
        <div className="relative flex flex-1 flex-col px-6 py-8 sm:px-10 sm:py-10">
          <header className="flex items-center justify-between">
            <span className="font-display text-display-sm text-fg-on-media">Junto</span>
            <div className="flex items-center gap-2">
              <ButtonLink href="/login" variant="onMedia" size="sm">
                Log in
              </ButtonLink>
              <ButtonLink href="/signup" variant="onMedia" size="sm">
                Sign up
              </ButtonLink>
            </div>
          </header>

          <div className="flex flex-1 items-center">
            <div className="max-w-3xl py-16">
              <p className="text-ui-2xs font-medium uppercase tracking-[0.16em] text-accent-on-dark">
                Collaborative trip planning
              </p>
              <h1 className="mt-5 font-display text-display-2xl text-fg-on-media sm:text-display-3xl">
                Plan it together,
                <br />
                properly.
              </h1>
              <p className="mt-6 max-w-xl text-ui-lg text-fg-on-media-dim">
                Propose places, vote on them, split the costs and argue about dinner — all in one
                itinerary that updates for everyone the moment anyone changes it.
              </p>
              <div className="mt-9 flex flex-wrap items-center gap-3">
                <ButtonLink href="/signup" variant="primary" size="lg">
                  Start a trip
                </ButtonLink>
                <ButtonLink href="/login" variant="onMedia" size="lg">
                  I already have an account
                </ButtonLink>
              </div>
            </div>
          </div>

          <footer className="flex flex-wrap gap-x-8 gap-y-2 border-t border-white/15 pt-6 text-ui-sm text-fg-on-media-dim">
            <span>Live presence</span>
            <span>Conflict-free concurrent editing</span>
            <span>Shared budgets that always balance</span>
          </footer>
        </div>
      </Media>
    </main>
  );
}
