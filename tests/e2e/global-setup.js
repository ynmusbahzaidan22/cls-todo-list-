import { Client } from "pg";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { setTimeout as delay } from "node:timers/promises";
import { assertRateLimitRowsMatchBaseline, getTestDatabaseURL, readRateLimitRows } from "./test-database.js";

const lockKey = [731920242, 18080808];

async function stopManagedAPIServer() {
  const pidFile = process.env.PLAYWRIGHT_API_PID_FILE;
  const stopFile = process.env.PLAYWRIGHT_API_STOP_FILE;
  if (!pidFile || !stopFile || !existsSync(pidFile)) return;
  const pid = readFileSync(pidFile, "utf8").trim();
  if (!/^[1-9][0-9]*$/.test(pid) || Number(pid) === process.pid) throw new Error("Playwright API process identity is invalid.");
  writeFileSync(stopFile, "stop", { flag: "wx" });
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (!existsSync(pidFile)) return;
    await delay(50);
  }

  // The Playwright webServer wrapper can outlive global setup on Windows.
  // After giving its marker-based shutdown a chance, stop only the exact API
  // PID registered by this run, then allow the wrapper to remove its files.
  try {
    process.kill(Number(pid), "SIGTERM");
  } catch (error) {
    if (error.code !== "ESRCH") throw new Error("Playwright API process could not be stopped safely.");
  }
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (!existsSync(pidFile)) return;
    await delay(50);
  }
  let processStillExists = true;
  try {
    process.kill(Number(pid), 0);
  } catch (error) {
    processStillExists = error.code !== "ESRCH";
  }
  if (!processStillExists) {
    for (const file of [pidFile, stopFile]) {
      try { rmSync(file, { force: true }); } catch { /* exact run marker cleanup is best effort */ }
    }
    return;
  }
  throw new Error("Playwright API process did not stop during global teardown.");
}

export default async function globalSetup() {
  const connectionString = getTestDatabaseURL();
  const clientIP = process.env.PLAYWRIGHT_TEST_CLIENT_IP;
  if (!clientIP?.toLowerCase().startsWith("2001:db8:")) {
    throw new Error("Playwright test client identity was not configured safely.");
  }

  const client = new Client({ connectionString });
  await client.connect();
  let locked = false;
  try {
    await client.query("SELECT pg_advisory_lock($1, $2)", lockKey);
    locked = true;

    const baselineRows = await readRateLimitRows(client);

    return async function globalTeardown() {
      const cleanupFailures = [];
      try {
        await stopManagedAPIServer();
      } catch {
        cleanupFailures.push("API server process shutdown failed");
      }
      try {
        await assertRateLimitRowsMatchBaseline(client, baselineRows);
      } catch {
        cleanupFailures.push("complete rate-limit baseline verification failed");
      }
      if (locked) {
        try {
          await client.query("SELECT pg_advisory_unlock($1, $2)", lockKey);
        } catch {
          cleanupFailures.push("suite database lock release failed");
        }
      }
      try {
        await client.end();
      } catch {
        cleanupFailures.push("verification connection close failed");
      }
      if (cleanupFailures.length) throw new Error(`Playwright rate-limit cleanup or preservation verification failed: ${cleanupFailures.join(", ")}.`);
    };
  } catch (error) {
    if (locked) await client.query("SELECT pg_advisory_unlock($1, $2)", lockKey).catch(() => {});
    await client.end().catch(() => {});
    throw error;
  }
}
