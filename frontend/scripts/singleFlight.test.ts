import test from "node:test";
import assert from "node:assert/strict";
import { createSingleFlight } from "../src/lib/singleFlight.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

test("createSingleFlight coalesces concurrent calls for the same key into one invocation", async () => {
  const sf = createSingleFlight<string>();
  const gate = deferred<string>();
  let calls = 0;
  const task = () => {
    calls++;
    return gate.promise;
  };

  // Two rapid restart/resume calls race on the SAME old sessionID before the
  // first backend replace resolves. The second must NOT spawn a second PTY.
  const first = sf.run("s1", task);
  const second = sf.run("s1", task);

  assert.equal(calls, 1);

  gate.resolve("new-session");
  assert.equal(await first, "new-session");
  assert.equal(await second, "new-session");
});

test("createSingleFlight runs different keys independently", async () => {
  const sf = createSingleFlight<string>();
  let calls = 0;
  const [a, b] = await Promise.all([
    sf.run("s1", () => {
      calls++;
      return Promise.resolve("a");
    }),
    sf.run("s2", () => {
      calls++;
      return Promise.resolve("b");
    }),
  ]);

  assert.equal(calls, 2);
  assert.equal(a, "a");
  assert.equal(b, "b");
});

test("createSingleFlight re-runs the task after the in-flight call settles", async () => {
  const sf = createSingleFlight<string>();
  let calls = 0;
  const task = () => {
    calls++;
    return Promise.resolve("s");
  };

  await sf.run("s1", task);
  await sf.run("s1", task);

  assert.equal(calls, 2);
});

test("createSingleFlight clears the in-flight entry when the task rejects", async () => {
  const sf = createSingleFlight<string>();
  let calls = 0;
  const task = (fail: boolean) => {
    calls++;
    return fail ? Promise.reject(new Error("boom")) : Promise.resolve("ok");
  };

  await assert.rejects(sf.run("s1", () => task(true)), /boom/);
  const result = await sf.run("s1", () => task(false));

  assert.equal(calls, 2);
  assert.equal(result, "ok");
});

test("createSingleFlight rejects all concurrent callers with the same error", async () => {
  const sf = createSingleFlight<string>();
  const gate = deferred<string>();
  let calls = 0;
  const task = () => {
    calls++;
    return gate.promise;
  };

  const first = sf.run("s1", task);
  const second = sf.run("s1", task);

  gate.reject(new Error("backend down"));

  await assert.rejects(first, /backend down/);
  await assert.rejects(second, /backend down/);
  assert.equal(calls, 1);
});

test("createSingleFlight reports in-flight status per key", async () => {
  const sf = createSingleFlight<string>();
  const gate = deferred<string>();

  assert.equal(sf.isInFlight("s1"), false);

  const running = sf.run("s1", () => gate.promise);
  assert.equal(sf.isInFlight("s1"), true);

  gate.resolve("x");
  await running;
  assert.equal(sf.isInFlight("s1"), false);
});
