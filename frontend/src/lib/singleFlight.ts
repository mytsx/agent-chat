// Single-flight coalescer: dedupes concurrent async operations by key so a
// rapid double-trigger runs the underlying task exactly once.
//
// Used to guard terminal restart/resume: both replace the same old sessionID,
// and each backend call spawns a fresh PTY. Without coalescing, two overlapping
// calls start two PTYs but the store only tracks the first replacement's new id,
// orphaning the second PTY (a resource leak). All callers racing on one key
// share a single in-flight Promise and therefore a single result.
export function createSingleFlight<T>() {
  const inFlight = new Map<string, Promise<T>>();

  const run = (key: string, task: () => Promise<T>): Promise<T> => {
    const existing = inFlight.get(key);
    if (existing) {
      return existing;
    }

    // Invoke task() synchronously via an async IIFE so a synchronous throw becomes a
    // rejection (a failed key stays retryable) and concurrent callers coalesce with
    // the task already started (the "one invocation" contract). Store AND return the
    // same finally-chained promise so every caller awaits one object — no raw-vs-
    // chained microtask skew, and no unhandled rejection since the stored promise is
    // the one callers handle. `let` lets the cleanup close over `promise`, which is
    // always assigned before the async .finally callback can run.
    let promise: Promise<T>;
    promise = (async () => task())().finally(() => {
      // Guard against clobbering a newer entry if the key was reused after this
      // one settled (only delete the promise we actually stored).
      if (inFlight.get(key) === promise) {
        inFlight.delete(key);
      }
    });
    inFlight.set(key, promise);

    return promise;
  };

  return {
    run,
    isInFlight: (key: string): boolean => inFlight.has(key),
  };
}
