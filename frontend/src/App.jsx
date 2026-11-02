import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity,
  Bell,
  Check,
  FileText,
  FolderKanban,
  Home,
  KeyRound,
  LogOut,
  Menu,
  Monitor,
  Moon,
  Search,
  Settings,
  ShieldAlert,
  Sun,
  X,
} from "lucide-react";
import { api, post, remove } from "./api.js";
import { AppContext, useApp } from "./context.jsx";
import { useResource, useRoute } from "./hooks.js";
import {
  notificationHref,
  projectHref,
  readStored,
  shortTime,
  truncate,
  writeStored,
} from "./utils.js";
import {
  Badge,
  Brand,
  Button,
  ConfirmModal,
  Empty,
  ErrorMessage,
  IconButton,
  Loading,
  Modal,
  ResourceState,
  SearchInput,
} from "./components/ui.jsx";
import Auth, { PasswordModal } from "./pages/Auth.jsx";
import Dashboard from "./pages/Dashboard.jsx";
import Projects, { ProjectCreateModal } from "./pages/Projects.jsx";
import Workspace from "./pages/Workspace.jsx";
import Vulnerabilities from "./pages/Vulnerabilities.jsx";
import Workers from "./pages/Workers.jsx";
import Templates from "./pages/Templates.jsx";
import SystemSettings from "./pages/Settings.jsx";
import Audit from "./pages/Audit.jsx";

const navigation = [
  ["dashboard", "总览", Home],
  ["projects", "项目", FolderKanban],
  ["vulnerabilities", "漏洞", ShieldAlert],
  ["workers", "本地 Worker", Monitor],
  ["templates", "项目模板", FileText],
  ["audit", "审计日志", Activity],
  ["settings", "系统设置", Settings],
];

export default function App() {
  const route = useRoute();
  const [user, setUser] = useState(undefined);
  const [authError, setAuthError] = useState(null);
  const [expired, setExpired] = useState(false);
  const [theme, setTheme] = useState(() => readStored("theme", "dark"));
  const [toast, setToast] = useState(null);
  const [confirmation, setConfirmation] = useState(null);
  const [dialog, setDialog] = useState(null);
  const [createTemplate, setCreateTemplate] = useState(null);
  const [mobile, setMobile] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const notify = useCallback(
    (message, type = "success") => setToast({ message, type, id: Date.now() }),
    [],
  );
  const confirm = useCallback((options) => setConfirmation(options), []);
  const createProject = useCallback((template = null) => {
    setCreateTemplate(template);
    setDialog("project");
  }, []);
  const checkAuth = useCallback(async () => {
    setAuthError(null);
    setUser(undefined);
    try {
      setUser(await api("/api/auth/me", { session: false }));
    } catch (error) {
      if (error.status !== 401) {
        setAuthError(error);
        return;
      }
      try {
        const health = await api("/api/settings/health", { session: false });
        if (
          health.checks?.some(
            (check) => check.key === "auth" && check.status === "warning",
          )
        )
          setUser({ username: "本地模式", development: true });
        else setUser(null);
      } catch (failure) {
        if (failure.status === 401) setUser(null);
        else setAuthError(failure);
      }
    }
  }, []);
  useEffect(() => {
    checkAuth();
  }, [checkAuth]);
  useEffect(() => {
    const unauthorized = () => {
      setUser(null);
      setExpired(true);
      setDialog(null);
      setConfirmation(null);
    };
    window.addEventListener("hotmouse:unauthorized", unauthorized);
    return () =>
      window.removeEventListener("hotmouse:unauthorized", unauthorized);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    writeStored("theme", theme);
  }, [theme]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(
      () => setToast(null),
      toast.type === "error" ? 9000 : 4500,
    );
    return () => clearTimeout(timer);
  }, [toast]);
  useEffect(() => {
    setMobile(false);
    setAccountOpen(false);
  }, [route.page, route.id]);
  useEffect(() => {
    const keyboard = (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key === "k") {
        event.preventDefault();
        if (user) setDialog("search");
      }
      if (event.key === "Escape") {
        setMobile(false);
        setAccountOpen(false);
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, [user]);
  const run = useCallback(
    async (work, message) => {
      try {
        const result = await work();
        if (message)
          notify(typeof message === "function" ? message(result) : message);
        return true;
      } catch (error) {
        notify(error.message, "error");
        return false;
      }
    },
    [notify],
  );
  const context = useMemo(
    () => ({ user, theme, notify, confirm, run, createProject }),
    [user, theme, notify, confirm, run, createProject],
  );
  const closeDialog = () => setDialog(null);
  if (authError)
    return (
      <div className="boot">
        <Brand />
        <ErrorMessage error={authError} onRetry={checkAuth} />
      </div>
    );
  if (user === undefined)
    return (
      <div className="boot">
        <Brand />
        <Loading text="正在连接 HotMouse…" />
      </div>
    );
  if (user === null)
    return (
      <Auth
        expired={expired}
        onLogin={(value) => {
          setUser(value);
          setExpired(false);
        }}
      />
    );
  const current = navigation.find(([key]) => key === route.page);
  const content =
    route.page === "dashboard" ? (
      <Dashboard />
    ) : route.page === "projects" ? (
      route.id ? (
        <Workspace key={route.id} id={route.id} />
      ) : (
        <Projects />
      )
    ) : route.page === "vulnerabilities" ? (
      <Vulnerabilities
        key={route.params.toString()}
        projectFilter={route.params.get("project_id") || ""}
      />
    ) : route.page === "workers" ? (
      <Workers />
    ) : route.page === "templates" ? (
      <Templates />
    ) : route.page === "settings" ? (
      <SystemSettings />
    ) : route.page === "audit" ? (
      <Audit />
    ) : (
      <Empty
        title="页面不存在"
        action={
          <a className="button primary" href="#/dashboard">
            返回总览
          </a>
        }
      />
    );
  return (
    <AppContext.Provider value={context}>
      <div className="app-shell">
        <aside className={`sidebar ${mobile ? "open" : ""}`}>
          <Brand />
          <div className="sidebar-label">工作空间</div>
          <nav aria-label="主导航">
            {navigation.map(([key, name, Icon]) => (
              <a
                key={key}
                href={`#/${key}`}
                className={route.page === key ? "active" : ""}
                aria-current={route.page === key ? "page" : undefined}
              >
                <Icon size={18} />
                <span>{name}</span>
                {key === "workers" && <small>01</small>}
              </a>
            ))}
          </nav>
          <div className="sidebar-bottom">
            <div className="local-label">
              <span className="status-dot green" />
              <span>本地工作空间</span>
              <Badge tone="muted">LOCAL</Badge>
            </div>
            <p>事实 → 步骤 → 证据</p>
            <span className="version">HOTMOUSE / v0.1</span>
          </div>
        </aside>
        {mobile && (
          <button
            className="sidebar-backdrop"
            aria-label="关闭导航"
            onClick={() => setMobile(false)}
          />
        )}
        <div className="app-main">
          <header className="topbar">
            <IconButton
              icon={Menu}
              label="展开导航"
              onClick={() => setMobile(!mobile)}
            />
            <div className="breadcrumb">
              <span>工作空间</span>
              <span>/</span>
              <strong>
                {current?.[1] || "页面"}
                {route.id ? " / 项目工作区" : ""}
              </strong>
            </div>
            <button
              className="global-search"
              onClick={() => setDialog("search")}
            >
              <Search size={15} />
              <span>搜索项目、漏洞…</span>
              <kbd>⌘ K</kbd>
            </button>
            <div className="topbar-actions">
              <NotificationButton onClick={() => setDialog("notifications")} />
              <IconButton
                icon={theme === "dark" ? Sun : Moon}
                label={theme === "dark" ? "切换浅色主题" : "切换深色主题"}
                onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
              />
              <div className="account">
                <button
                  className="account-button"
                  aria-expanded={accountOpen}
                  aria-label="账户菜单"
                  onClick={() => setAccountOpen(!accountOpen)}
                >
                  <span>{user.username.slice(0, 1).toUpperCase()}</span>
                  <strong>{user.username}</strong>
                </button>
                {accountOpen && (
                  <div className="account-menu">
                    {user.development ? (
                      <p>服务已禁用登录认证</p>
                    ) : (
                      <>
                        <Button
                          icon={KeyRound}
                          onClick={() => {
                            setAccountOpen(false);
                            setDialog("password");
                          }}
                        >
                          修改密码
                        </Button>
                        <Button
                          icon={LogOut}
                          onClick={() =>
                            confirm({
                              title: "退出登录",
                              message: "退出当前工作台会话。",
                              danger: false,
                              onConfirm: async () => {
                                await post("/api/auth/logout");
                                setUser(null);
                                setExpired(false);
                              },
                            })
                          }
                        >
                          退出登录
                        </Button>
                      </>
                    )}
                  </div>
                )}
              </div>
            </div>
          </header>
          {user.development && (
            <div className="dev-banner">
              <LockIcon />
              认证已关闭 · 当前处于本地开发模式
            </div>
          )}
          <main
            className={`page ${route.id && route.page === "projects" ? "workspace-page" : ""}`}
            key={route.page}
          >
            {content}
          </main>
        </div>
      </div>
      {toast && (
        <div
          className={`toast ${toast.type}`}
          role={toast.type === "error" ? "alert" : "status"}
        >
          {toast.type === "success" && <Check size={17} />}
          <span>{toast.message}</span>
          <IconButton
            icon={X}
            label="关闭提示"
            onClick={() => setToast(null)}
          />
        </div>
      )}
      {confirmation && (
        <ConfirmModal {...confirmation} onClose={() => setConfirmation(null)} />
      )}
      {dialog === "project" && (
        <ProjectCreateModal template={createTemplate} onClose={closeDialog} />
      )}
      {dialog === "password" && (
        <PasswordModal onClose={closeDialog} notify={notify} />
      )}
      {dialog === "search" && <GlobalSearch onClose={closeDialog} />}
      {dialog === "notifications" && <Notifications onClose={closeDialog} />}
    </AppContext.Provider>
  );
}

function LockIcon() {
  return <KeyRound size={13} />;
}
function NotificationButton({ onClick }) {
  const resource = useResource("/api/notifications/unread-count", {
    interval: 20000,
  });
  useEffect(() => {
    const refresh = () => resource.reload(true);
    window.addEventListener("hotmouse:notifications", refresh);
    return () => window.removeEventListener("hotmouse:notifications", refresh);
  }, [resource.reload]);
  return (
    <button
      type="button"
      className="notification-button icon-button"
      aria-label={`通知${resource.data?.count ? `，${resource.data.count} 条未读` : ""}`}
      title={resource.error ? "通知加载失败，点击重试" : "通知"}
      onClick={onClick}
    >
      <Bell size={18} />
      {resource.data?.count > 0 && (
        <span>{resource.data.count > 99 ? "99+" : resource.data.count}</span>
      )}
    </button>
  );
}
function Notifications({ onClose }) {
  const resource = useResource("/api/notifications?limit=200");
  const { run, confirm } = useApp();
  const [busy, setBusy] = useState(false);
  const change = async (work) => {
    setBusy(true);
    await run(async () => {
      await work();
      await resource.reload();
      window.dispatchEvent(new Event("hotmouse:notifications"));
    });
    setBusy(false);
  };
  return (
    <Modal title="通知中心" description="最近 200 条通知" onClose={onClose}>
      <div className="notification-toolbar">
        <Button
          icon={Check}
          busy={busy}
          onClick={() =>
            change(() => post("/api/notifications/read", { ids: [] }))
          }
        >
          全部标为已读
        </Button>
        <Button
          disabled={busy}
          onClick={() =>
            confirm({
              title: "清空通知",
              message: "将删除全部通知，包括尚未阅读的通知。",
              onConfirm: () => change(() => remove("/api/notifications")),
            })
          }
        >
          清空通知
        </Button>
      </div>
      <div className="modal-body">
        <ResourceState resource={resource}>
          {resource.data?.length === 0 && <Empty title="暂无通知" />}
          {resource.data?.map((item) => (
            <article
              className={`notification-item ${item.read ? "" : "unread"}`}
              key={item.id}
            >
              <div>
                <Badge value={item.level} />
                <time>{shortTime(item.created_at)}</time>
              </div>
              <h3>{item.title}</h3>
              <p>{item.body}</p>
              <div className="row-actions">
                {notificationHref(item.link) && (
                  <a
                    className="text-link"
                    href={notificationHref(item.link)}
                    onClick={onClose}
                  >
                    查看详情
                  </a>
                )}
                {!item.read && (
                  <Button
                    disabled={busy}
                    onClick={() =>
                      change(() =>
                        post("/api/notifications/read", { ids: [item.id] }),
                      )
                    }
                  >
                    标为已读
                  </Button>
                )}
              </div>
            </article>
          ))}
        </ResourceState>
      </div>
    </Modal>
  );
}
function GlobalSearch({ onClose }) {
  const [query, setQuery] = useState("");
  const projects = useResource("/projects");
  const vulnerabilities = useResource("/api/vulnerabilities");
  const needle = query.trim().toLowerCase();
  const projectResults = (projects.data || [])
    .filter((item) => `${item.title} ${item.id}`.toLowerCase().includes(needle))
    .slice(0, 8);
  const vulnerabilityResults = needle
    ? (vulnerabilities.data || [])
        .filter((item) =>
          `${item.title} ${item.id} ${item.project_name} ${item.fact_id}`
            .toLowerCase()
            .includes(needle),
        )
        .slice(0, 8)
    : [];
  return (
    <Modal
      title="全局搜索"
      description="搜索项目标题、漏洞标题和编号"
      onClose={onClose}
    >
      <div className="modal-body">
        <SearchInput
          value={query}
          onChange={setQuery}
          placeholder="输入关键词或编号"
        />
        <h3 className="section-label">项目</h3>
        <ResourceState resource={projects}>
          {projectResults.map((item) => (
            <a
              className="search-result"
              key={item.id}
              href={projectHref(item.id)}
              onClick={onClose}
            >
              <FolderKanban size={17} />
              <span>
                <strong>{item.title}</strong>
                <small>{item.id}</small>
              </span>
              <Badge value={item.status} />
            </a>
          ))}
          {!projectResults.length && <p className="muted">没有匹配的项目。</p>}
        </ResourceState>
        {needle && (
          <>
            <h3 className="section-label">漏洞</h3>
            <ResourceState resource={vulnerabilities}>
              {vulnerabilityResults.map((item) => (
                <a
                  className="search-result"
                  key={item.id}
                  href={`#/vulnerabilities?project_id=${encodeURIComponent(item.project_id)}&id=${encodeURIComponent(item.id)}`}
                  onClick={onClose}
                >
                  <ShieldAlert size={17} />
                  <span>
                    <strong>{truncate(item.title, 55)}</strong>
                    <small>{item.project_name}</small>
                  </span>
                  <Badge value={item.severity} />
                </a>
              ))}
              {!vulnerabilityResults.length && (
                <p className="muted">没有匹配的漏洞。</p>
              )}
            </ResourceState>
          </>
        )}
      </div>
    </Modal>
  );
}
