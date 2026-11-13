import { mkdir } from "node:fs/promises";
import path from "node:path";

export async function captureVisual(page, name) {
  const directory = path.resolve("test-results", "visual-review");
  await mkdir(directory, { recursive: true });
  try {
    await page.screenshot({ path: path.join(directory, name), fullPage: false });
    return true;
  } catch {
    process.stderr.write(`Visual screenshot unavailable: ${name}\n`);
    return false;
  }
}
