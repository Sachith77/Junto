import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Not automatic here: RTL's built-in auto-cleanup only self-registers when it detects a
// global `afterEach`, which requires `test.globals: true`. This project deliberately imports
// vitest's API explicitly instead (no globals), so without this the DOM from one test leaks
// into the next — which is exactly what happened before this was added (a later test's
// `getByTestId` matched a still-mounted card from an earlier test).
afterEach(() => {
  cleanup();
});
