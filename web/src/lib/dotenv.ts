import type { EnvVar } from '@/api/types';

const SECRET_HINT = /(SECRET|TOKEN|PASSWORD|PASSWD|PWD|PRIVATE|API_?KEY|ACCESS_?KEY|CREDENTIAL|DSN|CONNECTION_?STRING)/i;

export function looksSecret(name: string): boolean {
  return SECRET_HINT.test(name);
}

/** Parses a .env file. Supports comments, `export`, quotes and escaped newlines in double quotes. */
export function parseDotEnv(text: string): { vars: EnvVar[]; skipped: number } {
  const vars: EnvVar[] = [];
  let skipped = 0;
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  for (let i = 0; i < lines.length; i++) {
    let line = lines[i].trim();
    if (!line || line.startsWith('#')) continue;
    if (line.startsWith('export ')) line = line.slice(7).trim();
    const eq = line.indexOf('=');
    if (eq <= 0) {
      skipped++;
      continue;
    }
    const name = line.slice(0, eq).trim();
    let value = line.slice(eq + 1).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
      skipped++;
      continue;
    }
    const q = value[0];
    if (q === '"' || q === "'" || q === '`') {
      // Multi-line quoted values.
      let rest = value.slice(1);
      while (!endsWithQuote(rest, q) && i + 1 < lines.length) {
        i++;
        rest += '\n' + lines[i];
      }
      const end = rest.lastIndexOf(q);
      value = end >= 0 ? rest.slice(0, end) : rest;
      if (q === '"') value = value.replace(/\\n/g, '\n').replace(/\\r/g, '\r').replace(/\\t/g, '\t').replace(/\\"/g, '"');
    } else {
      const hash = value.search(/\s#/);
      if (hash >= 0) value = value.slice(0, hash).trim();
    }
    vars.push({ name, value, secret: looksSecret(name) });
  }
  return { vars, skipped };
}

function endsWithQuote(s: string, q: string): boolean {
  const idx = s.lastIndexOf(q);
  if (idx < 0) return false;
  // The closing quote may be followed by whitespace or a comment.
  return /^\s*(#.*)?$/.test(s.slice(idx + 1)) && s[idx - 1] !== '\\';
}
