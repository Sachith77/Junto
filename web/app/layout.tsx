import type { Metadata } from "next";
import { Fraunces, Inter } from "next/font/google";
import "./globals.css";
import { AuthProvider } from "@/context/AuthContext";

// Thread #1: one serif for every header in the product, outer shell and dense
// app alike. Fraunces is variable with an optical-size axis, which is what lets
// the same face carry a 72px landing hero and an 18px slot-detail section title
// without the small end looking like shrunken display type.
const fraunces = Fraunces({
  subsets: ["latin"],
  variable: "--font-fraunces",
  display: "swap",
  axes: ["SOFT", "WONK", "opsz"],
});

const inter = Inter({
  subsets: ["latin"],
  variable: "--font-inter",
  display: "swap",
});

export const metadata: Metadata = {
  title: "Junto",
  description: "Collaborative trip planning",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={`${fraunces.variable} ${inter.variable} h-full antialiased`}>
      {/* Browser extensions write their own attributes onto <body> before React hydrates —
          Bitdefender's `bis_register` / `__processed_<uuid>__` are the ones seen here, and
          password managers and ad blockers all do the same thing. React compares the server
          HTML against the mutated DOM and reports a mismatch the app cannot fix, since the
          markup was correct when it was sent.

          suppressHydrationWarning does NOT propagate: it silences attribute and text
          differences on THIS element only, so a real mismatch anywhere inside the tree is
          still reported. That is what makes it the right instrument here rather than a
          blanket mute — the noise came from one element, and only that element is exempted. */}
      <body suppressHydrationWarning className="min-h-full flex flex-col bg-surface text-fg">
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  );
}
