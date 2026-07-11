export function errorToString(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
