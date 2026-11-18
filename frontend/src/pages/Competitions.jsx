import { useState } from "react";
import {
  CheckCheck,
  CircleX,
  Clock3,
  Play,
  RefreshCw,
  Settings2,
  Square,
  Trophy,
} from "lucide-react";
import { patch, post, put } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource, useRoute } from "../hooks.js";
import { duration, formatTime, projectHref } from "../utils.js";
import {
  Badge,
  Button,
  DataTable,
  Empty,
  ErrorMessage,
  FormModal,
  Metric,
  Modal,
  PageHeader,
  Panel,
  ResourceState,
  SearchInput,
  SelectField,
  TextField,
} from "../components/ui.jsx";

const states = {
  planning: ["解读规则中", "amber"],
  running: ["运行中", "blue"],
  stopping: ["停止并清理中", "amber"],
  completed: ["已结束", "green"],
  stopped: ["已停止", "muted"],
  queued: ["待开始", "muted"],
  starting: ["启动题目中", "amber"],
  closing: ["清理中", "amber"],
  succeeded: ["已解出", "green"],
  failed: ["失败", "red"],
  skipped: ["已跳过", "muted"],
};
const activeRun = (run) =>
  ["planning", "running", "stopping"].includes(run.status);
const activeItem = (item) =>
  ["starting", "running", "closing"].includes(item.status);
const platformPath = (id) => `/api/platforms/${encodeURIComponent(id)}`;
const runPath = (run) =>
  `${platformPath(run.platform)}/runs/${encodeURIComponent(run.id)}`;
const maxConcurrency = (platform) =>
  Math.min(32, platform?.capabilities?.max_active_instances || 32);

function Status({ value }) {
  const [name, tone] = states[value] || [value, "muted"];
  return <Badge tone={tone}>{name}</Badge>;
}

async function request(action) {
  try {
    return await action();
  } catch (error) {
    error.message = competitionErrorMessage(error);
    throw error;
  }
}

function competitionErrorMessage(error) {
  const messages = {
    platform_not_configured: "请先配置所选平台的地址和任务令牌。",
    platform_task_active:
      "请先停止该平台的活跃比赛和项目，再更换地址或任务令牌。",
    competition_scheduler_disabled:
      "当前服务尚未启用自动调度，请在服务端启用后再启动比赛。",
    platform_task_changed: "当前平台配置与这场比赛的任务不一致，请恢复原配置。",
    concurrency_limit: "题目并发数超过了该平台的上限。",
    competition_not_started: "比赛尚未开赛，开赛后刷新题目列表。",
    competition_not_in_session: "比赛已结束，当前无法获取可解答的题目。",
    competition_not_found:
      "当前令牌看不到该比赛，请检查 competition_id 是否正确。",
    competition_selection_required:
      "无法自动确定赛道，请填写已报名比赛的 competition_id。",
    not_registered: "当前账号尚未报名该比赛，请先在平台完成报名。",
    token_competition_forbidden:
      "当前令牌无权访问该比赛，请检查 competition_id 和令牌所属比赛。",
    token_missing_scope: "令牌缺少参赛权限，CTFy 需要 agent_play 令牌。",
    http_404:
      "平台接口不存在，请检查 URL 是否为 API 根地址（CTFy 通常以 /api/v1 结尾）。",
    platform_transport_error: "无法读取平台响应，请检查 API 根地址和网络连接。",
    instance_list_incomplete:
      "平台报告有运行中靶机，但当前令牌无法列出实例详情；请使用能查看该队伍实例的令牌后再启动比赛。",
  };
  if (messages[error.payload?.code]) return messages[error.payload.code];
  if (error.status === 401 && error.payload?.code)
    return "平台令牌认证失败，请检查 Token 是否正确、是否已过期。";
  return error.message;
}

export default function Competitions() {
  const route = useRoute();
  const platforms = useResource("/api/platforms", { interval: 10000 });
  const scheduler = useResource("/api/competitions/status", {
    interval: 10000,
  });
  const workers = useResource("/api/workers/config", { interval: 10000 });
  const platformID =
    route.params.get("platform") || platforms.data?.[0]?.id || "";
  const platform = platforms.data?.find((entry) => entry.id === platformID);
  const runs = useResource(
    platform ? `${platformPath(platformID)}/runs` : null,
    {
      interval: 5000,
    },
  );
  const challenges = useResource(
    platform?.configured ? `${platformPath(platformID)}/challenges` : null,
    { interval: platformID === "ctfy" ? 15000 : 0 },
  );
  const selectedID = route.params.get("run");
  const selectedRun = selectedID
    ? runs.data?.find((run) => run.id === selectedID)
    : runs.data?.at(-1);
  const [modal, setModal] = useState(null);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState(null);
  const { notify, confirm } = useApp();
  const navigate = (id, run = "") => {
    const params = new URLSearchParams({ platform: id });
    if (run) params.set("run", run);
    location.hash = `/competitions?${params}`;
  };
  const refresh = async () => {
    await Promise.all([
      platforms.reload(true),
      runs.reload(true),
      scheduler.reload(true),
      workers.reload(true),
      challenges.reload(),
    ]);
  };
  const hasActiveRun = runs.data?.some(activeRun);
  const instanceListIncomplete =
    platformID === "ctfy" &&
    challenges.data?.some(
      (challenge) => challenge.metadata?.instance_visibility === "incomplete",
    );
  const dispatcher = workers.data?.dispatcher || workers.data;
  const mock =
    (workers.data?.driver === "mock" && !workers.data.command) ||
    (dispatcher?.driver === "mock" && !dispatcher.command);
  const startCTFy = async () => {
    setStarting(true);
    setStartError(null);
    try {
      const run = await request(() => post(`${platformPath("ctfy")}/runs`, {}));
      notify("CTFy 比赛已启动，正在接管运行中的题目并依次解题。");
      navigate("ctfy", run.id);
      await runs.reload(true);
    } catch (error) {
      setStartError(error);
    } finally {
      setStarting(false);
    }
  };
  const stop = (run) =>
    confirm({
      title: "停止比赛",
      message: `停止 ${run.id}？当前解题将取消，活跃容器会自动清理，未开始的题目将跳过。`,
      confirmLabel: "停止比赛",
      onConfirm: async () => {
        await request(() => post(`${runPath(run)}/stop`));
        notify("已请求停止比赛，正在清理活跃题目。");
        await runs.reload(true);
      },
    });
  return (
    <>
      <PageHeader
        eyebrow="COMPETITIONS / 比赛调度"
        title="比赛"
        description={
          platformID === "ctfy"
            ? "按 CTFy 平台接口接管题目、解题、提交答案并关闭靶机。"
            : "选择平台和规则，自动解题、提交 flag，并跟踪整场进度。"
        }
        actions={
          <>
            <Button
              icon={RefreshCw}
              busy={runs.loading || challenges.loading}
              onClick={refresh}
            >
              刷新
            </Button>
            <Button
              icon={Play}
              tone="primary"
              busy={starting}
              disabled={
                starting ||
                !platform?.configured ||
                !scheduler.data?.scheduler_enabled ||
                !runs.data ||
                hasActiveRun ||
                instanceListIncomplete
              }
              onClick={
                platformID === "ctfy" ? startCTFy : () => setModal("start")
              }
            >
              启动比赛
            </Button>
          </>
        }
      />
      <ResourceState resource={platforms}>
        {platforms.data?.length === 0 ? (
          <Empty icon={Trophy} title="尚未接入比赛平台" />
        ) : (
          <Panel title="比赛平台">
            <div className="competition-toolbar">
              <SelectField
                label="平台"
                value={platformID}
                onChange={(event) => {
                  setStartError(null);
                  navigate(event.target.value);
                }}
              >
                {!platform && <option value={platformID}>平台不存在</option>}
                {platforms.data?.map((entry) => (
                  <option key={entry.id} value={entry.id}>
                    {entry.name}
                  </option>
                ))}
              </SelectField>
              {platform && (
                <>
                  <Badge tone={platform.configured ? "green" : "amber"}>
                    {platform.configured ? "已配置" : "待配置"}
                  </Badge>
                  <Button icon={Settings2} onClick={() => setModal("config")}>
                    配置平台
                  </Button>
                  <span className="muted">
                    {platform.id === "ctfy"
                      ? "题目并发受赛道限制"
                      : `题目并发上限 ${maxConcurrency(platform)}`}
                    {platform.capabilities.requires_vpn &&
                      " · 靶场访问需要 VPN"}
                  </span>
                </>
              )}
            </div>
            {platform && !platform.configured && (
              <div className="notice amber">
                {platform.id === "ctfy"
                  ? "配置平台 API 地址、agent_play 令牌和比赛编号后即可启动。"
                  : "配置平台地址和任务令牌后，即可上传规则并启动比赛。"}
              </div>
            )}
          </Panel>
        )}
      </ResourceState>
      <ErrorMessage error={startError} />
      {platform?.configured && (
        <ChallengeList key={platformID} resource={challenges} />
      )}
      <ErrorMessage error={scheduler.error} onRetry={scheduler.reload} />
      {scheduler.data && !scheduler.data.scheduler_enabled && (
        <div className="notice amber">
          当前服务未启用自动调度，请以 --autorun 启动服务。使用 ./start.sh
          会默认启用。
        </div>
      )}
      {workers.data && !workers.data.enabled && (
        <div className="notice amber">
          Worker 已禁用，请在 <a href="#/workers">本地 Worker</a>{" "}
          中启用后再运行比赛。
        </div>
      )}
      {mock && (
        <div className="notice amber">
          当前 Worker 或 Dispatcher 使用模拟驱动。真实解题请先在{" "}
          <a href="#/workers">本地 Worker</a> 中配置模型驱动和凭据。
        </div>
      )}
      {platform && (
        <ResourceState resource={runs}>
          {runs.data?.length > 0 ? (
            <>
              <div className="competition-toolbar competition-run-picker">
                <SelectField
                  label="比赛记录"
                  value={selectedRun?.id || selectedID || ""}
                  onChange={(event) => navigate(platformID, event.target.value)}
                >
                  {!selectedRun && (
                    <option value={selectedID || ""}>比赛不存在</option>
                  )}
                  {[...(runs.data || [])].reverse().map((run) => (
                    <option key={run.id} value={run.id}>
                      {run.id} · {states[run.status]?.[0] || run.status} ·{" "}
                      {formatTime(run.created_at)}
                    </option>
                  ))}
                </SelectField>
                <span className="muted">每 5 秒自动刷新</span>
                {hasActiveRun && (
                  <span className="muted">该平台已有活跃比赛</span>
                )}
              </div>
              {selectedRun ? (
                <RunDetail
                  key={selectedRun.id}
                  run={selectedRun}
                  platform={platform}
                  onStop={() => stop(selectedRun)}
                  onConcurrency={() => setModal("concurrency")}
                />
              ) : (
                <Empty title="找不到这场比赛，请选择其他比赛记录" />
              )}
            </>
          ) : (
            <Empty
              icon={Trophy}
              title="还没有比赛记录"
              description={
                platform.id === "ctfy"
                  ? "点击“启动比赛”即可接管 CTFy 题目并查看解题进度。"
                  : "点击“启动比赛”填写规则，启动后可在这里查看解题进度。"
              }
            />
          )}
        </ResourceState>
      )}
      {modal === "config" && platform && (
        <PlatformConfig
          key={platformID}
          platform={platform}
          onClose={() => setModal(null)}
          onSaved={async () => {
            notify("平台配置已保存，正在加载题目。");
            await platforms.reload(true);
            challenges.reload();
          }}
        />
      )}
      {modal === "start" && platform && platform.id !== "ctfy" && (
        <StartCompetition
          key={platformID}
          platform={platform}
          onClose={() => setModal(null)}
          onStarted={async (run) => {
            notify("比赛已启动。");
            navigate(platformID, run.id);
            await runs.reload(true);
          }}
        />
      )}
      {modal === "concurrency" && selectedRun && (
        <ConcurrencyEditor
          run={selectedRun}
          platform={platform}
          onClose={() => setModal(null)}
          onSaved={async () => {
            notify("题目并发数已更新。");
            await runs.reload(true);
          }}
        />
      )}
    </>
  );
}

function ChallengeList({ resource }) {
  const [query, setQuery] = useState("");
  const visibleRunningCount = (resource.data || []).filter(
    (challenge) =>
      challenge.instance_status === "ready" ||
      challenge.instance_status === "available",
  ).length;
  const incomplete = resource.data?.some(
    (challenge) => challenge.metadata?.instance_visibility === "incomplete",
  );
  const runningCount = incomplete
    ? resource.data.find(
        (challenge) => challenge.metadata?.instance_visibility === "incomplete",
      ).metadata.team_running_instances
    : visibleRunningCount;
  const rows = (resource.data || []).filter((challenge) =>
    `${challenge.id} ${challenge.title || ""} ${challenge.description || ""} ${challenge.metadata?.category || ""} ${challenge.difficulty || ""}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  return (
    <Panel
      title="题目列表"
      extra={
        <Button
          icon={RefreshCw}
          busy={resource.loading}
          onClick={() => resource.reload()}
        >
          刷新题目
        </Button>
      }
    >
      <ResourceState
        resource={{
          ...resource,
          error: resource.error && {
            message: competitionErrorMessage(resource.error),
          },
        }}
      >
        {!resource.error &&
          (resource.data?.length > 0 ? (
            <>
              <div className="competition-toolbar">
                <SearchInput
                  value={query}
                  onChange={setQuery}
                  placeholder="搜索题目编号、名称、描述或分类"
                />
                <span className="muted">共 {resource.data.length} 道题</span>
                <span className="muted">运行中靶机 {runningCount} 个</span>
              </div>
              {incomplete && (
                <div className="notice amber">
                  CTFy 报告当前队伍有 {runningCount}{" "}
                  台运行中靶机，但此令牌的实例列表没有返回详情，暂时无法确定对应题目。请核对令牌所属队伍和实例读取权限。
                </div>
              )}
              <DataTable
                columns={["题目", "靶机", "分类", "难度", "答题进度", "状态"]}
                count={rows.length}
              >
                {rows.map((challenge) => (
                  <tr key={challenge.id}>
                    <td>
                      {challenge.title && <strong>{challenge.title}</strong>}
                      <div>
                        <code>{challenge.id}</code>
                      </div>
                      {challenge.description && (
                        <details className="competition-description">
                          <summary>题目描述</summary>
                          <pre>{challenge.description}</pre>
                        </details>
                      )}
                    </td>
                    <td>
                      <Badge
                        tone={
                          challenge.instance_status === "ready" ||
                          challenge.instance_status === "available"
                            ? "blue"
                            : challenge.instance_status === "starting" ||
                                challenge.instance_status === "pending" ||
                                challenge.instance_status === "stopping"
                              ? "amber"
                              : "muted"
                        }
                      >
                        {{
                          ready: "运行中",
                          available: "运行中",
                          starting: "启动中",
                          pending: "启动中",
                          stopping: "关闭中",
                          stopped: "未启动",
                          unknown: "待核实",
                        }[challenge.instance_status] ||
                          challenge.instance_status ||
                          "—"}
                      </Badge>
                    </td>
                    <td>{challenge.metadata?.category || "—"}</td>
                    <td>{challenge.difficulty || "—"}</td>
                    <td>
                      {challenge.flag_count > 0
                        ? `${challenge.correct_flag_count || 0} / ${challenge.flag_count}`
                        : "—"}
                    </td>
                    <td>
                      <Badge
                        tone={
                          challenge.completed
                            ? "green"
                            : challenge.correct_flag_count > 0
                              ? "amber"
                              : "muted"
                        }
                      >
                        {challenge.completed
                          ? "已解出"
                          : challenge.correct_flag_count > 0
                            ? "部分完成"
                            : "未解出"}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </DataTable>
            </>
          ) : (
            <Empty
              icon={Trophy}
              title="当前没有可解答的题目"
              description="确认平台已发布题目且当前账号有访问权限，再刷新题目列表。"
            />
          ))}
      </ResourceState>
    </Panel>
  );
}

function RunDetail({ run, platform, onStop, onConcurrency }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("");
  const items = run.items || [];
  const count = (status) =>
    items.filter((item) => item.status === status).length;
  const succeeded = count("succeeded"),
    failed = count("failed"),
    skipped = count("skipped");
  const finished = succeeded + failed + skipped;
  const running = items.filter(activeItem).length;
  const rows = items.filter(
    (item) =>
      (!filter || item.status === filter) &&
      `${item.challenge_id} ${item.project_id || ""} ${item.initial_description || ""}`
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  return (
    <>
      <div className="metrics">
        <Metric
          title="题目总数"
          value={items.length}
          icon={Trophy}
          hint={`${finished} 已处理 · ${skipped} 已跳过`}
        />
        <Metric
          title="已解出"
          value={succeeded}
          icon={CheckCheck}
          hint="平台确认所有 flag 通过"
        />
        <Metric
          title="失败"
          value={failed}
          icon={CircleX}
          tone="red"
          hint="包含解题失败和超时"
        />
        <Metric
          title="进行中"
          value={running}
          icon={Clock3}
          tone="blue"
          hint={`${count("queued")} 待开始 · ${run.concurrency} 题并发`}
        />
      </div>
      <Panel
        title="比赛进度"
        className="competition-detail"
        extra={
          <div className="row-actions">
            <Status value={run.status} />
            {["planning", "running"].includes(run.status) && (
              <Button icon={Settings2} onClick={onConcurrency}>
                调整并发
              </Button>
            )}
            {activeRun(run) && run.status !== "stopping" && (
              <Button icon={Square} tone="danger" onClick={onStop}>
                停止比赛
              </Button>
            )}
          </div>
        }
      >
        <div className="competition-summary">
          <div className="competition-progress-label">
            <strong>{run.id}</strong>
            <span>
              已处理 {finished} / {items.length}
            </span>
          </div>
          <progress
            aria-label="题目处理进度"
            max={items.length || 1}
            value={finished}
          />
          <p className="muted">
            {platform.name} · 每题最多 {run.max_workers} 个 Worker · 每题时限{" "}
            {duration(run.challenge_timeout_seconds)} · 开始于{" "}
            {formatTime(run.created_at)}
            {run.finished_at && ` · 结束于 ${formatTime(run.finished_at)}`}
          </p>
          {run.last_error && (
            <div className="notice amber">{run.last_error}</div>
          )}
          {(run.rules || run.rules_summary) && (
            <details className="competition-rules">
              <summary>比赛规则与解读</summary>
              {run.rules_summary && <p>{run.rules_summary}</p>}
              <pre>{run.rules}</pre>
            </details>
          )}
        </div>
        <div className="competition-toolbar">
          <SearchInput
            value={query}
            onChange={setQuery}
            placeholder="搜索题目、项目编号或初始描述"
          />
          <SelectField
            label="题目状态"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          >
            <option value="">全部</option>
            {[
              "queued",
              "starting",
              "running",
              "closing",
              "succeeded",
              "failed",
              "skipped",
            ].map((state) => (
              <option key={state} value={state}>
                {states[state][0]}
              </option>
            ))}
          </SelectField>
        </div>
        <DataTable
          columns={[
            "顺序",
            "题目",
            "状态",
            "项目",
            "剩余时限",
            "开始 / 结束",
            "结果 / 错误",
          ]}
          count={rows.length}
        >
          {rows.map((item) => (
            <tr
              key={item.challenge_id}
              className={activeItem(item) ? "competition-active-row" : ""}
            >
              <td>{item.ordinal + 1}</td>
              <td>
                <code>{item.challenge_id}</code>
                {item.initial_description ? (
                  <details className="competition-description">
                    <summary>初始描述</summary>
                    <pre>{item.initial_description}</pre>
                  </details>
                ) : (
                  <small className="muted competition-description-empty">
                    尚未获得初始描述
                  </small>
                )}
              </td>
              <td>
                <Status value={item.status} />
              </td>
              <td>
                {item.project_id ? (
                  <a className="text-link" href={projectHref(item.project_id)}>
                    {item.project_id}
                  </a>
                ) : (
                  "—"
                )}
              </td>
              <td>
                {["starting", "running"].includes(item.status) &&
                item.started_at
                  ? duration(
                      Math.max(
                        0,
                        Math.ceil(
                          (Date.parse(item.started_at) +
                            run.challenge_timeout_seconds * 1000 -
                            Date.now()) /
                            1000,
                        ),
                      ),
                    )
                  : "—"}
              </td>
              <td>
                <time>{formatTime(item.started_at)}</time>
                <br />
                <time>{formatTime(item.finished_at)}</time>
              </td>
              <td className="competition-result">
                {item.outcome && <Status value={item.outcome} />}
                {item.last_error ? (
                  <p>{item.last_error}</p>
                ) : (
                  !item.outcome && "—"
                )}
              </td>
            </tr>
          ))}
        </DataTable>
      </Panel>
    </>
  );
}

function StartCompetition({ platform, onClose, onStarted }) {
  const workerConfig = useResource("/api/workers/config");
  const settings = useResource("/api/settings");
  const [rules, setRules] = useState("");
  const [filename, setFilename] = useState("");
  const [fileError, setFileError] = useState(null);
  const [reading, setReading] = useState(false);
  const [concurrency, setConcurrency] = useState("");
  const [maxWorkers, setMaxWorkers] = useState("");
  const [minutes, setMinutes] = useState(20);
  const upload = async (event) => {
    const file = event.target.files?.[0];
    if (!file) return;
    setReading(true);
    setFileError(null);
    try {
      if (file.size > 65536) throw new Error("规则文件不能超过 64 KiB。");
      const text = new TextDecoder("utf-8", { fatal: true }).decode(
        await file.arrayBuffer(),
      );
      setRules(text);
      setFilename(file.name);
    } catch (error) {
      setFileError(
        new Error(
          error instanceof TypeError
            ? "规则文件需要使用 UTF-8 编码。"
            : error.message,
        ),
      );
    } finally {
      setReading(false);
      event.target.value = "";
    }
  };
  return (
    <FormModal
      title="启动比赛"
      description={`在 ${platform.name} 上运行整场比赛。规则将用于总调度和每道题的解题。`}
      wide
      submitLabel="启动比赛"
      disabled={reading || !rules.trim()}
      onClose={onClose}
      onSubmit={async () => {
        if (new TextEncoder().encode(rules).length > 65536)
          throw new Error("比赛规则不能超过 64 KiB。");
        const run = await request(() =>
          post(`${platformPath(platform.id)}/runs`, {
            rules,
            ...(concurrency === "" ? {} : { concurrency: Number(concurrency) }),
            ...(maxWorkers === "" ? {} : { max_workers: Number(maxWorkers) }),
            challenge_timeout_seconds: Math.round(Number(minutes) * 60),
          }),
        );
        await onStarted(run);
      }}
    >
      <TextField
        label="规则文件"
        type="file"
        accept=".md,.txt,text/plain,text/markdown"
        onChange={upload}
        hint={
          filename
            ? `已读取：${filename}`
            : "选择本机的 rules.md 或文本文件，也可以在下方直接填写。"
        }
      />
      <ErrorMessage error={fileError} />
      <TextField
        label="比赛规则"
        multiline
        rows={9}
        required
        value={rules}
        onChange={(event) => setRules(event.target.value)}
        placeholder="填写比赛范围、解题规则、flag 要求等。"
      />
      <div className="form-grid">
        <TextField
          label="同时解答题目数"
          type="number"
          min={1}
          max={maxConcurrency(platform)}
          step={1}
          value={concurrency}
          placeholder={String(
            Math.min(
              workerConfig.data?.project_concurrency || 1,
              maxConcurrency(platform),
            ),
          )}
          onChange={(event) => setConcurrency(event.target.value)}
          hint={`留空使用系统默认值；该平台最多 ${maxConcurrency(platform)} 道题并发。`}
        />
        <TextField
          label="每题 Worker 上限"
          type="number"
          min={1}
          max={32}
          step={1}
          value={maxWorkers}
          placeholder={String(settings.data?.default_project_max_workers ?? "")}
          onChange={(event) => setMaxWorkers(event.target.value)}
          hint="留空使用系统配置的每题 Worker 上限。"
        />
        <TextField
          label="每题超时（分钟）"
          type="number"
          required
          min={1 / 60}
          max={1440}
          step="any"
          value={minutes}
          onChange={(event) => setMinutes(event.target.value)}
          hint="默认 20 分钟，包含题目准备与解题。"
        />
      </div>
    </FormModal>
  );
}

function PlatformConfig({ platform, onClose, onSaved }) {
  const config = useResource(`${platformPath(platform.id)}/config`);
  if (config.data)
    return (
      <PlatformConfigEditor
        initial={config.data}
        platform={platform}
        onClose={onClose}
        onSaved={onSaved}
      />
    );
  return (
    <Modal title={`配置平台 · ${platform.name}`} onClose={onClose}>
      <div className="modal-body">
        <ResourceState resource={config} />
      </div>
    </Modal>
  );
}

function PlatformConfigEditor({ initial, platform, onClose, onSaved }) {
  const [baseURL, setBaseURL] = useState(initial.base_url || "");
  const [token, setToken] = useState("");
  const [competitionID, setCompetitionID] = useState(
    initial.competition_id || "",
  );
  return (
    <FormModal
      title={`配置平台 · ${platform.name}`}
      description="配置保存在当前服务的数据库中，保存后生效。"
      onClose={onClose}
      onSubmit={async () => {
        await request(() =>
          put(`${platformPath(platform.id)}/config`, {
            base_url: baseURL,
            ...(token ? { token } : {}),
            ...(platform.id === "ctfy"
              ? { competition_id: competitionID.trim() }
              : {}),
          }),
        );
        await onSaved();
      }}
    >
      <TextField
        label="平台地址"
        type="url"
        required
        maxLength={4096}
        value={baseURL}
        onChange={(event) => setBaseURL(event.target.value)}
        placeholder={
          platform.id === "ctfy"
            ? "https://ctfy.example/api/v1"
            : "https://平台地址"
        }
        hint={
          platform.id === "ctfy"
            ? "填写 CTFy API 根地址；多个已报名赛道可在下方指定 competition_id。"
            : undefined
        }
      />
      <TextField
        label={platform.id === "ctfy" ? "agent_play 参赛令牌" : "任务令牌"}
        type="password"
        autoComplete="new-password"
        required={!initial.token_set}
        maxLength={8192}
        value={token}
        onChange={(event) => setToken(event.target.value)}
        placeholder={
          initial.token_set
            ? "已配置，留空保留原令牌"
            : platform.id === "ctfy"
              ? "输入 CTFy agent_play 令牌"
              : "输入平台下发的任务令牌"
        }
        hint={
          platform.id === "ctfy"
            ? "令牌需包含 agent_play 权限。已有活跃比赛时，请先停止再更换凭据。"
            : "已有活跃比赛时，请先停止比赛再更换任务凭据。"
        }
      />
      {platform.id === "ctfy" && (
        <TextField
          label="competition_id（可选）"
          type="text"
          maxLength={256}
          value={competitionID}
          onChange={(event) => setCompetitionID(event.target.value)}
          placeholder="留空时自动选择唯一已报名赛道"
          hint="有多个可见赛道时填写平台返回的比赛编号。修改已有比赛的赛道前，请先停止比赛。"
        />
      )}
    </FormModal>
  );
}

function ConcurrencyEditor({ run, platform, onClose, onSaved }) {
  const [concurrency, setConcurrency] = useState(run.concurrency);
  return (
    <FormModal
      title="调整题目并发"
      onClose={onClose}
      onSubmit={async () => {
        await request(() =>
          patch(runPath(run), { concurrency: Number(concurrency) }),
        );
        await onSaved();
      }}
    >
      <TextField
        label="同时解答题目数"
        type="number"
        required
        min={1}
        max={maxConcurrency(platform)}
        step={1}
        value={concurrency}
        onChange={(event) => setConcurrency(event.target.value)}
        hint={
          platform.id === "ctfy"
            ? "减少并发会等待已有题目结束；赛道的实例上限由平台验证。"
            : "减少并发会等待已有题目结束；正在执行的题目会继续。"
        }
      />
    </FormModal>
  );
}
