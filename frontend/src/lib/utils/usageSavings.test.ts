import { describe, it, expect } from "vitest";
import { savingsState } from "./usageSavings.js";

describe("savingsState", () => {
  it("returns 'saved' for positive values", () => {
    expect(savingsState(0.01)).toBe("saved");
    expect(savingsState(2.7)).toBe("saved");
    expect(savingsState(1_000_000)).toBe("saved");
  });

  it("returns 'costlier' for negative values", () => {
    // Write-heavy workloads: creation premium > read discount.
    expect(savingsState(-0.01)).toBe("costlier");
    expect(savingsState(-0.75)).toBe("costlier");
    expect(savingsState(-42)).toBe("costlier");
  });

  it("returns 'none' for exactly zero", () => {
    // No cache activity, or exact break-even.
    expect(savingsState(0)).toBe("none");
    expect(savingsState(-0)).toBe("none");
  });
});
