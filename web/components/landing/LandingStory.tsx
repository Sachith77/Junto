"use client";

import Image, { type StaticImageData } from "next/image";
import Link from "next/link";
import {
  type CSSProperties,
  type PropsWithChildren,
  useEffect,
  useRef,
  useState,
} from "react";
import itineraryShot from "@/shots/B1-itinerary.png";
import votingShot from "@/shots/B2-slot-detail.png";
import budgetShot from "@/shots/B4-budget-split.png";
import memoriesShot from "@/shots/C1-memories.png";

export function LandingStory({ authenticated }: { authenticated: boolean }) {
  const finalHref = authenticated ? "/trips" : "/signup";
  const finalLabel = authenticated ? "Go to my trips" : "Start planning";

  return (
    <div className="mx-auto mt-0 max-w-[100rem] overflow-hidden bg-[#0d1213] sm:mt-5 sm:rounded-[1.4rem] lg:mt-7">
      <section
        aria-labelledby="product-story-title"
        className="border-b border-white/10 px-6 py-20 sm:px-10 sm:py-24 lg:px-16 lg:py-28"
      >
        <Reveal className="mx-auto max-w-4xl text-center">
          <p className="text-ui-2xs font-semibold uppercase tracking-[.18em] text-accent-text">
            The whole trip, together
          </p>
          <h2
            id="product-story-title"
            className="mt-5 font-editorial text-[clamp(3.6rem,8vw,7.5rem)] font-semibold uppercase leading-[.84] tracking-[-.045em] text-white"
          >
            Three modes.<br />One shared story.
          </h2>
          <p className="mx-auto mt-6 max-w-2xl text-ui-lg leading-relaxed text-white/65">
            Junto stays useful before, during and after the trip—so the plan never disappears
            into a dozen different group chats.
          </p>
        </Reveal>
      </section>

      <article className="grid border-b border-white/10 lg:min-h-[46rem] lg:grid-cols-[.78fr_1.22fr]">
        <FeatureCopy
          number="01"
          label="Planning"
          title="Turn ideas into one plan."
          description="Build the shared itinerary, propose and vote on options, then split the budget without losing the conversation."
          points={["Shared itinerary", "Propose + vote", "Budget splits"]}
        />
        <Reveal className="relative flex items-center bg-[#182025] p-5 sm:p-8 lg:p-12" delay={100}>
          <div className="relative w-full pb-0 lg:pb-28 lg:pr-14">
            <ProductShot
              src={itineraryShot}
              alt="Junto itinerary showing shared days, time slots and chosen options"
              label="Shared itinerary"
              parallax
              priority
            />
            <div className="mt-4 lg:absolute lg:bottom-0 lg:right-0 lg:w-[62%]">
              <ProductShot
                src={budgetShot}
                alt="Junto budget screen showing balanced expense splits"
                label="Budget split"
                compact
              />
            </div>
          </div>
        </Reveal>
      </article>

      <article className="grid border-b border-white/10 lg:min-h-[43rem] lg:grid-cols-[1.15fr_.85fr]">
        <Reveal className="relative flex items-center bg-[#d8e7ef] p-5 sm:p-8 lg:order-1 lg:p-12" delay={100}>
          <div className="relative w-full">
            <div className="absolute -left-2 -top-3 z-10 flex items-center gap-2 rounded-full border border-[#111619]/10 bg-white/90 px-3 py-1.5 text-ui-xs font-semibold text-[#111619] shadow-md backdrop-blur-sm sm:left-4 sm:top-4">
              <span className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[#678a9c] opacity-40 motion-reduce:animate-none" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-[#527382]" />
              </span>
              Updates live
            </div>
            <ProductShot
              src={votingShot}
              alt="Junto option voting screen with live vote counts and group discussion"
              label="Live decisions"
              parallax
            />
          </div>
        </Reveal>
        <FeatureCopy
          number="02"
          label="Live / tracking"
          title="Keep the group in step."
          description="See who is present, watch decisions update in real time, and track the places still left to cover while the trip is happening."
          points={["Live presence", "Instant updates", "Places left"]}
          className="lg:order-2"
        />
      </article>

      <article className="grid border-b border-white/10 lg:min-h-[43rem] lg:grid-cols-[.78fr_1.22fr]">
        <FeatureCopy
          number="03"
          label="Memories"
          title="Come home with the whole story."
          description="Revisit everything the group settled on in a post-trip gallery, kept in order by day and destination."
          points={["Organized by day", "Chosen places", "Photos + notes"]}
        />
        <Reveal className="relative flex items-center bg-[#20202a] p-5 sm:p-8 lg:p-12" delay={100}>
          <ProductShot
            src={memoriesShot}
            alt="Junto memories gallery organized by trip day and chosen destination"
            label="Memories gallery"
            parallax
          />
        </Reveal>
      </article>

      <section className="relative isolate overflow-hidden bg-[#d8e7ef] px-6 py-20 text-[#111619] sm:px-10 sm:py-24 lg:px-16 lg:py-28">
        <div aria-hidden className="absolute -right-24 -top-28 -z-10 h-80 w-80 rounded-full border border-[#111619]/10" />
        <div aria-hidden className="absolute -bottom-40 right-20 -z-10 h-96 w-96 rounded-full border border-[#111619]/10" />
        <Reveal className="mx-auto flex max-w-5xl flex-col items-start justify-between gap-9 sm:items-center sm:text-center lg:flex-row lg:text-left">
          <div>
            <p className="text-ui-2xs font-semibold uppercase tracking-[.18em] text-[#527382]">
              Make the next one count
            </p>
            <h2 className="mt-4 max-w-[13ch] font-editorial text-[clamp(3.5rem,7vw,6.5rem)] font-semibold uppercase leading-[.86] tracking-[-.045em]">
              Plan your next trip together.
            </h2>
          </div>
          <Link
            href={finalHref}
            className="group flex h-14 shrink-0 items-center gap-10 rounded-full bg-[#0d1213] px-7 text-ui-md font-semibold text-white shadow-lg transition-[transform,background-color] duration-200 hover:-translate-y-0.5 hover:bg-black"
          >
            {finalLabel}
            <span aria-hidden className="text-xl transition-transform group-hover:translate-x-1">→</span>
          </Link>
        </Reveal>
      </section>

      <footer className="flex flex-col gap-3 px-6 py-8 text-ui-xs text-white/50 sm:flex-row sm:items-center sm:justify-between sm:px-10 lg:px-16">
        <span className="font-display text-ui-lg font-semibold text-white">Junto</span>
        <span>Plan it together, properly.</span>
      </footer>
    </div>
  );
}

function FeatureCopy({
  number,
  label,
  title,
  description,
  points,
  className = "",
}: {
  number: string;
  label: string;
  title: string;
  description: string;
  points: string[];
  className?: string;
}) {
  return (
    <div className={`flex items-center px-6 py-16 sm:px-10 sm:py-20 lg:px-14 lg:py-24 ${className}`}>
      <Reveal className="w-full max-w-xl">
        <div className="flex items-center gap-4 text-ui-2xs font-semibold uppercase tracking-[.18em] text-accent-text">
          <span data-numeric>{number}</span>
          <span className="h-px w-9 bg-accent-text/40" />
          <span>{label}</span>
        </div>
        <h2 className="mt-6 font-editorial text-[clamp(3.35rem,6vw,6rem)] font-semibold uppercase leading-[.86] tracking-[-.045em] text-white">
          {title}
        </h2>
        <p className="mt-6 max-w-lg text-ui-lg leading-relaxed text-white/65">{description}</p>
        <ul className="mt-8 flex flex-wrap gap-2" aria-label={`${label} features`}>
          {points.map((point) => (
            <li key={point} className="rounded-full border border-white/15 px-3 py-1.5 text-ui-xs text-white/75">
              {point}
            </li>
          ))}
        </ul>
      </Reveal>
    </div>
  );
}

function ProductShot({
  src,
  alt,
  label,
  compact = false,
  parallax = false,
  priority = false,
}: {
  src: StaticImageData;
  alt: string;
  label: string;
  compact?: boolean;
  parallax?: boolean;
  priority?: boolean;
}) {
  const parallaxRef = useParallax(parallax);

  return (
    <figure
      className="overflow-hidden rounded-[1rem] border border-white/15 bg-[#0d1213] shadow-[0_24px_70px_rgba(0,0,0,.28)]"
      ref={parallaxRef}
      style={{ transform: "translate3d(0,var(--landing-parallax-y,0px),0)" } as CSSProperties}
    >
      <figcaption className="flex h-9 items-center justify-between border-b border-white/10 px-3 text-[.625rem] font-semibold uppercase tracking-[.14em] text-white/50">
        <span className="flex gap-1.5" aria-hidden>
          <span className="h-1.5 w-1.5 rounded-full bg-white/25" />
          <span className="h-1.5 w-1.5 rounded-full bg-white/15" />
          <span className="h-1.5 w-1.5 rounded-full bg-white/10" />
        </span>
        <span>{label}</span>
      </figcaption>
      <div className={compact ? "aspect-[16/8] overflow-hidden" : "aspect-[16/10] overflow-hidden"}>
        <Image
          src={src}
          alt={alt}
          className="h-full w-full object-cover object-top"
          sizes={compact ? "(max-width: 1024px) 100vw, 38vw" : "(max-width: 1024px) 100vw, 58vw"}
          placeholder="blur"
          priority={priority}
        />
      </div>
    </figure>
  );
}

function Reveal({ children, className = "", delay = 0 }: PropsWithChildren<{ className?: string; delay?: number }>) {
  const ref = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { threshold: 0.14, rootMargin: "0px 0px -8%" }
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return (
    <div
      ref={ref}
      className={`${className} transition-[opacity,transform] duration-700 ease-[cubic-bezier(.22,1,.36,1)] motion-reduce:translate-y-0 motion-reduce:opacity-100 motion-reduce:transition-none ${
        visible ? "translate-y-0 opacity-100" : "translate-y-7 opacity-0"
      }`}
      style={{ transitionDelay: visible ? `${delay}ms` : "0ms" }}
    >
      {children}
    </div>
  );
}

function useParallax(enabled: boolean) {
  const ref = useRef<HTMLElement>(null);

  useEffect(() => {
    const element = ref.current;
    if (!enabled || !element || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    let frame = 0;
    const update = () => {
      frame = 0;
      const rect = element.getBoundingClientRect();
      const distance = rect.top + rect.height / 2 - window.innerHeight / 2;
      const offset = Math.max(-20, Math.min(20, distance * -0.025));
      element.style.setProperty("--landing-parallax-y", `${offset.toFixed(1)}px`);
    };
    const onScroll = () => {
      if (!frame) frame = window.requestAnimationFrame(update);
    };

    update();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [enabled]);

  return ref;
}
