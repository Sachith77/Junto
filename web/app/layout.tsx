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
      <body className="min-h-full flex flex-col bg-surface text-fg">
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  );
}
