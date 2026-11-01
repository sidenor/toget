/* Toget account recovery page. All cryptography runs locally in Go/WASM. */
(() => {
  "use strict";

  const MAX_CREDENTIAL_BYTES = 16 * 1024;
  const LANG_KEY = "toget-reset-lang";

  const I18N = {
    zh: {
      brandTag: "账号恢复",
      loadingTitle: "正在准备安全页面",
      loadingBody: "密码和恢复文件都只在这台设备的浏览器里处理。",
      verifyTitle: "正在验证邮箱",
      verifyBody: "请稍候，不要关闭页面。",
      verifyDoneTitle: "邮箱已验证",
      verifyDoneBody: "现在可以关闭这个页面，回到 Toget 继续。之后就能用这个邮箱找回密码。",
      resetEyebrow: "账号恢复",
      resetTitle: "重设账号密码",
      resetBody:
        "邮箱只用来确认是你本人。要解开发件历史和账号密钥，还需要你的恢复文件——整个过程都在本机完成。",
      accountName: "账号",
      accountId: "账号 ID",
      accountEmail: "邮箱",
      fileTitle: "第 1 步 · 选择恢复文件",
      fileHint: "注册账号时保存的 .togetkey 文件。它不会上传，只在本机解锁账号主密钥。",
      fileChoose: "选择恢复文件",
      fileChooseSub: "或把它拖到这里",
      fileMatched: "恢复文件已匹配",
      passwordTitle: "第 2 步 · 设置新密码",
      newPassword: "新密码",
      confirmPassword: "再输入一次",
      showPassword: "显示密码",
      passwordHint: "至少 8 个字符。这个密码以后用来登录 Toget 官方账号。",
      rotateTitle: "恢复文件会同时换新",
      rotateBody: "完成后旧恢复文件立即失效。请务必保存页面随后提供的新文件。",
      resetButton: "重设密码",
      workingTitle: "正在重新加密账号密钥",
      workingBody: "这通常只需要几秒。请不要关闭或刷新页面。",
      doneTitle: "密码已重设",
      doneBody: "现在可以用新密码登录账号了。还差最后一步：保存新的恢复文件。",
      saveTitle: "请立即保存恢复文件",
      saveBody:
        "旧恢复文件已经失效。如果新文件丢了，又没有其他已登录设备，账号历史将无法再解开。",
      downloadButton: "下载恢复文件",
      copyButton: "复制内容",
      savedCheck: "我已将新的恢复文件保存到安全位置",
      finishButton: "完成",
      finishHint: "建议放在加密磁盘或密码管理器里，不要只留在下载文件夹。",
      errorTitle: "无法继续",
      errorReload: "重新打开链接",
      privacy: "恢复文件和新密码不会离开这台设备。服务器只保存无法解密的密文。",
      fileReading: "正在读取恢复文件…",
      fileTooLarge: "恢复文件太大，这个文件看起来不是 Toget 恢复文件。",
      fileEmpty: "恢复文件是空的。",
      fileUnreadable: "无法读取这个文件，请重新选择。",
      checkingFile: "正在验证恢复文件…",
      fileMatchedAccount: "已匹配账号 {account}",
      fileMatchedHint: "恢复文件正确，可以继续设置新密码。",
      passwordRequired: "请先设置新密码。",
      passwordTooShort: "新密码至少需要 8 个字符。",
      passwordTooLong: "新密码太长了，请控制在 1024 字节以内。",
      passwordMismatch: "两次输入的密码不一致。",
      downloadedAuto: "如果浏览器没有自动下载，请点击上面的按钮保存。",
      downloaded: "已开始下载。请确认文件保存好了。",
      copyFailed: "复制失败，请使用“下载恢复文件”按钮。",
      copied: "已复制到剪贴板。请粘贴到安全的位置保存。",
      canClose: "全部完成，可以安全关闭这个页面了。",
      finished: "已完成",
      err: {
        no_token: "链接不完整，缺少重置令牌。",
        no_wasm: "这个浏览器无法运行安全解密模块，请升级浏览器后再试。",
        internal: "操作失败，请刷新页面后重试。",
        invalid_token: "重置链接无效或已过期。",
        rate_limited: "操作太频繁，请稍后再试。",
        host_unavailable: "无法连接账号服务器。",
        invalid_host: "账号服务器身份校验失败。",
        recovery_mismatch: "恢复文件与该账号不匹配。",
        invalid_recovery_file: "恢复文件格式不正确。",
        invalid_blob: "账号密钥数据损坏。",
        recovery_not_ready: "这个账号没有可用的恢复文件，无法通过邮箱重置密码。",
        invalid_credentials: "账号或密码不正确。",
        session_invalid: "登录状态已失效。",
        email_unavailable: "这个账号暂时无法使用邮箱找回。",
        generic: "操作失败，请刷新页面后重试。",
      },
      hint: {
        invalid_token: "请回到 Toget 重新发送找回邮件，并使用最新的链接。",
        recovery_mismatch: "请确认使用的是这个账号最新下载的恢复文件；每次重设后旧文件都会失效。",
        invalid_recovery_file: "请选择注册或上次重设后保存的 .togetkey 文件。",
        host_unavailable: "请检查网络连接；如果你使用的是自建服务器，请确认服务器地址可访问。",
        invalid_host: "为安全起见已停止。请确认地址栏中的服务器地址没有变化。",
        recovery_not_ready:
          "邮箱验证只能重设密码，解开发件历史需要恢复文件。这个账号没有可用的恢复文件，因此无法在线重置。如果还有仍登录的设备，可以在那里生成新的恢复文件。",
        reset_failed:
          "如果重设请求可能已经成功，请先尝试用新密码登录；登录后可以在设备上生成新的恢复文件。",
        default: "请刷新页面重试；如果仍然失败，请回到 Toget 重新发起找回。",
      },
    },
    en: {
      brandTag: "Account recovery",
      loadingTitle: "Preparing a secure page",
      loadingBody: "Your password and recovery file are handled only in this browser.",
      verifyTitle: "Verifying your email",
      verifyBody: "This only takes a moment. Please keep this page open.",
      verifyDoneTitle: "Email verified",
      verifyDoneBody:
        "You can close this page and return to Toget. You can now use this email to recover your password.",
      resetEyebrow: "Account recovery",
      resetTitle: "Reset your password",
      resetBody:
        "Email confirms it's really you. Unlocking your message history and account keys still requires your recovery file — and that happens entirely on this device.",
      accountName: "Account",
      accountId: "Account ID",
      accountEmail: "Email",
      fileTitle: "Step 1 · Choose your recovery file",
      fileHint:
        "The .togetkey file you saved when creating the account. It is never uploaded; it unlocks the account master key locally.",
      fileChoose: "Choose recovery file",
      fileChooseSub: "or drop it here",
      fileMatched: "Recovery file matched",
      passwordTitle: "Step 2 · Set a new password",
      newPassword: "New password",
      confirmPassword: "Enter it again",
      showPassword: "Show password",
      passwordHint:
        "At least 8 characters. You'll use this password to sign in to your hosted Toget account.",
      rotateTitle: "Your recovery file will be rotated too",
      rotateBody:
        "The old recovery file stops working as soon as this finishes. Be sure to save the new file the page gives you.",
      resetButton: "Reset password",
      workingTitle: "Re-encrypting your account keys",
      workingBody: "This usually takes only a few seconds. Please don't close or refresh the page.",
      doneTitle: "Password reset",
      doneBody: "You can sign in with your new password now. One last step: save your new recovery file.",
      saveTitle: "Save your recovery file now",
      saveBody:
        "The old recovery file no longer works. If you lose the new one and have no other signed-in device, your account history cannot be unlocked again.",
      downloadButton: "Download recovery file",
      copyButton: "Copy contents",
      savedCheck: "I've saved the new recovery file somewhere safe",
      finishButton: "Done",
      finishHint: "Keep it on an encrypted disk or in a password manager — not just in your downloads folder.",
      errorTitle: "Can't continue",
      errorReload: "Reopen link",
      privacy:
        "Your recovery file and new password never leave this device. The server only stores ciphertext it cannot read.",
      fileReading: "Reading the recovery file…",
      fileTooLarge: "That file is too large to be a Toget recovery file.",
      fileEmpty: "The recovery file is empty.",
      fileUnreadable: "Couldn't read that file. Please choose it again.",
      checkingFile: "Checking recovery file…",
      fileMatchedAccount: "Matched account {account}",
      fileMatchedHint: "The recovery file is correct. You can set a new password.",
      passwordRequired: "Please enter a new password.",
      passwordTooShort: "Your new password must be at least 8 characters.",
      passwordTooLong: "That password is too long. Keep it under 1024 bytes.",
      passwordMismatch: "The passwords don't match.",
      downloadedAuto: "If your browser didn't download it automatically, use the button above to save it.",
      downloaded: "Download started. Make sure the file was saved.",
      copyFailed: "Copy failed — use “Download recovery file” instead.",
      copied: "Copied. Paste it somewhere safe.",
      canClose: "All done. You can safely close this page.",
      finished: "Done",
      err: {
        no_token: "This link is missing its reset token.",
        no_wasm: "This browser can't run the secure decryption module. Please update your browser and try again.",
        internal: "Something went wrong. Please refresh the page and try again.",
        invalid_token: "This reset link is invalid or has expired.",
        rate_limited: "Too many attempts. Please wait a moment and try again.",
        host_unavailable: "Can't reach the account server.",
        invalid_host: "The account server's identity could not be verified.",
        recovery_mismatch: "That recovery file doesn't match this account.",
        invalid_recovery_file: "That isn't a valid recovery file.",
        invalid_blob: "The account key data is damaged.",
        recovery_not_ready: "This account has no usable recovery file, so email can't reset its password.",
        invalid_credentials: "Incorrect account or password.",
        session_invalid: "Your session has expired.",
        email_unavailable: "Email recovery is unavailable for this account.",
        generic: "Something went wrong. Please refresh and try again.",
      },
      hint: {
        invalid_token: "Return to Toget and request a new recovery email, then use the newest link.",
        recovery_mismatch:
          "Make sure you're using the recovery file most recently downloaded for this account. Every reset invalidates the old one.",
        invalid_recovery_file: "Choose the .togetkey file you saved when you registered or after your last reset.",
        host_unavailable:
          "Check your connection. On a self-hosted account, make sure the server address is reachable.",
        invalid_host:
          "For your safety, the page stopped. Check that the server address in the address bar hasn't changed.",
        recovery_not_ready:
          "Email verification can reset a password, but unlocking history needs a recovery file. This account doesn't have a usable one, so it can't be reset online. If you still have a signed-in device, create a new recovery file there.",
        reset_failed:
          "If the reset may have succeeded, try signing in with your new password first. Once signed in, you can create a new recovery file from your device.",
        default: "Refresh and try again. If it still fails, start a new recovery request from Toget.",
      },
    },
  };

  const state = {
    lang: detectLanguage(),
    purpose: null,
    token: null,
    context: null,
    inspection: null,
    credentialText: null,
    resetError: null,
    result: null,
    saved: false,
    busy: false,
  };
  let api = null;

  const el = (id) => document.getElementById(id);
  const views = [
    "view-loading",
    "view-verify",
    "view-verify-done",
    "view-reset",
    "view-working",
    "view-done",
    "view-error",
  ];

  function detectLanguage() {
    try {
      const saved = localStorage.getItem(LANG_KEY);
      if (saved === "zh" || saved === "en") return saved;
    } catch {
      /* storage can be unavailable in private mode */
    }
    const languages = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language || "en"];
    return languages.some((tag) => String(tag).toLowerCase().startsWith("zh")) ? "zh" : "en";
  }

  function t(key, params) {
    const dict = I18N[state.lang] || I18N.en;
    let value = dict[key];
    if (value === undefined && key.startsWith("err.")) value = dict.err[key.slice(4)];
    if (value === undefined && key.startsWith("hint.")) value = dict.hint[key.slice(5)];
    if (value === undefined) value = I18N.en[key] !== undefined ? I18N.en[key] : key;
    if (typeof value !== "string") return key;
    if (params) {
      for (const [name, replacement] of Object.entries(params)) {
        value = value.split("{" + name + "}").join(String(replacement));
      }
    }
    return value;
  }

  function errorText(code) {
    const dict = I18N[state.lang] || I18N.en;
    return dict.err[code] || dict.err.generic;
  }

  function hintText(code) {
    const dict = I18N[state.lang] || I18N.en;
    return dict.hint[code] || dict.hint.default;
  }

  function applyLanguage() {
    document.documentElement.lang = state.lang === "zh" ? "zh-CN" : "en";
    document.title = state.purpose === "verify" ? (state.lang === "zh" ? "邮箱验证 · Toget" : "Email verification · Toget") : (state.lang === "zh" ? "账号恢复 · Toget" : "Account recovery · Toget");
    for (const node of document.querySelectorAll("[data-i18n]")) {
      const key = node.getAttribute("data-i18n");
      const value = t(key);
      if (typeof value === "string" && value !== key) node.textContent = value;
    }
    const toggle = el("lang-toggle");
    toggle.textContent = state.lang === "zh" ? "EN" : "中文";
    toggle.setAttribute("aria-label", state.lang === "zh" ? "Switch to English" : "切换到中文");
    if (state.saved) el("finish-button").textContent = t("finished");
    refreshDynamicText();
  }

  function refreshDynamicText() {
    if (state.inspection) {
      el("file-chip-text").textContent = t("fileMatchedAccount", { account: state.inspection.account_id });
    }
    if (state.resetError) {
      el("reset-status").textContent = errorText(state.resetError.code);
    }
    if (state.result && !state.saved) {
      el("save-status").textContent = t("downloadedAuto");
    }
  }

  function showView(name) {
    for (const id of views) el(id).hidden = id !== "view-" + name;
  }

  function normalizeError(error) {
    if (!error) return { code: "internal", message: t("err.internal") };
    if (typeof error === "string") return { code: "internal", message: error };
    if (error.code && typeof error.code === "string") {
      return { code: error.code, message: error.message || "" };
    }
    if (error.message) return { code: "internal", message: String(error.message) };
    return { code: "internal", message: "" };
  }

  function showError(error, hintKey) {
    const normalized = normalizeError(error);
    state.resetError = null;
    el("error-message").textContent = errorText(normalized.code);
    el("error-hint").textContent = hintText(hintKey || normalized.code);
    showView("error");
  }

  function setStatus(node, text, kind) {
    node.textContent = text || "";
    node.className = "status" + (kind ? " " + kind : "");
  }

  async function loadWasm() {
    if (!window.WebAssembly || !window.Go) throw { code: "no_wasm" };
    const go = new window.Go();
    const response = await fetch("toget-reset.wasm", { cache: "no-store" });
    if (!response.ok) throw { code: "internal" };
    let instance;
    if (window.WebAssembly.instantiateStreaming) {
      try {
        instance = (await window.WebAssembly.instantiateStreaming(response.clone(), go.importObject)).instance;
      } catch {
        instance = (await window.WebAssembly.instantiate(await response.arrayBuffer(), go.importObject)).instance;
      }
    } else {
      instance = (await window.WebAssembly.instantiate(await response.arrayBuffer(), go.importObject)).instance;
    }
    go.run(instance).catch(() => {
      /* The Go runtime exiting must not leave the page claiming success. */
      if (!state.result) showError({ code: "internal" });
    });
    const deadline = Date.now() + 8000;
    while (!window.togetReset) {
      if (Date.now() > deadline) throw { code: "internal" };
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    return window.togetReset;
  }

  function parseLink() {
    const params = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    const resetToken = params.get("reset");
    if (resetToken) return { purpose: "reset", token: resetToken };
    const verifyToken = params.get("verify");
    if (verifyToken) return { purpose: "verify", token: verifyToken };
    return null;
  }

  function fillAccount(context) {
    el("account-name").textContent = context.name || "—";
    el("account-id").textContent = context.account_id || "—";
    if (context.email_hint) {
      el("account-email").textContent = context.email_hint;
      el("account-email-row").hidden = false;
    } else {
      el("account-email-row").hidden = true;
    }
  }

  async function route() {
    const link = parseLink();
    if (!link) {
      showError({ code: "no_token" }, "no_token");
      return;
    }
    state.purpose = link.purpose;
    state.token = link.token;
    applyLanguage();

    try {
      api = await loadWasm();
    } catch (error) {
      showError(normalizeError(error));
      return;
    }

    if (state.purpose === "verify") {
      showView("verify");
      try {
        await api.verifyEmail(state.token);
        showView("verify-done");
      } catch (error) {
        showError(normalizeError(error));
      }
      return;
    }

    try {
      const context = await api.recoverContext(state.token);
      state.context = context;
      if (!context.recovery_ready) {
        showError({ code: "recovery_not_ready" }, "recovery_not_ready");
        return;
      }
      fillAccount(context);
      showView("reset");
      el("dropzone").focus({ preventScroll: true });
    } catch (error) {
      showError(normalizeError(error));
    }
  }

  async function handleFile(file) {
    if (!file || state.busy) return;
    state.credentialText = null;
    state.inspection = null;
    el("step-password").hidden = true;
    if (file.size === 0) {
      setStatus(el("file-status"), t("fileEmpty"), "error");
      return;
    }
    if (file.size > MAX_CREDENTIAL_BYTES) {
      setStatus(el("file-status"), t("fileTooLarge"), "error");
      return;
    }
    setStatus(el("file-status"), t("fileReading"));
    let text;
    try {
      text = await file.text();
    } catch {
      setStatus(el("file-status"), t("fileUnreadable"), "error");
      return;
    }
    setStatus(el("file-status"), t("checkingFile"));
    try {
      const inspection = await api.inspect(state.token, text);
      state.credentialText = text;
      state.inspection = inspection;
      el("file-chip-text").textContent = t("fileMatchedAccount", { account: inspection.account_id });
      setStatus(el("file-status"), t("fileMatchedHint"), "ok");
      el("step-password").hidden = false;
      el("new-password").focus({ preventScroll: false });
    } catch (error) {
      const normalized = normalizeError(error);
      if (["recovery_mismatch", "invalid_recovery_file", "invalid_blob"].includes(normalized.code)) {
        setStatus(el("file-status"), errorText(normalized.code), "error");
      } else {
        showError(normalized);
      }
    }
  }

  async function submitReset() {
    if (state.busy) return;
    const password = el("new-password").value;
    const confirmation = el("confirm-password").value;
    if (!password) {
      setStatus(el("reset-status"), t("passwordRequired"), "error");
      el("new-password").focus();
      return;
    }
    if ([...password].length < 8) {
      setStatus(el("reset-status"), t("passwordTooShort"), "error");
      el("new-password").focus();
      return;
    }
    if (new TextEncoder().encode(password).length > 1024) {
      setStatus(el("reset-status"), t("passwordTooLong"), "error");
      el("new-password").focus();
      return;
    }
    if (password !== confirmation) {
      setStatus(el("reset-status"), t("passwordMismatch"), "error");
      el("confirm-password").focus();
      return;
    }
    if (!state.credentialText) {
      showError({ code: "invalid_recovery_file" });
      return;
    }
    state.busy = true;
    state.resetError = null;
    setStatus(el("reset-status"), "");
    el("reset-button").disabled = true;
    showView("working");
    try {
      const result = await api.reset(state.token, state.credentialText, password);
      state.result = result;
      state.saved = false;
      state.credentialText = null;
      el("new-password").value = "";
      el("confirm-password").value = "";
      el("credential-filename").textContent = result.filename;
      el("saved-check").checked = false;
      el("finish-button").disabled = true;
      el("finish-button").textContent = t("finishButton");
      setStatus(el("save-status"), t("downloadedAuto"));
      showView("done");
      downloadCredential();
    } catch (error) {
      const normalized = normalizeError(error);
      showView("reset");
      el("step-password").hidden = false;
      if (normalized.code === "invalid_token") {
        showError(normalized, "reset_failed");
      } else {
        state.resetError = normalized;
        const message =
          normalized.code === "host_unavailable" || normalized.code === "internal"
            ? errorText(normalized.code) + " " + hintText("reset_failed")
            : errorText(normalized.code);
        setStatus(el("reset-status"), message, "error");
      }
    } finally {
      state.busy = false;
      el("reset-button").disabled = false;
    }
  }

  function downloadCredential() {
    if (!state.result) return;
    const blob = new Blob([state.result.credential], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = state.result.filename;
    anchor.rel = "noopener";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60 * 1000);
    el("saved-check").checked = false;
    el("finish-button").disabled = true;
  }

  async function copyCredential() {
    if (!state.result) return;
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(state.result.credential);
      } else {
        const area = document.createElement("textarea");
        area.value = state.result.credential;
        area.setAttribute("readonly", "");
        area.className = "copy-buffer";
        document.body.appendChild(area);
        area.select();
        const copied = document.execCommand("copy");
        area.remove();
        if (!copied) throw new Error("copy failed");
      }
      setStatus(el("save-status"), t("copied"), "ok");
    } catch {
      setStatus(el("save-status"), t("copyFailed"), "error");
    }
  }

  function finish() {
    if (!el("saved-check").checked) return;
    state.saved = true;
    el("finish-button").disabled = true;
    el("finish-button").textContent = t("finished");
    setStatus(el("save-status"), t("canClose"), "ok");
  }

  function setupListeners() {
    el("lang-toggle").addEventListener("click", () => {
      state.lang = state.lang === "zh" ? "en" : "zh";
      try {
        localStorage.setItem(LANG_KEY, state.lang);
      } catch {
        /* ignore */
      }
      applyLanguage();
    });

    const input = el("recovery-file");
    input.addEventListener("change", () => {
      const file = input.files && input.files[0];
      handleFile(file);
      input.value = "";
    });

    const dropzone = el("dropzone");
    dropzone.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        input.click();
      }
    });
    for (const name of ["dragenter", "dragover"]) {
      dropzone.addEventListener(name, (event) => {
        event.preventDefault();
        dropzone.classList.add("dragover");
      });
    }
    for (const name of ["dragleave", "drop"]) {
      dropzone.addEventListener(name, (event) => {
        event.preventDefault();
        dropzone.classList.remove("dragover");
      });
    }
    dropzone.addEventListener("drop", (event) => {
      const files = event.dataTransfer && event.dataTransfer.files;
      if (files && files.length > 0) handleFile(files[0]);
    });

    el("show-password").addEventListener("change", (event) => {
      const type = event.target.checked ? "text" : "password";
      el("new-password").type = type;
      el("confirm-password").type = type;
    });

    el("reset-button").addEventListener("click", submitReset);
    el("confirm-password").addEventListener("keydown", (event) => {
      if (event.key === "Enter") submitReset();
    });

    el("download-button").addEventListener("click", () => {
      downloadCredential();
      setStatus(el("save-status"), t("downloaded"));
    });
    el("copy-button").addEventListener("click", copyCredential);
    el("saved-check").addEventListener("change", (event) => {
      el("finish-button").disabled = !event.target.checked;
    });
    el("finish-button").addEventListener("click", finish);
    el("error-reload").addEventListener("click", () => window.location.reload());

    window.addEventListener("beforeunload", (event) => {
      if (state.result && !state.saved) {
        event.preventDefault();
        event.returnValue = "";
      }
    });
  }

  function init() {
    applyLanguage();
    setupListeners();
    showView("loading");
    route();
  }

  init();
})();
