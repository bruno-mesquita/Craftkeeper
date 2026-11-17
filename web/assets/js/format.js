const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

export function bytes(n) {
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export const rate = (bps) => `${bytes(bps)}/s`;

export const percent = (v) => `${v.toFixed(1)}%`;

export function dateTime(iso) {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'medium' });
}
