import * as modules from 'node:module';
import { resolveSync } from './resolve.mjs';

if (typeof modules.registerHooks === 'function') {
  modules.registerHooks({ resolve: resolveSync });
} else if (typeof modules.register === 'function') {
  modules.register('./resolve.mjs', import.meta.url);
} else {
  throw new Error('playwright-node needs a newer Node.js; run skytools update --only node');
}
