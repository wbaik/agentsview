export type SavingsState = "saved" | "costlier" | "none";

// savingsState classifies a cache-savings dollar delta into
// the three states the Cache Efficiency panel renders:
//   - "saved"    : positive — cache reduced total cost
//   - "costlier" : negative — cache creation premium outweighed
//                  any read discount (common in write-only
//                  workloads where cache_creation > input rate)
//   - "none"     : exactly zero — no signal to show
export function savingsState(value: number): SavingsState {
  if (value > 0) return "saved";
  if (value < 0) return "costlier";
  return "none";
}
