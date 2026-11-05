// Module hooks that let `node --test` import Svelte components, as Vite
// would: npm test preloads this file with --import. They
//
//   - compile .svelte files with svelte/compiler (client, dev mode, CSS
//     left out), and .svelte.ts/.svelte.js modules with compileModule,
//     stripping TypeScript types first;
//   - resolve extensionless relative imports (`from './state'`) from web/src
//     to the .ts or .svelte.ts file, as Vite's resolver does;
//   - resolve `svelte` with the browser condition, so that mount and
//     flushSync are the client's, not the server's.
//
// Component tests also need a DOM: import ./dom.ts first (see web/README.md).
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { compile, compileModule } from 'svelte/compiler';

const src = new URL('../src/', import.meta.url).href;

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === 'svelte' || specifier.startsWith('svelte/')) {
      return nextResolve(specifier, {
        ...context,
        conditions: ['browser', ...context.conditions],
      });
    }
    const parent = context.parentURL ?? '';
    if (parent.startsWith(src) && specifier.startsWith('.') && !/\.[cm]?[jt]s$|\.svelte$/.test(specifier)) {
      for (const ext of ['.ts', '.svelte.ts', '.js']) {
        try {
          return nextResolve(specifier + ext, context);
        } catch {
          // Try the next extension.
        }
      }
    }
    return nextResolve(specifier, context);
  },

  load(url, context, nextLoad) {
    if (url.endsWith('.svelte')) {
      const filename = fileURLToPath(url);
      const { js } = compile(readFileSync(filename, 'utf8'), {
        filename,
        generate: 'client',
        dev: true,
        css: 'external',
      });
      return { format: 'module', source: js.code, shortCircuit: true };
    }
    if (/\.svelte\.[jt]s$/.test(url)) {
      const filename = fileURLToPath(url);
      let source = readFileSync(filename, 'utf8');
      if (url.endsWith('.ts')) source = stripTypeScriptTypes(source);
      const { js } = compileModule(source, { filename, generate: 'client', dev: true });
      return { format: 'module', source: js.code, shortCircuit: true };
    }
    return nextLoad(url, context);
  },
});
