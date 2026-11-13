import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { parse as parseConnectionString } from "pg-connection-string";
import { pathToFileURL } from "node:url";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  buildWebServerEnvironment,
  generatePlaywrightClientIP,
  getTestDatabaseURL,
  validateTestDatabaseConfiguration,
} from "../e2e/test-database.js";

const configPath = pathToFileURL(path.resolve("playwright.config.js")).href;
const inspectConfigScript = `
  const { default: config } = await import(${JSON.stringify(configPath)});
  const { getTestDatabaseURL } = await import(${JSON.stringify(pathToFileURL(path.resolve("tests/e2e/test-database.js")).href)});
  const { isIP } = await import("node:net");
  const encodedMarker = "Buffer.from('";
  const encodedStart = config.webServer.command.indexOf(encodedMarker);
  const payloadStart = encodedStart < 0 ? -1 : encodedStart + encodedMarker.length;
  const payloadEnd = payloadStart < 0 ? -1 : config.webServer.command.indexOf("'", payloadStart);
  const launcherSource = payloadEnd < 0 ? "" : Buffer.from(config.webServer.command.slice(payloadStart, payloadEnd), "base64").toString();
  console.log(JSON.stringify({
    command: config.webServer.command,
    launcherUsesValidatedDatabaseURL: launcherSource.includes("databaseURL: process.env.DATABASE_URL")
      && !launcherSource.includes("databaseURL: testDatabaseURL"),
    launcherForwardsTerminationSignals: launcherSource.includes('process.once("SIGTERM"')
      && launcherSource.includes('process.once("SIGINT"')
      && launcherSource.includes("child.kill(signal)")
      && launcherSource.includes('process.stdin.on("end"')
      && launcherSource.includes("const parentPID = process.ppid")
      && launcherSource.includes('process.kill(parentPID, 0)')
      && (process.platform !== "win32" || launcherSource.includes('"/T", "/F"')),
    launcherBuildsStandaloneAPI: launcherSource.includes('["build", "-o", apiBinary, "./cmd/api"]')
      && launcherSource.includes('spawn(apiBinary, [], { env: childEnv')
      && launcherSource.includes('stdio: "ignore"'),
    launcherRegistersAPIForGlobalTeardown: launcherSource.includes('writeFileSync(process.env.PLAYWRIGHT_API_PID_FILE, String(apiProcess.pid), { flag: "wx" })')
      && launcherSource.includes("process.env.PLAYWRIGHT_API_STOP_FILE && existsSync(process.env.PLAYWRIGHT_API_STOP_FILE)")
      && launcherSource.includes("rmSync(file, { force: true })"),
    childEnvKeys: Object.keys(config.webServer.env).sort(),
    apiUsesTestDatabase: config.webServer.env.DATABASE_URL === process.env.TEST_DATABASE_URL,
    testsUseTestDatabase: getTestDatabaseURL() === process.env.TEST_DATABASE_URL,
    apiHasNoTestDatabaseURL: !Object.hasOwn(config.webServer.env, "TEST_DATABASE_URL"),
    apiHasNoSentinel: !Object.hasOwn(config.webServer.env, "TEST_SENTINEL_SECRET"),
    apiHasNoRuntimeRole: !Object.hasOwn(config.webServer.env, "DATABASE_RUNTIME_ROLE"),
    apiHasOnlyLoopbackProxyConfig: config.webServer.env.TRUSTED_PROXY_CIDRS === "127.0.0.1/32",
    apiHasNoBrowserConfig: !Object.hasOwn(config.webServer.env, "PLAYWRIGHT_BROWSER_CHANNEL"),
    apiUsesGuardedTestMode: config.webServer.env.PLAYWRIGHT_TEST_MODE === "1",
    apiTrustsOnlyLoopbackProxy: config.webServer.env.TRUSTED_PROXY_CIDRS === "127.0.0.1/32",
    browserUsesIsolatedClientIdentity: Boolean(config.use.extraHTTPHeaders?.["X-Forwarded-For"]),
    clientIdentityIsValidIPv6: isIP(config.webServer.env.PLAYWRIGHT_TEST_CLIENT_IP) === 6,
    clientIdentityHasCanonicalGroups: config.webServer.env.PLAYWRIGHT_TEST_CLIENT_IP
      .split(":").slice(2).every((group) => /^[1-9a-f][0-9a-f]{0,3}$/.test(group)),
    clientIP: config.webServer.env.PLAYWRIGHT_TEST_CLIENT_IP,
    serialSuiteHasGlobalCleanup: config.workers === 1 && config.fullyParallel === false && Boolean(config.globalSetup),
    appEnv: config.webServer.env.APP_ENV,
    httpAddr: config.webServer.env.HTTP_ADDR,
    trustedOrigin: config.webServer.env.TRUSTED_ORIGIN,
    secureCookie: config.webServer.env.SESSION_COOKIE_SECURE,
  }));
`;

const TEST_URL = "postgres://test:test-secret@127.0.0.1:25432/todo_test";
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
  const safeOutput = output.replace(/postgres(?:ql)?:\/\/[^\s"']+/gi, "[redacted PostgreSQL URL]");
  assert.ok(output.includes(message), `configuration failed without the expected actionable message: ${safeOutput}`);
  return output;
}

function canonicalizeWithGo(addresses) {
  const directory = mkdtempSync(path.join(os.tmpdir(), "playwright-ipv6-parity-"));
  const source = path.join(directory, "main.go");
  writeFileSync(source, `package main
import ("fmt"; "net"; "os")
func main() {
  for _, raw := range os.Args[1:] {
    parsed := net.ParseIP(raw)
    if parsed == nil { os.Exit(2) }
    fmt.Println(parsed.String())
  }
}
`);
  try {
    const env = {
      PATH: process.env.PATH || "",
      ...(process.platform === "win32" && process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    };
    for (const key of ["LOCALAPPDATA", "USERPROFILE", "TEMP", "TMP", "GOCACHE"]) {
      if (process.env[key] !== undefined) env[key] = process.env[key];
    }
    return spawnSync("go", ["run", source, ...addresses], {
      cwd: process.cwd(),
      encoding: "utf8",
      env,
      shell: false,
      timeout: 120_000,
    });
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

test("requires TEST_DATABASE_URL even when DATABASE_URL is present", () => {
  const output = rejectedConfiguration({ DATABASE_URL: DEV_URL }, "Set TEST_DATABASE_URL");
  assert.ok(!output.includes("dev-secret"), "configuration error exposed development credentials");
});

test("rejects empty TEST_DATABASE_URL without exposing credentials", () => {
  const output = rejectedConfiguration({ TEST_DATABASE_URL: "" }, "Set TEST_DATABASE_URL");
  assert.ok(!output.includes("test-secret"), "configuration error exposed test credentials");
});

test("rejects surrounding whitespace in either database URL", () => {
  assert.throws(
    () => getTestDatabaseURL({ TEST_DATABASE_URL: ` ${TEST_URL}` }),
    /unambiguous PostgreSQL URL/,
  );
  assert.throws(
    () => validateTestDatabaseConfiguration({ TEST_DATABASE_URL: TEST_URL, DATABASE_URL: `${DEV_URL} ` }),
    /unambiguous PostgreSQL URL/,
  );
});

test("rejects the same database target even when credentials or loopback aliases differ", () => {
  const output = rejectedConfiguration({
    DATABASE_URL: "postgres://dev:dev-secret@localhost:25432/todo_test",
    TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:25432/todo_test",
  }, "different database");
  assert.ok(!output.includes("dev-secret") && !output.includes("test-secret"), "configuration error exposed database credentials");
  assert.throws(
    () => validateTestDatabaseConfiguration({
      DATABASE_URL: "postgres://dev:dev-secret@127.0.0.2:25432/todo_test",
      TEST_DATABASE_URL: TEST_URL,
    }),
    /different database/,
  );
});

test("rejects a non-loopback test database without exposing credentials", () => {
  const output = rejectedConfiguration({
    TEST_DATABASE_URL: "postgres://test:test-secret@db.example.test:25432/todo_test",
  }, "loopback PostgreSQL host");
  assert.ok(!output.includes("test-secret"), "configuration error exposed test credentials");
});

test("requires the isolated PostgreSQL test port and exact database name", () => {
  assert.throws(
    () => getTestDatabaseURL({ TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:15432/todo_test" }),
    /isolated test PostgreSQL port 25432/,
  );
  assert.throws(
    () => getTestDatabaseURL({ TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:25432/todo_test_other" }),
    /dedicated todo_test database/,
  );
  const valid = "postgres://test:test-secret@127.0.0.1:25432/todo_test";
  assert.equal(getTestDatabaseURL({ TEST_DATABASE_URL: valid }), valid);
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
    "servicefile=/tmp/pg_service.conf",
    "options=-csearch_path%3Dpublic",
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
    "postgres://test:test-secret@127.0.0.1,192.0.2.1:25432/todo_test",
    "postgres://test:test-secret@127.1:25432/todo_test",
    "postgres://test:test-secret@127.0.0.1:025432/todo_test",
    "postgres://test:test-secret@127.0.0.1:25432/todo%5Ftest",
    "postgres://test:test-secret@127.0.0.1:25432/todo_test#fragment",
  ]) {
    assert.throws(
      () => getTestDatabaseURL({ TEST_DATABASE_URL: url }),
      (error) => /unambiguous PostgreSQL URL|loopback PostgreSQL host/.test(error.message),
    );
  }
});

test("requires an explicit test user and the dedicated test database naming convention", () => {
  for (const url of [
    "postgres://127.0.0.1:25432/todo_test",
    "postgres://test:test-secret@127.0.0.1:25432/todo_dev",
    "postgres://test:test-secret@127.0.0.1:25432/todo_test_extra",
    "postgres://test:test-secret@127.0.0.1:25432/TODO_TEST",
  ]) {
    assert.throws(
      () => getTestDatabaseURL({ TEST_DATABASE_URL: url }),
      /unambiguous PostgreSQL URL|dedicated todo_test database/i,
    );
  }
});

test("accepts a strict loopback URL with one supported sslmode query parameter", () => {
  const url = "postgresql://test:p%40ss@[::1]:25432/todo_test?sslmode=disable";
  assert.equal(getTestDatabaseURL({ TEST_DATABASE_URL: url }), url);
  const parsed = parseConnectionString(url);
  assert.equal(parsed.host, "::1");
  assert.equal(parsed.port, "25432");
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

test("rejects unsupported options in DATABASE_URL so Go and E2E parse the same target contract", () => {
  for (const databaseURL of [
    `${DEV_URL}?options=-csearch_path%3Dpublic`,
    `${DEV_URL}?servicefile=/tmp/pg_service.conf`,
    `${DEV_URL}?application_name=api`,
    `${DEV_URL}?sslmode=disable&sslmode=require`,
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
  assert.equal(config.launcherUsesValidatedDatabaseURL, true, "API launcher did not read the validated URL from its webServer environment");
  assert.equal(config.launcherForwardsTerminationSignals, true, "API launcher did not stop its child Go process during shutdown");
  assert.equal(config.launcherBuildsStandaloneAPI, true, "API launcher did not run a standalone server binary for reliable process cleanup");
  assert.equal(config.launcherRegistersAPIForGlobalTeardown, true, "API launcher did not register a controlled shutdown path for global teardown");
  assert.equal(config.testsUseTestDatabase, true, "E2E helper did not return the validated test URL");
  assert.deepEqual(config.childEnvKeys, ["APP_ENV", "DATABASE_URL", "HTTP_ADDR", "PLAYWRIGHT_API_PID_FILE", "PLAYWRIGHT_API_STOP_FILE", "PLAYWRIGHT_TEST_CLIENT_IP", "PLAYWRIGHT_TEST_MODE", "SESSION_COOKIE_SECURE", "TRUSTED_ORIGIN", "TRUSTED_PROXY_CIDRS"]);
  assert.equal(config.appEnv, "development");
  assert.equal(config.httpAddr, "127.0.0.1:18080");
  assert.equal(config.trustedOrigin, "http://127.0.0.1:18080");
  assert.equal(config.secureCookie, "false");
  assert.equal(config.apiUsesGuardedTestMode, true);
  assert.equal(config.apiTrustsOnlyLoopbackProxy, true);
  assert.equal(config.browserUsesIsolatedClientIdentity, true);
  assert.equal(config.clientIdentityIsValidIPv6, true, "generated Playwright client identity is not valid IPv6");
  assert.equal(config.clientIdentityHasCanonicalGroups, true, "generated Playwright client identity is not in Go's canonical form");
  assert.equal(config.serialSuiteHasGlobalCleanup, true);
  assert.ok(config.command.includes("Go Tools"), "GO_EXE override was not used");
  assert.ok(config.command.endsWith(process.platform === "win32" ? '"C:\\Go Tools\\bin\\go.exe"' : "'/opt/Go Tools/bin/go'"), "GO_EXE override was not quoted safely");
  assert.ok(!result.stdout.includes("test-secret") && !result.stderr.includes("test-secret"), "configuration output exposed database credentials");
  assert.ok(!result.stdout.includes("sentinel-secret-value") && !result.stderr.includes("sentinel-secret-value"), "configuration output exposed an unrelated secret");
  assert.equal(config.apiHasNoTestDatabaseURL, true, "E2E-only database URL leaked into API child environment");
  assert.equal(config.apiHasNoSentinel, true, "unrelated secret leaked into API child environment");
  assert.equal(config.apiHasNoRuntimeRole, true, "runtime role leaked into API child environment");
  assert.equal(config.apiHasOnlyLoopbackProxyConfig, true, "API child environment did not restrict proxy trust to loopback");
  assert.equal(config.apiHasNoBrowserConfig, true, "browser configuration leaked into API child environment");
});

test("generated Playwright IPv6 identities exactly match Go canonical text", () => {
  const addresses = Array.from({ length: 8 }, () => generatePlaywrightClientIP());
  const canonical = canonicalizeWithGo(addresses);
  const goFailure = canonical.error?.code || canonical.stderr.trim().split(/\r?\n/)[0] || "unknown Go execution error";
  assert.equal(canonical.status, 0, `Go could not parse generated Playwright client identities: ${goFailure}`);
  assert.ok(
    canonical.stdout.trim().split(/\r?\n/).every((address, index) => address === addresses[index]),
    "generated Playwright client identity differs from Go's canonical representation",
  );
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
    playwrightTestMode: "1",
    trustedProxyCIDRs: "127.0.0.1/32",
    testClientIP: "2001:db8::1234",
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
    PLAYWRIGHT_TEST_MODE: "1",
    TRUSTED_PROXY_CIDRS: "127.0.0.1/32",
    PLAYWRIGHT_TEST_CLIENT_IP: "2001:db8::1234",
  });
  assert.ok(!JSON.stringify(environment).includes("secret-value"));
  assert.ok(!JSON.stringify(environment).includes("runtime-sentinel"));
  assert.ok(!JSON.stringify(environment).includes("untrusted.json"));
});

test("API launcher accepts its exact test DATABASE_URL alias and rejects a conflicting same-target alias", () => {
  const configuration = {
    databaseURL: TEST_URL,
    httpAddr: "127.0.0.1:18080",
    trustedOrigin: "http://127.0.0.1:18080",
    playwrightTestMode: "1",
    trustedProxyCIDRs: "127.0.0.1/32",
    testClientIP: "2001:db8::1234",
  };
  const environment = buildWebServerEnvironment(configuration, { DATABASE_URL: TEST_URL });
  assert.equal(environment.DATABASE_URL, TEST_URL);

  assert.throws(() => buildWebServerEnvironment(configuration, {
    DATABASE_URL: "postgres://dev:dev-secret@localhost:25432/todo_test",
  }), /different database/);
});

test("API launcher refuses an unguarded maintenance override or non-isolated proxy identity", () => {
  const validServerConfig = {
    databaseURL: TEST_URL,
    httpAddr: "127.0.0.1:18080",
    trustedOrigin: "http://127.0.0.1:18080",
    playwrightTestMode: "1",
    trustedProxyCIDRs: "127.0.0.1/32",
    testClientIP: "2001:db8::1234",
  };
  assert.throws(() => buildWebServerEnvironment({
    ...validServerConfig,
    trustedProxyCIDRs: "127.0.0.0/8",
  }), /guarded Playwright test identity/);
  assert.throws(() => buildWebServerEnvironment({
    ...validServerConfig,
    testClientIP: "192.0.2.10",
  }), /guarded Playwright test identity/);
  assert.throws(() => buildWebServerEnvironment({
    ...validServerConfig,
    playwrightTestMode: "",
  }), /requires validated database and HTTP settings/);
  assert.throws(() => buildWebServerEnvironment({
    ...validServerConfig,
    databaseURL: DEV_URL,
  }), /isolated test port|dedicated todo_test/);
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
    DATABASE_URL: "postgres://dev:dev-secret@localhost:25432/todo_test",
    TEST_DATABASE_URL: "postgres://test:test-secret@127.0.0.1:25432/todo_test",
    }),
    (error) => error.message.includes("different database")
      && !error.message.includes("dev-secret")
      && !error.message.includes("test-secret"),
  );
});

test("shared E2E helper validates actual pg-connection-string target semantics", () => {
  for (const url of [
    "postgres://test:test-secret@127.0.0.1:25432/todo_test",
    "postgresql://test:p%40ss@[::1]:25432/todo_test?sslmode=disable",
    "postgres://test:test-secret@localhost:25432/todo_test",
  ]) {
    const validated = getTestDatabaseURL({ TEST_DATABASE_URL: url });
    const actual = parseConnectionString(validated);
    const expected = new URL(url);
    assert.equal(actual.database, expected.pathname.slice(1));
    assert.equal(actual.port || "5432", expected.port || "5432");
    assert.equal(actual.host, expected.hostname.replace(/^\[|\]$/g, ""));
  }
});
