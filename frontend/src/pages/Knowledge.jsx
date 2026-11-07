import { useState } from "react";
import { BookOpen } from "lucide-react";
import { api, queryString } from "../api.js";
import { useResource } from "../hooks.js";
import { useApp } from "../context.jsx";
import {
  Badge,
  Button,
  Empty,
  Modal,
  Panel,
  ResourceState,
  SearchInput,
  SelectField,
} from "../components/ui.jsx";

export default function Knowledge() {
  const [query, setQuery] = useState("");
  const [tag, setTag] = useState("");
  const [selected, setSelected] = useState(null);
  const { run } = useApp();
  const info = useResource("/api/knowledge");
  const skills = useResource("/api/knowledge/skills");
  const entries = useResource(
    `/api/knowledge/entries${queryString({ q: query, tag, limit: 50 })}`,
  );
  const read = (id) =>
    run(async () =>
      setSelected(
        await api(`/api/knowledge/entries/${encodeURIComponent(id)}`),
      ),
    );

  return (
    <>
      <Panel title="CTF 知识库">
        <ResourceState resource={info}>
          <p>
            {info.data?.entry_count} 条离线知识 · {info.data?.skill_count}{" "}
            个技能 · 来源 sapper-v2
          </p>
          <p>
            Dispatcher 和 Worker 会根据当前目标、步骤与事实检索相关方法。
            选择条目可查看完整方法、命令示例和来源。
          </p>
          <div className="workspace-list-toolbar">
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder="检索技术、产品或 CVE"
            />
            <SelectField
              label="知识分类"
              value={tag}
              onChange={(event) => setTag(event.target.value)}
            >
              <option value="">全部分类</option>
              {Object.entries(info.data?.tags || {})
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([name, count]) => (
                  <option key={name} value={name}>
                    {name} ({count})
                  </option>
                ))}
            </SelectField>
          </div>
        </ResourceState>
        <ResourceState resource={entries}>
          <div className="entity-list">
            {(entries.data || []).map((entry) => (
              <article className="entity-card" key={entry.id}>
                <div>
                  <code>{entry.id}</code>
                  <strong>{entry.title}</strong>
                  {entry.tags.map((name) => (
                    <Badge key={name}>{name}</Badge>
                  ))}
                </div>
                <p>{entry.description}</p>
                <Button icon={BookOpen} onClick={() => read(entry.id)}>
                  查看方法
                </Button>
              </article>
            ))}
            {!entries.data?.length && <Empty title="暂无匹配知识" />}
          </div>
        </ResourceState>
      </Panel>
      <Panel title="已接入技能">
        <ResourceState resource={skills}>
          <div className="entity-list">
            {(skills.data || []).map((skill) => (
              <article className="entity-card" key={skill.name}>
                <strong>{skill.name}</strong>
                <p>{skill.description}</p>
                <Button onClick={() => setSelected(skill)}>查看技能</Button>
              </article>
            ))}
          </div>
        </ResourceState>
      </Panel>
      {selected && (
        <Modal
          title={selected.title || selected.name}
          onClose={() => setSelected(null)}
          wide
        >
          <div className="modal-body">
            {selected.source && <p>来源：{selected.source}</p>}
            <pre className="block-code">
              {selected.detail || selected.content}
            </pre>
            {!!selected.commands?.length && (
              <>
                <h3>命令示例</h3>
                <pre className="block-code">{selected.commands.join("\n")}</pre>
              </>
            )}
          </div>
        </Modal>
      )}
    </>
  );
}
