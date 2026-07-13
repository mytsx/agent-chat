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

    // Invoke task() synchronously via an async IIFE: a synchronous throw becomes a
    // rejection (keeping a failed key retryable), and concurrent callers coalesce
    // with the task already started (the "one invocation" contract). Store the raw
    // promise, then RETURN a finally-chained promise so its rejection is handled by
    // callers (a detached .finally would leave an unhandled rejection). The cleanup
    // references the fully-initialized `promise`, not the returned expression, so
    // there is no self-reference in an initializer.
    const promise = (async () => task())();
    inFlight.set(key, promise);

    return promise.finally(() => {
      // Guard against clobbering a newer entry if the key was reused after this
      // one settled (only delete the promise we actually stored).
      if (inFlight.get(key) === promise) {
        inFlight.delete(key);
      }
    });
  };

  return {
    run,
    isInFlight: (key: string): boolean => inFlight.has(key),
  };
}
