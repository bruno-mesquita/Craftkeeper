// Old console logs (logs/*.log, logs/*.log.gz) read in the browser through the file API:
// gzip is decompressed with the native DecompressionStream, streamed, keeping only the tail.
// Also the line filter shared by the live console and the log viewer.
import { api } from './api.js';

const MAX_LINES = 20000; // newest lines kept from a file
const MAX_CHARS = 64 * 1024 * 1024; // decompressed text read at most (characters, ~bytes)
const LOG_FILE = /\.log(\.gz)?$/;

// listLogs returns the log files of the server, latest.log first, then newest first.
export async function listLogs(id) {
  const { entries } = await api('GET', `/servers/${id}/files?path=logs`);
  return entries
    .filter((e) => e.type === 'file' && LOG_FILE.test(e.name))
    .sort((a, b) => (a.name === 'latest.log' ? -1 : b.name === 'latest.log' ? 1 : b.modified_at.localeCompare(a.modified_at)));
}

// readLog streams logs/<name> and resolves to { lines, truncated }: the last MAX_LINES lines of
// at most MAX_CHARS decompressed characters. truncated is true when anything was left out.
export async function readLog(id, name, signal) {
  const res = await fetch(`/api/v1/servers/${id}/files/download?path=${encodeURIComponent(`logs/${name}`)}`,
    { credentials: 'same-origin', signal });
  if (!res.ok) {
    const data = await res.json().catch(() => null);
    throw new Error(data?.error?.message ?? res.statusText);
  }
  let stream = res.body;
  if (name.endsWith('.gz')) stream = stream.pipeThrough(new DecompressionStream('gzip'));
  const reader = stream.pipeThrough(new TextDecoderStream()).getReader();

  const lines = [];
  let partial = '';
  let read = 0;
  let truncated = false;
  const keep = (newLines) => {
    lines.push(...newLines);
    if (lines.length > MAX_LINES) {
      lines.splice(0, lines.length - MAX_LINES);
      truncated = true;
    }
  };
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      read += value.length;
      const parts = (partial + value).split(/\r?\n/);
      partial = parts.pop();
      keep(parts);
      if (read > MAX_CHARS) {
        truncated = true;
        break;
      }
    }
  } finally {
    reader.cancel().catch(() => {});
  }
  if (partial) keep([partial]);
  return { lines, truncated };
}

// filterLines hides the line elements of container that do not contain query (case-insensitive)
// and returns how many match. An empty query shows everything.
export function filterLines(container, query) {
  const q = query.toLowerCase();
  let matches = 0;
  for (const el of container.children) {
    const ok = matchesLine(el, q);
    el.hidden = !ok;
    if (ok) matches++;
  }
  return matches;
}

export const matchesLine = (el, q) => q === '' || el.textContent.toLowerCase().includes(q);
