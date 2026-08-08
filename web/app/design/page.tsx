import type { CSSProperties } from "react";

// Living specimen for the design tokens. A dev artifact, not product surface —
// it exists so a visual direction can be reviewed as pixels rather than as a
// list of hex codes, and so drift in the token layer is visible in one place.
//
// The "photography" here is CSS gradients rather than real images on purpose:
// the specimen has to render deterministically for a screenshot, and the one
// case that actually needs proving — the scrim rule holding over a BRIGHT
// image — is easier to construct than to find.

export const metadata = { title: "Junto — design tokens" };

const MEDIA_DUSK =
  "linear-gradient(155deg, #2b1a3d 0%, #7c3f2f 45%, #d98324 78%, #f2c14e 100%)";
const MEDIA_SEA =
  "linear-gradient(155deg, #0b2b3a 0%, #14596b 48%, #2f9ba6 82%, #7fd4cd 100%)";
const MEDIA_BRIGHT =
  "linear-gradient(155deg, #fdfcf8 0%, #f7f1e0 40%, #fbe9c8 70%, #ffffff 100%)";

function Section({
  n,
  title,
  blurb,
  children,
}: {
  n: string;
  title: string;
  blurb: string;
  children: React.ReactNode;
}) {
  return (
    <section className="border-t border-line-subtle pt-10">
      <div className="mb-6 flex items-baseline gap-3">
        <span className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
          {n}
        </span>
        <h2 className="font-display text-display-md text-fg">{title}</h2>
      </div>
      <p className="mb-8 max-w-2xl text-ui-md text-fg-muted">{blurb}</p>
      {children}
    </section>
  );
}

function Swatch({
  name,
  value,
  note,
  dark,
}: {
  name: string;
  value: string;
  note?: string;
  dark?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div
        className="h-14 rounded-sm border border-line-subtle"
        style={{ background: `var(${value})` }}
      />
      <p className={`mt-1.5 truncate text-ui-2xs ${dark ? "text-fg" : "text-fg"}`}>{name}</p>
      {note && <p className="truncate text-ui-2xs text-fg-subtle">{note}</p>}
    </div>
  );
}

export default function DesignTokensPage() {
  return (
    <main className="mx-auto w-full max-w-6xl px-8 py-14">
      <header className="mb-14">
        <p className="text-ui-2xs font-medium uppercase tracking-[0.14em] text-accent-text">
          Junto design system
        </p>
        <h1 className="mt-3 font-display text-display-2xl text-fg">
          Two languages, one product
        </h1>
        <p className="mt-4 max-w-2xl text-ui-lg text-fg-muted">
          A cinematic outer shell for choosing and savouring trips, a dense functional inner app
          for planning them. Threaded together by one serif, one accent, and one card form.
        </p>
      </header>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="01"
        title="The two languages, side by side"
        blurb="Left: outer shell — full-bleed media, serif display, generous air. Right: inner app — sans body, compact rows, real data. The serif header and the amber accent appear in both; nothing else crosses over."
      >
        <div className="grid gap-6 lg:grid-cols-2">
          {/* OUTER */}
          <div>
            <p className="mb-3 text-ui-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
              Outer shell
            </p>
            <div
              data-on-media
              className="relative h-80 overflow-hidden rounded-card shadow-lg"
              style={{ background: MEDIA_DUSK }}
            >
              <div className="absolute inset-0" style={{ background: "var(--scrim-media)" }} />
              <div className="absolute inset-x-0 bottom-0 p-7">
                <span className="inline-block rounded-xs bg-accent-on-dark/20 px-2 py-0.5 text-ui-2xs font-medium uppercase tracking-[0.12em] text-accent-on-dark">
                  6 days · 4 travellers
                </span>
                <h3 className="mt-3 font-display text-display-xl text-fg-on-media">
                  Lisbon in autumn
                </h3>
                <p className="mt-2 max-w-sm text-ui-lg text-fg-on-media-dim">
                  Tiled streets, long lunches, and one very contested dinner reservation.
                </p>
              </div>
            </div>
          </div>

          {/* INNER */}
          <div>
            <p className="mb-3 text-ui-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
              Inner app
            </p>
            <div className="h-80 overflow-hidden rounded-card border border-line-subtle bg-surface-raised shadow-sm">
              <div className="flex items-center justify-between border-b border-line-subtle px-4 py-3">
                <h3 className="font-display text-display-sm text-fg">Where are we staying</h3>
                <div className="flex -space-x-1.5">
                  {["A", "B", "M"].map((i, idx) => (
                    <span
                      key={i}
                      className="flex h-6 w-6 items-center justify-center rounded-full text-ui-2xs font-semibold text-fg-inverse ring-2 ring-live"
                      style={{ background: ["#7c3f2f", "#14596b", "#5c554d"][idx] }}
                    >
                      {i}
                    </span>
                  ))}
                </div>
              </div>
              <div className="space-y-2 p-3">
                {/* selected */}
                <div className="rounded-card border border-accent-300 bg-accent-tint p-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <p className="truncate text-ui-md font-medium text-fg">Alfama guesthouse</p>
                        <span className="shrink-0 rounded-xs bg-accent px-1.5 py-0.5 text-ui-2xs font-semibold text-fg-inverse">
                          ✓ Chosen
                        </span>
                      </div>
                      <p className="mt-0.5 text-ui-xs text-fg-subtle">proposed by Mira</p>
                    </div>
                    <span data-numeric className="shrink-0 text-ui-sm text-fg-muted">
                      €148
                    </span>
                  </div>
                </div>
                {/* not selected, more votes */}
                <div className="rounded-card border border-line-subtle bg-surface-raised p-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate text-ui-md font-medium text-fg">Baixa apartment</p>
                      <p className="mt-0.5 text-ui-xs text-fg-subtle">proposed by Alex</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      <span
                        data-numeric
                        className="rounded-full bg-surface-sunken px-2 py-0.5 text-ui-xs font-semibold text-fg-muted"
                      >
                        3 votes
                      </span>
                      <span data-numeric className="text-ui-sm text-fg-muted">
                        €132
                      </span>
                    </div>
                  </div>
                </div>
                <div className="rounded-card border border-line-subtle bg-surface-raised p-3 opacity-70">
                  <p className="truncate text-ui-md font-medium text-fg">Chiado studio</p>
                  <p className="mt-0.5 text-ui-xs text-fg-subtle">proposed by Sam</p>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="mt-5 rounded-md border border-accent-200 bg-accent-tint px-4 py-3">
          <p className="text-ui-sm text-fg">
            <strong className="font-semibold">Note the D41 distinction, made visual:</strong>{" "}
            &ldquo;Chosen&rdquo; is a filled accent badge plus a tinted card; &ldquo;3
            votes&rdquo; is a neutral grey counter. The option with the most votes is
            deliberately <em>not</em> the highlighted one here — that is the whole point, and
            the design has to make it legible at a glance rather than require reading numbers.
          </p>
        </div>
      </Section>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="02"
        title="Colour"
        blurb="A warm ink ramp for everything structural, one amber accent that means 'chosen / active / live' in both contexts. The accent changes step with the ground so a single hue survives WCAG on dark photography and on light UI alike."
      >
        <p className="mb-3 text-ui-sm font-medium text-fg-muted">Ink — warm neutral</p>
        <div className="mb-8 grid grid-cols-6 gap-2 lg:grid-cols-11">
          {[
            ["50", "paper"],
            ["100", ""],
            ["200", "hairline"],
            ["300", "border"],
            ["400", "2.6:1 ✗ text"],
            ["500", "4.6:1 tertiary"],
            ["600", "7.2:1 secondary"],
            ["700", ""],
            ["800", ""],
            ["900", "16.5:1 primary"],
            ["950", "scrim base"],
          ].map(([step, note]) => (
            <Swatch
              key={step}
              name={`ink-${step}`}
              value={`--color-ink-${step}`}
              note={note || undefined}
            />
          ))}
        </div>

        <p className="mb-3 text-ui-sm font-medium text-fg-muted">Accent — amber</p>
        <div className="mb-8 grid grid-cols-5 gap-2 lg:grid-cols-10">
          {[
            ["50", "selected tint"],
            ["100", ""],
            ["200", ""],
            ["300", ""],
            ["400", "on dark 9:1"],
            ["500", "brand / live"],
            ["600", "fill 5.0:1"],
            ["700", "text on light"],
            ["800", ""],
            ["900", ""],
          ].map(([step, note]) => (
            <Swatch
              key={step}
              name={`accent-${step}`}
              value={`--color-accent-${step}`}
              note={note || undefined}
            />
          ))}
        </div>

        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div className="rounded-md border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Accent on light
            </p>
            <button className="rounded-md bg-accent px-3.5 py-2 text-ui-md font-medium text-fg-inverse">
              Create trip
            </button>
            <p className="mt-3 text-ui-sm text-accent-text">Accent text uses step 700</p>
          </div>
          <div
            data-on-media
            className="rounded-md p-4"
            style={{ background: "var(--color-ink-950)" }}
          >
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-on-media-dim">
              Accent on dark
            </p>
            <span className="inline-block rounded-xs border border-accent-on-dark/40 px-2.5 py-1 text-ui-sm text-accent-on-dark">
              Live · 3 planning
            </span>
          </div>
          <div className="rounded-md border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Balanced
            </p>
            <p data-numeric className="text-ui-lg font-semibold text-positive-600">
              €480.00 ✓
            </p>
            <p className="mt-1 text-ui-xs text-fg-subtle">splits sum to total</p>
          </div>
          <div className="rounded-md border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Conflict
            </p>
            <p data-numeric className="text-ui-lg font-semibold text-critical-600">
              €12.00 off
            </p>
            <p className="mt-1 text-ui-xs text-fg-subtle">version conflict / unbalanced</p>
          </div>
        </div>
      </Section>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="03"
        title="Type"
        blurb="Fraunces for every header at every size — that is thread #1. Inter for all UI text and, critically, for tabular figures on the budget screens. The display scale ends at 18px, which is exactly where it hands off to the dense app."
      >
        <div className="grid gap-10 lg:grid-cols-2">
          <div className="space-y-5">
            <p className="text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Display — Fraunces
            </p>
            {[
              ["display-3xl", "72 / hero", "Lisbon"],
              ["display-2xl", "56 / landing", "Choose your trip"],
              ["display-xl", "44 / card title", "Lisbon in autumn"],
              ["display-lg", "32 / page title", "Day 3 — Alfama"],
              ["display-md", "24 / subsection", "Budget"],
              ["display-sm", "18 / IN-APP header", "Where are we staying"],
            ].map(([cls, meta, sample]) => (
              <div key={cls} className="border-b border-line-subtle pb-4">
                <p className="mb-1 text-ui-2xs text-fg-subtle">
                  {cls} · {meta}
                </p>
                {/* Inline vars, not `text-${cls}`: Tailwind extracts class names
                    statically, so an interpolated utility never gets generated. */}
                <p
                  className="font-display text-fg"
                  style={{
                    fontSize: `var(--text-${cls})`,
                    lineHeight: `var(--text-${cls}--line-height)`,
                    letterSpacing: `var(--text-${cls}--letter-spacing)`,
                    fontWeight: `var(--text-${cls}--font-weight)` as CSSProperties["fontWeight"],
                  }}
                >
                  {sample}
                </p>
              </div>
            ))}
          </div>

          <div className="space-y-5">
            <p className="text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              UI — Inter
            </p>
            {[
              ["ui-2xl", "20"],
              ["ui-xl", "18"],
              ["ui-lg", "16 / inputs, outer prose"],
              ["ui-md", "14 / APP DEFAULT"],
              ["ui-sm", "13 / dense rows"],
              ["ui-xs", "12 / meta"],
              ["ui-2xs", "11 / timestamps"],
            ].map(([cls, meta]) => (
              <div key={cls} className="border-b border-line-subtle pb-4">
                <p className="mb-1 text-ui-2xs text-fg-subtle">
                  {cls} · {meta}
                </p>
                <p
                  className="text-fg"
                  style={{
                    fontSize: `var(--text-${cls})`,
                    lineHeight: `var(--text-${cls}--line-height)`,
                  }}
                >
                  Three members voted on this option
                </p>
              </div>
            ))}
            <div className="rounded-md bg-surface-sunken p-4">
              <p className="mb-2 text-ui-2xs text-fg-subtle">
                Tabular figures — money must not jitter as it updates live
              </p>
              <div className="space-y-1">
                {["1,248.00", "94.50", "1,342.50"].map((n, i) => (
                  <p
                    key={n}
                    data-numeric
                    className={`text-ui-md ${i === 2 ? "border-t border-line pt-1 font-semibold text-fg" : "text-fg-muted"}`}
                  >
                    € {n}
                  </p>
                ))}
              </div>
            </div>
          </div>
        </div>
      </Section>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="04"
        title="Form — one card language"
        blurb="Thread #3. Every card type reads the same --radius-card token, so trip cards, option cards and memory cards cannot drift apart. Elevation differs between contexts; vocabulary does not."
      >
        <div className="mb-8 grid gap-4 sm:grid-cols-3">
          {[
            ["Trip card", "outer · shadow-lg", MEDIA_SEA],
            ["Memory card", "outer · shadow-lg", MEDIA_DUSK],
          ].map(([label, meta, bg]) => (
            <div key={label}>
              <div
                data-on-media
                className="relative h-44 overflow-hidden rounded-card shadow-lg"
                style={{ background: bg }}
              >
                <div className="absolute inset-0" style={{ background: "var(--scrim-media)" }} />
                <p className="absolute bottom-4 left-4 font-display text-display-md text-fg-on-media">
                  {label}
                </p>
              </div>
              <p className="mt-2 text-ui-2xs text-fg-subtle">{meta}</p>
            </div>
          ))}
          <div>
            <div className="flex h-44 flex-col justify-between rounded-card border border-line-subtle bg-surface-raised p-4 shadow-sm">
              <div>
                <p className="text-ui-md font-medium text-fg">Option card</p>
                <p className="mt-1 text-ui-xs text-fg-subtle">proposed by Mira</p>
              </div>
              <span
                data-numeric
                className="self-start rounded-full bg-surface-sunken px-2 py-0.5 text-ui-xs font-semibold text-fg-muted"
              >
                2 votes
              </span>
            </div>
            <p className="mt-2 text-ui-2xs text-fg-subtle">inner · shadow-sm · same radius</p>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
          {["xs", "sm", "md", "lg", "xl"].map((r) => (
            <div key={r} className="text-center">
              <div
                className="mx-auto h-16 w-full border border-line bg-surface-sunken"
                style={{ borderRadius: `var(--radius-${r})` }}
              />
              <p className="mt-1.5 text-ui-2xs text-fg-subtle">radius-{r}</p>
            </div>
          ))}
        </div>
        <div className="mt-6 grid grid-cols-2 gap-4 lg:grid-cols-5">
          {["xs", "sm", "md", "lg", "xl"].map((s) => (
            <div key={s} className="text-center">
              <div
                className="mx-auto h-16 w-full rounded-card bg-surface-raised"
                style={{ boxShadow: `var(--shadow-${s})` } as CSSProperties}
              />
              <p className="mt-1.5 text-ui-2xs text-fg-subtle">shadow-{s}</p>
            </div>
          ))}
        </div>
      </Section>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="05"
        title="The scrim rule"
        blurb="The failure mode of photography-forward design is white text over an image that happens to be bright — and user-supplied trip photos are exactly the case nobody gets to art-direct. The rule: text over media ALWAYS sits on a scrim, never directly on the photo."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <div
              className="relative h-52 overflow-hidden rounded-card"
              style={{ background: MEDIA_BRIGHT }}
            >
              <p className="absolute bottom-5 left-5 font-display text-display-lg text-white">
                Santorini
              </p>
            </div>
            <p className="mt-2 text-ui-sm font-medium text-critical-600">
              ✗ No scrim — unreadable over a bright photo
            </p>
          </div>
          <div>
            <div
              data-on-media
              className="relative h-52 overflow-hidden rounded-card"
              style={{ background: MEDIA_BRIGHT }}
            >
              <div className="absolute inset-0" style={{ background: "var(--scrim-media)" }} />
              <p className="absolute bottom-5 left-5 font-display text-display-lg text-fg-on-media">
                Santorini
              </p>
            </div>
            <p className="mt-2 text-ui-sm font-medium text-positive-600">
              ✓ --scrim-media — same photo, 4.5:1 guaranteed
            </p>
          </div>
        </div>
      </Section>

      {/* ------------------------------------------------------------------ */}
      <Section
        n="06"
        title="Real-time feedback"
        blurb="This project's entire claim is a real-time sync engine. State that mutates silently gives a user no evidence any of that exists. Every change arriving from another member over the WebSocket gets a brief accent tint that fades — noticeable peripherally, never stealing focus, and safe to fire several times at once."
      >
        <div className="grid gap-4 lg:grid-cols-3">
          <div className="rounded-card border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Arriving edit
            </p>
            <div
              className="rounded-md border border-accent-300 p-3"
              style={{ background: "var(--color-live-tint)" }}
            >
              <p className="text-ui-md text-fg">Baixa apartment</p>
              <p className="mt-0.5 text-ui-xs text-accent-text">Alex just voted</p>
            </div>
            <p className="mt-2 text-ui-2xs text-fg-subtle">
              animate-live-flash · 1.2s tint → transparent
            </p>
          </div>
          <div className="rounded-card border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Presence
            </p>
            <div className="flex -space-x-2">
              {["A", "M", "S", "K"].map((i, idx) => (
                <span
                  key={i}
                  className="flex h-9 w-9 items-center justify-center rounded-full text-ui-xs font-semibold text-fg-inverse ring-2 ring-live"
                  style={{ background: ["#7c3f2f", "#14596b", "#5c554d", "#8f4a0b"][idx] }}
                >
                  {i}
                </span>
              ))}
            </div>
            <p className="mt-3 text-ui-2xs text-fg-subtle">
              live ring = connected now, not merely a member
            </p>
          </div>
          <div className="rounded-card border border-line-subtle bg-surface-raised p-4">
            <p className="mb-3 text-ui-xs font-medium uppercase tracking-wider text-fg-subtle">
              Focus ring (keyboard)
            </p>
            <div className="space-y-2">
              <button
                className="w-full rounded-md bg-accent px-3 py-2 text-ui-md font-medium text-fg-inverse"
                style={{ outline: "2px solid var(--color-accent-600)", outlineOffset: "2px" }}
              >
                Vote
              </button>
              <p className="text-ui-2xs text-fg-subtle">
                one ring, :focus-visible only, switches to accent-400 over media
              </p>
            </div>
          </div>
        </div>
      </Section>
    </main>
  );
}
