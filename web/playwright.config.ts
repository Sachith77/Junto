import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    // Port 3000 is occupied by an unrelated process on this machine — the dev server for this
    // app runs on 3001 (see README): `npm run dev -- -p 3001`.
    baseURL: "http://localhost:3001",
    trace: "retain-on-failure",
  },
  // The Next dev server and the Go API are started separately (see README) — both must
  // already be reachable, matching the same "real stack" bar the backend's own tests hold to.
});
