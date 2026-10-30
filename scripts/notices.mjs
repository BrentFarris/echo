import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
process.chdir(join(dirname(fileURLToPath(import.meta.url)), '..'));
let notices = 'Echo third-party notices\nGenerated from the pinned Go modules and bundled browser dependencies. Build/test dependencies are recorded separately in package-lock.json.\n';
function licenses(label, dir) {
  notices += `\n===== ${label} =====\n`;
  for (const name of readdirSync(dir)) if (/^(LICENSE|COPYING|NOTICE)/i.test(name)) {
    try { notices += readFileSync(join(dir, name), 'utf8') + '\n'; } catch { /* License directories have their own bundled files. */ }
  }
}
const modules = execFileSync('go', ['list', '-deps', '-f', '{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}', '.'], { encoding: 'utf8' });
for (const entry of [...new Set(modules.trim().split(/\r?\n/))].sort()) { const [name, version, dir] = entry.split('|'); if (version && dir) licenses(`${name} ${version}`, dir); }
licenses('Go runtime', execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim());
for (const name of ['dompurify', 'marked', 'vite', 'monaco-editor', '@tanstack/virtual-core', '@vscode/codicons', '@xterm/xterm', '@xterm/addon-fit', '@xterm/addon-web-links']) licenses(name, join('web/node_modules', name));
notices += '\n===== Unicode CLDR Windows timezone mapping =====\n' + readFileSync('internal/calendar/UNICODE-LICENSE.txt', 'utf8');
writeFileSync('THIRD-PARTY-NOTICES.txt', notices.replace(/[ \t]+$/gm, ''));
