import { useEffect, useState } from "react";
import { ArrowUpRight, Check, RefreshCw, ShieldAlert, X } from "lucide-react";
import { api, patch, post, projectPath, queryString } from "../api.js";
import { useApp } from "../context.jsx";
import { useResource, useRoute } from "../hooks.js";
import { formatTime, projectHref, truncate } from "../utils.js";
import {
  Badge,
  Button,
  DataTable,
  Empty,
  ErrorMessage,
  Metric,
  Modal,
  PageHeader,
  Panel,
  ResourceState,
  SearchInput,
  SelectField,
} from "../components/ui.jsx";

export default function Vulnerabilities({ projectFilter = "" }) {
  const route = useRoute();
  const projects = useResource("/projects");
  const [project, setProject] = useState(projectFilter);
  const [severity, setSeverity] = useState("");
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState([]);
  const [detailID, setDetailID] = useState(route.params.get("id"));
  const [busy, setBusy] = useState(false);
  const resource = useResource(
    `/api/vulnerabilities${queryString({ project_id: project, severity, status })}`,
  );
  const summary = useResource("/api/vulnerabilities/summary");
  const { run, confirm, notify } = useApp();
  const items = (resource.data || []).filter((item) =>
    `${item.id} ${item.title} ${item.description} ${item.project_name} ${item.fact_id}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  const chosen = resource.data?.find((item) => item.id === detailID);
  useEffect(() => {
    setSelected([]);
  }, [project, severity, status]);
  useEffect(() => {
    if (resource.data)
      setSelected((ids) =>
        ids.filter((id) => resource.data.some((item) => item.id === id)),
      );
  }, [resource.data]);
  const reload = async () => {
    await resource.reload(true);
    await summary.reload(true);
  };
  const update = async (ids, next) => {
    setBusy(true);
    await run(async () => {
      if (ids.length === 1) {
        await patch(
          `/api/vulnerabilities/${encodeURIComponent(ids[0])}/status`,
          { status: next },
        );
        notify(next === "confirmed" ? "漏洞已确认" : "漏洞已忽略");
      } else {
        const result = await post("/api/vulnerabilities/batch/status", {
          ids,
          status: next,
        });
        notify(
          `已更新 ${result.count} 条${result.missing_ids?.length ? `，${result.missing_ids.length} 条已不存在，请查看最新列表` : ""}`,
          result.missing_ids?.length ? "info" : "success",
        );
      }
      setSelected([]);
      await reload();
    });
    setBusy(false);
  };
  const refreshProjection = async () => {
    setBusy(true);
    await run(async () => {
      await post(
        project
          ? `/api/projects/${encodeURIComponent(project)}/vulnerabilities/refresh`
          : "/api/vulnerabilities/refresh",
      );
      await reload();
    }, "漏洞已重新同步");
    setBusy(false);
  };
  return (
    <>
      <PageHeader
        eyebrow="VULNERABILITIES / 发现复核"
        title="漏洞"
        description="从项目发现中汇总漏洞，保留来源事实与执行证据。"
        actions={
          <>
            <Button
              icon={RefreshCw}
              busy={resource.loading}
              onClick={() => reload()}
            >
              刷新列表
            </Button>
            <Button
              tone="primary"
              icon={RefreshCw}
              busy={busy}
              onClick={refreshProjection}
            >
              同步发现
            </Button>
          </>
        }
      />
      <ErrorMessage error={summary.error} onRetry={summary.reload} />
      <div className="metrics compact-metrics">
        {[
          ["critical", "严重"],
          ["high", "高危"],
          ["medium", "中危"],
          ["low", "低危"],
        ].map(([key, name]) => (
          <Metric
            key={key}
            title={name}
            value={summary.data?.[key]}
            icon={ShieldAlert}
            tone={
              key === "critical"
                ? "red"
                : key === "high"
                  ? "orange"
                  : key === "medium"
                    ? "amber"
                    : "blue"
            }
            hint="全工作空间 · 含已忽略"
          />
        ))}
      </div>
      <Panel>
        <div className="filter-bar">
          <SearchInput
            value={query}
            onChange={setQuery}
            placeholder="搜索漏洞标题、证据或编号"
          />
          <SelectField
            label="项目"
            value={project}
            onChange={(event) => {
              setProject(event.target.value);
              setDetailID(null);
            }}
          >
            <option value="">全部项目</option>
            {projects.data?.map((item) => (
              <option key={item.id} value={item.id}>
                {item.title}
              </option>
            ))}
          </SelectField>
          <SelectField
            label="严重程度"
            value={severity}
            onChange={(event) => setSeverity(event.target.value)}
          >
            <option value="">全部等级</option>
            {["critical", "high", "medium", "low"].map((key) => (
              <option key={key} value={key}>
                {
                  {
                    critical: "严重",
                    high: "高危",
                    medium: "中危",
                    low: "低危",
                  }[key]
                }
              </option>
            ))}
          </SelectField>
          <SelectField
            label="复核状态"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
          >
            <option value="">全部状态</option>
            <option value="confirmed">已确认</option>
            <option value="ignored">已忽略</option>
          </SelectField>
        </div>
        <ErrorMessage error={projects.error} onRetry={projects.reload} />
        {selected.length > 0 && (
          <div className="selection-bar">
            <strong>已选 {selected.length} 条</strong>
            <Button
              icon={Check}
              disabled={busy}
              onClick={() => update(selected, "confirmed")}
            >
              批量确认
            </Button>
            <Button
              icon={X}
              disabled={busy}
              onClick={() =>
                confirm({
                  title: "批量忽略漏洞",
                  message: `将选中的 ${selected.length} 条漏洞标为已忽略，可随时重新确认。`,
                  danger: false,
                  onConfirm: () => update(selected, "ignored"),
                })
              }
            >
              批量忽略
            </Button>
            <Button className="subtle" onClick={() => setSelected([])}>
              取消选择
            </Button>
          </div>
        )}
        <ResourceState resource={resource}>
          <DataTable
            columns={[
              <input
                type="checkbox"
                aria-label="选择当前筛选结果"
                checked={
                  items.length > 0 &&
                  items.every((item) => selected.includes(item.id))
                }
                onChange={(event) =>
                  setSelected(
                    event.target.checked ? items.map((item) => item.id) : [],
                  )
                }
              />,
              "漏洞 / 证据",
              "严重程度",
              "项目",
              "状态",
              "发现时间",
            ]}
            count={items.length}
            empty="暂无匹配漏洞"
          >
            {items.map((item) => (
              <tr key={item.id}>
                <td>
                  <input
                    type="checkbox"
                    aria-label={`选择漏洞 ${item.title}`}
                    checked={selected.includes(item.id)}
                    onChange={(event) =>
                      setSelected(
                        event.target.checked
                          ? [...selected, item.id]
                          : selected.filter((id) => id !== item.id),
                      )
                    }
                  />
                </td>
                <td>
                  <button
                    className="cell-link"
                    onClick={() => setDetailID(item.id)}
                  >
                    <strong>{item.title}</strong>
                    <small>
                      <code>{item.fact_id}</code> ·{" "}
                      {truncate(item.description, 75)}
                    </small>
                  </button>
                </td>
                <td>
                  <Badge value={item.severity} />
                </td>
                <td>
                  <a className="text-link" href={projectHref(item.project_id)}>
                    {item.project_name}
                  </a>
                </td>
                <td>
                  <Badge value={item.status} />
                </td>
                <td>
                  <time>{formatTime(item.discovered_at)}</time>
                </td>
              </tr>
            ))}
          </DataTable>
          <footer className="table-footer">
            {items.length} 条漏洞 · 相似发现已由服务合并，操作使用完整漏洞编号
          </footer>
        </ResourceState>
      </Panel>
      {chosen && (
        <VulnerabilityDetail
          item={chosen}
          onClose={() => setDetailID(null)}
          onUpdate={update}
          busy={busy}
        />
      )}
      {detailID && resource.data && !chosen && (
        <div className="notice">
          该漏洞不在当前筛选结果中。
          <Button
            onClick={() => {
              setStatus("");
              setSeverity("");
              setDetailID(null);
            }}
          >
            清除筛选
          </Button>
        </div>
      )}
    </>
  );
}

function VulnerabilityDetail({ item, onClose, onUpdate, busy }) {
  const detail = useResource(projectPath(item.project_id));
  const factIDs = [
    ...new Set([item.fact_id, ...(item.related_fact_ids || [])]),
  ];
  const finding = detail.data?.findings.find(
    (finding) => finding.id === item.finding_id,
  );
  return (
    <Modal
      title={item.title}
      description={`${item.project_name} · ${item.id}`}
      wide
      onClose={onClose}
    >
      <div className="modal-body vulnerability-detail">
        <div className="row-actions">
          <Badge value={item.severity} />
          <Badge value={item.status} />
          <span className="muted">{formatTime(item.discovered_at)}</span>
        </div>
        <h3>发现描述</h3>
        <p className="preserve">{item.description}</p>
        <h3>来源步骤</h3>
        <div className="evidence-box">
          <code>{item.source_step_id || "未绑定步骤"}</code>
          <p>{item.source_step_description || "此发现直接绑定到事实。"}</p>
          <small>
            Worker：{item.source_worker || "—"} · 来源事实：
            {item.source_fact_ids?.join(", ") || "—"}
          </small>
        </div>
        <h3>证据事实</h3>
        <ResourceState resource={detail}>
          {factIDs.map((id) => (
            <div className="evidence-box" key={id}>
              <code>{id}</code>
              <p>
                {detail.data?.facts.find((fact) => fact.id === id)
                  ?.description || "该事实当前不可用"}
              </p>
            </div>
          ))}
          {finding?.data && Object.keys(finding.data).length > 0 && (
            <>
              <h3>发现附加数据</h3>
              <pre>{JSON.stringify(finding.data, null, 2)}</pre>
            </>
          )}
        </ResourceState>
        {item.evidence?.length > 0 && (
          <>
            <h3>汇总证据</h3>
            {item.evidence.map((text, index) => (
              <p className="evidence-box preserve" key={index}>
                {text}
              </p>
            ))}
          </>
        )}
      </div>
      <footer className="modal-footer">
        <a
          href={projectHref(item.project_id)}
          className="button"
          onClick={onClose}
        >
          进入项目 <ArrowUpRight size={14} />
        </a>
        <Button
          onClick={() =>
            onUpdate(
              [item.id],
              item.status === "confirmed" ? "ignored" : "confirmed",
            )
          }
          busy={busy}
          tone={item.status === "ignored" ? "primary" : ""}
        >
          {item.status === "confirmed" ? "标为已忽略" : "重新确认"}
        </Button>
      </footer>
    </Modal>
  );
}
