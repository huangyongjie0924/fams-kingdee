package yunzhijia

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// 本文件是「sync 未序列化 → action{read:1} 必然失败」这个 P0 的**回归测试**。
//
// ⚠️ 关键：断言一律打在**序列化后的 JSON** 上，绝不打在结构体字段上。
// 该 bug 长期未被发现，正是因为既有测试（或缺失的测试）只看结构体、不看请求体——
// 未导出的 sync 被 encoding/json 静默跳过，结构体里 `sync == true` 与请求体里有没有
// sync 完全是两回事。

// rawSyncTrue 判断序列化结果里是否含真正的 `"sync":true`（键存在且值为布尔 true）。
// 用 RawMessage 判定，避免把 `"sync":"true"`（字符串）或 `"sync":false` 误判为通过。
func rawSyncTrue(t *testing.T, raw []byte) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v（原文 %s）", err, raw)
	}
	got, ok := m["sync"]
	if !ok {
		t.Fatalf("请求体缺少 sync 字段（正是本 bug：未导出字段被 encoding/json 跳过）。字段：%v，原文 %s", keysOf(m), raw)
	}
	if string(got) != "true" {
		t.Fatalf("sync 期望布尔 true，实际 %s（原文 %s）", got, raw)
	}
}

// 1) TodoRequest 序列化后必须含 "sync":true。
func TestTodoRequestMarshalsSyncTrue(t *testing.T) {
	req := TodoRequest{
		AppID:    "500000084",
		SourceID: "repair-1",
		URL:      "https://fams.example.com/#/repairs/1",
		Content:  "有新维修单待受理",
		Title:    "维修单待受理",
		HeadImg:  "https://yzj.example.com/x.png",
		OpenIDs:  []string{"5de50017e4b0b2767b870423"},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(TodoRequest) 失败：%v", err)
	}
	rawSyncTrue(t, raw)
}

// 2) TodoActionRequest 序列化后必须含 "sync":true。
func TestTodoActionRequestMarshalsSyncTrue(t *testing.T) {
	req := TodoActionRequest{
		SourceType:   "500000084",
		SourceItemID: "repair-1",
		ActionType:   TodoActionType{Read: intPtr(1)},
		OpenIDs:      []string{"5de50017e4b0b2767b870423"},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(TodoActionRequest) 失败：%v", err)
	}
	rawSyncTrue(t, raw)
}

// 3a) 既有形态不变：TodoActionRequest 仍是嵌套 {"actiontype":{"read":1}}，
// 且不得出现顶层平铺的 read/deal/delete（K1）。
func TestTodoActionRequestMarshalKeepsNestedActionType(t *testing.T) {
	raw, err := json.Marshal(TodoActionRequest{
		SourceType:   "500000084",
		SourceItemID: "repair-1",
		ActionType:   TodoActionType{Read: intPtr(1)},
	})
	if err != nil {
		t.Fatalf("Marshal(TodoActionRequest) 失败：%v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v（原文 %s）", err, raw)
	}
	atRaw, ok := m["actiontype"]
	if !ok {
		t.Fatalf("必须保留嵌套 actiontype（K1），实际字段 %v，原文 %s", keysOf(m), raw)
	}
	var at map[string]int
	if err := json.Unmarshal(atRaw, &at); err != nil {
		t.Fatalf("actiontype 应为对象：%v（原文 %s）", err, raw)
	}
	if at["read"] != 1 {
		t.Fatalf("actiontype.read 期望 1，实际 %v", at)
	}
	// 顶层不得出现平铺的 read/deal/delete。
	for _, k := range []string{"read", "deal", "delete"} {
		if _, exists := m[k]; exists {
			t.Fatalf("K1 回归：序列化后出现了顶层平铺字段 %q，必须使用嵌套 actiontype", k)
		}
	}
	// 其余字段名逐字不变。
	if string(m["sourcetype"]) != `"500000084"` || string(m["sourceitemid"]) != `"repair-1"` {
		t.Fatalf("sourcetype/sourceitemid 应逐字保留，实际 sourcetype=%s sourceitemid=%s", m["sourcetype"], m["sourceitemid"])
	}
}

// 3b) 既有形态不变：TodoRequest 仍按 OpenIDs 自动补齐 params（CreateTodo 内逻辑），
// 且真实发到 HTTP 线上的请求体含 "sync":true。这条把「序列化」与「CreateTodo 逻辑」一起覆盖。
func TestCreateTodoWireHasSyncAndAutoParams(t *testing.T) {
	var captured map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("请求体不是合法 JSON：%v（原文 %s）", err, body)
		}
		_, _ = w.Write([]byte(`{"data":{},"success":"true","errorCode":200,"error":"待办创建成功"}`))
	})
	// 故意不传 Params，交给 CreateTodo 按 OpenIDs 补齐。
	_, err := c.CreateTodo(context.Background(), &TodoRequest{
		SourceID: "repair-1",
		URL:      "https://fams.example.com/#/repairs/1",
		Content:  "有新维修单待受理",
		Title:    "维修单待受理",
		HeadImg:  "https://yzj.example.com/x.png",
		OpenIDs:  []string{"5de50017e4b0b2767b870423"},
	})
	if err != nil {
		t.Fatalf("CreateTodo 失败：%v", err)
	}
	rawSyncTrue(t, mustReMarshal(t, captured))
	// params 必须被自动补齐，且 openId 正确。
	paramsRaw, ok := captured["params"]
	if !ok {
		t.Fatalf("CreateTodo 应按 OpenIDs 自动补齐 params，实际字段 %v", keysOf(captured))
	}
	var params []TodoParam
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		t.Fatalf("params 应为数组：%v（原文 %s）", err, paramsRaw)
	}
	if len(params) != 1 || params[0].OpenID != "5de50017e4b0b2767b870423" {
		t.Fatalf("params 应按 OpenIDs 补齐，实际 %+v", params)
	}
}

// 5) 真实 HTTP 线路上：ActionTodo（变已读）请求体必须含 "sync":true。
// 这是最贴近生产失败场景的断言（read 在异步模式下必然失败）。
func TestActionTodoReadWireHasSyncTrue(t *testing.T) {
	var captured map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("请求体不是合法 JSON：%v（原文 %s）", err, body)
		}
		_, _ = w.Write([]byte(`{"data":{},"success":"true","errorCode":200,"error":"待办更新阅读状态成功"}`))
	})
	_, err := c.ActionTodo(context.Background(), &TodoActionRequest{
		SourceItemID: "repair-1",
		ActionType:   TodoActionType{Read: intPtr(1)},
		OpenIDs:      []string{"5de50017e4b0b2767b870423"},
	})
	if err != nil {
		t.Fatalf("ActionTodo 失败：%v", err)
	}
	rawSyncTrue(t, mustReMarshal(t, captured))
}

// mustReMarshal 把已解析的字段 map 重新序列化，供 rawSyncTrue 统一判定。
func mustReMarshal(t *testing.T, m map[string]json.RawMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("重新序列化捕获字段失败：%v", err)
	}
	return raw
}
