import { useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { api, patch, post } from "../api.js";
import { useResource } from "../hooks.js";
import { useApp } from "../context.jsx";
import {
  Badge,
  Button,
  Empty,
  FormModal,
  Modal,
  ResourceState,
  SearchInput,
  SelectField,
  TextField,
} from "../components/ui.jsx";
import { formatTime } from "../utils.js";

export const resultLabels = {
  progress: "取得进展",
  negative: "已确认负面结果",
  blocked: "目标侧阻碍",
  environment_failure: "执行环境失败",
  no_information: "无新增信息",
  needs_attention: "需要人工介入",
  unclassified: "未分类",
};
const flagLabels = {
  captured: "已捕获",
  submission_pending: "等待提交结果",
  accepted: "平台已接受",
  rejected: "平台已拒绝",
  submission_unknown: "提交结果未知",
};

export default function Strategy({ mode, detail, base, refresh }) {
  const [query, setQuery] = useState("");
  const [modal, setModal] = useState(null);
  const [selected, setSelected] = useState(null);
  const { run } = useApp();
  const resource = useResource(
    mode === "routes"
      ? `${base}/route-records`
      : mode === "flags"
        ? `${base}/flags`
        : mode === "history"
          ? `${base}/history?q=${encodeURIComponent(query)}`
          : `/api/experiences?q=${encodeURIComponent(query)}&limit=50`,
    { interval: 10000 },
  );
  const events = useResource(
    mode === "routes" ? `${base}/decide/events` : null,
    { interval: 10000 },
  );
  const reload = async () => {
    await refresh();
    await resource.reload(true);
    await events.reload(true);
  };
  const act = (work) =>
    run(async () => {
      await work();
      await reload();
    }, "记录已保存");
  const read = (path) => run(async () => setSelected(await api(path)));
  const saved = async () => {
    setModal(null);
    await reload();
  };
  return (
    <>
      <div className="workspace-list-toolbar">
        {["history", "experiences"].includes(mode) && (
          <SearchInput
            value={query}
            onChange={setQuery}
            placeholder={
              mode === "history"
                ? "检索当前项目的历史与证据"
                : "检索跨项目的方法与条件"
            }
          />
        )}
        {mode === "routes" && (
          <>
            <Button
              icon={Plus}
              disabled={detail.project.status !== "active"}
              onClick={() => setModal({ kind: "route" })}
            >
              添加路线
            </Button>
            <Button
              disabled={detail.project.status !== "active"}
              onClick={() =>
                act(() =>
                  post(`${base}/decide/request`, {
                    reason: "manual strategy review",
                  }),
                )
              }
            >
              请求重新规划
            </Button>
          </>
        )}
        {mode === "experiences" && (
          <Button icon={Plus} onClick={() => setModal({ kind: "experience" })}>
            保存经验
          </Button>
        )}
        {mode === "flags" && (
          <Button icon={Plus} onClick={() => setModal({ kind: "flag" })}>
            保存 flag
          </Button>
        )}
        <Button icon={RefreshCw} onClick={reload}>
          刷新记录
        </Button>
      </div>
      {mode === "routes" && (
        <div className="entity-list">
          {(detail.routes || []).map((route) => (
            <article className="entity-card" key={route.id}>
              <div>
                <code>{route.id}</code>
                <Badge tone={route.status === "active" ? "green" : "orange"}>
                  {route.status === "active" ? "可投入" : "已搁置"}
                </Badge>
                <strong>{route.title}</strong>
              </div>
              <p>
                累计 {Math.round(route.invested_ms / 1000)} 秒 /{" "}
                {route.budget_seconds || "不限"} 秒 · 进展{" "}
                {route.progress_count} · 负面结果 {route.negative_count} ·
                连续无信息 {route.no_progress_count}/{route.no_progress_limit} ·
                环境失败 {route.environment_failure_count}
              </p>
              {route.shelved_reason && <p>搁置原因：{route.shelved_reason}</p>}
              {route.reopen_condition && (
                <p>重新开启条件：{route.reopen_condition}</p>
              )}
              {route.next_direction && <p>下一方向：{route.next_direction}</p>}
              <Button
                disabled={detail.project.status !== "active"}
                onClick={() => setModal({ kind: "route-edit", item: route })}
              >
                调整路线
              </Button>
            </article>
          ))}
          {!detail.routes?.length && (
            <Empty
              title="暂无路线"
              description="创建路线或步骤后开始记录投入。"
            />
          )}
          {events.data?.length > 0 && (
            <article className="entity-card">
              <h3>等待重新规划</h3>
              {events.data.map((event) => (
                <p key={event.id}>
                  {event.reason} · {event.source_id} ·{" "}
                  {formatTime(event.created_at)}
                </p>
              ))}
            </article>
          )}
        </div>
      )}
      <ResourceState resource={resource}>
        <div className="entity-list">
          {mode === "routes" &&
            resource.data?.map((record) => (
              <article className="entity-card" key={record.id}>
                <code>{record.route_id}</code>
                <p>
                  {record.action} · {record.reason} ·{" "}
                  {formatTime(record.created_at)}
                </p>
                <p>
                  {record.reopen_condition} {record.next_direction}
                </p>
              </article>
            ))}
          {mode === "history" &&
            resource.data?.map((match) => (
              <button
                className="entity-card"
                key={`${match.kind}:${match.id}`}
                onClick={() => read(`${base}/${match.kind}s/${match.id}`)}
              >
                <div>
                  <code>{match.id}</code>
                  <Badge tone="blue">{match.kind}</Badge>
                  {match.result_type && (
                    <span>
                      {resultLabels[match.result_type] || match.result_type}
                    </span>
                  )}
                </div>
                <p>
                  {match.title} {match.description}
                </p>
              </button>
            ))}
          {mode === "experiences" && (
            <p className="muted">
              复用方法前，请重新验证适用条件、目标状态和证据。
            </p>
          )}
          {mode === "experiences" &&
            resource.data?.map((entry) => (
              <button
                className="entity-card"
                key={entry.id}
                onClick={() => read(`/api/experiences/${entry.id}`)}
              >
                <strong>{entry.title}</strong>
                <p>适用条件：{entry.conditions}</p>
                <p>{entry.method}</p>
                {entry.failure_conditions && (
                  <p>失败条件：{entry.failure_conditions}</p>
                )}
                <small>
                  {entry.tags.join(" · ")} · 来源{" "}
                  {entry.source_project_id || "手动整理"}
                </small>
              </button>
            ))}
          {mode === "flags" &&
            resource.data?.map((flag) => (
              <article className="entity-card" key={flag.id}>
                <div>
                  <code>
                    #{flag.id} {flag.challenge_id}
                  </code>
                  <Badge tone={flag.status === "accepted" ? "green" : "blue"}>
                    {flagLabels[flag.status]}
                  </Badge>
                </div>
                <p>
                  <code>{flag.value}</code>
                </p>
                <p>来源事实：{flag.fact_id}</p>
                {["captured", "rejected"].includes(flag.status) && (
                  <Button
                    onClick={() =>
                      act(() => post(`${base}/flags/${flag.id}/attempts`))
                    }
                  >
                    登记提交尝试
                  </Button>
                )}
                {flag.attempts.map((attempt) => (
                  <div key={attempt.id}>
                    <p>
                      尝试 #{attempt.id} · {flagLabels[attempt.status]} ·{" "}
                      {attempt.response}
                    </p>
                    {["submission_pending", "submission_unknown"].includes(
                      attempt.status,
                    ) && (
                      <Button
                        onClick={() =>
                          setModal({
                            kind: "flag-result",
                            item: { flag, attempt },
                          })
                        }
                      >
                        核对并记录平台结果
                      </Button>
                    )}
                  </div>
                ))}
              </article>
            ))}
          {mode !== "routes" && !resource.data?.length && (
            <Empty title={mode === "history" ? "暂无匹配历史" : "暂无记录"} />
          )}
        </div>
      </ResourceState>
      {selected && (
        <Modal title="完整记录" onClose={() => setSelected(null)} wide>
          <div className="modal-body">
            <pre className="block-code">
              {JSON.stringify(selected, null, 2)}
            </pre>
          </div>
        </Modal>
      )}
      {modal && (
        <StrategyModal
          key={`${modal.kind}:${modal.item?.id || ""}`}
          {...modal}
          detail={detail}
          base={base}
          onClose={() => setModal(null)}
          onSaved={saved}
        />
      )}
    </>
  );
}

function StrategyModal({ kind, item, detail, base, onClose, onSaved }) {
  const [values, setValues] = useState({
    title: item?.title || "",
    goal_id: item?.goal_id || "",
    budget_seconds: item?.budget_seconds ?? 1200,
    no_progress_limit: item?.no_progress_limit ?? 3,
    status: item?.status || (kind === "flag-result" ? "accepted" : "active"),
    reason: "",
    reopen_condition: item?.reopen_condition || "",
    next_direction: item?.next_direction || "",
    conditions: "",
    method: "",
    failure_conditions: "",
    detail: "",
    tags: "",
    value: "",
    fact_id: "",
    challenge_id: "",
    response: "",
  });
  const field = (
    key,
    label,
    required = false,
    multiline = false,
    type = "text",
  ) => (
    <TextField
      key={key}
      label={label}
      required={required}
      multiline={multiline}
      type={type}
      value={values[key]}
      onChange={(e) => setValues((v) => ({ ...v, [key]: e.target.value }))}
    />
  );
  const select = (key, label, options) => (
    <SelectField
      label={label}
      value={values[key]}
      onChange={(e) => setValues((v) => ({ ...v, [key]: e.target.value }))}
    >
      {options.map(([id, name]) => (
        <option key={id} value={id}>
          {name}
        </option>
      ))}
    </SelectField>
  );
  const submit = async () => {
    if (kind === "route")
      await post(`${base}/routes`, {
        title: values.title,
        goal_id: values.goal_id || null,
        budget_seconds: Number(values.budget_seconds),
        no_progress_limit: Number(values.no_progress_limit),
      });
    if (kind === "route-edit")
      await patch(`${base}/routes/${item.id}`, {
        status: values.status,
        budget_seconds: Number(values.budget_seconds),
        no_progress_limit: Number(values.no_progress_limit),
        reason: values.reason,
        reopen_condition: values.reopen_condition,
        next_direction: values.next_direction,
      });
    if (kind === "experience")
      await post("/api/experiences", {
        title: values.title,
        conditions: values.conditions,
        method: values.method,
        failure_conditions: values.failure_conditions,
        detail: values.detail,
        tags: values.tags.split(/[\s,]+/).filter(Boolean),
        source_project_id: detail.project.id,
        source_fact_ids: values.fact_id ? [values.fact_id] : [],
      });
    if (kind === "flag") {
      if (!values.fact_id) throw new Error("请选择来源事实");
      await post(`${base}/flags`, {
        value: values.value,
        fact_id: values.fact_id,
        challenge_id: values.challenge_id,
      });
    }
    if (kind === "flag-result")
      await patch(`${base}/flags/${item.flag.id}/attempts/${item.attempt.id}`, {
        status: values.status,
        response: values.response,
      });
    await onSaved();
  };
  const titles = {
    route: "添加路线",
    "route-edit": "调整路线",
    experience: "保存经验",
    flag: "保存 flag",
    "flag-result": "记录平台结果",
  };
  return (
    <FormModal title={titles[kind]} onClose={onClose} onSubmit={submit}>
      {["route", "experience"].includes(kind) && field("title", "标题", true)}
      {kind === "route" &&
        select("goal_id", "关联目标", [
          ["", "不关联目标"],
          ...detail.goals
            .filter((g) => g.status === "active")
            .map((g) => [g.id, `${g.id} · ${g.description}`]),
        ])}
      {["route", "route-edit"].includes(kind) && (
        <>
          {field(
            "budget_seconds",
            "路线预算（秒，0 为不限）",
            true,
            false,
            "number",
          )}
          {field("no_progress_limit", "连续无信息上限", true, false, "number")}
        </>
      )}
      {kind === "route-edit" && (
        <>
          {select("status", "路线状态", [
            ["active", "可投入"],
            ["shelved", "搁置"],
          ])}
          {field("reason", "调整原因", true)}
          {field("reopen_condition", "重新开启条件")}
          {field("next_direction", "下一方向")}
        </>
      )}
      {kind === "experience" && (
        <>
          {field("conditions", "适用条件", true, true)}
          {field("method", "已验证方法", true, true)}
          {field("failure_conditions", "失败条件", false, true)}
          {field("detail", "详细记录", false, true)}
          {field("tags", "标签（逗号或空格分隔）")}
        </>
      )}
      {["flag", "experience"].includes(kind) &&
        select("fact_id", "来源事实", [
          ["", "请选择"],
          ...detail.facts
            .filter(
              (f) =>
                ![
                  "environment_failure",
                  "no_information",
                  "needs_attention",
                ].includes(f.result_type),
            )
            .map((f) => [f.id, `${f.id} · ${f.description.slice(0, 70)}`]),
        ])}
      {kind === "flag" && (
        <>
          {field("value", "flag", true)}
          {field("challenge_id", "题目编号")}
        </>
      )}
      {kind === "flag-result" && (
        <>
          {select("status", "平台结果", [
            ["accepted", "已接受"],
            ["rejected", "已拒绝"],
            ["submission_unknown", "未知，需要核对"],
          ])}
          {field("response", "平台回复", false, true)}
        </>
      )}
    </FormModal>
  );
}
