import { useEffect, useState } from "react";
import {
  ArrowRight,
  LockKeyhole,
  RefreshCw,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { api, post, put } from "../api.js";
import {
  Brand,
  Button,
  ErrorMessage,
  FormModal,
  TextField,
} from "../components/ui.jsx";

export default function Auth({ onLogin, expired }) {
  const [mode, setMode] = useState("login");
  const [captcha, setCaptcha] = useState(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [answer, setAnswer] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [captchaError, setCaptchaError] = useState(null);
  const refresh = async () => {
    setCaptcha(null);
    setAnswer("");
    setCaptchaError(null);
    try {
      setCaptcha(await api("/api/auth/captcha", { session: false }));
    } catch (failure) {
      setCaptchaError(failure);
    }
  };
  useEffect(() => {
    refresh();
  }, [mode]);
  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const user = await post(
        `/api/auth/${mode}`,
        {
          username,
          password,
          captcha_id: captcha.captcha_id,
          captcha_answer: answer,
        },
        { session: false },
      );
      setPassword("");
      onLogin(user);
    } catch (failure) {
      setError(failure);
      await refresh();
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="auth-page">
      <div className="auth-story">
        <Brand />
        <div className="auth-story-body">
          <div className="eyebrow">YOUR LOCAL OPERATIONS, CONNECTED.</div>
          <h1>
            从线索出发，
            <br />
            让每一步都有<span>依据。</span>
          </h1>
          <p>
            将目标、执行步骤与证据连接起来，
            <br />
            在一个工作台中掌握安全评估的完整进展。
          </p>
          <div className="auth-graph" aria-hidden="true">
            <div>初始事实</div>
            <i />
            <div>执行步骤</div>
            <i />
            <div className="filled">发现与证据</div>
          </div>
          <div className="auth-points">
            <span>
              <LockKeyhole size={16} />
              本地数据
            </span>
            <span>
              <Waypoints size={16} />
              事实驱动
            </span>
            <span>
              <ShieldCheck size={16} />
              证据可追溯
            </span>
          </div>
        </div>
        <footer>
          HOTMOUSE <span>单进程 · 多 Agent Worker · SQLite</span>
        </footer>
      </div>
      <section className="auth-entry">
        <div className="auth-card">
          <div className="eyebrow">WELCOME TO HOTMOUSE</div>
          <h2>{mode === "login" ? "登录工作台" : "创建账户"}</h2>
          <p>
            {mode === "login"
              ? "继续你的项目，查看最新执行结果。"
              : "创建本地账户，开始管理你的安全项目。"}
          </p>
          <div className="auth-tabs">
            <button
              className={mode === "login" ? "active" : ""}
              onClick={() => {
                setMode("login");
                setError(null);
              }}
            >
              登录
            </button>
            <button
              className={mode === "register" ? "active" : ""}
              onClick={() => {
                setMode("register");
                setError(null);
              }}
            >
              注册
            </button>
          </div>
          {expired && (
            <div className="notice amber">登录会话已过期，请重新登录。</div>
          )}
          <form onSubmit={submit}>
            <ErrorMessage error={error} />
            <TextField
              label="用户名"
              autoComplete="username"
              autoFocus
              required
              minLength={mode === "register" ? 3 : undefined}
              maxLength={32}
              pattern={mode === "register" ? "[A-Za-z0-9_-]+" : undefined}
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="输入用户名"
              hint={
                mode === "register"
                  ? "3–32 位字母、数字、下划线或短横线"
                  : undefined
              }
            />
            <TextField
              label="密码"
              type="password"
              required
              minLength={mode === "register" ? 8 : undefined}
              maxLength={72}
              autoComplete={
                mode === "login" ? "current-password" : "new-password"
              }
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder="输入密码"
              hint={mode === "register" ? "至少 8 位，最多 72 字节" : undefined}
            />
            <div className="captcha-row">
              <TextField
                label="验证码"
                required
                inputMode="numeric"
                autoComplete="off"
                value={answer}
                onChange={(event) => setAnswer(event.target.value)}
                placeholder="计算右侧结果"
              />
              <button
                type="button"
                className="captcha"
                onClick={refresh}
                title="换一道验证码"
                aria-label="刷新验证码"
              >
                {captcha?.question || "加载中…"}
                <RefreshCw size={14} />
              </button>
            </div>
            <ErrorMessage error={captchaError} onRetry={refresh} />
            <Button
              type="submit"
              tone="primary"
              className="auth-submit"
              icon={ArrowRight}
              busy={busy}
              disabled={!captcha}
            >
              {mode === "login" ? "进入工作台" : "注册并登录"}
            </Button>
          </form>
          <div className="auth-footnote">
            <span className="status-dot green" />
            通过本地服务建立会话
          </div>
        </div>
      </section>
    </main>
  );
}

export function PasswordModal({ onClose, notify }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirmation, setConfirmation] = useState("");
  return (
    <FormModal
      title="修改密码"
      description="其他会话将失效，当前登录会保留。"
      onClose={onClose}
      onSubmit={async () => {
        if (next !== confirmation) throw new Error("两次输入的新密码不一致。");
        if (
          !/[a-z]/.test(next) ||
          !/[A-Z]/.test(next) ||
          !/\d/.test(next) ||
          !/[^A-Za-z0-9]/.test(next)
        )
          throw new Error("新密码需包含大写、小写字母、数字和特殊字符。");
        await put("/api/auth/password", {
          current_password: current,
          new_password: next,
        });
        notify("密码已更新");
      }}
    >
      <TextField
        label="当前密码"
        type="password"
        required
        autoComplete="current-password"
        value={current}
        onChange={(event) => setCurrent(event.target.value)}
      />
      <TextField
        label="新密码"
        type="password"
        required
        minLength={8}
        maxLength={72}
        autoComplete="new-password"
        value={next}
        onChange={(event) => setNext(event.target.value)}
        hint="8–72 字节，包含大小写字母、数字和特殊字符"
      />
      <TextField
        label="确认新密码"
        type="password"
        required
        minLength={8}
        autoComplete="new-password"
        value={confirmation}
        onChange={(event) => setConfirmation(event.target.value)}
      />
    </FormModal>
  );
}
