import { createHash, randomBytes } from "node:crypto";
import { isIP } from "node:net";

const POSTGRES_SCHEMES = new Set(["postgres:", "postgresql:"]);
const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1"]);
const TEST_DATABASE_NAME = "todo_test";
const TEST_DATABASE_PORT = "25432";
const SSL_MODES = new Set(["disable", "allow", "prefer", "require", "verify-ca", "verify-full"]);
const GO_RUNTIME_ENV_KEYS = [
  "PATH", "HOME", "GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN",
  "SystemRoot", "SystemDrive", "PATHEXT", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
  "LOCALAPPDATA", "APPDATA", "TEMP", "TMP",
];

function invalidURL(variableName) {
  return new Error(`${variableName} must be an unambiguous PostgreSQL URL.`);
}

function parsePort(rawPort, variableName) {
  if (rawPort === "") return "5432";
  if (!/^(?:[1-9][0-9]{0,4})$/.test(rawPort)) throw invalidURL(variableName);
  const port = Number(rawPort);
  if (port < 1 || port > 65535) throw invalidURL(variableName);
  return String(port);
}

function splitRawHostPort(rawHostPort, variableName, testURL) {
  let hostname;
  let rawPort = "";

  if (rawHostPort.startsWith("[")) {
    const closeBracket = rawHostPort.indexOf("]");
    if (closeBracket < 0) throw invalidURL(variableName);
    hostname = rawHostPort.slice(1, closeBracket).toLowerCase();
    const suffix = rawHostPort.slice(closeBracket + 1);
    if (suffix && !suffix.startsWith(":")) throw invalidURL(variableName);
    rawPort = suffix ? suffix.slice(1) : "";
    if (isIP(hostname) !== 6 || (testURL && hostname !== "::1")) throw invalidURL(variableName);
  } else {
    const colon = rawHostPort.lastIndexOf(":");
    if (colon >= 0) {
      if (rawHostPort.indexOf(":") !== colon) throw invalidURL(variableName);
      hostname = rawHostPort.slice(0, colon).toLowerCase();
      rawPort = rawHostPort.slice(colon + 1);
    } else {
      hostname = rawHostPort.toLowerCase();
    }
    if (!/^(?:localhost|127\.0\.0\.1|[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)$/.test(hostname)) {
      throw invalidURL(variableName);
    }
    if (hostname.includes("..") || hostname.endsWith(".")) throw invalidURL(variableName);
  }

  return { hostname, port: parsePort(rawPort, variableName) };
}

function validateQuery(search, variableName, testURL) {
  if (!search) return;

  // pgx and node-postgres differ on query precedence. Only one explicit,
  // documented TLS option is supported in either URL; reject unknown,
  // repeated, encoded, or target-changing parameters consistently with Go.
  const match = /^\?sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$/.exec(search);
  if (!match || !SSL_MODES.has(match[1])) throw invalidURL(variableName);
}

function parseDatabaseTarget(value, variableName, { testURL = false } = {}) {
  if (typeof value !== "string" || !value || /[\0\r\n\s\\#]/.test(value) || /%(?![0-9a-f]{2})/i.test(value)) {
    throw invalidURL(variableName);
  }

  const match = /^(postgres|postgresql):\/\/([^/?#]+)(\/[^/?#]+)(\?[^#]*)?$/.exec(value);
  if (!match) throw invalidURL(variableName);

  const [, , rawAuthority, rawPath, rawSearch = ""] = match;
  const atSigns = [...rawAuthority].filter((character) => character === "@").length;
  if (atSigns > 1) throw invalidURL(variableName);
  const rawHostPort = atSigns === 1 ? rawAuthority.slice(rawAuthority.lastIndexOf("@") + 1) : rawAuthority;
  if (!rawHostPort || rawHostPort.includes(",") || rawHostPort.includes("%")) throw invalidURL(variableName);

  const target = splitRawHostPort(rawHostPort, variableName, testURL);
  if (!/^\/[A-Za-z0-9_-]+$/.test(rawPath)) throw invalidURL(variableName);
  target.databaseName = rawPath.slice(1);
  if (testURL && target.databaseName !== TEST_DATABASE_NAME) {
    throw new Error("TEST_DATABASE_URL must use the dedicated todo_test database.");
  }
  validateQuery(rawSearch, variableName, testURL);

  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw invalidURL(variableName);
  }
  if (!POSTGRES_SCHEMES.has(parsed.protocol)
      || parsed.hostname.toLowerCase().replace(/^\[|\]$/g, "") !== target.hostname
      || (parsed.port || "5432") !== target.port
      || parsed.pathname !== rawPath
      || parsed.hash
      || !parsed.username) {
    throw invalidURL(variableName);
  }

  if (testURL && !LOOPBACK_HOSTS.has(target.hostname)) {
    throw new Error("TEST_DATABASE_URL must use a loopback PostgreSQL host.");
  }
  if (testURL && target.port !== TEST_DATABASE_PORT) {
    throw new Error("TEST_DATABASE_URL must use the isolated test PostgreSQL port 25432.");
  }

  return target;
}

function canonicalHost(hostname) {
  return LOOPBACK_HOSTS.has(hostname) || (isIP(hostname) === 4 && hostname.startsWith("127."))
    ? "<loopback>"
    : hostname;
}

function sameDatabaseTarget(left, right) {
  return canonicalHost(left.hostname) === canonicalHost(right.hostname)
    && left.port === right.port
    && left.databaseName === right.databaseName;
}

/** Validate one explicit loopback test URL and its separation from DATABASE_URL. */
export function validateTestDatabaseConfiguration(environment = process.env) {
  const testDatabaseURL = environment.TEST_DATABASE_URL;
  if (typeof testDatabaseURL !== "string" || !testDatabaseURL.trim()) {
    throw new Error("Set TEST_DATABASE_URL to a dedicated loopback PostgreSQL test database before running E2E tests.");
  }
  if (testDatabaseURL !== testDatabaseURL.trim()) throw invalidURL("TEST_DATABASE_URL");

  const testTarget = parseDatabaseTarget(testDatabaseURL, "TEST_DATABASE_URL", { testURL: true });
  const developmentDatabaseURL = environment.DATABASE_URL;
  if (developmentDatabaseURL) {
    if (developmentDatabaseURL !== developmentDatabaseURL.trim()) throw invalidURL("DATABASE_URL");
    const developmentTarget = parseDatabaseTarget(developmentDatabaseURL, "DATABASE_URL");
    if (sameDatabaseTarget(testTarget, developmentTarget)) {
      throw new Error("TEST_DATABASE_URL must identify a different database from DATABASE_URL.");
    }
  }

  return { testDatabaseURL, testTarget };
}

/** Return an explicitly configured, validated loopback E2E database URL. */
export function getTestDatabaseURL(environment = process.env) {
  return validateTestDatabaseConfiguration(environment).testDatabaseURL;
}

/** Build the reduced environment passed from the Playwright launcher to Go. */
export function buildWebServerEnvironment(configuration, parentEnvironment = process.env) {
  const { databaseURL, httpAddr, trustedOrigin, playwrightTestMode, trustedProxyCIDRs, testClientIP } = configuration;
  if (![databaseURL, httpAddr, trustedOrigin, playwrightTestMode, trustedProxyCIDRs, testClientIP]
    .every((value) => typeof value === "string" && value.length > 0)) {
    throw new Error("The API server requires validated database and HTTP settings.");
  }
  const validationEnvironment = { ...parentEnvironment, TEST_DATABASE_URL: databaseURL };
  // Playwright intentionally sets DATABASE_URL to the validated test URL for
  // the API process. Treat that exact duplicate as the test target itself,
  // while still rejecting any other DATABASE_URL that resolves to the same
  // database (or otherwise violates the shared target rules).
  if (validationEnvironment.DATABASE_URL === databaseURL) delete validationEnvironment.DATABASE_URL;
  validateTestDatabaseConfiguration(validationEnvironment);
  if (playwrightTestMode !== "1" || trustedProxyCIDRs !== "127.0.0.1/32"
      || isIP(testClientIP) !== 6 || !testClientIP.toLowerCase().startsWith("2001:db8:")) {
    throw new Error("The API server requires the guarded Playwright test identity.");
  }

  const environment = {};
  for (const key of GO_RUNTIME_ENV_KEYS) {
    if (parentEnvironment[key] !== undefined) environment[key] = parentEnvironment[key];
  }
  return {
    ...environment,
    APP_ENV: "development",
    DATABASE_URL: databaseURL,
    HTTP_ADDR: httpAddr,
    SESSION_COOKIE_SECURE: "false",
    TRUSTED_ORIGIN: trustedOrigin,
    PLAYWRIGHT_TEST_MODE: playwrightTestMode,
    TRUSTED_PROXY_CIDRS: trustedProxyCIDRs,
    PLAYWRIGHT_TEST_CLIENT_IP: testClientIP,
  };
}

export function rateLimitKeyHash(key) {
  return createHash("sha256").update(key).digest();
}

export function rateLimitEmailKeyHash(email) {
  const normalized = email.trim().toLowerCase();
  const digest = createHash("sha256").update(normalized).digest("base64url");
  return rateLimitKeyHash(`login:email:${digest}`);
}

export function rateLimitIPKeyHashes(remoteIP) {
  return [rateLimitKeyHash(`register:ip:${remoteIP}`), rateLimitKeyHash(`login:ip:${remoteIP}`)];
}

/** Create a unique documentation-range IPv6 identity in Go's canonical text form. */
export function generatePlaywrightClientIP() {
  const groups = randomBytes(12).toString("hex").match(/.{4}/g)
    .map((group) => (Number.parseInt(group, 16) || 1).toString(16));
  return `2001:db8:${groups.join(":")}`;
}

/** Enumerate exactly the production limiter keys an E2E identity can create. */
export function rateLimitCleanupHashes(emails, clientIP) {
  if (typeof clientIP !== "string" || isIP(clientIP) !== 6 || !clientIP.toLowerCase().startsWith("2001:db8:")) {
    throw new Error("E2E rate-limit cleanup requires a unique documentation-range IPv6 identity.");
  }
  return [...new Map([
    ...rateLimitIPKeyHashes(clientIP),
    ...emails.map(rateLimitEmailKeyHash),
  ].map((hash) => [hash.toString("hex"), hash])).values()];
}

export async function readRateLimitRows(queryable) {
  const { rows } = await queryable.query(`
    SELECT encode(key_hash, 'hex') AS key_hash,
           extract(epoch FROM window_started_at)::text AS window_started_at,
           attempt_count::text AS attempt_count,
           extract(epoch FROM expires_at)::text AS expires_at
    FROM app.auth_rate_limits
    ORDER BY key_hash`);
  return rows;
}

export async function assertRateLimitRowsMatchBaseline(queryable, baselineRows) {
  const currentRows = await readRateLimitRows(queryable);
  if (JSON.stringify(currentRows) !== JSON.stringify(baselineRows)) {
    throw new Error("E2E rate-limit table differs from its complete pre-suite baseline.");
  }
}

// Delete only hashes proven absent from the baseline. Callers pass the exact
// test-owned hashes; this helper never constructs broad IP/email/table filters.
export async function cleanupAbsentBaselineRateLimitKeys(queryable, hashes, baselineHashes) {
  if (!(baselineHashes instanceof Set)) {
    throw new Error("Rate-limit cleanup requires an explicit pre-test baseline set.");
  }
  const failures = [];
  for (const hash of hashes) {
    const key = hash.toString("hex");
    if (baselineHashes.has(key)) continue;
    try {
      await queryable.query("DELETE FROM app.auth_rate_limits WHERE key_hash = $1", [hash]);
      const { rows } = await queryable.query(
        "SELECT EXISTS(SELECT 1 FROM app.auth_rate_limits WHERE key_hash = $1) AS exists",
        [hash],
      );
      if (rows[0]?.exists) failures.push("test-owned key remains");
    } catch {
      failures.push("exact-key cleanup or verification failed");
    }
  }
  if (failures.length) throw new Error("E2E rate-limit cleanup or verification failed.");
}

export function registerEmailRateLimitCleanup(test, emails) {
  const clientIP = generatePlaywrightClientIP();
  const hashes = rateLimitCleanupHashes(emails, clientIP);
  const baselineHashes = new Set();
  let pool;
  let armed = false;
  test.beforeAll(async () => {
    const { Pool } = await import("pg");
    pool = new Pool({ connectionString: getTestDatabaseURL(), max: 1 });
    try {
      for (const hash of hashes) {
        const { rows } = await pool.query(
          "SELECT EXISTS(SELECT 1 FROM app.auth_rate_limits WHERE key_hash = $1) AS exists",
          [hash],
        );
        if (rows[0]?.exists) throw new Error("An E2E account rate-limit key already exists; refusing to reuse it.");
      }
      armed = true;
    } catch (error) {
      await pool.end();
      pool = undefined;
      throw error;
    }
  });
  test.afterAll(async () => {
    if (!pool || !armed) return;
    try {
      await cleanupAbsentBaselineRateLimitKeys(pool, hashes, baselineHashes);
    } finally {
      await pool.end();
      pool = undefined;
    }
  });
  return clientIP;
}
