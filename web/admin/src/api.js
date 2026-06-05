export async function api(path, options = {}) {
  const init = {
    method: options.method || "GET",
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(options.headers || {})
    }
  };
  if (options.body) {
    init.body = JSON.stringify(options.body);
  }
  const response = await fetch(path, init);
  const contentType = response.headers.get("content-type") || "";
  if (response.redirected && response.url.includes("/login")) {
    redirectToLogin();
    return {};
  }
  const payload = contentType.includes("application/json") ? await response.json() : await response.text();
  if (response.status === 401 && shouldRedirectForAuth(payload)) {
    redirectToLogin();
    return {};
  }
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || payload.message || response.statusText;
    throw new Error(message);
  }
  if (payload && payload.status === "error") {
    throw new Error(payload.message || payload.error || "请求失败");
  }
  return payload;
}

function shouldRedirectForAuth(payload) {
  if (typeof payload === "string") {
    const text = payload.toLowerCase();
    return text.includes("login required") || text.includes("authorization required") || text.includes("2fa session required");
  }
  const message = String(payload?.message || payload?.error || "").toLowerCase();
  return message.includes("login required") || message.includes("authorization required") || message.includes("2fa session required");
}

function redirectToLogin() {
  const next = `${window.location.pathname}${window.location.search}`;
  window.location.href = `/login?next=${encodeURIComponent(next)}`;
}

export function queryString(params) {
  const query = new URLSearchParams();
  Object.entries(params).forEach(([key, value]) => {
    if (value !== undefined && value !== null && String(value).trim() !== "") {
      query.set(key, value);
    }
  });
  return query.toString();
}
