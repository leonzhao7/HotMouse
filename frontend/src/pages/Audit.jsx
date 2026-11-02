import { useState } from "react";
import { Activity, RefreshCw } from "lucide-react";
import { useResource } from "../hooks.js";
import { formatTime } from "../utils.js";
import {
  Badge,
  Button,
  DataTable,
  PageHeader,
  Panel,
  ResourceState,
  SearchInput,
  SelectField,
} from "../components/ui.jsx";

export default function Audit() {
  const [limit, setLimit] = useState(100);
  const [query, setQuery] = useState("");
  const resource = useResource(`/api/audit?limit=${limit}`);
  const rows = (resource.data || []).filter((item) =>
    `${item.actor} ${item.action} ${item.target_id} ${item.summary}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="AUDIT / 操作记录"
        title="审计日志"
        description="查看当前服务中的最近操作，追踪人员与资源变更。"
        actions={
          <Button
            icon={RefreshCw}
            busy={resource.loading}
            onClick={() => resource.reload()}
          >
            刷新
          </Button>
        }
      />
      <Panel>
        <div className="toolbar padded">
          <SearchInput
            value={query}
            onChange={setQuery}
            placeholder="搜索操作、人员或资源"
          />
          <SelectField
            label="最近记录"
            value={limit}
            onChange={(event) => setLimit(Number(event.target.value))}
          >
            {[50, 100, 200, 500].map((value) => (
              <option key={value} value={value}>
                {value} 条
              </option>
            ))}
          </SelectField>
        </div>
        <ResourceState resource={resource}>
          <DataTable
            columns={["时间", "操作人员", "操作", "资源", "摘要", "详情"]}
            count={rows.length}
          >
            {rows.map((item) => (
              <tr key={item.id}>
                <td>
                  <time>{formatTime(item.created_at)}</time>
                </td>
                <td>{item.actor || "system"}</td>
                <td>
                  <Badge tone="blue">{item.action}</Badge>
                </td>
                <td>
                  <small>{item.target_type || "—"}</small>
                  <code className="block-code">{item.target_id || "—"}</code>
                </td>
                <td>{item.summary || "—"}</td>
                <td>
                  {item.detail ? (
                    <details className="history-detail">
                      <summary>查看详情</summary>
                      <pre>
                        {typeof item.detail === "string"
                          ? item.detail
                          : JSON.stringify(item.detail, null, 2)}
                      </pre>
                    </details>
                  ) : (
                    "—"
                  )}
                </td>
              </tr>
            ))}
          </DataTable>
          <footer className="table-footer">
            当前显示 {rows.length} 条 · 最多读取最近 500 条
          </footer>
        </ResourceState>
      </Panel>
    </>
  );
}
