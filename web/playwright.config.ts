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
    baseURL: "http://localhost:3000",
    trace: "retain-on-failure",
  },
  // The Next dev server and the Go API are started separately (see README) — both must
  // already be reachable, matching the same "real stack" bar the backend's own tests hold to.
});
