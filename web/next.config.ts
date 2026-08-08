import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // The floating dev badge sits bottom-left, exactly where the landing page's
  // footer rule and the trips list's content are — it covered real UI during
  // design review. Route/build info is still available in the terminal.
  devIndicators: false,
};

export default nextConfig;
