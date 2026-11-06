import { useState } from "react";
import {
  Edit3,
  Monitor,
  Plus,
  Power,
  RefreshCw,
  Terminal,
  Trash2,
} from "lucide-react";
import { api, post, put, workerPayload } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import { duration, formatTime, label, projectHref } from "../utils.js";
import {
  Badge,
  Button,
  DataTable,
  ErrorMessage,
  FormModal,
  Metric,
  PageHeader,
  Panel,
  ResourceState,
  SelectField,
  TextField,
} from "../components/ui.jsx";

export const runtimeFields = [
  ["decide_timeout_seconds", "规划超时（秒）", 1, 86400],
  ["execute_timeout_seconds", "执行超时（秒）", 1, 86400],
  ["conclude_timeout_seconds", "总结超时（秒）", 1, 3600],
  ["conclude_reserve_seconds", "总结预留（秒）", 0, 120],
  ["recovery_attempts", "恢复尝试次数", 1, 10],
  ["format_retries", "格式重试次数", 0, 1],
];
const drivers = ["mock", "command", "pi", "codex", "claude"];

export default function Workers() {
  const observation = useResource("/api/workers/observability", {
    interval: 10000,
  });
  const history = useResource("/api/workers/history", { interval: 15000 });
  const config = useResource("/api/workers/config");
  const { run, notify, confirm } = useApp();
  const [editor, setEditor] = useState(false);
  const [busy, setBusy] = useState(null);
  const [testResult, setTestResult] = useState(null);
  const worker = observation.data?.workers?.[0];
  const refresh = async () => {
    await Promise.all([
      observation.reload(true),
      history.reload(true),
      config.reload(true),
    ]);
  };
  const toggle = async () => {
    const latest = await api("/api/workers/config");
    await put("/api/workers/config", {
      ...workerPayload(
        latest,
        Object.entries(latest.environment || {}).map(([key, value]) => ({
          key,
          value,
        })),
      ),
      enabled: !latest.enabled,
    });
    await refresh();
    notify(latest.enabled ? "Worker 已禁用" : "Worker 已启用");
  };
  const test = async () => {
    setBusy("test");
    await run(async () => {
      setTestResult(await post("/api/workers/config/test"));
    });
    setBusy(null);
  };
  return (
    <>
      <PageHeader
        eyebrow="LOCAL WORKER / 执行与观察"
        title="本地 Worker"
        description="每个项目由 Dispatcher 维护事实图与规划，Agent Worker 并行执行步骤。"
        actions={
          <>
            <Button
              icon={RefreshCw}
              busy={observation.loading}
              onClick={() => refresh()}
            >
              刷新
            </Button>
            <Button
              tone="primary"
              icon={Edit3}
              disabled={!config.data}
              onClick={() => setEditor(true)}
            >
              编辑配置
            </Button>
          </>
        }
      />
      <div className="metrics">
        <Metric
          title="Worker 状态"
          value={worker && label(worker.status)}
          icon={Monitor}
          hint={worker ? `${worker.name} · ${worker.driver}` : ""}
        />
        <Metric
          title="已完成任务"
          value={worker?.completed_tasks}
          icon={Terminal}
          tone="blue"
          hint="当前 Worker 的历史累计"
        />
        <Metric
          title="失败 / 拒绝任务"
          value={worker?.failed_tasks}
          icon={RefreshCw}
          tone="amber"
          hint="含需要恢复的任务"
        />
        <Metric
          title="执行失败的步骤"
          value={observation.data?.summary.attention_steps}
          icon={Monitor}
          tone="red"
          hint={`${observation.data?.summary.summary_pending_steps || 0} 个恢复中步骤`}
        />
      </div>
      <div className="worker-page-grid">
        <Panel
          title="执行状态"
          extra={worker && <Badge value={worker.status} />}
        >
          <ResourceState resource={observation}>
            <div className="worker-detail">
              <span className="worker-glyph">
                <Monitor size={38} />
              </span>
              <h2>{worker?.name}</h2>
              <p>{worker?.current_task || "当前没有执行任务"}</p>
              {worker?.project_id && (
                <a className="text-link" href={projectHref(worker.project_id)}>
                  查看当前项目 · {worker.step_id || "Decide"}
                </a>
              )}
              <dl>
                <dt>执行类型</dt>
                <dd>{worker?.task_type || "—"}</dd>
                <dt>最近心跳</dt>
                <dd>
                  {worker?.last_heartbeat_seconds_ago === null
                    ? "—"
                    : `${duration(worker?.last_heartbeat_seconds_ago)}前`}
                </dd>
                <dt>平均任务时长</dt>
                <dd>{duration(worker?.avg_duration_seconds)}</dd>
                <dt>并发上限</dt>
                <dd>{worker?.worker_count || 1}</dd>
              </dl>
            </div>
            {observation.data?.rejections?.map((item, index) => (
              <div className="notice amber" key={index}>
                {item.project_name || item.project_id} · {item.task_type}{" "}
                拒绝冷却中，剩余 {duration(item.seconds_remaining)}
              </div>
            ))}
          </ResourceState>
        </Panel>
        <Panel
          title="当前配置"
          extra={<Badge tone="muted">SINGLE PROCESS</Badge>}
        >
          <ResourceState resource={config}>
            <dl className="config-summary">
              <dt>驱动</dt>
              <dd>
                <code>{config.data?.driver}</code>
              </dd>
              <dt>命令</dt>
              <dd>
                <code>{config.data?.command || "默认驱动命令"}</code>
              </dd>
              <dt>工作区根目录</dt>
              <dd>
                <code>{config.data?.workspace_root || "服务默认目录"}</code>
              </dd>
              <dt>上下文目录</dt>
              <dd>
                <code>{config.data?.context_root || "未配置"}</code>
              </dd>
              <dt>完成后工作区</dt>
              <dd>
                {config.data?.completed_action === "remove"
                  ? "删除受标记保护的项目工作区"
                  : "保留"}
              </dd>
              <dt>环境变量</dt>
              <dd>
                {Object.keys(config.data?.environment || {}).length}{" "}
                个键（敏感值已掩码）
              </dd>
            </dl>
            <div className="worker-config-actions">
              <Button
                icon={Power}
                onClick={() =>
                  confirm({
                    title: config.data.enabled ? "禁用 Worker" : "启用 Worker",
                    message: config.data.enabled
                      ? "禁用后 Worker 不再接收后续任务。"
                      : "启用本地 Worker 配置。自动调度需要服务启动时带有 --autorun。",
                    danger: false,
                    onConfirm: toggle,
                  })
                }
              >
                {config.data?.enabled ? "禁用 Worker" : "启用 Worker"}
              </Button>
              <Button icon={Terminal} busy={busy === "test"} onClick={test}>
                测试已保存配置
              </Button>
            </div>
            <p className="panel-copy muted">
              测试检查本地命令或 CLI 是否可用；command 驱动会执行 healthcheck。
            </p>
            {testResult && (
              <div className={`notice ${testResult.ok ? "green" : "amber"}`}>
                <Badge tone={testResult.ok ? "green" : "red"}>
                  {testResult.ok ? "检查通过" : "检查未通过"}
                </Badge>
                <p>{testResult.detail}</p>
              </div>
            )}
          </ResourceState>
        </Panel>
      </div>
      <Panel
        title="最近任务"
        extra={<span className="muted">服务返回最近 20 条任务记录</span>}
      >
        <ResourceState resource={history}>
          <DataTable
            columns={[
              "任务",
              "项目",
              "步骤",
              "结果",
              "耗时",
              "开始时间",
              "详情",
            ]}
            count={history.data?.length}
          >
            {history.data?.map((item) => (
              <tr key={item.id}>
                <td>
                  <code>{item.task_type}</code>
                </td>
                <td>
                  {item.project_id ? (
                    <a
                      className="text-link"
                      href={projectHref(item.project_id)}
                    >
                      {item.project_name || item.project_id}
                    </a>
                  ) : (
                    "—"
                  )}
                </td>
                <td>
                  <code>{item.step_id || "—"}</code>
                </td>
                <td>
                  <Badge value={item.outcome || item.status} />
                </td>
                <td>{duration(item.duration_seconds)}</td>
                <td>
                  <time>{formatTime(item.started_at)}</time>
                </td>
                <td>
                  <details className="history-detail">
                    <summary>{item.description ? "查看详情" : "—"}</summary>
                    <pre>{item.detail || item.description}</pre>
                    <small>原始状态：{item.status}</small>
                  </details>
                </td>
              </tr>
            ))}
          </DataTable>
        </ResourceState>
      </Panel>
      {editor && config.data && (
        <WorkerEditor
          initial={config.data}
          onClose={() => setEditor(false)}
          onSaved={async () => {
            notify("Worker 配置已保存");
            setTestResult(null);
            await refresh();
          }}
        />
      )}
    </>
  );
}

function WorkerEditor({ initial, onClose, onSaved }) {
  const { run } = useApp();
  const [draft, setDraft] = useState(() => ({
    ...initial,
    runtime: { ...initial.runtime },
  }));
  const [rows, setRows] = useState(() =>
    Object.entries(initial.environment || {}).map(([key, value]) => ({
      key,
      value,
    })),
  );
  const [showValues, setShowValues] = useState(false);
  const set = (key, value) =>
    setDraft((previous) => ({ ...previous, [key]: value }));
  const changeRow = (index, key, value) =>
    setRows((previous) =>
      previous.map((row, position) =>
        position === index ? { ...row, [key]: value } : row,
      ),
    );
  const toggleShowValues = async (show) => {
    if (!show) {
      setShowValues(false);
      return;
    }
    const revealed = await run(async () => {
      const current = await api("/api/workers/config?reveal=1");
      const environment = current.environment || {};
      setRows((previous) =>
        previous.map((row) =>
          row.value === "***" && Object.hasOwn(environment, row.key)
            ? { ...row, value: environment[row.key] }
            : row,
        ),
      );
      setShowValues(true);
    });
    if (!revealed) setShowValues(false);
  };
  return (
    <FormModal
      title="编辑本地 Worker"
      description="配置保存后用于后续任务；当前执行任务继续使用已读取的配置。"
      wide
      onClose={onClose}
      onSubmit={async () => {
        await put("/api/workers/config", workerPayload(draft, rows));
        await onSaved();
      }}
    >
      <div className="form-grid">
        <TextField
          label="Worker 名称"
          required
          value={draft.name}
          onChange={(event) => set("name", event.target.value)}
        />
        <SelectField
          label="驱动"
          value={draft.driver}
          onChange={(event) => set("driver", event.target.value)}
        >
          {!drivers.includes(draft.driver) && (
            <option value={draft.driver}>{draft.driver}</option>
          )}
          {drivers.map((driver) => (
            <option key={driver} value={driver}>
              {driver}
            </option>
          ))}
        </SelectField>
      </div>
      <TextField
        label="命令"
        value={draft.command}
        onChange={(event) => set("command", event.target.value)}
        placeholder={
          draft.driver === "command"
            ? "输入本地执行命令"
            : "留空使用驱动默认命令"
        }
        required={draft.driver === "command"}
      />
      <TextField
        label="Agent Worker 数量"
        hint="保存后重启后端以应用新的并发数量。"
        type="number"
        min="1"
        max="32"
        step="1"
        required
        value={draft.worker_count ?? 4}
        onChange={(event) => set("worker_count", Number(event.target.value))}
      />
      <div className="form-grid">
        <TextField
          label="工作区根目录"
          value={draft.workspace_root}
          onChange={(event) => set("workspace_root", event.target.value)}
          placeholder="使用服务默认目录"
        />
        <TextField
          label="上下文根目录"
          value={draft.context_root}
          onChange={(event) => set("context_root", event.target.value)}
          placeholder="可选"
        />
      </div>
      <SelectField
        label="项目完成后工作区"
        value={draft.completed_action}
        onChange={(event) => set("completed_action", event.target.value)}
      >
        <option value="keep">保留工作区</option>
        <option value="remove">删除匹配标记的工作区</option>
      </SelectField>
      <label className="checkbox-field">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={(event) => set("enabled", event.target.checked)}
        />
        启用 Worker
      </label>
      <h3 className="section-label">运行参数</h3>
      <div className="form-grid">
        {runtimeFields.map(([key, name, min, max]) => (
          <TextField
            key={key}
            label={name}
            required
            type="number"
            min={min}
            max={max}
            step="1"
            value={draft.runtime[key]}
            onChange={(event) =>
              set("runtime", {
                ...draft.runtime,
                [key]: Number(event.target.value),
              })
            }
          />
        ))}
      </div>
      <div className="section-heading">
        <h3>环境变量</h3>
        <label className="checkbox-field">
          <input
            type="checkbox"
            checked={showValues}
            onChange={(event) => toggleShowValues(event.target.checked)}
          />
          显示输入值
        </label>
      </div>
      <p className="muted">
        显示输入值会读取已保存的敏感环境变量；删除行后，保存会移除对应键。
      </p>
      <div className="environment-rows">
        {rows.map((row, index) => (
          <div key={index}>
            <input
              aria-label={`环境变量 ${index + 1} 名称`}
              placeholder="KEY"
              required
              value={row.key}
              onChange={(event) => changeRow(index, "key", event.target.value)}
            />
            <input
              aria-label={`环境变量 ${index + 1} 值`}
              type={showValues ? "text" : "password"}
              autoComplete="off"
              placeholder="value"
              value={row.value}
              onFocus={(event) => {
                if (row.value === "***") event.target.select();
              }}
              onChange={(event) =>
                changeRow(index, "value", event.target.value)
              }
            />
            <Button
              icon={Trash2}
              aria-label={`删除环境变量 ${row.key || index + 1}`}
              onClick={() =>
                setRows((previous) =>
                  previous.filter((_, position) => position !== index),
                )
              }
            />
          </div>
        ))}
      </div>
      <Button
        icon={Plus}
        onClick={() =>
          setRows((previous) => [...previous, { key: "", value: "" }])
        }
      >
        添加环境变量
      </Button>
    </FormModal>
  );
}
