import { isIP } from "node:net";

const POSTGRES_SCHEMES = new Set(["postgres:", "postgresql:"]);
const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1"]);
const TARGET_QUERY_KEYS = new Set(["host", "hostaddr", "port", "database", "dbname", "service"]);
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

  if (testURL) {
    // pgx and node-postgres differ on query precedence, so only this non-target
    // option is shared; all other test URL query parameters are rejected.
    const match = /^\?sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$/.exec(search);
    if (!match || !SSL_MODES.has(match[1])) throw invalidURL(variableName);
    return;
  }

  // DATABASE_URL is used only for a safe target comparison. Reject every
  // target selector even when its spelling is percent-encoded or repeated.
  let params;
  try {
    params = new URLSearchParams(search.slice(1));
    for (const [key] of params) {
      if (TARGET_QUERY_KEYS.has(key.toLowerCase())) throw invalidURL(variableName);
    }
  } catch {
    throw invalidURL(variableName);
  }
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
      || parsed.hash) {
    throw invalidURL(variableName);
  }

  if (testURL && !LOOPBACK_HOSTS.has(target.hostname)) {
    throw new Error("TEST_DATABASE_URL must use a loopback PostgreSQL host.");
  }

  return target;
}

function canonicalHost(hostname) {
  return LOOPBACK_HOSTS.has(hostname) ? "<loopback>" : hostname;
}

function sameDatabaseTarget(left, right) {
  return canonicalHost(left.hostname) === canonicalHost(right.hostname)
    && left.port === right.port
    && left.databaseName === right.databaseName;
}

/** Validate one explicit loopback test URL and its separation from DATABASE_URL. */
export function validateTestDatabaseConfiguration(environment = process.env) {
  const testDatabaseURL = environment.TEST_DATABASE_URL?.trim();
  if (!testDatabaseURL) {
    throw new Error("Set TEST_DATABASE_URL to a dedicated loopback PostgreSQL test database before running E2E tests.");
  }

  const testTarget = parseDatabaseTarget(testDatabaseURL, "TEST_DATABASE_URL", { testURL: true });
  const developmentDatabaseURL = environment.DATABASE_URL?.trim();
  if (developmentDatabaseURL) {
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
  const { databaseURL, httpAddr, trustedOrigin } = configuration;
  if (![databaseURL, httpAddr, trustedOrigin].every((value) => typeof value === "string" && value.length > 0)) {
    throw new Error("The API server requires validated database and HTTP settings.");
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
  };
}
