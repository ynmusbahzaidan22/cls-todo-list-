// Validate the exact page the Go server assembles, without writing generated HTML.
import { HtmlValidate } from 'html-validate';
import { assembleFrontendPage } from './assemble-frontend-page.mjs';
const frontend = new URL('../frontend/', import.meta.url);
const page = await assembleFrontendPage(frontend);

const report = await new HtmlValidate().validateString(page, 'frontend/pages/index.html');
if (!report.valid) {
  for (const result of report.results) {
    for (const message of result.messages) {
      console.error(`${message.line}:${message.column} ${message.ruleId}: ${message.message}`);
    }
  }
  process.exitCode = 1;
} else {
  console.log('Assembled frontend page is valid HTML.');
}
