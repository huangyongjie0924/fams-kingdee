package yunzhijia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// writeScopedToken 输出认证接口成功响应，token 由调用方指定（用于区分 app / team）。
// 与 client_test.go 的 writeToken 同构，但 token 可定制。
func writeScopedToken(w http.ResponseWriter, token string, expireIn int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"data":{"accessToken":%q,"refreshToken":"refresh-12345678901234567890123456789012","expireIn":%d},"errorCode":0,"success":true,"error":""}`, token, expireIn)
}

// captureTokenReq 从认证请求体解析 scope / eid（缺省返回空串）。
func captureTokenReq(t *testing.T, r *http.Request) (scope, eid string) {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Errorf("认证请求体不是合法 JSON：%v（原文 %s）", err, body)
		return "", ""
	}
	if raw, ok := m["scope"]; ok {
		_ = json.Unmarshal(raw, &scope)
	}
	if raw, ok := m["eid"]; ok {
		_ = json.Unmarshal(raw, &eid)
	}
	return scope, eid
}

// ---------------------------------------------------------------------------
// 第一部分：双 token（scope 分流 + 独立缓存）
// ---------------------------------------------------------------------------

// team token 分支：请求体必须 scope=="team" 且 eid 非空（缺 eid 实测返回 11000400）。
func TestGetTeamTokenSendsScopeTeamAndEID(t *testing.T) {
	var gotScope, gotEID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !isTokenPath(r) {
			t.Errorf("意外请求路径：%s", r.URL.Path)
			return
		}
		gotScope, gotEID = captureTokenReq(t, r)
		writeScopedToken(w, "tok-team", 7200)
	})
	c.SetEID("eid-26527676")

	tok, err := c.GetTeamToken(context.Background())
	if err != nil {
		t.Fatalf("GetTeamToken 失败：%v", err)
	}
	if tok != "tok-team" {
		t.Fatalf("team token 期望 tok-team，实际 %q", tok)
	}
	if gotScope != "team" {
		t.Fatalf("team token 请求体 scope 期望 team，实际 %q", gotScope)
	}
	if gotEID != "eid-26527676" {
		t.Fatalf("team token 请求体 eid 期望 eid-26527676（缺 eid 会返回 11000400），实际 %q", gotEID)
	}
}

// 回归守卫：app token 分支仍 scope=="app"，且**不携带** eid（即使已 SetEID）。
func TestGetAppTokenSendsScopeAppWithoutEID(t *testing.T) {
	var gotScope, gotEID string
	var hasEID bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !isTokenPath(r) {
			t.Errorf("意外请求路径：%s", r.URL.Path)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(body, &m)
		if raw, ok := m["scope"]; ok {
			_ = json.Unmarshal(raw, &gotScope)
		}
		if raw, ok := m["eid"]; ok {
			hasEID = true
			_ = json.Unmarshal(raw, &gotEID)
		}
		writeScopedToken(w, "tok-app", 7200)
	})
	c.SetEID("eid-should-not-appear")

	if _, err := c.GetAppToken(context.Background()); err != nil {
		t.Fatalf("GetAppToken 失败：%v", err)
	}
	if gotScope != "app" {
		t.Fatalf("app token 请求体 scope 期望 app，实际 %q", gotScope)
	}
	if hasEID {
		t.Fatalf("app token 请求体不应携带 eid，实际 %q", gotEID)
	}
}

// team 缓存与 app 缓存物理分离：取 team token 不得污染 c.token。
func TestGetTeamTokenDoesNotTouchAppToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !isTokenPath(r) {
			t.Errorf("意外请求路径：%s", r.URL.Path)
			return
		}
		scope, _ := captureTokenReq(t, r)
		writeScopedToken(w, "tok-"+scope, 7200)
	})
	c.SetEID("eid-1")

	if _, err := c.GetTeamToken(context.Background()); err != nil {
		t.Fatalf("GetTeamToken 失败：%v", err)
	}
	c.mu.Lock()
	appTok := c.token
	c.mu.Unlock()
	if appTok != "" {
		t.Fatalf("取 team token 不应污染 app token 缓存（c.token 应仍为空），实际 %q", appTok)
	}
}

// team 分支未配置 eid 时应在本地即失败，不发起认证请求（避免 11000400 的困惑）。
func TestGetTeamTokenWithoutEIDFails(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("eid 未配置时不应发起任何请求，实际 %s", r.URL.Path)
	})
	if _, err := c.GetTeamToken(context.Background()); err == nil {
		t.Fatalf("eid 未配置时 GetTeamToken 应返回错误")
	}
}

// ---------------------------------------------------------------------------
// 第二部分：flowcenter 路径 / team token / appId 回退
// ---------------------------------------------------------------------------

// 全部 7 个接口必须打到各自正确的路径。
func TestFlowCenterPaths(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeScopedToken(w, "tok-team", 7200)
			return
		}
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"errorCode":0,"error":null,"data":null}`))
	})
	c.SetEID("eid-1")
	ctx := context.Background()

	cases := []struct {
		name string
		want string
		call func() error
	}{
		{"init", pathFlowInit, func() error { _, e := c.InitFlowTemplate(ctx, &InitFlowRequest{}); return e }},
		{"todo/send", pathFlowTodoSend, func() error { _, e := c.SendFlowTodo(ctx, &SendFlowTodoRequest{}); return e }},
		{"todo/done", pathFlowTodoDone, func() error { _, e := c.DoneFlowTodo(ctx, &FlowBizRequest{}); return e }},
		{"todo/delete", pathFlowTodoDelete, func() error { _, e := c.DeleteFlowTodo(ctx, &FlowBizRequest{}); return e }},
		{"instance/finish", pathFlowInstanceFinish, func() error { _, e := c.FinishFlowInstance(ctx, &FlowInstanceRequest{}); return e }},
		{"instance/delete", pathFlowInstanceDelete, func() error { _, e := c.DeleteFlowInstance(ctx, &FlowInstanceRequest{}); return e }},
		{"instance/reset", pathFlowInstanceReset, func() error { _, e := c.ResetFlowInstance(ctx, &FlowInstanceRequest{}); return e }},
	}
	for _, tc := range cases {
		gotPath = ""
		if err := tc.call(); err != nil {
			t.Fatalf("%s 调用失败：%v", tc.name, err)
		}
		if gotPath != tc.want {
			t.Fatalf("%s 路径期望 %s，实际 %s", tc.name, tc.want, gotPath)
		}
	}
}

// 流程中心请求必须打到 todo/send，query 携带 **team** token（而非 app token），
// 且 AppID 为空时回退客户端 appID。
func TestSendFlowTodoUsesTeamTokenAndPath(t *testing.T) {
	var gotPath, gotTokenQuery string
	var captured map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			scope, _ := captureTokenReq(t, r)
			writeScopedToken(w, "tok-"+scope, 7200)
			return
		}
		gotPath = r.URL.Path
		gotTokenQuery = r.URL.Query().Get("accessToken")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"success":true,"errorCode":0,"error":null,"data":null}`))
	})
	c.SetEID("eid-1")

	res, err := c.SendFlowTodo(context.Background(), &SendFlowTodoRequest{
		WebLink:      "https://fams.example.com/#/repairs/1",
		WebFlowLink:  "https://fams.example.com/#/repairs/1",
		FlowID:       "repair-1",
		TypeID:       "WXSQD",
		Title:        "维修派工待接单",
		Urgency:      3,
		Creator:      "creator-openid",
		ActivityID:   "repair",
		ActivityName: "待接单",
		Todo:         []SendFlowTodoItem{{Approver: "emp-openid", BizID: "repair-1-127-repair"}},
	})
	if err != nil {
		t.Fatalf("SendFlowTodo 失败：%v", err)
	}
	if !res.OK() {
		t.Fatalf("OK() 应为 true，实际 success=%v errorCode=%d error=%q", res.Success.Bool(), res.ErrorCode, res.Error)
	}
	if gotPath != pathFlowTodoSend {
		t.Fatalf("路径期望 %s，实际 %s", pathFlowTodoSend, gotPath)
	}
	if gotTokenQuery != "tok-team" {
		t.Fatalf("流程中心请求 query 应携带 team token（tok-team），实际 %q（若为 tok-app 说明误用了 app token）", gotTokenQuery)
	}
	var appID string
	_ = json.Unmarshal(captured["appId"], &appID)
	if appID != "app-test-1" {
		t.Fatalf("AppID 为空时应回退客户端 appID=app-test-1，实际 %q", appID)
	}
}

// ---------------------------------------------------------------------------
// 第三部分：field 结构差异（init 用 title；send 用 name/value）——防坑守卫
// ---------------------------------------------------------------------------

func TestFlowFieldSerializationInitVsSend(t *testing.T) {
	var initBody, sendBody map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeScopedToken(w, "tok-team", 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case pathFlowInit:
			_ = json.Unmarshal(body, &initBody)
		case pathFlowTodoSend:
			_ = json.Unmarshal(body, &sendBody)
		}
		_, _ = w.Write([]byte(`{"success":true,"errorCode":0,"error":null,"data":null}`))
	})
	c.SetEID("eid-1")

	if _, err := c.InitFlowTemplate(context.Background(), &InitFlowRequest{
		TypeID:   "WXSQD",
		TypeName: "维修工单",
		AppName:  "固定资产台账",
		Creator:  "creator-openid",
		Field: []InitFlowField{
			{Title: "资产名称"},
			{Title: "报销金额", Type: "moneyWidget"},
		},
	}); err != nil {
		t.Fatalf("InitFlowTemplate 失败：%v", err)
	}
	if _, err := c.SendFlowTodo(context.Background(), &SendFlowTodoRequest{
		WebLink:      "w",
		WebFlowLink:  "wf",
		FlowID:       "repair-1",
		TypeID:       "WXSQD",
		Title:        "t",
		Urgency:      3,
		Creator:      "c",
		ActivityID:   "a",
		ActivityName: "n",
		Todo:         []SendFlowTodoItem{{Approver: "p", BizID: "b"}},
		Field:        []SendFlowField{{Name: "资产名称", Value: "ThinkPad"}},
	}); err != nil {
		t.Fatalf("SendFlowTodo 失败：%v", err)
	}

	// init：field[0] 必须是 {title,...}，且不含 name/value。
	var initFields []map[string]json.RawMessage
	if err := json.Unmarshal(initBody["field"], &initFields); err != nil {
		t.Fatalf("init field 解析失败：%v", err)
	}
	if len(initFields) != 2 {
		t.Fatalf("init field 期望 2 个，实际 %d", len(initFields))
	}
	if _, ok := initFields[0]["title"]; !ok {
		t.Fatalf("init field[0] 必须含 title，实际字段 %v", initFields[0])
	}
	for _, bad := range []string{"name", "value"} {
		if _, ok := initFields[0][bad]; ok {
			t.Fatalf("init field[0] 不应含 %q（init 用 title 注册结构，send 才用 name/value）", bad)
		}
	}

	// send：field[0] 必须是 {name,value}，且不含 title。
	var sendFields []map[string]json.RawMessage
	if err := json.Unmarshal(sendBody["field"], &sendFields); err != nil {
		t.Fatalf("send field 解析失败：%v", err)
	}
	if len(sendFields) != 1 {
		t.Fatalf("send field 期望 1 个，实际 %d", len(sendFields))
	}
	if _, ok := sendFields[0]["name"]; !ok {
		t.Fatalf("send field[0] 必须含 name，实际字段 %v", sendFields[0])
	}
	if _, ok := sendFields[0]["value"]; !ok {
		t.Fatalf("send field[0] 必须含 value，实际字段 %v", sendFields[0])
	}
	if _, ok := sendFields[0]["title"]; ok {
		t.Fatalf("send field[0] 不应含 title（那是 init 的注册结构）")
	}
}

// bizId / flowId 必须是**数组**（文档：类型 Array）。
func TestFlowBatchIDFieldsAreArrays(t *testing.T) {
	var doneBody, finishBody map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeScopedToken(w, "tok-team", 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case pathFlowTodoDone:
			_ = json.Unmarshal(body, &doneBody)
		case pathFlowInstanceFinish:
			_ = json.Unmarshal(body, &finishBody)
		}
		_, _ = w.Write([]byte(`{"success":true,"errorCode":0,"error":null,"data":null}`))
	})
	c.SetEID("eid-1")

	if _, err := c.DoneFlowTodo(context.Background(), &FlowBizRequest{BizID: []string{"b1", "b2"}}); err != nil {
		t.Fatalf("DoneFlowTodo 失败：%v", err)
	}
	if _, err := c.FinishFlowInstance(context.Background(), &FlowInstanceRequest{FlowID: []string{"f1"}}); err != nil {
		t.Fatalf("FinishFlowInstance 失败：%v", err)
	}

	var biz []string
	if err := json.Unmarshal(doneBody["bizId"], &biz); err != nil {
		t.Fatalf("bizId 应为数组：%v（原文 %s）", err, doneBody["bizId"])
	}
	if len(biz) != 2 || biz[0] != "b1" || biz[1] != "b2" {
		t.Fatalf("bizId 数组内容错误：%v", biz)
	}
	var flow []string
	if err := json.Unmarshal(finishBody["flowId"], &flow); err != nil {
		t.Fatalf("flowId 应为数组：%v（原文 %s）", err, finishBody["flowId"])
	}
	if len(flow) != 1 || flow[0] != "f1" {
		t.Fatalf("flowId 数组内容错误：%v", flow)
	}
}
