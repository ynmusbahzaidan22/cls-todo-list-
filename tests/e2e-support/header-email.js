import { expect } from "@playwright/test";

export function expectMaskedHeaderEmail(page, email) {
  const value = typeof email === "string" ? email.trim() : "";
  const at = value.lastIndexOf("@");
  let expected = "•••@•••";
  if (at > 0 && at < value.length - 1) {
    const first = Array.from(value.slice(0, at))[0] || "•";
    const domain = value.slice(at + 1);
    const dot = domain.lastIndexOf(".");
    const candidate = dot > 0 ? domain.slice(dot) : "";
    const extension = /^\.[a-z\d-]{1,24}$/i.test(candidate) ? candidate : "";
    expected = `${first}•••@•••${extension}`;
  }
  return expect(page.locator("#signed-in-email")).toHaveText(expected);
}
