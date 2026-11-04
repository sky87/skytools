import path from 'node:path';
import { pathToFileURL } from 'node:url';

export async function resolve(specifier, context, nextResolve) {
  try {
    return await nextResolve(specifier, context);
  } catch (error) {
    return nextResolve(specifier, fallbackContext(specifier, context, error));
  }
}

export function resolveSync(specifier, context, nextResolve) {
  try {
    return nextResolve(specifier, context);
  } catch (error) {
    return nextResolve(specifier, fallbackContext(specifier, context, error));
  }
}

function fallbackContext(specifier, context, error) {
  const shared = specifier === 'playwright' || specifier.startsWith('playwright/') ||
    specifier === 'playwright-core' || specifier.startsWith('playwright-core/');
  if (!shared || error.code !== 'ERR_MODULE_NOT_FOUND') throw error;
  const modules = process.env.SKYTOOLS_PLAYWRIGHT_MODULES;
  if (!modules) throw error;
  const parentURL = pathToFileURL(path.join(modules, '..', '__skytools_resolver__.mjs')).href;
  return { ...context, parentURL };
}
