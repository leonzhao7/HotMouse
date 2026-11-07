import { useState } from "react";
import { Database, RefreshCw, Save, Settings, Trash2 } from "lucide-react";
import { api, post, put } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import { formatTime } from "../utils.js";
import {
  Badge,
  Button,
  ErrorMessage,
  PageHeader,
  Panel,
  ResourceState,
  TextField,
} from "../components/ui.jsx";

const groups = [
  [
    "项目预算",
    "新建项目自动保存此预算，所有路线共同消耗项目额度；已有项目保留创建时的预算。",
    [["default_project_budget_seconds", "默认项目预算（秒，0为不限）", 0]],
  ],
  [
    "租约与重试",
    "控制步骤租约、规划租约和失败后的重试间隔。",
    [
      ["step_timeout", "步骤租约超时（秒）", 5],
      ["decide_timeout", "规划租约超时（秒）", 5],
      [
        "worker_unhealthy_retry_after_seconds",
        "Worker 不可用重试（秒）",
        1,
        3600,
      ],
      ["worker_rejected_retry_after_seconds", "任务拒绝重试（秒）", 1, 3600],
    ],
  ],
  [
    "登录与会话",
    "调整失败登录限制与会话有效时间。",
    [
      ["max_failed_login_attempts", "最大失败登录次数", 1, 50],
      ["rate_limit_window_minutes", "登录限流窗口（分钟）", 1, 1440],
      ["session_duration_hours", "会话有效期（小时）", 1, 168],
    ],
  ],
  [
    "保留与提醒",
    "清理历史记录时使用以下保留天数。",
    [
      ["log_retention_days", "日志保留（天）", 1, 3650],
      ["notification_retention_days", "已读通知保留（天）", 1, 3650],
      ["project_idle_alert_hours", "项目闲置提醒（小时）", 1, 720],
    ],
  ],
];

export default function SystemSettings() {
  const settings = useResource("/api/settings");
  const health = useResource("/api/settings/health", { interval: 30000 });
  const { confirm, notify } = useApp();
  const [cleanup, setCleanup] = useState(null);
  return (
    <>
      <PageHeader
        eyebrow="SYSTEM / 服务与策略"
        title="系统设置"
        description="查看本地服务健康，管理项目预算、租约、会话与历史记录。"
        actions={
          <Button
            icon={RefreshCw}
            busy={health.loading}
            onClick={() => health.reload()}
          >
            检查健康
          </Button>
        }
      />
      <div className="settings-layout">
        <ResourceState resource={settings}>
          {settings.data && (
            <SettingsForm
              key={settings.updated?.getTime()}
              initial={settings.data}
              onSaved={async () => {
                notify("设置已保存");
                await settings.reload();
                await health.reload(true);
              }}
            />
          )}
        </ResourceState>
        <div>
          <Panel
            title="服务健康"
            extra={health.data && <Badge value={health.data.summary.status} />}
          >
            <ResourceState resource={health}>
              <div className="health-checks">
                {health.data?.checks.map((check) => (
                  <article key={check.key}>
                    <header>
                      <strong>
                        {{
                          server: "API 服务",
                          database: "SQLite 数据库",
                          dispatcher: "本地 Worker",
                          auth: "认证策略",
                          retention: "数据保留",
                        }[check.key] || check.label}
                      </strong>
                      <Badge value={check.status} />
                    </header>
                    <p>{check.summary}</p>
                    <small>{check.detail}</small>
                  </article>
                ))}
              </div>
              {health.data?.alerts?.map((alert, index) => (
                <div className="notice amber" key={index}>
                  <strong>{alert.title}</strong>
                  <p>{alert.detail}</p>
                </div>
              ))}
            </ResourceState>
          </Panel>
          <Panel title="历史记录清理" extra={<Database size={17} />}>
            <p className="panel-copy">
              按已保存的保留策略删除过期会话、旧审计、Worker
              历史、登录尝试及旧已读通知。
            </p>
            <div className="panel-action">
              <Button
                icon={Trash2}
                onClick={() =>
                  confirm({
                    title: "清理历史记录",
                    message:
                      "按当前已保存的策略永久删除过期历史记录。未读通知和项目数据会保留。",
                    confirmLabel: "执行清理",
                    onConfirm: async () => {
                      setCleanup(await post("/api/settings/cleanup"));
                      await health.reload(true);
                      notify("历史记录清理已完成");
                      window.dispatchEvent(new Event("hotmouse:notifications"));
                    },
                  })
                }
              >
                执行清理
              </Button>
            </div>
            {cleanup && (
              <div className="cleanup-result">
                <small>{formatTime(cleanup.ran_at)}</small>
                <dl>
                  {Object.entries(cleanup.deleted).map(([key, count]) => (
                    <div key={key}>
                      <dt>
                        {{
                          expired_sessions: "过期会话",
                          audit_log: "审计记录",
                          worker_task_history: "任务历史",
                          login_attempts: "登录尝试",
                          notifications: "已读通知",
                        }[key] || key}
                      </dt>
                      <dd>{count} 条</dd>
                    </div>
                  ))}
                </dl>
              </div>
            )}
          </Panel>
        </div>
      </div>
    </>
  );
}
function SettingsForm({ initial, onSaved }) {
  const [values, setValues] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const dirty = Object.keys(values).some((key) => values[key] !== initial[key]);
  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const latest = await api("/api/settings");
      const changes = Object.fromEntries(
        Object.entries(values).filter(([key, value]) => value !== initial[key]),
      );
      await put("/api/settings", { ...latest, ...changes });
      await onSaved();
    } catch (failure) {
      setError(failure);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="settings-form">
      <ErrorMessage error={error} />
      {groups.map(([name, description, fields]) => (
        <Panel key={name} title={name}>
          <p className="panel-copy muted">{description}</p>
          <div className="form-grid settings-fields">
            {fields.map(([key, title, min, max]) => (
              <TextField
                key={key}
                label={title}
                type="number"
                required
                step="1"
                min={min}
                max={max}
                value={values[key]}
                onChange={(event) =>
                  setValues((previous) => ({
                    ...previous,
                    [key]: Number(event.target.value),
                  }))
                }
              />
            ))}
          </div>
        </Panel>
      ))}
      <div className="settings-save">
        <span className="muted">
          {dirty ? "有未保存的更改" : "设置已与服务同步"}
        </span>
        <Button
          type="submit"
          tone="primary"
          icon={Save}
          busy={busy}
          disabled={!dirty}
        >
          保存设置
        </Button>
      </div>
    </form>
  );
}
