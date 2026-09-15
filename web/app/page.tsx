"use client";

import Image from "next/image";
import Link from "next/link";
import { useAuth } from "@/context/AuthContext";
import { LandingStory } from "@/components/landing/LandingStory";

const PRINCIPLES = [
  {
    number: "01",
    title: "One plan",
    copy: "Every day, idea and booking lives in the same shared itinerary.",
  },
  {
    number: "02",
    title: "Every voice",
    copy: "Compare options and vote without losing the conversation around them.",
  },
  {
    number: "03",
    title: "Always current",
    copy: "Edits, decisions and costs reach the whole group as they happen.",
  },
];

export default function Home() {
  const { status } = useAuth();
  const authenticated = status === "authenticated";

  return (
    <main className="min-h-screen bg-[#303a40] p-0 sm:p-5 lg:p-7">
      <div className="mx-auto max-w-[100rem] overflow-hidden bg-[#d8e7ef] sm:rounded-[1.4rem] lg:grid lg:h-[calc(100svh-3.5rem)] lg:grid-rows-[minmax(0,1fr)_auto]">
        <section className="relative min-h-[44rem] overflow-hidden lg:min-h-0">
          <Image
            src="/junto-valley.png"
            alt="A river winding through forested mountains at sunrise"
            fill
            priority
            quality={92}
            sizes="100vw"
            className="object-cover"
          />
          <div className="absolute inset-0 bg-[linear-gradient(180deg,rgba(7,13,14,.5)_0%,rgba(7,13,14,.08)_35%,rgba(7,13,14,.08)_58%,rgba(7,13,14,.68)_100%)]" />

          <header className="absolute inset-x-0 top-0 z-20 grid grid-cols-[minmax(0,1fr)_auto] items-start px-5 py-5 text-white sm:px-8 sm:py-7 md:grid-cols-[minmax(0,1fr)_minmax(12rem,18rem)_minmax(0,1fr)] lg:px-10">
            <Link href="/" className="flex min-w-0 items-start gap-1.5 justify-self-start" aria-label="Junto home">
              <span className="font-display text-[1.45rem] font-semibold leading-none tracking-[-0.03em]">Junto</span>
              <span className="mt-0.5 text-[.55rem] font-semibold uppercase tracking-[.14em] text-white/65">Together</span>
            </Link>

            <p className="hidden max-w-[14rem] justify-self-center text-ui-xs leading-relaxed text-white/75 md:block">
              A shared workspace for trips shaped by more than one person.
            </p>

            <nav className="flex shrink-0 items-center gap-1.5 justify-self-end whitespace-nowrap text-ui-sm" aria-label="Account">
              {authenticated ? (
                <Link href="/trips" className="rounded-full border border-white/45 bg-white/10 px-5 py-2 text-white backdrop-blur-sm transition-colors hover:bg-white hover:text-[#101416]">
                  My trips
                </Link>
              ) : status === "anonymous" ? (
                <Link href="/login" className="rounded-full border border-white/45 bg-white/10 px-5 py-2 text-white backdrop-blur-sm transition-colors hover:bg-white hover:text-[#101416]">
                  Log in
                </Link>
              ) : null}
            </nav>
          </header>

          <div className="absolute inset-x-0 top-[8.5rem] z-10 px-4 sm:top-[9.5rem] sm:px-8 lg:top-[clamp(6rem,14svh,8.75rem)] lg:px-10">
            <h1 className="max-w-full font-editorial text-[clamp(5rem,13.4vw,13.5rem)] font-semibold uppercase leading-[.76] tracking-[-0.052em] text-white drop-shadow-[0_3px_18px_rgba(0,0,0,.12)] lg:text-[clamp(5rem,min(13.4vw,20svh),13.5rem)]">
              Go far.<br />Stay close.
            </h1>
          </div>

          <div className="absolute bottom-[7rem] left-5 z-10 max-w-[17rem] text-ui-xs leading-relaxed text-white/78 sm:bottom-9 sm:left-8 lg:left-10">
            <p className="font-semibold uppercase tracking-[.14em] text-white">Plan with confidence</p>
            <p className="mt-2">From the first saved idea to the final split, everyone works from the same version of the trip.</p>
          </div>

          <div className="absolute bottom-0 right-0 z-20 w-full sm:w-[24rem] lg:w-[27rem]">
            <div className="hidden px-8 pb-5 text-ui-sm leading-relaxed text-white/85 sm:block">
              Bring the group into the planning—not just the group chat. Build the days, weigh the options and decide together.
            </div>
            <Link
              href={authenticated ? "/trips/new" : "/signup"}
              className="group flex h-[5.5rem] items-center justify-between rounded-tl-[2.25rem] bg-[#0d1213] px-7 text-ui-md font-medium text-white transition-colors hover:bg-black sm:h-[6.25rem] sm:px-9"
            >
              <span>{authenticated ? "Plan another trip" : "Start planning"}</span>
              <span aria-hidden className="text-xl transition-transform group-hover:translate-x-1">→</span>
            </Link>
          </div>
        </section>

        <section id="why-junto" className="grid bg-[#d8e7ef] px-6 py-8 text-[#111619] sm:grid-cols-3 sm:px-8 sm:py-10 lg:px-16 lg:py-8">
          {PRINCIPLES.map((item, index) => (
            <article
              key={item.number}
              className={`grid grid-cols-[2.25rem_1fr] gap-3 py-5 sm:block sm:px-8 sm:py-0 ${index > 0 ? "border-t border-[#111619]/20 sm:border-l sm:border-t-0" : ""}`}
            >
              <span className="pt-1 text-ui-2xs font-semibold tracking-[.14em] text-[#111619]/45 sm:block sm:pt-0">{item.number}</span>
              <div>
                <h2 className="font-editorial text-[2.15rem] font-semibold uppercase leading-none tracking-[-0.035em] sm:min-h-[4.3rem] lg:min-h-0">{item.title}</h2>
                <p className="mt-2 max-w-[17rem] text-ui-xs leading-relaxed text-[#111619]/70 sm:mt-1 lg:mt-2">{item.copy}</p>
              </div>
            </article>
          ))}
        </section>
      </div>
      <LandingStory authenticated={authenticated} />
    </main>
  );
}
