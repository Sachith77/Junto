import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    // Next's default port, which is also the API's default CORS origin
    // (configs/config.go) — so `npm run dev` and `go run ./cmd/api` agree with
    // no extra configuration on either side.
    //
    // Overridable because port 3000 is popular: any other dev server already holding it sends
    // `next dev` to 3001, and the failure that produces is genuinely confusing — the suite
    // drives whatever unrelated site answers on 3000 and reports "Email field not found".
    // When overriding, the API needs CORS_ALLOWED_ORIGINS and WEB_BASE_URL moved to match,
    // or logins are blocked by CORS and invite links point at the wrong origin.
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:3000",
    trace: "retain-on-failure",
  },
  // The Next dev server and the Go API are started separately (see README) — both must
  // already be reachable, matching the same "real stack" bar the backend's own tests hold to.
});
