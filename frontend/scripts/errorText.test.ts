import test from "node:test";
import assert from "node:assert/strict";
import { errorToString } from "../src/lib/errorText.ts";

test("errorToString returns Error.message", () => {
  assert.equal(errorToString(new Error("backend unavailable")), "backend unavailable");
});

test("errorToString falls back to String for non-Error throws", () => {
  assert.equal(errorToString({ code: "E_FAIL" }), "[object Object]");
});
