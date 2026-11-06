export const labels = {
  active: "运行中",
  stopped: "已停止",
  completed: "已完成",
  pending: "待执行",
  working: "执行中",
  concluded: "已结束",
  abandoned: "已放弃",
  critical: "严重",
  high: "高危",
  medium: "中危",
  low: "低危",
  info: "信息",
  confirmed: "已确认",
  ignored: "已忽略",
  failed: "失败",
  idle: "空闲",
  busy: "忙碌",
  offline: "离线",
  disabled: "已禁用",
  ok: "正常",
  warning: "需关注",
  error: "异常",
  achieved: "目标达成",
  in_progress: "推进中",
  blocked: "待推进",
  success: "成功",
  rejected: "已拒绝",
  released: "已释放",
  step_declaration: "声明步骤",
  step_conclusion: "步骤结束",
  fact_discovery: "发现事实",
  goal_completion: "完成目标",
};
export const label = (value) => labels[value] || value || "—";
export const tones = {
  active: "green",
  completed: "blue",
  stopped: "muted",
  pending: "amber",
  working: "blue",
  concluded: "green",
  abandoned: "muted",
  critical: "red",
  high: "orange",
  medium: "amber",
  low: "blue",
  info: "muted",
  confirmed: "green",
  ignored: "muted",
  failed: "red",
  idle: "green",
  busy: "blue",
  offline: "red",
  disabled: "muted",
  ok: "green",
  warning: "amber",
  error: "red",
  achieved: "green",
  in_progress: "blue",
  blocked: "amber",
  success: "green",
  rejected: "amber",
  released: "muted",
};
export const stepStatus = (step) =>
  step.to || step.concluded_at
    ? "concluded"
    : step.abandoned
      ? "abandoned"
      : step.recovery_status === "needs_attention"
        ? "failed"
        : step.worker
          ? "working"
          : "pending";
export const formatTime = (value) =>
  value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
export const shortTime = (value) =>
  value
    ? new Date(value).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })
    : "—";
export const duration = (value) =>
  value === null || value === undefined
    ? "—"
    : value < 60
      ? `${Math.round(value)} 秒`
      : `${Math.floor(value / 60)} 分 ${Math.round(value % 60)} 秒`;
export const truncate = (text, max = 64) =>
  Array.from(text || "").length > max
    ? `${Array.from(text).slice(0, max).join("")}…`
    : text || "";
export const sortSeverity = (a, b) =>
  ["critical", "high", "medium", "low", "info"].indexOf(a.severity) -
  ["critical", "high", "medium", "low", "info"].indexOf(b.severity);
export function readStored(key, fallback) {
  try {
    return localStorage.getItem(`hotmouse:${key}`) || fallback;
  } catch {
    return fallback;
  }
}
export function writeStored(key, value) {
  try {
    localStorage.setItem(`hotmouse:${key}`, value);
  } catch {
    /* Storage is optional. */
  }
}
export function routeFromHash(hash) {
  try {
    const [path, query = ""] = hash.replace(/^#\/?/, "").split("?");
    const parts = path.split("/").map(decodeURIComponent);
    return {
      page: parts[0] || "dashboard",
      id: parts[1],
      params: new URLSearchParams(query),
    };
  } catch {
    return { page: "dashboard", params: new URLSearchParams() };
  }
}
export const projectHref = (id) => `#/projects/${encodeURIComponent(id)}`;
export function notificationHref(link) {
  if (typeof link !== "string") return null;
  if (link.startsWith("#/")) return link;
  if (/^\/projects\/[^/?#]+$/.test(link)) return `#${link}`;
  if (
    [
      "/projects",
      "/settings",
      "/workers",
      "/vulnerabilities",
      "/templates",
      "/audit",
    ].includes(link)
  )
    return `#${link}`;
  return null;
}
export function parseObject(raw) {
  const data = JSON.parse(raw || "{}");
  if (!data || typeof data !== "object" || Array.isArray(data))
    throw new Error("附加数据必须是 JSON 对象。");
  return data;
}
