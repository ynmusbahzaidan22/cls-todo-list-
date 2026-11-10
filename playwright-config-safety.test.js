import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { parse as parseConnectionString } from "pg-connection-string";
import { pathToFileURL } from "node:url";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { buildWebServerEnvironment, getTestDatabaseURL, validateTestDatabaseConfiguration } from "./e2e/test-database.js";

const configPath = pathToFileURL(path.resolve("playwright.config.js")).href;
const inspectConfigScript = `
  const { default: config } = await import(${JSON.stringify(configPath)});
  const { getTestDatabaseURL } = await import(${JSON.stringify(pathToFileURL(path.resolve("e2e/test-database.js")).href)});
  console.log(JSON.stringify({
    command: config.webServer.command,
    childEnvKeys: Object.keys(config.webServer.env).sort(),
    apiUsesTestDatabase: config.webServer.env.DATABASE_URL === process.env.TEST_DATABASE_URL,
    testsUseTestDatabase: getTestDatabaseURL() === process.env.TEST_DATABASE_URL,
    apiHasNoTestDatabaseURL: !Object.hasOwn(config.webServer.env, "TEST_DATABASE_URL"),
    apiHasNoSentinel: !Object.hasOwn(config.webServer.env, "TEST_SENTINEL_SECRET"),
    apiHasNoRuntimeRole: !Object.hasOwn(config.webServer.env, "DATABASE_RUNTIME_ROLE"),
    apiHasNoProxyConfig: !Object.hasOwn(config.webServer.env, "TRUSTED_PROXY_CIDRS"),
    apiHasNoBrowserConfig: !Object.hasOwn(config.webServer.env, "PLAYWRIGHT_BROWSER_CHANNEL"),
    appEnv: config.webServer.env.APP_ENV,
    httpAddr: config.webServer.env.HTTP_ADDR,
    trustedOrigin: config.webServer.env.TRUSTED_ORIGIN,
    secureCookie: config.webServer.env.SESSION_COOKIE_SECURE,
  }));
`;

const TEST_URL = "postgres://test:test-secret@127.0.0.1:15432/todo_test";
const DEV_URL = "postgres://dev:dev-secret@127.0.0.1:15432/todo_dev";

function loadConfig(overrides = {}) {
  const isolatedEnv = {
    PATH: process.env.PATH || "",
    PHASE8_BASE_URL: "http://127.0.0.1:18080",
    ...(process.platform === "win32" && process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...overrides,
  };
  const isolatedCwd = mkdtempSync(path.join(os.tmpdir(), "playwright-config-safety-"));

  try {
    return spawnSync(process.execPath, ["--input-type=module", "-e", inspectConfigScript], {
      cwd: isolatedCwd,
      encoding: "utf8",
      env: isolatedEnv,
      shell: false,
      timeout: 10_000,
    });
  } finally {
    rmSync(isolatedCwd, { recursive: true, force: true });
  }
}

function rejectedConfiguration(overrides, message) {
  const result = loadConfig(overrides);
  assert.notEqual(result.status, 0, "unsafe Playwright configuration unexpectedly loaded");
  const output = `${result.stdout}${result.stderr}`;
  assert.ok(output.includes(message), "configuration failed without the expected actionable message");
  return output;
}

test("requires TEST_DATABASE_URL even when DATABASE_URL is present", () => {
  const output = rejectedConfiguration({ DATABASE_URL: DEV_URL }, "Set TEST_DATABASE_URL");
  assert.ok(!output.includes("dev-secret"), "configuration error exposed development credentials");
});

test("rejects empty TEST_DATABASE_URL without exposing credentials", () => {
  const output = rejectedConfiguration({ TEST_DATABASE_URL: "" }, "Set TEST_DATABASE_URL");
  assert.ok(!output.includes("test-secret"), "configuration error exposed test credentials");
});

test("rejects the same database target even when credentials or loopback aliases differ", () => {
  const output = rejectedConfiguration({
    DATABASE_URL: "postgres://dev:dev-secret@localhost:15432/todo_dev",
    TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:15432/todo_dev",
  }, "different database");
  assert.ok(!output.includes("dev-secret") && !output.includes("test-secret"), "configuration error exposed database credentials");
});

test("rejects a non-loopback test database without exposing credentials", () => {
  const output = rejectedConfiguration({
    TEST_DATABASE_URL: "postgres://test:test-secret@db.example.test:5432/todo_test",
  }, "loopback PostgreSQL host");
  assert.ok(!output.includes("test-secret"), "configuration error exposed test credentials");
});

test("rejects every query parameter that could change the test connection target", () => {
  for (const parameter of [
    "host=db.example.test",
    "%68ost=db.example.test",
    "Host=db.example.test",
    "hostaddr=192.0.2.1",
    "port=5432",
    "database=todo_dev",
    "dbname=todo_dev",
    "service=production",
    "%64atabase=todo_dev",
    "host=127.0.0.1&host=192.0.2.1",
    "sslmode=disable&sslmode=require",
    "application_name=e2e",
  ]) {
    const output = rejectedConfiguration({
      TEST_DATABASE_URL: `${TEST_URL}?${parameter}`,
    }, "unambiguous PostgreSQL URL");
    assert.ok(!output.includes("test-secret"), `validation exposed credentials for ${parameter}`);
  }
});

test("rejects ambiguous host, port, path, fragment, and alternate numeric host forms", () => {
  for (const url of [
    "postgres://test:test-secret@127.0.0.1,192.0.2.1:15432/todo_test",
    "postgres://test:test-secret@127.1:15432/todo_test",
    "postgres://test:test-secret@127.0.0.1:015432/todo_test",
    "postgres://test:test-secret@127.0.0.1:15432/todo%5Ftest",
    "postgres://test:test-secret@127.0.0.1:15432/todo_test#fragment",
  ]) {
    assert.throws(
      () => getTestDatabaseURL({ TEST_DATABASE_URL: url }),
      (error) => /unambiguous PostgreSQL URL|loopback PostgreSQL host/.test(error.message),
    );
  }
});

test("accepts a strict loopback URL with one supported sslmode query parameter", () => {
  const url = "postgresql://test:p%40ss@[::1]:15432/todo_test?sslmode=disable";
  assert.equal(getTestDatabaseURL({ TEST_DATABASE_URL: url }), url);
  const parsed = parseConnectionString(url);
  assert.equal(parsed.host, "::1");
  assert.equal(parsed.port, "15432");
  assert.equal(parsed.database, "todo_test");
});

test("the shared validator detects a target override in DATABASE_URL including encoded keys", () => {
  for (const databaseURL of [
    `${DEV_URL}?database=todo_test`,
    `${DEV_URL}?dbname=todo_test`,
    `${DEV_URL}?%68ost=127.0.0.1`,
    `${DEV_URL}?port=15432&port=5432`,
    "postgres://dev:dev-secret@127.0.0.1,localhost/todo_dev",
  ]) {
    assert.throws(
      () => validateTestDatabaseConfiguration({ TEST_DATABASE_URL: TEST_URL, DATABASE_URL: databaseURL }),
      /unambiguous PostgreSQL URL/,
    );
  }
});

test("routes the API and E2E tests to the same validated target using an explicit child environment", () => {
  const result = loadConfig({
    APP_ENV: "production",
    DATABASE_URL: DEV_URL,
    TEST_DATABASE_URL: TEST_URL,
    TEST_SENTINEL_SECRET: "sentinel-secret-value",
    DATABASE_RUNTIME_ROLE: "runtime-sentinel",
    TRUSTED_PROXY_CIDRS: "192.0.2.0/24",
    PLAYWRIGHT_BROWSER_CHANNEL: "sentinel-channel",
    GO_EXE: process.platform === "win32" ? "C:\\Go Tools\\bin\\go.exe" : "/opt/Go Tools/bin/go",
  });
  assert.equal(result.status, 0, "safe explicit test database configuration was rejected");
  const config = JSON.parse(result.stdout.trim().split(/\r?\n/).at(-1));
  assert.equal(config.apiUsesTestDatabase, true, "API server did not receive the validated test URL");
  assert.equal(config.testsUseTestDatabase, true, "E2E helper did not return the validated test URL");
  assert.deepEqual(config.childEnvKeys, ["APP_ENV", "DATABASE_URL", "HTTP_ADDR", "SESSION_COOKIE_SECURE", "TRUSTED_ORIGIN"]);
  assert.equal(config.appEnv, "development");
  assert.equal(config.httpAddr, "127.0.0.1:18080");
  assert.equal(config.trustedOrigin, "http://127.0.0.1:18080");
  assert.equal(config.secureCookie, "false");
  assert.ok(config.command.includes("Go Tools"), "GO_EXE override was not used");
  assert.ok(config.command.endsWith(process.platform === "win32" ? '"C:\\Go Tools\\bin\\go.exe"' : "'/opt/Go Tools/bin/go'"), "GO_EXE override was not quoted safely");
  assert.ok(!result.stdout.includes("test-secret") && !result.stderr.includes("test-secret"), "configuration output exposed database credentials");
  assert.ok(!result.stdout.includes("sentinel-secret-value") && !result.stderr.includes("sentinel-secret-value"), "configuration output exposed an unrelated secret");
  assert.equal(config.apiHasNoTestDatabaseURL, true, "E2E-only database URL leaked into API child environment");
  assert.equal(config.apiHasNoSentinel, true, "unrelated secret leaked into API child environment");
  assert.equal(config.apiHasNoRuntimeRole, true, "runtime role leaked into API child environment");
  assert.equal(config.apiHasNoProxyConfig, true, "unrelated proxy configuration leaked into API child environment");
  assert.equal(config.apiHasNoBrowserConfig, true, "browser configuration leaked into API child environment");
});

test("validates PHASE8_BASE_URL as a loopback origin before configuring the server", () => {
  const output = rejectedConfiguration({
    TEST_DATABASE_URL: TEST_URL,
    PHASE8_BASE_URL: "http://example.test:18080",
  }, "loopback HTTP origin");
  assert.ok(!output.includes("test-secret"), "configuration error exposed database credentials");
});

test("defaults to PATH Go and safely quotes GO_EXE overrides containing spaces", () => {
  const defaultResult = loadConfig({ TEST_DATABASE_URL: TEST_URL });
  assert.equal(defaultResult.status, 0, "default Go configuration failed");
  const defaultConfig = JSON.parse(defaultResult.stdout.trim().split(/\r?\n/).at(-1));
  assert.ok(defaultConfig.command.includes("-e \"eval(Buffer.from("), "API environment-isolation launcher was not configured");
  assert.ok(defaultConfig.command.endsWith(process.platform === "win32" ? '"go"' : "'go'"), "Go default does not resolve through PATH");

  const overrideResult = loadConfig({
    TEST_DATABASE_URL: TEST_URL,
    GO_EXE: process.platform === "win32" ? "C:\\Go Tools\\bin\\go.exe" : "/opt/Go Tools/bin/go",
  });
  assert.equal(overrideResult.status, 0, "GO_EXE override containing spaces was rejected");
  const overrideConfig = JSON.parse(overrideResult.stdout.trim().split(/\r?\n/).at(-1));
  assert.ok(overrideConfig.command.includes("Go Tools"), "GO_EXE override was not used");
  assert.ok(overrideConfig.command.endsWith(process.platform === "win32" ? '"C:\\Go Tools\\bin\\go.exe"' : "'/opt/Go Tools/bin/go'"), "GO_EXE path was not quoted safely");
});

test("API launcher forwards only required Go runtime settings and validated API configuration", () => {
  const environment = buildWebServerEnvironment({
    databaseURL: TEST_URL,
    httpAddr: "127.0.0.1:18080",
    trustedOrigin: "http://127.0.0.1:18080",
  }, {
    PATH: "safe-path",
    SystemRoot: "safe-system-root",
    USERPROFILE: "safe-profile",
    LOCALAPPDATA: "safe-local-app-data",
    TEMP: "safe-temp",
    TEST_DATABASE_URL: TEST_URL,
    TEST_SENTINEL_SECRET: "sentinel-secret-value",
    DATABASE_RUNTIME_ROLE: "runtime-sentinel",
    TRUSTED_PROXY_CIDRS: "192.0.2.0/24",
    PLAYWRIGHT_BROWSER_CHANNEL: "sentinel-channel",
    DATABASE_URL: DEV_URL,
    GOFLAGS: "-overlay=untrusted.json",
  });
  assert.deepEqual(environment, {
    PATH: "safe-path",
    SystemRoot: "safe-system-root",
    USERPROFILE: "safe-profile",
    LOCALAPPDATA: "safe-local-app-data",
    TEMP: "safe-temp",
    APP_ENV: "development",
    DATABASE_URL: TEST_URL,
    HTTP_ADDR: "127.0.0.1:18080",
    SESSION_COOKIE_SECURE: "false",
    TRUSTED_ORIGIN: "http://127.0.0.1:18080",
  });
  assert.ok(!JSON.stringify(environment).includes("secret-value"));
  assert.ok(!JSON.stringify(environment).includes("runtime-sentinel"));
  assert.ok(!JSON.stringify(environment).includes("untrusted.json"));
});

test("rejects Windows shell metacharacters in GO_EXE", { skip: process.platform !== "win32" }, () => {
  const output = rejectedConfiguration({
    TEST_DATABASE_URL: TEST_URL,
    GO_EXE: "C:\\Go Tools\\go.exe & whoami",
  }, "cannot be safely passed");
  assert.ok(!output.includes("test-secret"), "configuration error exposed test credentials");
});

test("E2E helper requires TEST_DATABASE_URL and never falls back to DATABASE_URL", () => {
  assert.throws(
    () => getTestDatabaseURL({ DATABASE_URL: DEV_URL }),
    (error) => error.message.includes("Set TEST_DATABASE_URL") && !error.message.includes("dev-secret"),
  );
  assert.throws(() => getTestDatabaseURL({ TEST_DATABASE_URL: "  " }), /Set TEST_DATABASE_URL/);
});

test("E2E helper rejects development target aliases without exposing credentials", () => {
  assert.throws(
    () => getTestDatabaseURL({
      DATABASE_URL: "postgres://dev:dev-secret@localhost:15432/todo_dev",
      TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:15432/todo_dev",
    }),
    (error) => error.message.includes("different database")
      && !error.message.includes("dev-secret")
      && !error.message.includes("test-secret"),
  );
});

test("shared E2E helper validates actual pg-connection-string target semantics", () => {
  for (const url of [
    "postgres://test:test-secret@127.0.0.1:15432/todo_test",
    "postgresql://test:p%40ss@[::1]:15432/todo_test?sslmode=disable",
    "postgres://test:test-secret@localhost/todo_test",
  ]) {
    const validated = getTestDatabaseURL({ TEST_DATABASE_URL: url });
    const actual = parseConnectionString(validated);
    const expected = new URL(url);
    assert.equal(actual.database, expected.pathname.slice(1));
    assert.equal(actual.port || "5432", expected.port || "5432");
    assert.equal(actual.host, expected.hostname.replace(/^\[|\]$/g, ""));
  }
});
