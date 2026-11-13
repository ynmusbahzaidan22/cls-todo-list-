import { defineConfig } from "@playwright/test";
import { tmpdir } from "node:os";
import path from "node:path";
import { generatePlaywrightClientIP, validateTestDatabaseConfiguration } from "./tests/e2e/test-database.js";

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
process.env.PLAYWRIGHT_TEST_CLIENT_IP = generatePlaywrightClientIP();
process.env.PLAYWRIGHT_API_PID_FILE = path.join(tmpdir(), `todo-playwright-api-${process.pid}.pid`);
process.env.PLAYWRIGHT_API_STOP_FILE = path.join(tmpdir(), `todo-playwright-api-${process.pid}.stop`);

function validateBaseURL(value) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("PHASE8_BASE_URL must be a loopback HTTP origin.");
  }
  const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, "");
  if (parsed.protocol !== "http:"
      || host !== "127.0.0.1"
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
    const [{ existsSync, mkdtempSync, rmSync, writeFileSync }, { tmpdir }, { join }] = await Promise.all([
      import("node:fs"), import("node:os"), import("node:path"),
    ]);
    const { buildWebServerEnvironment } = await import(${JSON.stringify(new URL("./tests/e2e/test-database.js", import.meta.url).href)});
    const childEnv = buildWebServerEnvironment({
      databaseURL: process.env.DATABASE_URL,
      httpAddr: process.env.HTTP_ADDR,
      trustedOrigin: process.env.TRUSTED_ORIGIN,
      playwrightTestMode: process.env.PLAYWRIGHT_TEST_MODE,
      trustedProxyCIDRs: process.env.TRUSTED_PROXY_CIDRS,
      testClientIP: process.env.PLAYWRIGHT_TEST_CLIENT_IP,
    }, process.env);
    const buildDirectory = mkdtempSync(join(tmpdir(), "todo-api-playwright-"));
    const apiBinary = join(buildDirectory, process.platform === "win32" ? "api.exe" : "api");
    let buildProcess;
    let apiProcess;
    let parentMonitor;
    let stopping = false;
    const removeBuildDirectory = () => {
      if (parentMonitor) clearInterval(parentMonitor);
      process.stdin.pause();
      try { rmSync(buildDirectory, { recursive: true, force: true }); } catch { /* best effort after process exit */ }
      for (const file of [process.env.PLAYWRIGHT_API_PID_FILE, process.env.PLAYWRIGHT_API_STOP_FILE]) {
        if (file) try { rmSync(file, { force: true }); } catch { /* best effort after process exit */ }
      }
    };
    const forwardSignal = (signal) => {
      if (stopping) return;
      stopping = true;
      const child = apiProcess || buildProcess;
      if (!child || child.exitCode !== null || child.signalCode !== null) {
        removeBuildDirectory();
        process.exitCode = 0;
        return;
      }
      if (process.platform === "win32" && child.pid) {
        const killer = spawn("taskkill.exe", ["/PID", String(child.pid), "/T", "/F"], { stdio: "ignore", windowsHide: true });
        killer.once("error", () => child.kill(signal));
        killer.unref();
        return;
      }
      child.kill(signal);
    };
    process.once("SIGINT", () => forwardSignal("SIGINT"));
    process.once("SIGTERM", () => forwardSignal("SIGTERM"));
    process.stdin.on("end", () => forwardSignal("SIGTERM"));
    process.stdin.resume();
    const parentPID = process.ppid;
    parentMonitor = setInterval(() => {
      if (process.env.PLAYWRIGHT_API_STOP_FILE && existsSync(process.env.PLAYWRIGHT_API_STOP_FILE)) {
        forwardSignal("SIGTERM");
        return;
      }
      try { process.kill(parentPID, 0); }
      catch (error) { if (error.code === "ESRCH") forwardSignal("SIGTERM"); }
    }, 250);
    parentMonitor.unref();
    buildProcess = spawn(process.argv[1], ["build", "-o", apiBinary, "./cmd/api"], { env: childEnv, stdio: "inherit", windowsHide: true });
    buildProcess.once("error", () => { process.stderr.write("Could not build the API server process.\\n"); process.exitCode = 1; removeBuildDirectory(); });
    buildProcess.once("exit", (code) => {
      buildProcess = undefined;
      if (code !== 0 || stopping) {
        process.exitCode = stopping ? 0 : (code ?? 1);
        removeBuildDirectory();
        return;
      }
      // Keep the long-running API process from inheriting Playwright's pipe
      // handles. Windows waits for those handles to close before the webServer
      // command exits, even after the test runner kills its process tree.
      apiProcess = spawn(apiBinary, [], { env: childEnv, stdio: "ignore", windowsHide: true });
      try {
        writeFileSync(process.env.PLAYWRIGHT_API_PID_FILE, String(apiProcess.pid), { flag: "wx" });
      } catch {
        forwardSignal("SIGTERM");
        process.stderr.write("Could not register the API server process for cleanup.\\n");
        process.exitCode = 1;
        return;
      }
      apiProcess.once("error", () => { process.stderr.write("Could not start the API server process.\\n"); process.exitCode = 1; removeBuildDirectory(); });
      apiProcess.once("exit", (apiCode) => { apiProcess = undefined; process.exitCode = stopping ? 0 : (apiCode ?? 1); removeBuildDirectory(); });
    });
  }).catch(() => { process.stderr.write("Could not prepare the API server process.\\n"); process.exitCode = 1; });
`;
const encodedLauncherScript = Buffer.from(launcherScript).toString("base64");
const webServerCommand = `${nodeExecutable} -e "eval(Buffer.from('${encodedLauncherScript}','base64').toString())" ${goExecutable}`;
const explicitBrowser = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;

export default defineConfig({
  testDir: "./tests/e2e",
  globalSetup: "./tests/e2e/global-setup.js",
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
    extraHTTPHeaders: { "X-Forwarded-For": process.env.PLAYWRIGHT_TEST_CLIENT_IP },
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
      PLAYWRIGHT_TEST_MODE: "1",
      TRUSTED_PROXY_CIDRS: "127.0.0.1/32",
      PLAYWRIGHT_TEST_CLIENT_IP: process.env.PLAYWRIGHT_TEST_CLIENT_IP,
      PLAYWRIGHT_API_PID_FILE: process.env.PLAYWRIGHT_API_PID_FILE,
      PLAYWRIGHT_API_STOP_FILE: process.env.PLAYWRIGHT_API_STOP_FILE,
    },
    stdout: "ignore",
    stderr: "pipe",
  },
});
