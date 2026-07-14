import test from "node:test";
import assert from "node:assert/strict";
import {
  focusAfterSessionRemove,
  focusAfterSessionReplace,
} from "../src/lib/terminalFocus.ts";

test("focusAfterSessionReplace preserves focused mode across restart/resume replacements", () => {
  assert.equal(focusAfterSessionReplace("old-session", "old-session", "new-session"), "new-session");
});

test("focusAfterSessionReplace leaves unrelated focus untouched", () => {
  assert.equal(focusAfterSessionReplace("other-session", "old-session", "new-session"), "other-session");
  assert.equal(focusAfterSessionReplace(null, "old-session", "new-session"), null);
});

test("focusAfterSessionRemove clears stale focus when closing a focused terminal", () => {
  assert.equal(focusAfterSessionRemove("focused-session", ["focused-session"]), null);
  assert.equal(
    focusAfterSessionRemove("focused-session", ["first", "focused-session", "last"]),
    null
  );
});

test("focusAfterSessionRemove keeps focus when another terminal closes", () => {
  assert.equal(focusAfterSessionRemove("focused-session", ["other-session"]), "focused-session");
  assert.equal(focusAfterSessionRemove(null, ["other-session"]), null);
});
