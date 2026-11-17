// Thin client for the /api/v1 REST contract (docs/API.md).

let csrfToken = '';

export function setCsrfToken(token) {
  csrfToken = token ?? '';
}

export class ApiError extends Error {
  constructor(status, code, message, field) {
    super(message);
    this.status = status;
    this.code = code;
    this.field = field;
  }
}

export async function api(method, path, body) {
  const headers = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;

  let res;
  try {
    res = await fetch(`/api/v1${path}`, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, 'network_error', 'Could not reach the panel. Check your connection.');
  }

  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const e = data?.error ?? {};
    const err = new ApiError(res.status, e.code ?? 'http_error', e.message ?? res.statusText, e.field);
    // A session that expired mid-use sends the app back to the login screen.
    if (res.status === 401 && err.code === 'unauthenticated') {
      window.dispatchEvent(new CustomEvent('session-expired'));
    }
    throw err;
  }
  return data;
}

// upload sends files as multipart/form-data with upload progress (fetch has none).
// onProgress receives a fraction between 0 and 1. Resolves to the JSON body; rejects with ApiError.
export function upload(path, files, onProgress) {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    for (const f of files) form.append('files', f, f.name);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `/api/v1${path}`);
    xhr.setRequestHeader('Accept', 'application/json');
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken);
    xhr.upload.addEventListener('progress', (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded / e.total);
    });
    xhr.addEventListener('load', () => {
      let data = null;
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        // not JSON
      }
      if (xhr.status >= 200 && xhr.status < 300) return resolve(data);
      const e = data?.error ?? {};
      if (xhr.status === 401 && e.code === 'unauthenticated') window.dispatchEvent(new CustomEvent('session-expired'));
      reject(new ApiError(xhr.status, e.code ?? 'http_error', e.message ?? xhr.statusText, e.field));
    });
    xhr.addEventListener('error', () => reject(new ApiError(0, 'network_error', 'The upload failed. Check your connection.')));
    xhr.send(form);
  });
}
