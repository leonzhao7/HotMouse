import { lazy, Suspense, useState } from "react";
import {
  ArrowLeft,
  Check,
  Clock3,
  Download,
  Edit3,
  FileSearch,
  GitBranch,
  Layers,
  Lightbulb,
  List,
  Pause,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Settings2,
  ShieldAlert,
  Target,
  Trash2,
  X,
} from "lucide-react";
import {
  downloadProject,
  patch,
  post,
  productPath,
  projectPath,
  put,
  remove,
} from "../api.js";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import {
  formatTime,
  label,
  parseObject,
  stepStatus,
  truncate,
} from "../utils.js";
import {
  Badge,
  Button,
  DataTable,
  Empty,
  FactPicker,
  FormModal,
  IconButton,
  Loading,
  PageHeader,
  Panel,
  ResourceState,
  SearchInput,
  SelectField,
  TextField,
} from "../components/ui.jsx";
import Strategy, { resultLabels } from "./Strategy.jsx";
import StepResponse from "../components/StepResponse.jsx";
const Graph = lazy(() => import("../components/Graph.jsx"));

const tabs = [
  ["graph", "关系图", GitBranch],
  ["goals", "目标", Target],
  ["steps", "步骤", List],
  ["facts", "事实", Layers],
  ["findings", "发现", ShieldAlert],
  ["hints", "提示", Lightbulb],
  ["timeline", "时间线", Clock3],
  ["campaign", "项目综述", FileSearch],
  ["routes", "路线与止损", GitBranch],
  ["history", "历史检索", FileSearch],
  ["experiences", "经验库", Lightbulb],
  ["flags", "flag 台账", ShieldAlert],
];

export default function Workspace({ id }) {
  const detail = useResource(projectPath(id), { interval: 10000 });
  const { user, notify, confirm, run } = useApp();
  const [tab, setTab] = useState("graph");
  const [selection, setSelection] = useState(null);
  const [modal, setModal] = useState(null);
  const [busy, setBusy] = useState(null);
  const [query, setQuery] = useState("");
  const timeline = useResource(
    tab === "timeline" ? `${productPath(id)}/timeline` : null,
    { interval: 15000 },
  );
  const campaign = useResource(
    tab === "campaign" ? `${productPath(id)}/campaign` : null,
    { interval: 15000 },
  );
  const data = detail.data;
  const base = projectPath(id);
  const active = data?.project.status === "active";
  const selected =
    selection &&
    data?.[
      `${selection.type === "finding" ? "finding" : selection.type}s`
    ]?.find((item) => item.id === selection.id);
  const refresh = async () => {
    await detail.reload(true);
    if (tab === "timeline") await timeline.reload(true);
    if (tab === "campaign") await campaign.reload(true);
  };
  const action = async (key, work, message) => {
    setBusy(key);
    await run(async () => {
      await work();
      await refresh();
    }, message);
    setBusy(null);
  };
  const choose = (type, item) => setSelection({ type, id: item.id });
  const matches = (item) =>
    `${item.id} ${item.description || item.title || item.content || ""}`
      .toLowerCase()
      .includes(query.toLowerCase());
  const edit = (kind, item = null) => setModal({ kind, item });
  return (
    <ResourceState resource={detail}>
      {data && (
        <>
          <a href="#/projects" className="back-link">
            <ArrowLeft size={14} />
            返回项目
          </a>
          <PageHeader
            eyebrow="PROJECT WORKSPACE / 项目工作区"
            title={
              <>
                {data.project.title}
                <Badge value={data.project.status} />
              </>
            }
            description={<code>{id}</code>}
            actions={
              <>
                <Button
                  icon={RefreshCw}
                  busy={detail.loading}
                  onClick={() => refresh()}
                >
                  刷新
                </Button>
                <Button icon={Edit3} onClick={() => edit("title")}>
                  重命名
                </Button>
                <Button
                  icon={Settings2}
                  onClick={() => edit("worker-limit")}
                  disabled={!!busy}
                >
                  Worker 上限 {data.project.max_workers || 1}
                </Button>
                {data.project.status === "active" && (
                  <Button
                    icon={Pause}
                    disabled={!!busy}
                    onClick={() =>
                      confirm({
                        title: "停止项目",
                        message:
                          "停止后将释放尚未结束的步骤租约，项目数据会保留。",
                        danger: false,
                        onConfirm: async () => {
                          await put(`${base}/status`, { status: "stopped" });
                          await refresh();
                          notify("项目已停止");
                        },
                      })
                    }
                  >
                    停止
                  </Button>
                )}
                {data.project.status === "stopped" && (
                  <Button
                    icon={Play}
                    tone="primary"
                    busy={busy === "resume"}
                    onClick={() =>
                      action(
                        "resume",
                        () => put(`${base}/status`, { status: "active" }),
                        "项目已恢复",
                      )
                    }
                  >
                    恢复运行
                  </Button>
                )}
                {data.project.status === "completed" && (
                  <Button
                    icon={RotateCcw}
                    tone="primary"
                    onClick={() => edit("reopen")}
                  >
                    重新打开
                  </Button>
                )}
                <details className="dropdown">
                  <summary className="button">
                    <Download size={15} />
                    导出
                  </summary>
                  <div>
                    <Button
                      onClick={() =>
                        action(
                          "export",
                          () => downloadProject(id, "yaml"),
                          "YAML 已导出",
                        )
                      }
                      disabled={!!busy}
                    >
                      项目 YAML
                    </Button>
                    <Button
                      onClick={() =>
                        action(
                          "export",
                          () => downloadProject(id, "timeline"),
                          "时间线已导出",
                        )
                      }
                      disabled={!!busy}
                    >
                      原始时间线
                    </Button>
                  </div>
                </details>
              </>
            }
          />
          <div className="workspace-summary">
            <div>
              <Layers size={15} />
              <strong>{data.facts.length}</strong> 事实
            </div>
            <div>
              <Target size={15} />
              <strong>
                {
                  data.goals.filter((goal) => goal.status === "completed")
                    .length
                }
                /{data.goals.length}
              </strong>{" "}
              目标完成
            </div>
            <div>
              <List size={15} />
              <strong>
                {
                  data.steps.filter((step) => stepStatus(step) === "pending")
                    .length
                }
              </strong>{" "}
              待执行
            </div>
            <div>
              <Play size={15} />
              <strong>
                {
                  data.steps.filter((step) => stepStatus(step) === "working")
                    .length
                }
              </strong>{" "}
              执行中
            </div>
            <div>
              <ShieldAlert size={15} />
              <strong>{data.findings.length}</strong> 发现
            </div>
            <span className="workspace-created">
              创建于 {formatTime(data.project.created_at)}
            </span>
          </div>
          <div className="workspace-lease">
            <span
              className={`status-dot ${data.project.decide ? "blue" : "muted"}`}
            />
            <span>
              {data.project.decide
                ? `Decide 由 ${data.project.decide.worker} 认领 · ${data.project.decide.trigger}`
                : "Decide 当前无租约"}
            </span>
            {active && (
              <Button className="subtle" onClick={() => edit("decide")}>
                {data.project.decide ? "管理租约" : "手动认领"}
              </Button>
            )}
          </div>
          <div className="workspace-toolbar">
            <div
              className="workspace-tabs"
              role="tablist"
              aria-label="项目视图"
            >
              {tabs.map(([key, name, Icon]) => (
                <button
                  key={key}
                  role="tab"
                  aria-selected={tab === key}
                  className={tab === key ? "active" : ""}
                  onClick={() => {
                    setTab(key);
                    setQuery("");
                  }}
                >
                  <Icon size={15} />
                  {name}
                  {["goals", "steps", "facts", "findings", "hints"].includes(
                    key,
                  ) && <small>{data[key].length}</small>}
                </button>
              ))}
            </div>
            <div className="row-actions">
              <Button
                icon={Plus}
                disabled={!active}
                onClick={() => edit("goal")}
              >
                目标
              </Button>
              <Button
                tone="primary"
                icon={Plus}
                disabled={!active}
                onClick={() => edit("step")}
              >
                步骤
              </Button>
            </div>
          </div>
          {!active && (
            <div className="notice">
              {data.project.status === "stopped"
                ? "项目已停止，恢复运行后可继续添加目标、步骤和发现。"
                : "项目已完成，重新打开后可继续推进。"}
            </div>
          )}
          {busy?.startsWith("execute:") && (
            <div className="notice blue">
              Worker 正在执行步骤并生成结果，请等待服务返回。
            </div>
          )}
          <div
            className={`workspace-content ${selection && selected ? "with-inspector" : ""}`}
          >
            <section
              className="workspace-view"
              role="tabpanel"
              aria-label={tabs.find(([key]) => key === tab)?.[1]}
            >
              {tab === "graph" ? (
                <Suspense fallback={<Loading text="正在加载关系图…" />}>
                  <Graph
                    detail={data}
                    selection={selection}
                    onSelect={setSelection}
                  />
                </Suspense>
              ) : (
                <>
                  {![
                    "timeline",
                    "campaign",
                    "routes",
                    "history",
                    "experiences",
                    "flags",
                  ].includes(tab) && (
                    <div className="workspace-list-toolbar">
                      <SearchInput
                        value={query}
                        onChange={setQuery}
                        placeholder="搜索当前视图"
                      />
                      {tab === "hints" && (
                        <Button
                          icon={Plus}
                          disabled={!active}
                          onClick={() => edit("hint")}
                        >
                          添加提示
                        </Button>
                      )}
                      {tab === "findings" && (
                        <Button
                          icon={Plus}
                          disabled={!active}
                          onClick={() => edit("finding")}
                        >
                          记录发现
                        </Button>
                      )}
                    </div>
                  )}
                  {tab === "goals" && (
                    <div className="entity-list">
                      {data.goals.filter(matches).map((goal) => (
                        <button
                          key={goal.id}
                          className={`entity-card ${goal.parent_goal_id ? "child" : ""} ${selection?.id === goal.id ? "selected" : ""}`}
                          onClick={() => choose("goal", goal)}
                        >
                          <div>
                            <code>{goal.id}</code>
                            <Badge
                              tone={
                                goal.status === "completed" ? "green" : "blue"
                              }
                            >
                              {goal.status === "completed"
                                ? "已完成"
                                : "待完成"}
                            </Badge>
                            <span className="muted">P{goal.priority}</span>
                            {goal.parent_goal_id && (
                              <small>父目标 {goal.parent_goal_id}</small>
                            )}
                          </div>
                          <p>{goal.description}</p>
                          {goal.completed_at && (
                            <small>
                              完成于 {formatTime(goal.completed_at)}
                            </small>
                          )}
                        </button>
                      ))}
                      {!data.goals.filter(matches).length && (
                        <Empty title="暂无匹配目标" />
                      )}
                    </div>
                  )}
                  {tab === "steps" && (
                    <DataTable
                      columns={[
                        "编号 / 步骤",
                        "状态",
                        "依据",
                        "目标",
                        "Worker",
                        "优先级",
                        "路线 / 结果",
                        "LLM 返回",
                      ]}
                      count={data.steps.filter(matches).length}
                    >
                      {data.steps.filter(matches).map((step) => (
                        <tr
                          key={step.id}
                          className={
                            selection?.id === step.id ? "selected" : ""
                          }
                        >
                          <td>
                            <button
                              className="cell-link"
                              onClick={() => choose("step", step)}
                            >
                              <code>{step.id}</code>
                              <span>{step.description}</span>
                            </button>
                          </td>
                          <td>
                            <Badge value={stepStatus(step)} />
                          </td>
                          <td>
                            <code>{step.from.join(", ")}</code>
                          </td>
                          <td>
                            <code>{step.goal_id || "—"}</code>
                          </td>
                          <td>{step.worker || "—"}</td>
                          <td>
                            <code>P{step.priority}</code>
                          </td>
                          <td>
                            <code>{step.route_id || "—"}</code>
                            <br />
                            {resultLabels[step.result?.type] || "—"}
                          </td>
                          <td>
                            <StepResponse step={step} />
                          </td>
                        </tr>
                      ))}
                    </DataTable>
                  )}
                  {tab === "facts" && (
                    <div className="entity-list">
                      {data.facts.filter(matches).map((fact) => (
                        <button
                          className={`entity-card ${selection?.id === fact.id ? "selected" : ""}`}
                          key={fact.id}
                          onClick={() => choose("fact", fact)}
                        >
                          <div>
                            <code>{fact.id}</code>
                            <Badge
                              tone={fact.id === "origin" ? "blue" : "green"}
                            >
                              {fact.id === "origin" ? "初始事实" : "结果事实"}
                            </Badge>
                          </div>
                          <p>{fact.description}</p>
                        </button>
                      ))}
                      {!data.facts.filter(matches).length && (
                        <Empty title="暂无匹配事实" />
                      )}
                    </div>
                  )}
                  {tab === "findings" && (
                    <div className="entity-list">
                      {data.findings.filter(matches).map((finding) => (
                        <button
                          className={`entity-card ${selection?.id === finding.id ? "selected" : ""}`}
                          key={finding.id}
                          onClick={() => choose("finding", finding)}
                        >
                          <div>
                            <code>{finding.id}</code>
                            <Badge value={finding.severity} />
                            <small>{finding.kind}</small>
                          </div>
                          <h3>{finding.title}</h3>
                          <p>{truncate(finding.description, 250)}</p>
                        </button>
                      ))}
                      {!data.findings.filter(matches).length && (
                        <Empty
                          title="暂无发现"
                          description="从结果事实中记录发现，绑定证据的漏洞会汇总到漏洞页面。"
                        />
                      )}
                    </div>
                  )}
                  {tab === "hints" && (
                    <div className="entity-list">
                      {data.hints.filter(matches).map((hint) => (
                        <article className="entity-card" key={hint.id}>
                          <div>
                            <code>{hint.id}</code>
                            <span className="muted">
                              {hint.creator} · {formatTime(hint.created_at)}
                            </span>
                          </div>
                          <p>{hint.content}</p>
                        </article>
                      ))}
                      {!data.hints.filter(matches).length && (
                        <Empty
                          title="暂无提示"
                          description="为后续计划提供补充信息或执行方向。"
                        />
                      )}
                    </div>
                  )}
                  {tab === "timeline" && (
                    <ResourceState resource={timeline}>
                      <div className="timeline">
                        {timeline.data?.map((event) => (
                          <article key={event.id}>
                            <span className="timeline-dot" />
                            <div>
                              <header>
                                <Badge tone="blue">
                                  {label(event.event_type)}
                                </Badge>
                                <code>{event.node_id}</code>
                                <time>{formatTime(event.timestamp)}</time>
                              </header>
                              <p>{event.description}</p>
                              <small>{event.actor || "system"}</small>
                            </div>
                          </article>
                        ))}
                        {!timeline.data?.length && (
                          <Empty
                            title="暂无时间线事件"
                            description="声明和完成步骤后会产生事件。"
                          />
                        )}
                      </div>
                    </ResourceState>
                  )}
                  {["routes", "history", "experiences", "flags"].includes(
                    tab,
                  ) && (
                    <Strategy
                      key={tab}
                      mode={tab}
                      detail={data}
                      base={base}
                      refresh={refresh}
                    />
                  )}
                  {tab === "campaign" && (
                    <ResourceState resource={campaign}>
                      <Campaign data={campaign.data} />
                    </ResourceState>
                  )}
                </>
              )}
            </section>
            {selected && (
              <Inspector
                selection={selection}
                item={selected}
                data={data}
                active={active}
                onClose={() => setSelection(null)}
                onSelect={setSelection}
                edit={edit}
                action={action}
                busy={busy}
                base={base}
                refresh={refresh}
              />
            )}
          </div>
          {modal && (
            <WorkspaceModal
              key={`${modal.kind}:${modal.item?.id || ""}`}
              {...modal}
              detail={data}
              base={base}
              actor={user.username}
              onClose={() => setModal(null)}
              onSaved={async () => {
                notify("操作已完成");
                await refresh();
              }}
            />
          )}
        </>
      )}
    </ResourceState>
  );
}

function Campaign({ data }) {
  if (!data) return null;
  return (
    <div className="campaign">
      <div className="campaign-lead">
        <div className="eyebrow">CAMPAIGN SYNTHESIS</div>
        <Badge value={data.goal_status} />
        <h2>{data.lead}</h2>
        <p>{data.summary}</p>
      </div>
      <div className="campaign-columns">
        <Panel title="关键信号">
          {data.top_findings?.map((item, index) => (
            <article className="campaign-signal" key={index}>
              <code>{item.source_id}</code>
              <h3>{item.title}</h3>
              <p>{item.summary}</p>
              <Badge tone={item.confidence === "confirmed" ? "green" : "amber"}>
                {{
                  confirmed: "已确认",
                  supported: "有依据",
                  tentative: "待验证",
                }[item.confidence] || item.confidence}
              </Badge>
            </article>
          ))}
          {!data.top_findings?.length && <Empty title="暂无关键信号" />}
        </Panel>
        <div>
          <Panel title="主要阻塞">
            {data.blockers?.length ? (
              <ul>
                {data.blockers.map((text, index) => (
                  <li key={index}>{text}</li>
                ))}
              </ul>
            ) : (
              <p className="panel-copy muted">当前没有识别到阻塞事实。</p>
            )}
          </Panel>
          <Panel title="后续方向">
            <ul>
              {data.next_steps?.map((text, index) => (
                <li key={index}>{text}</li>
              ))}
            </ul>
          </Panel>
          <Panel title="开放步骤">
            {data.open_steps?.length ? (
              <ul>
                {data.open_steps.map((text, index) => (
                  <li key={index}>{text}</li>
                ))}
              </ul>
            ) : (
              <p className="panel-copy muted">没有开放步骤。</p>
            )}
          </Panel>
        </div>
      </div>
    </div>
  );
}

function Inspector({
  selection,
  item,
  data,
  active,
  onClose,
  onSelect,
  edit,
  action,
  busy,
  base,
  refresh,
}) {
  const { confirm, notify } = useApp();
  const { type } = selection;
  const ref = (kind, id) => (
    <button
      className="ref-link"
      key={`${kind}:${id}`}
      onClick={() => onSelect({ type: kind, id })}
    >
      <code>{id}</code>
      <span>
        {truncate(
          data[`${kind}s`]?.find((item) => item.id === id)?.description || "",
          80,
        )}
      </span>
    </button>
  );
  const state = type === "step" ? stepStatus(item) : null;
  const referencedGoal =
    type === "goal" &&
    (data.goals.some((goal) => goal.parent_goal_id === item.id) ||
      data.steps.some((step) => step.goal_id === item.id));
  return (
    <aside className="inspector">
      <header>
        <div>
          <span className="eyebrow">
            {
              { fact: "事实", step: "步骤", goal: "目标", finding: "发现" }[
                type
              ]
            }
            详情
          </span>
          <code>{item.id}</code>
        </div>
        <IconButton icon={X} label="关闭详情" onClick={onClose} />
      </header>
      <div className="inspector-body">
        {state && <Badge value={state} />}
        {type === "goal" && (
          <Badge tone={item.status === "completed" ? "green" : "blue"}>
            {item.status === "completed" ? "已完成" : "待完成"}
          </Badge>
        )}
        {type === "finding" && (
          <>
            <Badge value={item.severity} />
            <h3>{item.title}</h3>
          </>
        )}
        <p className="inspector-description">{item.description}</p>
        {item.title && type === "fact" && <h3>{item.title}</h3>}
        {(item.result?.type || item.result_type) && (
          <Badge tone="blue">
            {resultLabels[item.result?.type || item.result_type]}
          </Badge>
        )}
        {(item.detail || item.result?.detail) && (
          <p className="inspector-description">
            {item.detail || item.result.detail}
          </p>
        )}
        {item.result?.evidence && (
          <pre className="block-code">
            {JSON.stringify(item.result.evidence, null, 2)}
          </pre>
        )}
        {type === "step" && (
          <>
            <h4>LLM 返回</h4>
            <StepResponse step={item} />
            <dl>
              <dt>优先级</dt>
              <dd>P{item.priority}</dd>
              <dt>路线</dt>
              <dd>{item.route_id || "—"}</dd>
              <dt>创建人</dt>
              <dd>{item.creator}</dd>
              <dt>Worker</dt>
              <dd>{item.worker || "尚未认领"}</dd>
              <dt>创建时间</dt>
              <dd>{formatTime(item.created_at)}</dd>
              <dt>最近心跳</dt>
              <dd>{formatTime(item.last_heartbeat_at)}</dd>
              {item.concluded_at && (
                <>
                  <dt>结束时间</dt>
                  <dd>{formatTime(item.concluded_at)}</dd>
                </>
              )}
            </dl>
            <h4>依据事实</h4>
            {item.from.map((id) => ref("fact", id))}
            {item.goal_id && (
              <>
                <h4>关联目标</h4>
                {ref("goal", item.goal_id)}
              </>
            )}
            {item.to && (
              <>
                <h4>结果事实</h4>
                {ref("fact", item.to)}
              </>
            )}
          </>
        )}
        {type === "goal" && (
          <>
            <dl>
              <dt>优先级</dt>
              <dd>P{item.priority}</dd>
              <dt>创建时间</dt>
              <dd>{formatTime(item.created_at)}</dd>
              {item.completed_at && (
                <>
                  <dt>完成时间</dt>
                  <dd>{formatTime(item.completed_at)}</dd>
                  <dt>完成人</dt>
                  <dd>{item.completed_by}</dd>
                </>
              )}
            </dl>
            {item.parent_goal_id && (
              <>
                <h4>父目标</h4>
                {ref("goal", item.parent_goal_id)}
              </>
            )}
            {item.from.length > 0 && (
              <>
                <h4>完成依据</h4>
                {item.from.map((id) => ref("fact", id))}
                <p>{item.completion_description}</p>
              </>
            )}
          </>
        )}
        {type === "fact" && (
          <>
            <h4>生成步骤</h4>
            {data.steps
              .filter((step) => step.to === item.id)
              .map((step) => ref("step", step.id))}
            {item.id === "origin" && (
              <p className="muted">项目创建时提供的初始事实。</p>
            )}
            <h4>后续步骤</h4>
            {data.steps
              .filter((step) => step.from.includes(item.id))
              .map((step) => ref("step", step.id))}
            <h4>相关发现</h4>
            {data.findings
              .filter((finding) => finding.fact_id === item.id)
              .map((finding) => (
                <button
                  className="ref-link"
                  key={finding.id}
                  onClick={() => onSelect({ type: "finding", id: finding.id })}
                >
                  <Badge value={finding.severity} />
                  <span>{finding.title}</span>
                </button>
              ))}
          </>
        )}
        {type === "finding" && (
          <>
            <dl>
              <dt>类型</dt>
              <dd>{item.kind}</dd>
              <dt>发现时间</dt>
              <dd>{formatTime(item.created_at)}</dd>
            </dl>
            {item.fact_id && (
              <>
                <h4>证据事实</h4>
                {ref("fact", item.fact_id)}
              </>
            )}
            <h4>附加数据</h4>
            <pre>{JSON.stringify(item.data || {}, null, 2)}</pre>
            <a
              className="text-link"
              href={`#/vulnerabilities?project_id=${encodeURIComponent(data.project.id)}`}
            >
              查看项目漏洞
            </a>
          </>
        )}
      </div>
      <footer className="inspector-actions">
        {type === "step" && state === "pending" && (
          <>
            <Button
              tone="primary"
              icon={Play}
              disabled={!active || (!!busy && busy !== `execute:${item.id}`)}
              busy={busy === `execute:${item.id}`}
              onClick={() =>
                action(
                  `execute:${item.id}`,
                  () =>
                    post(
                      `${productPath(data.project.id)}/steps/${encodeURIComponent(item.id)}/execute`,
                    ),
                  "步骤已执行并记录结果",
                )
              }
            >
              Worker 执行
            </Button>
            <Button
              disabled={!active || !!busy}
              onClick={() => edit("claim", item)}
            >
              手动认领
            </Button>
            <Button
              disabled={!active || !!busy}
              icon={Check}
              onClick={() => edit("conclude", item)}
            >
              记录结果
            </Button>
            <Button
              disabled={!active || !!busy}
              icon={Edit3}
              onClick={() => edit("step-edit", item)}
            >
              调整
            </Button>
            <Button
              disabled={!active || !!busy}
              onClick={() =>
                confirm({
                  title: "放弃步骤",
                  message: `放弃 ${item.id}？放弃后不可恢复。`,
                  onConfirm: async () => {
                    await patch(
                      `${base}/steps/${encodeURIComponent(item.id)}`,
                      { abandoned: true },
                    );
                    notify("步骤已放弃");
                    await refresh();
                  },
                })
              }
            >
              放弃
            </Button>
          </>
        )}
        {type === "step" && state === "working" && (
          <>
            <Button
              tone="primary"
              disabled={!active || !!busy}
              icon={Check}
              onClick={() => edit("conclude", item)}
            >
              记录结果
            </Button>
            <Button
              disabled={!active || !!busy}
              busy={busy === "heartbeat"}
              onClick={() =>
                action(
                  "heartbeat",
                  () =>
                    post(
                      `${base}/steps/${encodeURIComponent(item.id)}/heartbeat`,
                      { worker: item.worker },
                    ),
                  "心跳已更新",
                )
              }
            >
              更新心跳
            </Button>
            <Button
              disabled={!active || !!busy}
              onClick={() =>
                confirm({
                  title: "释放步骤",
                  message: `释放 ${item.id} 的 ${item.worker} 租约，步骤将回到待执行状态。`,
                  danger: false,
                  onConfirm: async () => {
                    await post(
                      `${base}/steps/${encodeURIComponent(item.id)}/release`,
                      { worker: item.worker },
                    );
                    await refresh();
                    notify("租约已释放");
                  },
                })
              }
            >
              释放租约
            </Button>
          </>
        )}
        {type === "goal" && item.status !== "completed" && (
          <>
            <Button
              tone="primary"
              icon={Check}
              disabled={!active}
              onClick={() => edit("complete", item)}
            >
              完成目标
            </Button>
            <Button
              icon={Plus}
              disabled={!active}
              onClick={() => edit("goal", { parent_goal_id: item.id })}
            >
              添加子目标
            </Button>
            <Button
              icon={Edit3}
              disabled={!active}
              onClick={() => edit("goal-edit", item)}
            >
              编辑目标
            </Button>
            {item.parent_goal_id && (
              <Button
                icon={Trash2}
                disabled={!active || referencedGoal}
                title={referencedGoal ? "目标仍有子目标或步骤引用" : undefined}
                onClick={() =>
                  confirm({
                    title: "删除子目标",
                    message: `删除 ${item.id}？`,
                    onConfirm: async () => {
                      await remove(
                        `${base}/goals/${encodeURIComponent(item.id)}`,
                      );
                      onClose();
                      await refresh();
                      notify("子目标已删除");
                    },
                  })
                }
              >
                删除
              </Button>
            )}
          </>
        )}
        {type === "fact" && (
          <>
            <Button
              icon={Plus}
              tone="primary"
              disabled={!active}
              onClick={() => edit("step", { from: [item.id] })}
            >
              从此事实添加步骤
            </Button>
            <Button
              icon={ShieldAlert}
              disabled={!active}
              onClick={() => edit("finding", { fact_id: item.id })}
            >
              记录发现
            </Button>
          </>
        )}
      </footer>
    </aside>
  );
}

function WorkspaceModal({ kind, item, detail, base, actor, onClose, onSaved }) {
  const [description, setDescription] = useState(
    kind === "title"
      ? detail.project.title
      : ["goal-edit"].includes(kind)
        ? item?.description || ""
        : "",
  );
  const [priority, setPriority] = useState(item?.priority || 0);
  const [maxWorkers, setMaxWorkers] = useState(detail.project.max_workers || 1);
  const [parent, setParent] = useState(item?.parent_goal_id || "");
  const [goal, setGoal] = useState(item?.goal_id || "");
  const [from, setFrom] = useState(item?.from || []);
  const [worker, setWorker] = useState(
    item?.worker || detail.project.decide?.worker || actor,
  );
  const [leaseAction, setLeaseAction] = useState(
    detail.project.decide ? "heartbeat" : "claim",
  );
  const [title, setTitle] = useState("");
  const [severity, setSeverity] = useState("medium");
  const [findingKind, setFindingKind] = useState("vulnerability");
  const [factID, setFactID] = useState(item?.fact_id || "");
  const [findingData, setFindingData] = useState("{}");
  const [withFinding, setWithFinding] = useState(false);
  const [resultType, setResultType] = useState("progress");
  const [resultDetail, setResultDetail] = useState("");
  const [routeID, setRouteID] = useState("");
  const titles = {
    title: "重命名项目",
    "worker-limit": "项目 Worker 并发上限",
    reopen: "重新打开项目",
    goal: "添加目标",
    "goal-edit": "编辑目标",
    step: "添加步骤",
    "step-edit": "调整步骤",
    claim: "手动认领步骤",
    conclude: "记录步骤结果",
    complete: "完成目标",
    hint: "添加提示",
    finding: "记录发现",
    decide: "Decide 租约",
  };
  const submit = async () => {
    const endpoint =
      kind.startsWith("goal") || kind === "complete"
        ? `${base}/goals/${encodeURIComponent(item?.id || "")}`
        : `${base}/steps/${encodeURIComponent(item?.id || "")}`;
    const finding = {
      title: title.trim(),
      description: description.trim(),
      severity,
      kind: findingKind,
      data: kind === "finding" || withFinding ? parseObject(findingData) : {},
    };
    if (kind === "title")
      await put(`${base}/title`, { title: description.trim() });
    else if (kind === "worker-limit")
      await put(`${base}/worker-limit`, { max_workers: Number(maxWorkers) });
    else if (kind === "reopen")
      await post(`${base}/reopen`, {
        description: description.trim(),
        creator: actor,
      });
    else if (kind === "goal")
      await post(`${base}/goals`, {
        description: description.trim(),
        priority: Number(priority),
        parent_goal_id: parent || null,
        creator: actor,
      });
    else if (kind === "goal-edit")
      await patch(endpoint, {
        description: description.trim(),
        priority: Number(priority),
      });
    else if (kind === "step")
      await post(`${base}/steps`, {
        description: description.trim(),
        from,
        goal_id: goal || null,
        priority: Number(priority),
        creator: actor,
        route_id: routeID || null,
      });
    else if (kind === "step-edit")
      await patch(endpoint, {
        priority: Number(priority),
        ...(goal ? { goal_id: goal } : {}),
      });
    else if (kind === "claim")
      await post(`${endpoint}/heartbeat`, { worker: worker.trim() });
    else if (kind === "conclude")
      await post(`${endpoint}/conclude`, {
        worker: worker.trim(),
        description: description.trim(),
        result: { type: resultType, detail: resultDetail },
        ...(withFinding ? { finding } : {}),
      });
    else if (kind === "complete")
      await post(`${endpoint}/complete`, {
        from,
        description: description.trim(),
        worker: worker.trim(),
      });
    else if (kind === "hint")
      await post(`${base}/hints`, {
        content: description.trim(),
        creator: actor,
      });
    else if (kind === "finding")
      await post(`${base}/findings`, { ...finding, fact_id: factID || null });
    else if (kind === "decide")
      await post(`${base}/decide/${leaseAction}`, {
        worker: worker.trim(),
        ...(leaseAction === "claim"
          ? { trigger: description.trim() || "manual" }
          : {}),
      });
    await onSaved();
  };
  const findingFields = (
    <>
      <TextField
        label="发现标题"
        required
        value={title}
        onChange={(event) => setTitle(event.target.value)}
      />
      <div className="form-grid">
        <SelectField
          label="严重程度"
          value={severity}
          onChange={(event) => setSeverity(event.target.value)}
        >
          {["critical", "high", "medium", "low", "info"].map((value) => (
            <option key={value} value={value}>
              {label(value)}
            </option>
          ))}
        </SelectField>
        <SelectField
          label="发现类型"
          value={findingKind}
          onChange={(event) => setFindingKind(event.target.value)}
        >
          <option value="vulnerability">漏洞</option>
          <option value="observation">观察</option>
          <option value="information">信息</option>
        </SelectField>
      </div>
      {kind === "finding" && (
        <SelectField
          label="证据事实"
          required={findingKind === "vulnerability" && severity !== "info"}
          value={factID}
          onChange={(event) => setFactID(event.target.value)}
        >
          <option value="">选择事实</option>
          {detail.facts.map((fact) => (
            <option key={fact.id} value={fact.id}>
              {fact.id} · {truncate(fact.description, 50)}
            </option>
          ))}
        </SelectField>
      )}
      <TextField
        label="附加数据（JSON 对象）"
        multiline
        value={findingData}
        onChange={(event) => setFindingData(event.target.value)}
        hint="可填写证据、复现步骤等结构化信息。"
      />
    </>
  );
  return (
    <FormModal
      title={titles[kind]}
      description={
        item?.id
          ? `当前对象：${item.id}`
          : kind === "reopen"
            ? "反馈将生成新的事实和步骤，并重新激活目标。"
            : undefined
      }
      wide={["step", "complete", "finding", "conclude"].includes(kind)}
      submitLabel={
        kind === "complete"
          ? "提交完成证据"
          : kind === "conclude"
            ? "提交结果"
            : "保存"
      }
      disabled={["step", "complete"].includes(kind) && !from.length}
      onClose={onClose}
      onSubmit={submit}
    >
      {!["claim", "step-edit", "decide"].includes(kind) && (
        <TextField
          label={
            kind === "worker-limit"
              ? "最大并发 Worker 数"
              : kind === "title"
                ? "项目名称"
                : kind === "hint"
                  ? "提示内容"
                  : kind === "finding"
                    ? "发现描述"
                    : kind === "complete"
                      ? "完成说明"
                      : kind === "conclude"
                        ? "结果事实描述"
                        : kind === "reopen"
                          ? "反馈 / 继续推进的原因"
                          : "描述"
          }
          required
          type={kind === "worker-limit" ? "number" : undefined}
          min={kind === "worker-limit" ? 1 : undefined}
          max={kind === "worker-limit" ? 32 : undefined}
          step={kind === "worker-limit" ? 1 : undefined}
          multiline={kind !== "title" && kind !== "worker-limit"}
          autoFocus
          value={kind === "worker-limit" ? maxWorkers : description}
          onChange={(event) =>
            kind === "worker-limit"
              ? setMaxWorkers(Number(event.target.value))
              : setDescription(event.target.value)
          }
        />
      )}
      {["goal", "goal-edit", "step", "step-edit"].includes(kind) && (
        <TextField
          label="优先级"
          type="number"
          required
          step="1"
          value={priority}
          onChange={(event) => setPriority(event.target.value)}
          hint="较大的优先级会优先执行。"
        />
      )}
      {kind === "goal" && (
        <SelectField
          label="父目标"
          value={parent}
          onChange={(event) => setParent(event.target.value)}
        >
          <option value="">顶层目标</option>
          {detail.goals
            .filter((goal) => goal.status === "active")
            .map((goal) => (
              <option value={goal.id} key={goal.id}>
                {goal.id} · {truncate(goal.description, 60)}
              </option>
            ))}
        </SelectField>
      )}
      {["step", "step-edit"].includes(kind) && (
        <SelectField
          label="关联目标"
          value={goal}
          onChange={(event) => setGoal(event.target.value)}
          hint={
            kind === "step-edit"
              ? "已关联的目标不能清空；仅能调整尚未认领的步骤。"
              : undefined
          }
        >
          <option value="" disabled={kind === "step-edit" && !!item?.goal_id}>
            不关联目标
          </option>
          {detail.goals
            .filter((goal) => goal.status === "active")
            .map((goal) => (
              <option value={goal.id} key={goal.id}>
                {goal.id} · {truncate(goal.description, 60)}
              </option>
            ))}
        </SelectField>
      )}
      {["step", "complete"].includes(kind) && (
        <FactPicker
          facts={detail.facts}
          value={from}
          onChange={setFrom}
          label={kind === "complete" ? "完成证据" : "依据事实"}
        />
      )}
      {["claim", "conclude", "complete", "decide"].includes(kind) && (
        <TextField
          label="Worker / 操作者"
          required
          value={worker}
          onChange={(event) => setWorker(event.target.value)}
          readOnly={kind === "conclude" && !!item?.worker}
          hint="手动操作使用此名称标识租约和结果的来源。"
        />
      )}
      {kind === "step" && (
        <SelectField
          label="路线"
          value={routeID}
          onChange={(e) => setRouteID(e.target.value)}
        >
          <option value="">按目标或来源自动关联</option>
          {(detail.routes || [])
            .filter((r) => r.status === "active")
            .map((r) => (
              <option key={r.id} value={r.id}>
                {r.id} · {r.title}
              </option>
            ))}
        </SelectField>
      )}
      {kind === "conclude" && (
        <>
          <SelectField
            label="结果类型"
            value={resultType}
            onChange={(e) => {
              setResultType(e.target.value);
              if (
                [
                  "environment_failure",
                  "no_information",
                  "needs_attention",
                ].includes(e.target.value)
              )
                setWithFinding(false);
            }}
          >
            {Object.entries(resultLabels)
              .filter(([key]) => key !== "unclassified")
              .map(([key, name]) => (
                <option key={key} value={key}>
                  {name}
                </option>
              ))}
          </SelectField>
          <TextField
            label="详细证据"
            multiline
            value={resultDetail}
            onChange={(e) => setResultDetail(e.target.value)}
          />
          <label className="checkbox-field">
            <input
              type="checkbox"
              checked={withFinding}
              disabled={[
                "environment_failure",
                "no_information",
                "needs_attention",
              ].includes(resultType)}
              onChange={(event) => setWithFinding(event.target.checked)}
            />
            同时记录一条发现（绑定新生成的结果事实）
          </label>
          {withFinding && findingFields}
        </>
      )}
      {kind === "finding" && findingFields}
      {kind === "decide" && (
        <>
          <SelectField
            label="租约操作"
            value={leaseAction}
            onChange={(event) => setLeaseAction(event.target.value)}
          >
            {detail.project.decide ? (
              <>
                <option value="heartbeat">更新心跳</option>
                <option value="release">释放租约</option>
              </>
            ) : (
              <option value="claim">认领</option>
            )}
          </SelectField>
          {leaseAction === "claim" && (
            <TextField
              label="触发原因"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="manual"
            />
          )}
          <p className="muted">
            此操作仅管理规划租约；自动规划需要服务以 --autorun 启动。
          </p>
        </>
      )}
    </FormModal>
  );
}
