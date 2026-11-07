import { useState } from "react";
import {
  ArrowUpRight,
  FolderKanban,
  Layers,
  Plus,
  RefreshCw,
  Target,
  Trash2,
} from "lucide-react";
import { post, projectPath, remove } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import { projectHref, shortTime } from "../utils.js";
import {
  Badge,
  Button,
  Empty,
  ErrorMessage,
  FormModal,
  PageHeader,
  ResourceState,
  SearchInput,
  SelectField,
  TextField,
} from "../components/ui.jsx";

export default function Projects() {
  const resource = useResource("/projects", { interval: 15000 });
  const { createProject, confirm, notify } = useApp();
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const projects = (resource.data || []).filter(
    (item) =>
      (!status || item.status === status) &&
      `${item.title} ${item.id}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="PROJECTS / 项目管理"
        title="项目"
        description="把评估范围、目标和执行过程组织到同一个项目中。"
        actions={
          <>
            <Button
              icon={RefreshCw}
              busy={resource.loading}
              onClick={() => resource.reload()}
            >
              刷新
            </Button>
            <Button tone="primary" icon={Plus} onClick={() => createProject()}>
              新建项目
            </Button>
          </>
        }
      />
      <div className="toolbar">
        <div className="segmented">
          {[
            ["", "全部"],
            ["active", "运行中"],
            ["stopped", "已停止"],
            ["completed", "已完成"],
            ["failed", "失败"],
          ].map(([key, name]) => (
            <button
              key={key}
              className={status === key ? "active" : ""}
              onClick={() => setStatus(key)}
            >
              {name}
              <small>
                {
                  (resource.data || []).filter(
                    (item) => !key || item.status === key,
                  ).length
                }
              </small>
            </button>
          ))}
        </div>
        <SearchInput
          value={query}
          onChange={setQuery}
          placeholder="搜索项目名称或编号"
        />
      </div>
      <ResourceState resource={resource}>
        {projects.length === 0 ? (
          <Empty
            icon={FolderKanban}
            title={query || status ? "没有匹配的项目" : "开始你的第一个项目"}
            description="定义初始事实与目标，让每个步骤都可追溯。"
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
        ) : (
          <div className="project-grid">
            {projects.map((project) => (
              <article className="project-card" key={project.id}>
                <header>
                  <span className="project-icon">
                    <FolderKanban size={21} />
                  </span>
                  <Badge value={project.status} />
                </header>
                <a href={projectHref(project.id)} className="project-title">
                  <h2>{project.title}</h2>
                  <ArrowUpRight size={17} />
                </a>
                <code className="project-id">{project.id}</code>
                <div className="project-card-stats">
                  <span>
                    <Layers size={14} />
                    {project.fact_count} <small>事实</small>
                  </span>
                  <span>
                    <Target size={14} />
                    {project.goal_count} <small>目标</small>
                  </span>
                  <span>
                    {project.finding_count} <small>发现</small>
                  </span>
                </div>
                <div className="project-progress">
                  <div>
                    <span>已处理步骤</span>
                    <strong>
                      {Math.max(
                        0,
                        project.step_count -
                          project.unclaimed_step_count -
                          project.working_step_count,
                      )}{" "}
                      / {project.step_count}
                    </strong>
                  </div>
                  <div className="progress-track">
                    <i
                      style={{
                        width: `${project.step_count ? (100 * (project.step_count - project.unclaimed_step_count - project.working_step_count)) / project.step_count : 0}%`,
                      }}
                    />
                  </div>
                  <small>
                    {project.working_step_count} 执行中 ·{" "}
                    {project.unclaimed_step_count} 待执行
                  </small>
                </div>
                <footer>
                  <time>{shortTime(project.created_at)}</time>
                  <Button
                    icon={Trash2}
                    className="subtle"
                    aria-label={`删除项目 ${project.title}`}
                    onClick={() =>
                      confirm({
                        title: "删除项目",
                        message: `删除「${project.title}」及其全部事实、目标、步骤和发现？此操作无法撤销。`,
                        confirmLabel: "删除项目",
                        onConfirm: async () => {
                          await remove(projectPath(project.id));
                          notify("项目已删除");
                          await resource.reload();
                        },
                      })
                    }
                  />
                  <a className="text-link" href={projectHref(project.id)}>
                    进入工作区 <ArrowUpRight size={14} />
                  </a>
                </footer>
              </article>
            ))}
          </div>
        )}
      </ResourceState>
    </>
  );
}

export function ProjectCreateModal({ template, onClose }) {
  const templates = useResource("/api/templates");
  const settings = useResource("/api/settings");
  const { user, notify } = useApp();
  const [title, setTitle] = useState(template?.title || "");
  const [origin, setOrigin] = useState(template?.origin || "");
  const [goal, setGoal] = useState(template?.goal || "");
  const [hints, setHints] = useState(
    (template?.hints || []).map((item) => item.content).join("\n"),
  );
  const [maxWorkers, setMaxWorkers] = useState(1);
  const [budget, setBudget] = useState("");
  const [selected, setSelected] = useState(template?.id || "");
  const select = (value) => {
    setSelected(value);
    const item = templates.data?.find((item) => item.id === value);
    if (item) {
      setTitle(item.title);
      setOrigin(item.origin);
      setGoal(item.goal);
      setHints((item.hints || []).map((hint) => hint.content).join("\n"));
    }
  };
  return (
    <FormModal
      title="新建项目"
      description="初始事实描述目标范围，目标定义成功条件。"
      onClose={onClose}
      submitLabel="创建项目"
      wide
      onSubmit={async () => {
        const detail = await post("/projects", {
          title: title.trim(),
          origin: origin.trim(),
          goal: goal.trim(),
          max_workers: Number(maxWorkers),
          ...(budget === "" ? {} : { budget_seconds: Number(budget) }),
          hints: hints
            .split("\n")
            .map((content) => content.trim())
            .filter(Boolean)
            .map((content) => ({ content, creator: user.username })),
        });
        notify("项目已创建");
        location.hash = projectHref(detail.project.id);
      }}
    >
      <ErrorMessage error={templates.error} onRetry={templates.reload} />
      <SelectField
        label="从模板开始"
        value={selected}
        onChange={(event) => select(event.target.value)}
      >
        <option value="">自定义项目</option>
        {templates.data?.map((item) => (
          <option key={item.id} value={item.id}>
            {item.title}
            {item.is_builtin ? " · 内置" : ""}
          </option>
        ))}
      </SelectField>
      <TextField
        label="项目名称"
        required
        maxLength={200}
        autoFocus
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        placeholder="例如：门户应用安全评估"
      />
      <TextField
        label="初始事实（范围）"
        multiline
        required
        value={origin}
        onChange={(event) => setOrigin(event.target.value)}
        placeholder="目标地址、已有信息和明确的评估边界…"
      />
      <TextField
        label="目标"
        multiline
        required
        value={goal}
        onChange={(event) => setGoal(event.target.value)}
        placeholder="希望达成什么？如何判断目标已完成？"
      />
      <TextField
        label="项目最大并发 Worker 数"
        type="number"
        min="1"
        max="32"
        step="1"
        required
        value={maxWorkers}
        onChange={(event) => setMaxWorkers(Number(event.target.value))}
        hint="项目 Dispatcher 始终独立负责规划和提交结果。"
      />
      <TextField
        label="项目预算（秒，0为不限）"
        type="number"
        min="0"
        step="1"
        value={budget}
        placeholder={String(
          settings.data?.default_project_budget_seconds ?? "",
        )}
        onChange={(event) => setBudget(event.target.value)}
        hint="留空自动使用系统配置的项目预算；所有路线共同消耗这一份额度。"
      />
      <TextField
        label="提示（可选）"
        multiline
        value={hints}
        onChange={(event) => setHints(event.target.value)}
        hint="每行一条提示，创建后可在项目中继续添加。"
      />
    </FormModal>
  );
}
