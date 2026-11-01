//go:build js && wasm

// Command toget-site-wasm runs the public site's account and room client in
// the browser. It performs OPAQUE login, device signing, and room encryption
// locally; the official host only ever sees sealed events and wrapped keys.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"syscall/js"
	"time"

	"github.com/sidenor/toget/internal/webclient"
)

const callTimeout = 5 * time.Minute

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	client, err := webclient.New(origin)
	if err != nil {
		api := js.Global().Get("Object").New()
		api.Set("fatal", errorValue(err))
		js.Global().Set("togetSite", api)
		select {}
	}
	api := js.Global().Get("Object").New()
	api.Set("restore", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return errorValue(errors.New("missing state"))
		}
		if err := client.Restore(args[0].String()); err != nil {
			return errorValue(err)
		}
		return okValue()
	}))
	api.Set("exportState", js.FuncOf(func(js.Value, []js.Value) any {
		encoded, err := client.Export()
		if err != nil {
			return errorValue(err)
		}
		return encoded
	}))
	api.Set("profile", js.FuncOf(func(js.Value, []js.Value) any {
		profile := client.Profile()
		if profile == nil {
			return js.Null()
		}
		return toJS(profile)
	}))
	api.Set("rooms", js.FuncOf(func(js.Value, []js.Value) any {
		return toJS(client.Rooms())
	}))
	api.Set("room", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return js.Null()
		}
		view, err := client.Room(args[0].String())
		if err != nil || view == nil {
			return js.Null()
		}
		return toJS(view)
	}))
	api.Set("consumeRecovery", js.FuncOf(func(js.Value, []js.Value) any {
		file := client.ConsumeRecovery()
		if file == nil {
			return js.Null()
		}
		return toJS(file)
	}))
	api.Set("logout", js.FuncOf(func(js.Value, []js.Value) any {
		if err := client.Logout(); err != nil {
			return errorValue(err)
		}
		return okValue()
	}))
	api.Set("call", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return rejectedPromise("invalid_request", "请求格式不正确。")
		}
		op := args[0].String()
		payload := ""
		if len(args) > 1 && args[1].Type() == js.TypeString {
			payload = args[1].String()
		}
		executor := js.FuncOf(func(_ js.Value, promiseArgs []js.Value) any {
			if len(promiseArgs) < 2 {
				return nil
			}
			resolve, reject := promiseArgs[0], promiseArgs[1]
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						reject.Invoke(errorObject("internal", "页面处理失败，请刷新后重试。"))
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
				defer cancel()
				result, err := dispatch(ctx, client, op, payload)
				if err != nil {
					reject.Invoke(errorValue(err))
					return
				}
				resolve.Invoke(toJS(result))
			}()
			return nil
		})
		return js.Global().Get("Promise").New(executor)
	}))
	js.Global().Set("togetSite", api)
	select {}
}

func dispatch(ctx context.Context, client *webclient.Client, op, payload string) (any, error) {
	switch op {
	case "register":
		var input webclient.RegisterInput
		if err := decode(payload, &input); err != nil {
			return nil, webclient.Errorf("invalid_request", "注册信息格式不正确。")
		}
		return client.Register(ctx, input)
	case "login":
		var input webclient.LoginInput
		if err := decode(payload, &input); err != nil {
			return nil, webclient.Errorf("invalid_request", "登录信息格式不正确。")
		}
		return client.Login(ctx, input)
	case "resume":
		return client.Resume(ctx)
	case "resendVerification":
		return struct{}{}, client.ResendVerification(ctx)
	case "listDevices":
		return client.ListDevices(ctx)
	case "revokeDevice":
		var input struct {
			DeviceID string `json:"device_id"`
		}
		if err := decode(payload, &input); err != nil || input.DeviceID == "" {
			return nil, webclient.Errorf("invalid_request", "设备信息格式不正确。")
		}
		return map[string]bool{"ok": true}, client.RevokeDevice(ctx, input.DeviceID)
	case "requestRecovery":
		var input struct {
			User string `json:"user"`
		}
		if err := decode(payload, &input); err != nil {
			return nil, webclient.Errorf("invalid_request", "账号信息格式不正确。")
		}
		return map[string]bool{"ok": true}, client.RequestRecovery(ctx, input.User)
	case "rotateRecovery":
		return client.RotateRecovery(ctx)
	case "refreshRooms":
		return client.RefreshRooms(ctx)
	case "syncRoom":
		roomID, err := roomIDPayload(payload)
		if err != nil {
			return nil, err
		}
		return client.SyncRoom(ctx, roomID)
	case "createRoom":
		var input webclient.CreateRoomInput
		if err := decode(payload, &input); err != nil {
			return nil, webclient.Errorf("invalid_request", "房间信息格式不正确。")
		}
		return client.CreateRoom(ctx, input)
	case "sendMessage":
		var input struct {
			RoomID string `json:"room_id"`
			Text   string `json:"text"`
		}
		if err := decode(payload, &input); err != nil || input.RoomID == "" {
			return nil, webclient.Errorf("invalid_request", "消息格式不正确。")
		}
		return client.SendMessage(ctx, input.RoomID, input.Text)
	case "createInvite":
		var input struct {
			RoomID   string `json:"room_id"`
			History  string `json:"history"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := decode(payload, &input); err != nil || input.RoomID == "" {
			return nil, webclient.Errorf("invalid_request", "邀请信息格式不正确。")
		}
		return client.CreateInvite(ctx, input.RoomID, webclient.InviteInput{
			History: input.History, TTLHours: input.TTLHours,
		})
	case "joinInvite":
		var input struct {
			Code string `json:"code"`
		}
		if err := decode(payload, &input); err != nil {
			return nil, webclient.Errorf("invalid_request", "邀请码格式不正确。")
		}
		return client.JoinInvite(ctx, input.Code)
	case "renameRoom":
		var input struct {
			RoomID string `json:"room_id"`
			Name   string `json:"name"`
			Topic  string `json:"topic"`
		}
		if err := decode(payload, &input); err != nil || input.RoomID == "" {
			return nil, webclient.Errorf("invalid_request", "房间信息格式不正确。")
		}
		return client.RenameRoom(ctx, input.RoomID, input.Name, input.Topic)
	case "leaveRoom":
		roomID, err := roomIDPayload(payload)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, client.LeaveRoom(ctx, roomID)
	case "markRead":
		var input struct {
			RoomID string `json:"room_id"`
			Seq    int64  `json:"seq"`
		}
		if err := decode(payload, &input); err != nil || input.RoomID == "" {
			return nil, webclient.Errorf("invalid_request", "阅读位置格式不正确。")
		}
		return map[string]bool{"ok": true}, client.MarkRead(ctx, input.RoomID, input.Seq)
	default:
		return nil, webclient.Errorf("unknown_operation", "不支持的操作。")
	}
}

func decode(payload string, target any) error {
	if payload == "" {
		return errors.New("empty payload")
	}
	return json.Unmarshal([]byte(payload), target)
}

func roomIDPayload(payload string) (string, error) {
	var input struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(payload, &input); err != nil || input.RoomID == "" {
		return "", webclient.Errorf("invalid_request", "房间信息格式不正确。")
	}
	return input.RoomID, nil
}

func toJS(value any) js.Value {
	if value == nil {
		return js.Null()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return js.Null()
	}
	return js.Global().Get("JSON").Call("parse", string(encoded))
}

func okValue() js.Value {
	object := js.Global().Get("Object").New()
	object.Set("ok", true)
	return object
}

func rejectedPromise(code, message string) js.Value {
	executor := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 {
			args[1].Invoke(errorObject(code, message))
		}
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}

func errorValue(err error) js.Value {
	clientErr := webclient.AsError(err)
	if clientErr == nil {
		return errorObject("internal", "操作失败，请重试。")
	}
	return errorObject(clientErr.Code, clientErr.Message)
}

func errorObject(code, message string) js.Value {
	object := js.Global().Get("Object").New()
	object.Set("code", code)
	object.Set("message", message)
	return object
}
