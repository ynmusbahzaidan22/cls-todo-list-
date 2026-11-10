import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";
import { validateTestDatabaseConfiguration } from "./e2e/test-database.js";

if (existsSync(".env")) process.loadEnvFile(".env");

const { testDatabaseURL } = validateTestDatabaseConfiguration(process.env);

function quoteExecutable(value, variableName) {
  const executable = value?.trim() || "go";
  if (/[\0\r\n]/.test(executable)) {
    throw new Error(`${variableName} must name an executable without control characters.`);
  }
  if (process.platform === "win32" && /[&|<>^%!\"]/.test(executable)) {
    throw new Error(`${variableName} contains characters that cannot be safely passed through the Playwright Windows command runner.`);
  }

  if (process.platform === "win32") return `"${executable}"`;
  return `'${executable.replaceAll("'", "'\\''")}'`;
}

function quoteGoExecutable(value) {
  return quoteExecutable(value?.trim() || "go", "GO_EXE");
}

process.env.TEST_DATABASE_URL = testDatabaseURL;

function validateBaseURL(value) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("PHASE8_BASE_URL must be a loopback HTTP origin.");
  }
  const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, "");
  if (parsed.protocol !== "http:"
      || !["127.0.0.1", "localhost", "::1"].includes(host)
      || parsed.username || parsed.password
      || (parsed.pathname !== "/" && parsed.pathname !== "")
      || parsed.search || parsed.hash) {
    throw new Error("PHASE8_BASE_URL must be a loopback HTTP origin without credentials, path, query, or fragment.");
  }
  return parsed.origin;
}

const baseURL = validateBaseURL(process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080");
process.env.PHASE8_BASE_URL = baseURL;
const goExecutable = quoteGoExecutable(process.env.GO_EXE);
const nodeExecutable = quoteExecutable(process.execPath, "Node executable");
const launcherScript = `
  import("node:child_process").then(async ({ spawn }) => {
    const { buildWebServerEnvironment } = await import(${JSON.stringify(new URL("./e2e/test-database.js", import.meta.url).href)});
    const childEnv = buildWebServerEnvironment({
      databaseURL: process.env.DATABASE_URL,
      httpAddr: process.env.HTTP_ADDR,
      trustedOrigin: process.env.TRUSTED_ORIGIN,
    }, process.env);
    const child = spawn(process.argv[1], ["run", "./cmd/api"], { env: childEnv, stdio: "inherit", windowsHide: true });
    child.once("error", () => { process.stderr.write("Could not start the API server process.\\n"); process.exitCode = 1; });
    child.once("exit", (code) => { process.exitCode = code ?? 1; });
  }).catch(() => { process.stderr.write("Could not prepare the API server process.\\n"); process.exitCode = 1; });
`;
const encodedLauncherScript = Buffer.from(launcherScript).toString("base64");
const webServerCommand = `${nodeExecutable} -e "eval(Buffer.from('${encodedLauncherScript}','base64').toString())" ${goExecutable}`;
const explicitBrowser = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 8_000 },
  reporter: [["list"], ["html", { outputFolder: "playwright-report", open: "never" }]],
  outputDir: "test-results",
  use: {
    baseURL,
    browserName: "chromium",
    ...(explicitBrowser
      ? { launchOptions: { executablePath: explicitBrowser } }
      : { channel: process.env.PLAYWRIGHT_BROWSER_CHANNEL || "chrome" }),
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
    viewport: { width: 1440, height: 1000 },
  },
  webServer: {
    command: webServerCommand,
    url: `${baseURL}/health/ready`,
    reuseExistingServer: false,
    timeout: 120_000,
    env: {
      APP_ENV: "development",
      DATABASE_URL: testDatabaseURL,
      SESSION_COOKIE_SECURE: "false",
      HTTP_ADDR: new URL(baseURL).host,
      TRUSTED_ORIGIN: baseURL,
    },
    stdout: "ignore",
    stderr: "pipe",
  },
});
