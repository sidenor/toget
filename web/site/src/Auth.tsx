import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { defaultDeviceName } from "./helpers";
import { BrandMark, Icon } from "./ui";

export interface AuthValues {
  mode: "login" | "register";
  accountID: string;
  name: string;
  email: string;
  password: string;
  deviceName: string;
}

export function AuthScreen({
  mode,
  initialAccountID,
  busy,
  error,
  notice,
  expired,
  onMode,
  onSubmit,
  onForgotPassword,
  onUseOtherAccount,
}: {
  mode: "login" | "register";
  initialAccountID: string;
  busy: boolean;
  error: string | null;
  notice: string | null;
  expired: boolean;
  onMode: (mode: "login" | "register") => void;
  onSubmit: (values: AuthValues) => void;
  onForgotPassword: () => void;
  onUseOtherAccount: () => void;
}) {
  const [accountID, setAccountID] = useState(initialAccountID);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [deviceName, setDeviceName] = useState(defaultDeviceName);
  const [showPassword, setShowPassword] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);

  useEffect(() => {
    setAccountID(initialAccountID);
    setPassword("");
    setConfirm("");
    setLocalError(null);
  }, [initialAccountID]);

  useEffect(() => {
    setLocalError(null);
    setPassword("");
    setConfirm("");
  }, [mode]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setLocalError(null);
    if (mode === "login") {
      if (accountID.length !== 14) {
        setLocalError("请输入 14 位账号。");
        return;
      }
      if (!password) {
        setLocalError("请输入密码。");
        return;
      }
    } else {
      if (!name.trim()) {
        setLocalError("请填写名字。");
        return;
      }
      if (!email.includes("@")) {
        setLocalError("请填写可用的邮箱。");
        return;
      }
      if (password.length < 10) {
        setLocalError("密码至少需要 10 位。");
        return;
      }
      if (password !== confirm) {
        setLocalError("两次输入的密码不一致。");
        return;
      }
    }
    onSubmit({
      mode,
      accountID: accountID.trim(),
      name: name.trim(),
      email: email.trim(),
      password,
      deviceName: deviceName.trim() || defaultDeviceName(),
    });
  };

  return (
    <div className="auth-shell">
      <div className="auth-aside">
        <div className="auth-brand">
          <BrandMark size={42} />
          <span>Toget</span>
        </div>
        <div className="auth-statement">
          <h1>安静地聊，<br />认真地记住。</h1>
          <p>
            人、Agent 和消息，在一个房间里。内容在设备上加密，
            服务器只负责把消息送到。
          </p>
        </div>
        <div className="auth-foot">
          <Icon name="lock" size={15} />
          <span>端到端加密 · 官方服务器</span>
        </div>
      </div>

      <main className="auth-main">
        <div className="auth-card">
          <div className="auth-mobile-brand">
            <BrandMark size={34} />
            <span>Toget</span>
          </div>
          <header className="auth-head">
            <h2>
              {mode === "login"
                ? expired
                  ? "重新登录"
                  : "欢迎回来"
                : "创建账号"}
            </h2>
            <p>
              {mode === "login"
                ? expired
                  ? "登录状态已过期，用密码重新进入。"
                  : "用账号和密码进入你的房间。"
                : "注册后会生成一份恢复文件，请妥善保存。"}
            </p>
          </header>

          {(error || localError) && (
            <div className="form-alert" role="alert">
              <Icon name="info" size={16} />
              <span>{localError || error}</span>
            </div>
          )}
          {notice && !error && !localError && (
            <div className="form-note" role="status">
              {notice}
            </div>
          )}

          <form className="auth-form" onSubmit={submit}>
            {mode === "login" ? (
              <>
                <label className="field">
                  <span>账号</span>
                  <input
                    type="text"
                    inputMode="numeric"
                    autoComplete="username"
                    maxLength={14}
                    value={accountID}
                    placeholder="14 位数字账号"
                    onChange={(event) =>
                      setAccountID(event.target.value.replace(/\D/g, "").slice(0, 14))
                    }
                  />
                </label>
                <label className="field">
                  <span>密码</span>
                  <span className="password-field">
                    <input
                      type={showPassword ? "text" : "password"}
                      autoComplete="current-password"
                      value={password}
                      placeholder="输入密码"
                      onChange={(event) => setPassword(event.target.value)}
                    />
                    <button
                      type="button"
                      className="field-action"
                      aria-label={showPassword ? "隐藏密码" : "显示密码"}
                      onClick={() => setShowPassword((shown) => !shown)}
                    >
                      <Icon name={showPassword ? "eyeOff" : "eye"} size={17} />
                    </button>
                  </span>
                </label>
                <button
                  type="button"
                  className="auth-forgot"
                  onClick={onForgotPassword}
                >
                  忘记密码？
                </button>
              </>
            ) : (
              <>
                <label className="field">
                  <span>名字</span>
                  <input
                    type="text"
                    autoComplete="name"
                    maxLength={40}
                    value={name}
                    placeholder="别人会看到的名字"
                    onChange={(event) => setName(event.target.value)}
                  />
                </label>
                <label className="field">
                  <span>邮箱</span>
                  <input
                    type="email"
                    autoComplete="email"
                    maxLength={254}
                    value={email}
                    placeholder="用于验证和找回账号"
                    onChange={(event) => setEmail(event.target.value)}
                  />
                </label>
                <label className="field">
                  <span>密码</span>
                  <span className="password-field">
                    <input
                      type={showPassword ? "text" : "password"}
                      autoComplete="new-password"
                      minLength={10}
                      value={password}
                      placeholder="至少 10 位"
                      onChange={(event) => setPassword(event.target.value)}
                    />
                    <button
                      type="button"
                      className="field-action"
                      aria-label={showPassword ? "隐藏密码" : "显示密码"}
                      onClick={() => setShowPassword((shown) => !shown)}
                    >
                      <Icon name={showPassword ? "eyeOff" : "eye"} size={17} />
                    </button>
                  </span>
                </label>
                <label className="field">
                  <span>确认密码</span>
                  <input
                    type={showPassword ? "text" : "password"}
                    autoComplete="new-password"
                    value={confirm}
                    placeholder="再输入一次"
                    onChange={(event) => setConfirm(event.target.value)}
                  />
                </label>
              </>
            )}

            <label className="field field-compact">
              <span>这台设备</span>
              <input
                type="text"
                maxLength={32}
                value={deviceName}
                placeholder="例如：我的 Mac"
                onChange={(event) => setDeviceName(event.target.value)}
              />
            </label>

            <button className="primary-button auth-submit" type="submit" disabled={busy}>
              {busy ? (
                <span className="button-busy">
                  <i className="spinner" />
                  {mode === "login" ? "正在登录" : "正在创建"}
                </span>
              ) : mode === "login" ? (
                "登录"
              ) : (
                "创建账号"
              )}
            </button>
          </form>

          <div className="auth-switch">
            {mode === "login" ? (
              <>
                <span>还没有账号？</span>
                <button type="button" onClick={() => onMode("register")}>
                  创建一个
                </button>
              </>
            ) : (
              <>
                <span>已经有账号？</span>
                <button type="button" onClick={() => onMode("login")}>
                  去登录
                </button>
              </>
            )}
          </div>

          {expired && (
            <button type="button" className="auth-other" onClick={onUseOtherAccount}>
              使用其他账号
            </button>
          )}
        </div>
      </main>
    </div>
  );
}
