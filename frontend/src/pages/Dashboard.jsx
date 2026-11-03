import {
  Activity,
  ArrowUpRight,
  CheckCheck,
  FolderKanban,
  Monitor,
  Plus,
  RefreshCw,
  ShieldAlert,
} from "lucide-react";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import {
  duration,
  projectHref,
  shortTime,
  sortSeverity,
  truncate,
} from "../utils.js";
import {
  Badge,
  Button,
  Empty,
  Metric,
  PageHeader,
  Panel,
  ResourceState,
} from "../components/ui.jsx";

export default function Dashboard() {
  const projects = useResource("/projects", { interval: 20000 });
  const vulnerabilities = useResource("/api/vulnerabilities", {
    interval: 20000,
  });
  const workers = useResource("/api/workers/observability", {
    interval: 20000,
  });
  const audit = useResource("/api/audit?limit=8", { interval: 20000 });
  const health = useResource("/api/settings/health", { interval: 30000 });
  const { createProject } = useApp();
  const projectList = projects.data || [];
  const vulnerabilityList = vulnerabilities.data || [];
  const confirmed = vulnerabilityList.filter(
    (item) => item.status === "confirmed",
  );
  const summary = workers.data?.summary;
  const worker = workers.data?.workers?.[0];
  const refresh = () => {
    projects.reload();
    vulnerabilities.reload();
    workers.reload();
    audit.reload();
    health.reload();
  };
  return (
    <>
      <PageHeader
        eyebrow="OVERVIEW / 本地工作空间"
        title="工作台总览"
        description="项目进展、风险发现和本地执行状态，一览可见。"
        actions={
          <>
            <span className="updated">
              <span className="status-dot green" />
              {projects.updated
                ? `更新于 ${projects.updated.toLocaleTimeString("zh-CN", { hour12: false })}`
                : "等待数据"}
            </span>
            <Button
              icon={RefreshCw}
              busy={projects.loading || workers.loading}
              onClick={refresh}
            >
              刷新
            </Button>
            <Button tone="primary" icon={Plus} onClick={() => createProject()}>
              新建项目
            </Button>
          </>
        }
      />
      <div className="metrics">
        <Metric
          title="全部项目"
          value={projects.data?.length}
          icon={FolderKanban}
          hint="查看工作项目"
          href="#/projects"
          tone="blue"
        />
        <Metric
          title="运行中项目"
          value={
            projects.data &&
            projectList.filter((item) => item.status === "active").length
          }
          icon={Activity}
          hint="持续推进中的评估"
          href="#/projects"
        />
        <Metric
          title="已确认漏洞"
          value={vulnerabilities.data && confirmed.length}
          icon={ShieldAlert}
          hint={`${confirmed.filter((item) => ["critical", "high"].includes(item.severity)).length} 个严重 / 高危发现`}
          href="#/vulnerabilities"
          tone="amber"
        />
        <Metric
          title="本地 Worker"
          value={
            summary
              ? `${summary.online} / ${summary.configured_workers || worker?.worker_count || 1}`
              : null
          }
          icon={Monitor}
          hint={worker ? `${worker.name} · ${worker.driver}` : "Dispatcher / Agent Worker"}
          href="#/workers"
          tone="purple"
        />
      </div>
      <div className="dashboard-grid">
        <Panel
          title="项目进展"
          extra={
            <a className="text-link" href="#/projects">
              全部项目 <ArrowUpRight size={14} />
            </a>
          }
          className="dashboard-projects"
        >
          <ResourceState resource={projects}>
            {!projectList.length && (
              <Empty
                title="工作空间已就绪"
                description="创建一个项目，从初始事实开始。"
                action={
                  <Button
                    tone="primary"
                    icon={Plus}
                    onClick={() => createProject()}
                  >
                    新建项目
                  </Button>
                }
              />
            )}
            {projectList.slice(0, 5).map((project) => (
              <a
                className="project-row"
                key={project.id}
                href={projectHref(project.id)}
              >
                <span className="project-icon">
                  <FolderKanban size={18} />
                </span>
                <div>
                  <strong>{project.title}</strong>
                  <small>
                    {project.fact_count} 事实 · {project.step_count} 步骤 ·{" "}
                    {project.finding_count} 发现
                  </small>
                </div>
                <Badge value={project.status} />
                <ArrowUpRight size={16} />
              </a>
            ))}
          </ResourceState>
          <footer className="panel-stats">
            {[
              ["active", "运行中"],
              ["completed", "已完成"],
              ["stopped", "已停止"],
            ].map(([key, name]) => (
              <div key={key}>
                <strong>
                  {projects.data
                    ? projectList.filter((item) => item.status === key).length
                    : "—"}
                </strong>
                <span>{name}</span>
              </div>
            ))}
          </footer>
        </Panel>
        <Panel
          title="Worker 状态"
          extra={worker && <Badge value={worker.status} />}
        >
          <ResourceState resource={workers}>
            <div className="worker-overview">
              <span className="worker-glyph">
                <Monitor size={32} />
              </span>
              <div>
                <h3>{worker?.name}</h3>
                <p>{worker?.current_task || "当前没有正在执行的任务"}</p>
                {worker?.project_id && (
                  <a
                    className="text-link"
                    href={projectHref(worker.project_id)}
                  >
                    查看执行项目 <ArrowUpRight size={14} />
                  </a>
                )}
              </div>
            </div>
            <div className="worker-stats">
              <div>
                <strong>{worker?.completed_tasks ?? 0}</strong>
                <small>已完成任务</small>
              </div>
              <div>
                <strong>{worker?.failed_tasks ?? 0}</strong>
                <small>失败 / 拒绝</small>
              </div>
              <div>
                <strong>{duration(worker?.avg_duration_seconds)}</strong>
                <small>平均耗时</small>
              </div>
            </div>
            {(summary?.attention_steps || 0) > 0 && (
              <a className="notice amber" href="#/workers">
                {summary.attention_steps} 个步骤需要人工关注
              </a>
            )}
            <div className="panel-bottom">
              <span className="muted">项目级 Dispatcher · 多 Agent Worker</span>
              <a className="text-link" href="#/workers">
                配置 <ArrowUpRight size={14} />
              </a>
            </div>
          </ResourceState>
        </Panel>
        <Panel
          title="漏洞发现 · 近 7 天"
          extra={<span className="muted">依据发现时间</span>}
        >
          <ResourceState resource={vulnerabilities}>
            <Trend items={vulnerabilityList} />
          </ResourceState>
        </Panel>
        <Panel
          title="风险分布"
          extra={
            <a className="text-link" href="#/vulnerabilities">
              查看漏洞 <ArrowUpRight size={14} />
            </a>
          }
        >
          <ResourceState resource={vulnerabilities}>
            <div className="risk-distribution">
              {[
                ["critical", "严重"],
                ["high", "高危"],
                ["medium", "中危"],
                ["low", "低危"],
              ].map(([key, name]) => {
                const count = confirmed.filter(
                  (item) => item.severity === key,
                ).length;
                return (
                  <div className="risk-row" key={key}>
                    <Badge value={key}>{name}</Badge>
                    <div className={`risk-bar ${key}`}>
                      <i
                        style={{
                          width: `${confirmed.length ? (count / confirmed.length) * 100 : 0}%`,
                        }}
                      />
                    </div>
                    <strong>{count}</strong>
                  </div>
                );
              })}
            </div>
            <div className="panel-bottom">
              <span className="muted">仅统计已确认漏洞</span>
              <span>
                <CheckCheck size={14} />{" "}
                {
                  vulnerabilityList.filter((item) => item.status === "ignored")
                    .length
                }{" "}
                已忽略
              </span>
            </div>
          </ResourceState>
        </Panel>
        <Panel title="优先关注的漏洞" extra={<ShieldAlert size={16} />}>
          <ResourceState resource={vulnerabilities}>
            {confirmed.length === 0 ? (
              <Empty
                title="暂无已确认漏洞"
                description="项目中的漏洞发现会在这里汇总。"
              />
            ) : (
              [...confirmed]
                .sort(sortSeverity)
                .slice(0, 5)
                .map((item, index) => (
                  <a
                    className="finding-row"
                    href={`#/vulnerabilities?project_id=${encodeURIComponent(item.project_id)}&id=${encodeURIComponent(item.id)}`}
                    key={item.id}
                  >
                    <span className="rank">
                      {String(index + 1).padStart(2, "0")}
                    </span>
                    <div>
                      <strong>{truncate(item.title, 40)}</strong>
                      <small>{item.project_name}</small>
                    </div>
                    <Badge value={item.severity} />
                  </a>
                ))
            )}
          </ResourceState>
        </Panel>
        <Panel
          title="最近活动"
          extra={
            <a className="text-link" href="#/audit">
              审计日志 <ArrowUpRight size={14} />
            </a>
          }
        >
          <ResourceState resource={audit}>
            {!audit.data?.length && <Empty title="暂无活动" />}
            {audit.data?.slice(0, 6).map((item) => (
              <div className="activity-row" key={item.id}>
                <span className="activity-dot" />
                <div>
                  <p>
                    {truncate(
                      item.summary || `${item.action} ${item.target_id}`,
                      66,
                    )}
                  </p>
                  <small>
                    {item.actor || "system"} · {shortTime(item.created_at)}
                  </small>
                </div>
              </div>
            ))}
          </ResourceState>
        </Panel>
      </div>
      <Panel
        title="服务健康"
        extra={health.data && <Badge value={health.data.summary?.status} />}
      >
        <ResourceState resource={health}>
          <div className="health-strip">
            {health.data?.checks?.map((check) => (
              <div key={check.key}>
                <span
                  className={`status-dot ${check.status === "ok" ? "green" : check.status === "error" ? "red" : "amber"}`}
                />
                <strong>
                  {{
                    server: "API 服务",
                    database: "数据库",
                    dispatcher: "本地 Worker",
                    auth: "认证",
                    retention: "数据保留",
                  }[check.key] || check.label}
                </strong>
                <span>{check.summary}</span>
              </div>
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
    </>
  );
}

function Trend({ items }) {
  const days = Array.from({ length: 7 }, (_, index) => {
    const date = new Date();
    date.setDate(date.getDate() - 6 + index);
    date.setHours(0, 0, 0, 0);
    const end = new Date(date);
    end.setDate(end.getDate() + 1);
    return {
      date,
      count: items.filter((item) => {
        const time = new Date(item.discovered_at);
        return time >= date && time < end;
      }).length,
    };
  });
  const max = Math.max(1, ...days.map((day) => day.count));
  return (
    <div className="trend">
      <div className="trend-heading">
        <strong>
          {days.reduce((sum, day) => sum + day.count, 0)}
          <small> 新发现</small>
        </strong>
        <span className="muted">包含已忽略项</span>
      </div>
      <div className="trend-bars">
        {days.map((day) => (
          <div className="trend-column" key={day.date.toISOString()}>
            <span>{day.count}</span>
            <div>
              <i
                style={{ height: `${(day.count / max) * 100}%` }}
                title={`${day.date.toLocaleDateString("zh-CN")}：${day.count} 条`}
              />
            </div>
            <small>{`${day.date.getMonth() + 1}/${day.date.getDate()}`}</small>
          </div>
        ))}
      </div>
    </div>
  );
}
