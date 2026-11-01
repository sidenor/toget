//go:build js && wasm

// Command toget-reset-wasm runs the Toget account reset page's cryptography
// inside the browser. It performs the same OPAQUE login and XChaCha20-Poly1305
// operations as the desktop client without ever sending the recovery file or
// new password anywhere except as the OPAQUE registration messages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"syscall/js"
	"time"

	"github.com/sidenor/toget/internal/hosted"
)

const callTimeout = 2 * time.Minute

// promiseCall runs one exported operation. Arguments are copied from the JS
// stack before the goroutine starts.
type promiseCall func(ctx context.Context, client *hosted.Client, args []string) (any, error)

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	api := js.Global().Get("Object").New()
	api.Set("verifyEmail", promiseFunc(origin, verifyEmail))
	api.Set("recoverContext", promiseFunc(origin, recoverContext))
	api.Set("inspect", promiseFunc(origin, inspectRecovery))
	api.Set("reset", promiseFunc(origin, resetPassword))
	js.Global().Set("togetReset", api)
	select {}
}

func promiseFunc(origin string, call promiseCall) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		copied := make([]string, len(args))
		for i := range args {
			if args[i].Type() == js.TypeString {
				copied[i] = args[i].String()
			}
		}
		executor := js.FuncOf(func(_ js.Value, promiseArgs []js.Value) any {
			if len(promiseArgs) < 2 {
				return nil
			}
			resolve, reject := promiseArgs[0], promiseArgs[1]
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						reject.Invoke(errorObject("internal", "页面处理失败，请刷新后重试"))
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
				defer cancel()
				client, err := hosted.NewBrowserClient(origin)
				if err != nil {
					reject.Invoke(errorObject("invalid_host", errorMessage(err)))
					return
				}
				result, err := call(ctx, client, copied)
				if err != nil {
					reject.Invoke(errorObject(errorCode(err), errorMessage(err)))
					return
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					reject.Invoke(errorObject("internal", "页面处理失败，请刷新后重试"))
					return
				}
				resolve.Invoke(js.Global().Get("JSON").Call("parse", string(encoded)))
			}()
			return nil
		})
		return js.Global().Get("Promise").New(executor)
	})
}

func verifyEmail(ctx context.Context, client *hosted.Client, args []string) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("invalid verification request")
	}
	response, err := client.VerifyEmail(ctx, args[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"email": response.Email, "verified": response.Verified}, nil
}

func recoverContext(ctx context.Context, client *hosted.Client, args []string) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("invalid recovery request")
	}
	response, err := client.RecoverContext(ctx, args[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"account_id": response.AccountID, "name": response.Name,
		"email_hint": response.EmailHint, "expires_at": response.ExpiresAt,
		"recovery_ready": response.RecoveryReady,
	}, nil
}

func inspectRecovery(ctx context.Context, client *hosted.Client, args []string) (any, error) {
	if len(args) != 2 {
		return nil, errors.New("invalid recovery file request")
	}
	inspection, err := client.InspectRecovery(ctx, args[0], []byte(args[1]))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"account_id": inspection.AccountID, "name": inspection.Name,
		"email_hint": inspection.EmailHint, "expires_at": inspection.ExpiresAt,
		"key_generation": inspection.KeyGeneration,
	}, nil
}

func resetPassword(ctx context.Context, client *hosted.Client, args []string) (any, error) {
	if len(args) != 3 {
		return nil, errors.New("invalid reset request")
	}
	result, err := client.ResetPassword(ctx, args[0], []byte(args[1]), args[2])
	if err != nil {
		return nil, err
	}
	// The caller must download this before leaving the page. The old recovery
	// file no longer works once the host has accepted the reset.
	return map[string]any{
		"account_id": result.AccountID,
		"credential": string(result.RecoveryCredentialRaw),
		"filename":   result.RecoveryCredential.Filename(),
		"reset_at":   result.ResetAt,
	}, nil
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, hosted.ErrRecoveryMismatch):
		return "recovery_mismatch"
	case errors.Is(err, hosted.ErrInvalidRecoveryCredential):
		return "invalid_recovery_file"
	case errors.Is(err, hosted.ErrHostUnavailable):
		return "host_unavailable"
	case errors.Is(err, hosted.ErrInvalidHost):
		return "invalid_host"
	case errors.Is(err, hosted.ErrInvalidCredentials):
		return "invalid_credentials"
	case errors.Is(err, hosted.ErrSessionInvalid):
		return "session_invalid"
	case errors.Is(err, hosted.ErrInvalidBlob):
		return "invalid_blob"
	}
	var apiError *hosted.APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "invalid_token", "expired_token":
			return "invalid_token"
		case "rate_limited":
			return "rate_limited"
		case "email_unavailable":
			return "email_unavailable"
		case "invalid_request", "unsupported_protocol":
			return "invalid_request"
		}
	}
	return "internal"
}

func errorMessage(err error) string {
	switch errorCode(err) {
	case "recovery_mismatch":
		return "这个恢复文件不属于该账号，或恢复文件已失效。请使用最新下载的恢复文件。"
	case "invalid_recovery_file":
		return "恢复文件格式不正确，请选择注册时保存的 .togetkey 文件。"
	case "host_unavailable":
		return "无法连接账号服务器，请检查网络后重试。"
	case "invalid_host":
		return "账号服务器的身份校验失败，为安全起见已停止。"
	case "invalid_token":
		return "重置链接无效或已过期，请回到 Toget 重新发送找回邮件。"
	case "rate_limited":
		return "操作太频繁，请稍后再试。"
	case "email_unavailable":
		return "这个账号无法使用邮箱找回。"
	case "invalid_blob":
		return "账号密钥数据损坏，无法继续重置。"
	}
	return fmt.Sprintf("操作失败：%v", err)
}

func errorObject(code, message string) js.Value {
	object := js.Global().Get("Object").New()
	object.Set("code", code)
	object.Set("message", message)
	return object
}
