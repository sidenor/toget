import { useState } from "react";
import { profileAvatar } from "./avatars";
import { copyText } from "./helpers";
import type { Profile, Theme } from "./types";
import { Avatar, BrandMark, Icon } from "./ui";
import type { IconName } from "./ui";

export function MePage({
  profile,
  theme,
  isMobile,
  online,
  syncing,
  vaultWarning,
  onTheme,
  onResendVerification,
  onRotateRecovery,
  onDevices,
  onRefresh,
  onLogout,
}: {
  profile: Profile;
  theme: Theme;
  isMobile: boolean;
  online: boolean;
  syncing: boolean;
  vaultWarning: string | null;
  onTheme: (theme: Theme) => void;
  onResendVerification: () => Promise<void>;
  onRotateRecovery: () => void;
  onDevices: () => void;
  onRefresh: () => void;
  onLogout: () => void;
}) {
  const [copied, setCopied] = useState<string | null>(null);
  const [resendBusy, setResendBusy] = useState(false);
  const [resendNote, setResendNote] = useState<string | null>(null);
  const [resendError, setResendError] = useState<string | null>(null);

  const copy = async (kind: string, value: string) => {
    if (await copyText(value)) {
      setCopied(kind);
      window.setTimeout(() => setCopied(null), 1800);
    }
  };

  const resend = async () => {
    setResendBusy(true);
    setResendError(null);
    setResendNote(null);
    try {
      await onResendVerification();
      setResendNote("验证邮件已发送，请查收。");
    } catch (cause) {
      setResendError(
        cause instanceof Error ? cause.message : "发送失败，请稍后再试。",
      );
    } finally {
      setResendBusy(false);
    }
  };

  const options: { value: Theme; label: string; icon: IconName }[] = [
    { value: "light", label: "浅色", icon: "sun" },
    { value: "dark", label: "深色", icon: "moon" },
    { value: "system", label: "自动", icon: "auto" },
  ];

  return (
    <div className={`page me-page ${isMobile ? "mobile-page" : ""}`}>
      {isMobile ? (
        <div className="mobile-head">
          <div className="mobile-heading">
            <BrandMark size={25} />
            <h1>我</h1>
          </div>
        </div>
      ) : (
        <div className="desktop-page-head">
          <h1>我</h1>
        </div>
      )}
      <div className="page-scroll">
        <div className="page-inner">
          <div className="profile-hero me-hero">
            <Avatar
              spec={profileAvatar(profile.account_id, profile.name)}
              size={isMobile ? 76 : 84}
            />
            <div className="profile-copy">
              <h2>{profile.name}</h2>
              <button
                type="button"
                className="profile-id"
                onClick={() => void copy("account", profile.account_id)}
              >
                <span className="mono">{profile.account_id}</span>
                <Icon name={copied === "account" ? "check" : "copy"} size={13} />
              </button>
              <span className="profile-device">
                {profile.device_name}
                {profile.email_verified && (
                  <span className="verified-chip">
                    <Icon name="check" size={11} />
                    邮箱已验证
                  </span>
                )}
              </span>
            </div>
          </div>

          {!profile.email_verified && (
            <div className="verify-card">
              <span className="verify-icon">
                <Icon name="info" size={18} />
              </span>
              <div className="verify-copy">
                <strong>验证一下邮箱</strong>
                <p>验证后，忘记密码时可以通过邮箱找回账号。</p>
                {resendNote && <p className="verify-note">{resendNote}</p>}
                {resendError && <p className="verify-error">{resendError}</p>}
              </div>
              <button
                type="button"
                className="secondary-button small"
                onClick={() => void resend()}
                disabled={resendBusy}
              >
                {resendBusy ? <i className="spinner tiny" /> : null}
                {resendBusy ? "发送中" : "发送邮件"}
              </button>
            </div>
          )}

          {(vaultWarning || profile.vault_pending) && (
            <div className="inline-banner warning">
              <Icon name="info" size={16} />
              <span>
                {vaultWarning ||
                  "房间数据正在等待同步。暂时不要清除浏览器数据。"}
              </span>
            </div>
          )}

          <div className="section-caption">账号</div>
          <div className="card about-card">
            <div className="about-row">
              <span>账号</span>
              <button
                type="button"
                className="copy-value"
                onClick={() => void copy("account-row", profile.account_id)}
                title="复制账号"
              >
                <span className="mono">{profile.account_id}</span>
                <Icon
                  name={copied === "account-row" ? "check" : "copy"}
                  size={14}
                />
              </button>
            </div>
            <div className="about-row">
              <span>邮箱</span>
              <span className={profile.email_verified ? "ok-text" : "dim"}>
                {profile.email_verified ? "已验证" : "待验证"}
              </span>
            </div>
            <div className="about-row">
              <span>这台设备</span>
              <span>{profile.device_name}</span>
            </div>
            <div className="about-row">
              <span>设备编号</span>
              <button
                type="button"
                className="copy-value"
                onClick={() => void copy("device", profile.device_id)}
                title="复制设备编号"
              >
                <span className="mono">{profile.device_id.slice(0, 12)}…</span>
                <Icon
                  name={copied === "device" ? "check" : "copy"}
                  size={14}
                />
              </button>
            </div>
          </div>

          <div className="section-caption">安全</div>
          <div className="card action-list">
            <button
              type="button"
              className="action-row"
              onClick={onRotateRecovery}
            >
              <span className="action-icon">
                <Icon name="download" size={18} />
              </span>
              <span className="action-copy">
                <strong>生成新的恢复文件</strong>
                <small>旧恢复文件会立即失效</small>
              </span>
              <Icon name="chevronRight" size={15} />
            </button>
            <button type="button" className="action-row" onClick={onDevices}>
              <span className="action-icon">
                <Icon name="device" size={18} />
              </span>
              <span className="action-copy">
                <strong>登录设备</strong>
                <small>查看并撤销其他设备的登录</small>
              </span>
              <Icon name="chevronRight" size={15} />
            </button>
            <button type="button" className="action-row" onClick={onLogout}>
              <span className="action-icon danger">
                <Icon name="logout" size={18} />
              </span>
              <span className="action-copy">
                <strong>退出登录</strong>
                <small>清除这台浏览器上的密钥</small>
              </span>
              <Icon name="chevronRight" size={15} />
            </button>
          </div>

          <div className="section-caption">外观</div>
          <div className="card">
            <div className="theme-options" role="radiogroup" aria-label="外观">
              {options.map((option) => (
                <button
                  type="button"
                  key={option.value}
                  role="radio"
                  aria-checked={theme === option.value}
                  className={`theme-option ${
                    theme === option.value ? "active" : ""
                  }`}
                  onClick={() => onTheme(option.value)}
                >
                  <Icon name={option.icon} size={21} />
                  <span>{option.label}</span>
                </button>
              ))}
            </div>
          </div>

          <div className="section-caption">服务</div>
          <div className="card about-card">
            <div className="about-row">
              <span>服务器</span>
              <span className="mono">{window.location.host}</span>
            </div>
            <div className="about-row">
              <span>状态</span>
              <span
                className={`service-state ${
                  online ? "ok" : "down"
                } ${syncing ? "checking" : ""}`}
              >
                <i />
                {syncing ? "同步中" : online ? "已连接" : "连接异常"}
              </span>
            </div>
            <button
              type="button"
              className="about-row about-action"
              onClick={onRefresh}
              disabled={syncing}
            >
              <span>房间同步</span>
              <span>
                {syncing ? "正在同步…" : "立即同步"}
              </span>
            </button>
            <div className="about-row">
              <span>加密</span>
              <span>端到端 · 服务器无法读取</span>
            </div>
          </div>

          <div className="me-footer">
            <BrandMark size={18} />
            <span>Toget 网页版</span>
          </div>
        </div>
      </div>
    </div>
  );
}
