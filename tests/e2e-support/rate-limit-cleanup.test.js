import assert from "node:assert/strict";
import test from "node:test";
import {
  cleanupAbsentBaselineRateLimitKeys,
  assertRateLimitRowsMatchBaseline,
  rateLimitEmailKeyHash,
  rateLimitCleanupHashes,
  rateLimitIPKeyHashes,
} from "../e2e/test-database.js";

function memoryTable(initial = []) {
  const rows = new Map(initial.map(([hash, row]) => [hash.toString("hex"), row]));
  return {
    rows,
    async query(sql, parameters) {
      const key = parameters[0].toString("hex");
      if (sql.startsWith("DELETE")) {
        rows.delete(key);
        return { rows: [] };
      }
      return { rows: [{ exists: rows.has(key) }] };
    },
  };
}

test("cleans only exact email limiter keys proven absent before spec setup", async () => {
  const hash = rateLimitEmailKeyHash("fixture@example.test");
  const table = memoryTable([[hash, { attempts: 1 }]]);
  await cleanupAbsentBaselineRateLimitKeys(table, [hash], new Set());
  assert.equal(table.rows.has(hash.toString("hex")), false);
  assert.equal(table.rows.size, 0);
});

test("refuses cleanup when the caller has not supplied a pre-test baseline", async () => {
  const hash = rateLimitEmailKeyHash("fixture@example.test");
  const existing = { attempts: 7 };
  const table = memoryTable([[hash, existing]]);

  await assert.rejects(
    () => cleanupAbsentBaselineRateLimitKeys(table, [hash]),
    /explicit pre-test baseline/,
  );
  assert.deepEqual(table.rows.get(hash.toString("hex")), existing);
});

test("preserves a pre-existing shared IP limiter key unchanged", async () => {
  const [registerHash, loginHash] = rateLimitIPKeyHashes("127.0.0.1");
  const existing = { window: "baseline", attempts: 7, expires: "future" };
  const table = memoryTable([[registerHash, existing], [loginHash, existing]]);
  const baseline = new Set([registerHash.toString("hex"), loginHash.toString("hex")]);

  await cleanupAbsentBaselineRateLimitKeys(table, [registerHash, loginHash], baseline);

  assert.deepEqual(table.rows.get(registerHash.toString("hex")), existing);
  assert.deepEqual(table.rows.get(loginHash.toString("hex")), existing);
});

test("removes run-owned shared IP keys only when absent from baseline", async () => {
  const owned = rateLimitIPKeyHashes("2001:db8::1234");
  const table = memoryTable(owned.map((hash) => [hash, { attempts: 2 }]));
  await cleanupAbsentBaselineRateLimitKeys(table, owned, new Set());
  assert.equal(table.rows.size, 0);
});

test("per-spec cleanup owns registration-IP, login-IP, and login-email keys", async () => {
  const ip = "2001:db8:1:2:3:4:5:6";
  const email = "scoped-fixture@example.test";
  const hashes = rateLimitCleanupHashes([email], ip);
  const expected = [...rateLimitIPKeyHashes(ip), rateLimitEmailKeyHash(email)];
  assert.deepEqual(
    hashes.map((hash) => hash.toString("hex")).sort(),
    expected.map((hash) => hash.toString("hex")).sort(),
  );

  const table = memoryTable(hashes.map((hash) => [hash, { attempts: 2 }]));
  await cleanupAbsentBaselineRateLimitKeys(table, hashes, new Set());
  assert.equal(table.rows.size, 0, "both IP limiter rows and the account limiter row must be removed");
});

test("complete suite verification detects rate-limit rows outside the expected key set", async () => {
  const baselineRows = [{ key_hash: "baseline", window_started_at: "1", attempt_count: "1", expires_at: "2" }];
  const queryable = { async query() { return { rows: [...baselineRows, { key_hash: "unexpected" }] }; } };
  await assert.rejects(
    () => assertRateLimitRowsMatchBaseline(queryable, baselineRows),
    /complete pre-suite baseline/,
  );
});

test("complete suite verification accepts an unchanged empty baseline", async () => {
  const queryable = { async query() { return { rows: [] }; } };
  await assertRateLimitRowsMatchBaseline(queryable, []);
});

test("surfaces cleanup verification failures", async () => {
  const hash = rateLimitEmailKeyHash("fixture@example.test");
  const queryable = { async query(sql) { return sql.startsWith("DELETE") ? { rows: [] } : { rows: [{ exists: true }] }; } };
  await assert.rejects(() => cleanupAbsentBaselineRateLimitKeys(queryable, [hash], new Set()), /cleanup or verification failed/);
});

test("continues cleaning other owned keys after one cleanup error", async () => {
  const hashes = [rateLimitEmailKeyHash("first@example.test"), rateLimitEmailKeyHash("second@example.test")];
  let deletes = 0;
  const queryable = {
    async query(sql) {
      if (sql.startsWith("DELETE")) {
        deletes += 1;
        if (deletes === 1) throw new Error("database error");
        return { rows: [] };
      }
      return { rows: [{ exists: false }] };
    },
  };
  await assert.rejects(() => cleanupAbsentBaselineRateLimitKeys(queryable, hashes, new Set()), /cleanup or verification failed/);
  assert.equal(deletes, 2);
});
