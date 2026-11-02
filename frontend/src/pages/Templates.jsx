import { useState } from "react";
import {
  ArrowUpRight,
  Copy,
  FileText,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { post, remove } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource } from "../hooks.js";
import {
  Badge,
  Button,
  Empty,
  FormModal,
  PageHeader,
  ResourceState,
  SearchInput,
  TextField,
} from "../components/ui.jsx";

export default function Templates() {
  const resource = useResource("/api/templates");
  const { user, createProject, confirm, notify } = useApp();
  const [query, setQuery] = useState("");
  const [editor, setEditor] = useState(null);
  const items = (resource.data || []).filter((item) =>
    `${item.title} ${item.origin} ${item.goal}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="TEMPLATES / 项目起点"
        title="项目模板"
        description="复用范围、目标和提示，快速建立新的工作项目。"
        actions={
          <>
            <Button
              icon={RefreshCw}
              busy={resource.loading}
              onClick={() => resource.reload()}
            >
              刷新
            </Button>
            <Button icon={Plus} tone="primary" onClick={() => setEditor({})}>
              新建模板
            </Button>
          </>
        }
      />
      <div className="toolbar">
        <span className="muted">内置模板与当前账户的自定义模板</span>
        <SearchInput value={query} onChange={setQuery} placeholder="搜索模板" />
      </div>
      <ResourceState resource={resource}>
        <div className="template-grid">
          {items.map((item) => (
            <article className="template-card panel" key={item.id}>
              <header>
                <span className="project-icon">
                  <FileText size={20} />
                </span>
                <Badge tone={item.is_builtin ? "blue" : "green"}>
                  {item.is_builtin ? "内置模板" : "自定义"}
                </Badge>
              </header>
              <h2>{item.title}</h2>
              <div className="template-copy">
                <span>初始事实</span>
                <p>{item.origin}</p>
                <span>目标</span>
                <p>{item.goal}</p>
                <span>{item.hints?.length || 0} 条提示</span>
              </div>
              <footer>
                <Button
                  tone="primary"
                  icon={ArrowUpRight}
                  onClick={() => createProject(item)}
                >
                  创建项目
                </Button>
                <Button
                  icon={Copy}
                  aria-label={`复制模板 ${item.title}`}
                  onClick={() =>
                    setEditor({ ...item, title: `${item.title}（副本）` })
                  }
                >
                  复制
                </Button>
                {!item.is_builtin &&
                  (!item.user_id || item.user_id === user.id) && (
                    <Button
                      icon={Trash2}
                      aria-label={`删除模板 ${item.title}`}
                      onClick={() =>
                        confirm({
                          title: "删除模板",
                          message: `删除「${item.title}」？已创建的项目会保留。`,
                          onConfirm: async () => {
                            await remove(
                              `/api/templates/${encodeURIComponent(item.id)}`,
                            );
                            notify("模板已删除");
                            await resource.reload();
                          },
                        })
                      }
                    />
                  )}
              </footer>
            </article>
          ))}
        </div>
        {items.length === 0 && <Empty title="没有匹配的模板" />}
      </ResourceState>
      {editor && (
        <TemplateEditor
          initial={editor}
          onClose={() => setEditor(null)}
          onSaved={resource.reload}
        />
      )}
    </>
  );
}
function TemplateEditor({ initial, onClose, onSaved }) {
  const { user, notify } = useApp();
  const [title, setTitle] = useState(initial.title || "");
  const [origin, setOrigin] = useState(initial.origin || "");
  const [goal, setGoal] = useState(initial.goal || "");
  const [hints, setHints] = useState(
    (initial.hints || []).map((item) => item.content).join("\n"),
  );
  return (
    <FormModal
      title={initial.id ? "复制模板" : "新建模板"}
      onClose={onClose}
      wide
      submitLabel="保存模板"
      onSubmit={async () => {
        await post("/api/templates", {
          title: title.trim(),
          origin: origin.trim(),
          goal: goal.trim(),
          hints: hints
            .split("\n")
            .map((line) => line.trim())
            .filter(Boolean)
            .map((content) => ({ content, creator: user.username })),
        });
        notify("模板已保存");
        await onSaved();
      }}
    >
      <TextField
        label="模板名称"
        required
        value={title}
        onChange={(event) => setTitle(event.target.value)}
      />
      <TextField
        label="初始事实"
        multiline
        required
        value={origin}
        onChange={(event) => setOrigin(event.target.value)}
      />
      <TextField
        label="目标"
        multiline
        required
        value={goal}
        onChange={(event) => setGoal(event.target.value)}
      />
      <TextField
        label="提示"
        multiline
        value={hints}
        onChange={(event) => setHints(event.target.value)}
        hint="每行一条。"
      />
    </FormModal>
  );
}
