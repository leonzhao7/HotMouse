export class ApiError extends Error {
  constructor(message, status, payload) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.payload = payload;
  }
}

async function responseError(response) {
  const raw = await response.text();
  let payload;
  try {
    payload = JSON.parse(raw);
  } catch {
    payload = null;
  }
  return new ApiError(
    payload?.detail ||
      payload?.message ||
      `请求失败（HTTP ${response.status}）`,
    response.status,
    payload,
  );
}

export async function api(
  path,
  { method = "GET", body, signal, session = true } = {},
) {
  let response;
  try {
    response = await fetch(path, {
      method,
      signal,
      credentials: "same-origin",
      headers:
        body === undefined
          ? { Accept: "application/json" }
          : { Accept: "application/json", "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (error) {
    if (error.name === "AbortError") throw error;
    throw new ApiError(
      "无法连接服务，请检查后端是否启动及代理地址是否正确。",
      0,
    );
  }
  if (!response.ok) {
    if (response.status === 401 && session)
      globalThis.dispatchEvent?.(new Event("hotmouse:unauthorized"));
    throw await responseError(response);
  }
  if (response.status === 204) return null;
  const raw = await response.text();
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    throw new ApiError(
      "服务返回了无效的 JSON 响应，请检查代理配置。",
      response.status,
    );
  }
}

export const post = (path, body = {}, options = {}) =>
  api(path, { ...options, method: "POST", body });
export const put = (path, body) => api(path, { method: "PUT", body });
export const patch = (path, body) => api(path, { method: "PATCH", body });
export const remove = (path) => api(path, { method: "DELETE" });
export const projectPath = (id) => `/projects/${encodeURIComponent(id)}`;
export const productPath = (id) => `/api/projects/${encodeURIComponent(id)}`;

export function queryString(values) {
  const query = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value !== "" && value !== null && value !== undefined)
      query.set(key, value);
  });
  return query.size ? `?${query}` : "";
}

export async function downloadProject(id, format) {
  let response;
  try {
    response = await fetch(`${projectPath(id)}/export?format=${format}`, {
      credentials: "same-origin",
    });
  } catch {
    throw new ApiError("无法连接服务，导出失败。", 0);
  }
  if (!response.ok) {
    if (response.status === 401)
      globalThis.dispatchEvent?.(new Event("hotmouse:unauthorized"));
    throw await responseError(response);
  }
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = `hotmouse-${id}-${format}.${format === "yaml" ? "yaml" : "txt"}`;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// Configuration PUTs require the existing complete configuration. Masked values
// retain secrets; explicit removals must never also appear in environment.
export function workerPayload(config, rows) {
  const environment = {};
  const keys = new Set();
  for (const row of rows) {
    const key = row.key.trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key))
      throw new Error(
        "环境变量名必须由字母、数字和下划线组成，且不能以数字开头。",
      );
    if (keys.has(key)) throw new Error(`环境变量 ${key} 重复。`);
    if (row.value === "***" && !Object.hasOwn(config.environment || {}, key))
      throw new Error(`环境变量 ${key} 使用了未知的掩码，请输入新值。`);
    keys.add(key);
    environment[key] = row.value;
  }
  return {
    name: config.name,
    driver: config.driver,
    command: config.command,
    enabled: config.enabled,
    workspace_root: config.workspace_root,
    context_root: config.context_root,
    completed_action: config.completed_action,
    worker_count: config.worker_count,
    runtime: config.runtime,
    environment,
    environment_remove: Object.keys(config.environment || {}).filter(
      (key) => !keys.has(key),
    ),
  };
}
