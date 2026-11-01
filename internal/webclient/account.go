package webclient

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

// RegisterInput creates a browser account.
type RegisterInput struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

// LoginInput signs in and adds this browser as a new account device.
type LoginInput struct {
	AccountID  string `json:"account_id"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

// AuthResult is returned by register and login.
type AuthResult struct {
	Profile     Profile       `json:"profile"`
	EmailSent   bool          `json:"email_sent"`
	Recovery    *RecoveryFile `json:"recovery,omitempty"`
	VaultLoaded bool          `json:"vault_loaded"`
	VaultError  string        `json:"vault_error,omitempty"`
}

// Register creates an account on this origin and signs this browser in.
func (c *Client) Register(ctx context.Context, input RegisterInput) (*AuthResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account != nil {
		return nil, Errorf("already_signed_in", "这台设备已经登录了一个账号。")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, Errorf("invalid_name", "请填写名字。")
	}
	if len([]rune(name)) > 40 {
		return nil, Errorf("invalid_name", "名字最多 40 个字。")
	}
	email := strings.TrimSpace(input.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, Errorf("invalid_email", "请填写邮箱。")
	}
	if len(input.Password) < 10 {
		return nil, Errorf("weak_password", "密码至少需要 10 位。")
	}
	deviceName := cleanDeviceName(input.DeviceName)
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	result, err := client.Register(ctx, hosted.RegisterOptions{
		Name: name, Email: email, Password: input.Password,
		DeviceName: deviceName,
	})
	if err != nil {
		return nil, mapHostError(err)
	}
	now := c.now().UTC().Truncate(time.Millisecond)
	acct := &AccountState{
		ID: result.AccountID, Name: result.Name,
		HostURL: c.origin, HostID: result.HostID, HostCode: result.HostCode,
		PublicKey:  append([]byte(nil), result.AccountPublicKey...),
		PrivateKey: append([]byte(nil), result.AccountPrivateKey...),
		MasterKey:  append([]byte(nil), result.MasterKey...),
		Device: DeviceState{
			ID: result.Device.ID, Name: result.Device.Name,
			PublicKey:            append([]byte(nil), result.Device.PublicKey...),
			PrivateKey:           append([]byte(nil), result.Device.PrivateKey...),
			EncryptionPublicKey:  append([]byte(nil), result.Device.EncryptionPublicKey...),
			EncryptionPrivateKey: append([]byte(nil), result.Device.EncryptionPrivateKey...),
			Certificate:          result.Device.Certificate,
			CreatedAt:            result.Device.CreatedAt,
		},
		SessionToken: result.SessionToken, SessionExpiresAt: result.SessionExpiresAt,
		EmailVerified: false, CreatedAt: result.CreatedAt,
	}
	if acct.CreatedAt.IsZero() {
		acct.CreatedAt = now
	}
	c.state.Account = acct
	c.state.Rooms = nil
	c.recovery = &RecoveryFile{
		Filename: recoveryFilename(result.RecoveryCredential, result.AccountID),
		Content:  string(result.RecoveryCredentialRaw),
	}
	return &AuthResult{
		Profile: *c.profileLocked(), EmailSent: result.EmailSent,
		Recovery: c.recovery,
	}, nil
}

// Login signs in with a password and publishes this browser as a new device.
func (c *Client) Login(ctx context.Context, input LoginInput) (*AuthResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || input.Password == "" {
		return nil, Errorf("invalid_credentials", "账号或密码不正确。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	result, err := client.Login(ctx, accountID, input.Password)
	if err != nil {
		return nil, mapHostError(err)
	}
	now := c.now().UTC().Truncate(time.Millisecond)
	device, err := newBrowserDevice(result.AccountID, input.DeviceName, result.AccountPrivateKey, now)
	if err != nil {
		return nil, err
	}
	record := account.DeviceRecord{
		ID: device.ID, AccountID: result.AccountID, Name: device.Name,
		PublicKey: device.PublicKey, EncryptionPublicKey: device.EncryptionPublicKey,
		Certificate: device.Certificate, Role: "web", CreatedAt: now,
	}
	syncResult, err := client.Sync(ctx, result.SessionToken, hosted.SyncRequest{
		Devices: []account.DeviceRecord{record},
	})
	if err != nil {
		return nil, mapHostError(err)
	}
	if !deviceInRecords(syncResult.Devices, device.ID) {
		return nil, Errorf("device_rejected", "服务器没有接受这台设备，请稍后重试。")
	}
	createdAt := result.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	c.state.Account = &AccountState{
		ID: result.AccountID, Name: result.Name,
		HostURL: c.origin, HostID: result.HostID, HostCode: result.HostCode,
		PublicKey:    append([]byte(nil), result.PublicKey...),
		PrivateKey:   append([]byte(nil), result.AccountPrivateKey...),
		MasterKey:    append([]byte(nil), result.MasterKey...),
		Device:       *device,
		SessionToken: result.SessionToken, SessionExpiresAt: result.SessionExpiresAt,
		EmailVerified: result.EmailVerified, CreatedAt: createdAt,
	}
	c.state.Rooms = nil
	vaultLoaded, vaultErr := c.loadVaultLocked(ctx, client)
	authResult := &AuthResult{Profile: *c.profileLocked(), VaultLoaded: vaultLoaded}
	if vaultErr != nil {
		authResult.VaultError = AsError(vaultErr).Message
	}
	return authResult, nil
}

// Resume refreshes the short-lived host session after a page reload. It never
// asks for the password: the device certificate authorizes the refresh.
func (c *Client) Resume(ctx context.Context) (*Profile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return nil, err
	}
	if err := c.refreshAccountStatusLocked(ctx, client); err != nil {
		return nil, err
	}
	// Best effort: a stale vault must not break sign-in. The next room
	// operation retries and reports a precise error.
	_, _ = c.loadVaultLocked(ctx, client)
	return c.profileLocked(), nil
}

// refreshAccountStatusLocked refreshes flags that are changed outside this
// browser, such as email verification completed in another tab.
func (c *Client) refreshAccountStatusLocked(ctx context.Context, client *hosted.Client) error {
	acct := c.state.Account
	if acct == nil {
		return Errorf("signed_out", "还没有登录。")
	}
	result, err := client.Sync(ctx, acct.SessionToken, hosted.SyncRequest{})
	if err != nil {
		return mapHostError(err)
	}
	acct.EmailVerified = result.EmailVerified
	return nil
}

// RequestRecovery asks the host to email password-reset links for an account
// ID or email address. The host deliberately gives the same response whether
// or not the account exists.
func (c *Client) RequestRecovery(ctx context.Context, user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return Errorf("invalid_user", "请输入账号或邮箱。")
	}
	if len([]rune(user)) > 254 {
		return Errorf("invalid_user", "账号或邮箱太长了。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return err
	}
	if err := client.RequestRecovery(ctx, user); err != nil {
		return mapHostError(err)
	}
	return nil
}

// ResendVerification asks the host to resend the email verification link.
func (c *Client) ResendVerification(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return err
	}
	if err := client.ResendEmail(ctx, c.state.Account.SessionToken); err != nil {
		return mapHostError(err)
	}
	return nil
}

// RotateRecovery installs a fresh recovery file. The previous file stops
// working as soon as the host accepts the new one.
func (c *Client) RotateRecovery(ctx context.Context) (*RecoveryFile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return nil, err
	}
	acct := c.state.Account
	credential, raw, err := client.RotateRecoveryAt(ctx, acct.SessionToken, acct.ID, acct.MasterKey, c.hostURLWithIDLocked())
	if err != nil {
		return nil, mapHostError(err)
	}
	return &RecoveryFile{
		Filename: recoveryFilename(credential, acct.ID),
		Content:  string(raw),
	}, nil
}

func (c *Client) refreshSessionLocked(ctx context.Context, client *hosted.Client) error {
	acct := c.state.Account
	if acct == nil {
		return Errorf("signed_out", "还没有登录。")
	}
	if acct.SessionToken != "" && acct.SessionExpiresAt.After(c.now().Add(2*time.Minute)) {
		return nil
	}
	token, expires, err := client.DeviceSession(ctx, acct.ID, acct.Device.ID, ed25519.PrivateKey(acct.Device.PrivateKey))
	if err != nil {
		return mapHostError(err)
	}
	acct.SessionToken = token
	acct.SessionExpiresAt = expires
	return nil
}

func (c *Client) hostedClient() (*hosted.Client, error) {
	client, err := hosted.NewBrowserClient(c.origin)
	if err != nil {
		return nil, Errorf("internal", "无法连接服务器。")
	}
	return client, nil
}

func newBrowserDevice(accountID, name string, accountKey ed25519.PrivateKey, now time.Time) (*DeviceState, error) {
	if !validAccountID(accountID) || len(accountKey) != ed25519.PrivateKeySize {
		return nil, Errorf("internal", "账号信息不完整。")
	}
	publicKey, privateKey, err := identity.NewDeviceKey()
	if err != nil {
		return nil, Errorf("internal", "无法生成设备密钥。")
	}
	encryptionPublic, encryptionPrivate, err := identity.NewEncryptionKey()
	if err != nil {
		return nil, Errorf("internal", "无法生成设备密钥。")
	}
	deviceID := identity.NewID()
	certificate, err := identity.SignDeviceCertificate(deviceID, accountID, publicKey, encryptionPublic, accountKey, now)
	if err != nil {
		return nil, Errorf("internal", "无法创建设备证书。")
	}
	return &DeviceState{
		ID: deviceID, Name: cleanDeviceName(name),
		PublicKey: append([]byte(nil), publicKey...), PrivateKey: append([]byte(nil), privateKey...),
		EncryptionPublicKey:  append([]byte(nil), encryptionPublic...),
		EncryptionPrivateKey: append([]byte(nil), encryptionPrivate...),
		Certificate:          string(certificate), CreatedAt: now,
	}, nil
}

func cleanDeviceName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" {
		name = "网页版"
	}
	runes := []rune(name)
	if len(runes) > 32 {
		name = string(runes[:32])
	}
	return name
}

func deviceInRecords(records []account.DeviceRecord, deviceID string) bool {
	for _, record := range records {
		if record.ID == deviceID && record.RevokedAt == nil {
			return true
		}
	}
	return false
}

func recoveryFilename(credential *hosted.RecoveryCredential, accountID string) string {
	if credential != nil {
		if name := credential.Filename(); name != "" {
			return name
		}
	}
	return "toget-account-recovery-" + accountID + ".togetkey"
}

func mapHostError(err error) error {
	if err == nil {
		return nil
	}
	var apiError *hosted.APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "invalid_credentials", "invalid_login", "account_not_found":
			return Errorf("invalid_credentials", "账号或密码不正确。")
		case "email_not_verified":
			return Errorf("email_not_verified", "请先验证邮箱。")
		case "rate_limited", "too_many_requests":
			return Errorf("rate_limited", "操作太频繁，请稍后再试。")
		case "session_expired", "unauthorized":
			return Errorf("session_expired", "登录已过期，请重新登录。")
		case "email_exists":
			return Errorf("email_exists", "这个邮箱已经注册过账号。")
		case "invalid_email":
			return Errorf("invalid_email", "邮箱格式不正确。")
		case "invalid_name":
			return Errorf("invalid_name", "名字格式不正确。")
		case "key_conflict":
			return Errorf("key_conflict", "账号密钥已更新，请重新登录。")
		case "vault_conflict":
			return Errorf("vault_conflict", "同步冲突，请稍后重试。")
		case "vault_too_large":
			return Errorf("vault_too_large", "本地房间数据过大，暂时无法同步。")
		}
		if apiError.Status >= 500 {
			return Errorf("host_unavailable", "服务器暂时不可用，请稍后重试。")
		}
		return Errorf(apiError.Code, "%s", apiError.Message)
	}
	switch {
	case errors.Is(err, hosted.ErrInvalidCredentials):
		return Errorf("invalid_credentials", "账号或密码不正确。")
	case errors.Is(err, hosted.ErrHostUnavailable):
		return Errorf("offline", "连不上服务器，请检查网络。")
	case errors.Is(err, hosted.ErrSessionInvalid):
		return Errorf("session_expired", "登录已过期，请重新登录。")
	case errors.Is(err, hosted.ErrInvalidHost):
		return Errorf("invalid_host", "服务器身份校验失败。")
	case errors.Is(err, hosted.ErrInvalidRecoveryCredential):
		return Errorf("insecure_origin", "当前页面无法安全地创建账号，请通过 HTTPS 打开。")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return Errorf("offline", "请求超时，请检查网络。")
	}
	return Errorf("network", "操作失败，请重试。")
}

func validAccountID(id string) bool {
	if len(id) != 14 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
