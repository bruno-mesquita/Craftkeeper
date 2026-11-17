// Reads the Minecraft version and loader of a server pack from its variables.txt without
// extracting the whole ZIP: only the central directory and that one entry are read.
import { LOADERS } from './server-actions.js';

const MAX_VARIABLES = 1 << 20;
const EOCD_SEARCH = 0x10000 + 22;

// Returns { loader, version, loaderVersion, heapMB } or null when the pack does not say.
export async function detectModpack(zip) {
  try {
    const text = await readVariables(zip);
    return text ? parseVariables(text) : null;
  } catch {
    return null;
  }
}

function parseVariables(text) {
  const vars = {};
  for (const line of text.split(/\r?\n/)) {
    const m = /^\s*([A-Z_]+)=(.*?)\s*$/.exec(line);
    if (m) vars[m[1]] = m[2].replace(/^"(.*)"$/, '$1');
  }
  const loader = (vars.MODLOADER ?? '').toLowerCase();
  const version = vars.MINECRAFT_VERSION ?? '';
  if (!(loader in LOADERS) || loader === 'vanilla' || !/^\d+\.\d+(\.\d+)?$/.test(version)) return null;
  return { loader, version, loaderVersion: vars.MODLOADER_VERSION ?? '', heapMB: heapMB(vars.JAVA_ARGS ?? '') };
}

// -Xmx from JAVA_ARGS ("-Xmx4G -Xms4G") in MB, or 0 when absent.
function heapMB(args) {
  const m = /-Xmx(\d+)([GgMm])/.exec(args);
  if (!m) return 0;
  return Number(m[1]) * (m[2].toLowerCase() === 'g' ? 1024 : 1);
}

async function readVariables(zip) {
  const tailStart = Math.max(0, zip.size - EOCD_SEARCH);
  const tail = new DataView(await zip.slice(tailStart).arrayBuffer());
  let eocd = -1;
  for (let i = tail.byteLength - 22; i >= 0; i--) {
    if (tail.getUint32(i, true) === 0x06054b50) { eocd = i; break; }
  }
  if (eocd < 0) return null;
  const cdSize = tail.getUint32(eocd + 12, true);
  const cdOffset = tail.getUint32(eocd + 16, true);
  if (cdSize === 0xffffffff || cdOffset === 0xffffffff) return null; // ZIP64: ask the user instead

  const cdBuf = await zip.slice(cdOffset, cdOffset + cdSize).arrayBuffer();
  const cd = new DataView(cdBuf);
  const names = new TextDecoder();
  let best = null;
  for (let p = 0; p + 46 <= cd.byteLength && cd.getUint32(p, true) === 0x02014b50;) {
    const nameLen = cd.getUint16(p + 28, true);
    const extraLen = cd.getUint16(p + 30, true);
    const commentLen = cd.getUint16(p + 32, true);
    const name = names.decode(new Uint8Array(cdBuf, p + 46, nameLen));
    if (name.split('/').at(-1) === 'variables.txt' && (!best || name.length < best.name.length)) {
      best = { name, method: cd.getUint16(p + 10, true), size: cd.getUint32(p + 20, true),
        plain: cd.getUint32(p + 24, true), offset: cd.getUint32(p + 42, true) };
    }
    p += 46 + nameLen + extraLen + commentLen;
  }
  if (!best || best.plain > MAX_VARIABLES || best.size > MAX_VARIABLES) return null;

  const local = new DataView(await zip.slice(best.offset, best.offset + 30).arrayBuffer());
  if (local.getUint32(0, true) !== 0x04034b50) return null;
  const start = best.offset + 30 + local.getUint16(26, true) + local.getUint16(28, true);
  const data = zip.slice(start, start + best.size);
  if (best.method === 0) return data.text();
  if (best.method !== 8) return null;
  return new Response(data.stream().pipeThrough(new DecompressionStream('deflate-raw'))).text();
}
