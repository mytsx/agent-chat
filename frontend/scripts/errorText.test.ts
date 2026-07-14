import test from "node:test";
import assert from "node:assert/strict";
import { errorToString } from "../src/lib/errorText.ts";

test("errorToString returns Error.message", () => {
  assert.equal(errorToString(new Error("backend unavailable")), "backend unavailable");
});

test("errorToString extracts message-like fields from backend objects", () => {
  assert.equal(errorToString({ message: "pty unavailable" }), "pty unavailable");
  assert.equal(errorToString({ error: "permission denied" }), "permission denied");
  assert.equal(errorToString({ detail: "room not found" }), "room not found");
});

test("errorToString falls back to String for opaque non-Error throws", () => {
  assert.equal(errorToString({ code: "E_FAIL" }), "[object Object]");
});
