export function focusAfterSessionReplace(
  focusedSessionID: string | null,
  oldSessionID: string,
  newSessionID: string
): string | null {
  return focusedSessionID === oldSessionID ? newSessionID : focusedSessionID;
}

export function focusAfterSessionRemove(
  focusedSessionID: string | null,
  removedSessionIDs: Iterable<string>
): string | null {
  if (!focusedSessionID) {
    return focusedSessionID;
  }

  for (const removedSessionID of removedSessionIDs) {
    if (removedSessionID === focusedSessionID) {
      return null;
    }
  }

  return focusedSessionID;
}
