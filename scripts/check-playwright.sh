#!/bin/sh
set -eu
SKYTOOLS_BIN=${SKYTOOLS_BIN:-skytools}
eval "$("$SKYTOOLS_BIN" env)"
"$SKYTOOLS_BIN" ensure --only playwright-cli
"$SKYTOOLS_BIN" status --only node,playwright,playwright-cli
playwright-cli --version
playwright --version
playwright-node -e 'const p = require("playwright"); const c = require("playwright-core"); if (p.chromium.name() !== "chromium" || c.chromium.name() !== "chromium") process.exit(1); console.log("CommonJS imports work")'
playwright-node --input-type=module -e '
import { chromium, firefox, webkit } from "playwright";
import { chromium as core } from "playwright-core";
if (core.name() !== "chromium") throw new Error("core import failed");
for (const engine of [chromium, firefox, webkit]) {
 const browser = await engine.launch({ headless: true });
 const page = await browser.newPage();
 await page.goto("data:text/html,<title>skytools-script</title>");
 if (await page.title() !== "skytools-script") throw new Error("navigation failed");
 await browser.close();
 console.log(engine.name() + " launched offline from an ES module script");
}'
playwright-cli open 'data:text/html,<title>skytools-cli</title>'
playwright-cli eval 'document.title'
playwright-cli close-all
# Repair a missing helper without changing installed package versions or using
# the network. The upstream installer also refreshes its native browser links.
rm "$HOME/.local/bin/playwright-node"
"$SKYTOOLS_BIN" ensure --only playwright
playwright-node -e 'if (require("playwright").chromium.name() !== "chromium") process.exit(1)'
# A partial npm package must be repairable from its cached, exact version.
rm "$HOME/.local/share/skytools/playwright/lib/node_modules/playwright/cli.js"
npm_config_offline=true "$SKYTOOLS_BIN" ensure --only playwright
mkdir -p /tmp/skytools-project/node_modules/playwright
printf '%s\n' '{"name":"playwright","type":"module","exports":"./index.mjs"}' > /tmp/skytools-project/node_modules/playwright/package.json
printf '%s\n' 'export const source = "project-local";' > /tmp/skytools-project/node_modules/playwright/index.mjs
cd /tmp/skytools-project
playwright-node --input-type=module -e 'import { source } from "playwright"; if (source !== "project-local") process.exit(1); console.log("Project-local module takes precedence")'
test ! -e /tmp/skytools-project/package.json
echo 'Playwright CLI and script checks passed'
