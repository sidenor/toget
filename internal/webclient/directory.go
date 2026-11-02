package webclient

import (
	"context"
	"errors"
	"strings"

	"github.com/sidenor/toget/internal/hosted"
)

// ResolveAccount looks up a 14-digit account ID. Local accounts are resolved
// directly by the host; other server codes go through the configured
// directory and the remote host's signed public lookup.
func (c *Client) ResolveAccount(ctx context.Context, accountID string) (*hosted.ResolvedAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	accountID = strings.TrimSpace(accountID)
	if !validAccountID(accountID) {
		return nil, Errorf("invalid_account", "账号 ID 应该是 14 位数字。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	resolved, err := client.ResolveAccount(ctx, c.signerLocked(), accountID)
	if err != nil {
		var apiError *hosted.APIError
		if errors.As(err, &apiError) {
			switch apiError.Code {
			case "account_not_found":
				return nil, Errorf("account_not_found", "没有找到这个账号。")
			case "directory_disabled":
				return nil, Errorf("directory_disabled", "当前服务器暂不支持查询其他服务器，请先用邀请码添加。")
			case "resolve_failed":
				return nil, Errorf("resolve_failed", "暂时联系不上对方的服务器，请稍后再试。")
			}
		}
		return nil, mapHostError(err)
	}
	if resolved.HostID == "" || resolved.AccountID != accountID {
		return nil, Errorf("invalid_host", "服务器返回的账号信息不一致。")
	}
	return resolved, nil
}
