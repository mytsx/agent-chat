import { SessionInfo } from "./types";

// Two sessions are "same period" when their [start,last] open-windows intersect.
export function overlaps(a: SessionInfo, b: SessionInfo): boolean {
  return a.startUnix < b.lastUnix && b.startUnix < a.lastUnix;
}

// overlapSeconds is the length of the intersection (0 when disjoint).
export function overlapSeconds(a: SessionInfo, b: SessionInfo): number {
  return Math.max(0, Math.min(a.lastUnix, b.lastUnix) - Math.max(a.startUnix, b.startUnix));
}

// bestMatch picks the candidate most overlapping `sel`; ties broken by nearest
// start. Returns null when nothing overlaps (caller leaves that agent unchanged).
export function bestMatch(sel: SessionInfo, candidates: SessionInfo[]): SessionInfo | null {
  let best: SessionInfo | null = null;
  let bestOv = 0;
  for (const c of candidates) {
    const ov = overlapSeconds(sel, c);
    if (ov <= 0) continue;
    const closer = best !== null && Math.abs(c.startUnix - sel.startUnix) < Math.abs(best.startUnix - sel.startUnix);
    if (ov > bestOv || (ov === bestOv && closer)) {
      best = c;
      bestOv = ov;
    }
  }
  return best;
}
