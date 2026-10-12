package api

import (
	"testing"
	"time"

	"asset-mgr/integration/yunzhijia"
)

// TestPickFlowCreator 验证 creator 优先级：报修人 openId > sender_id。
//
// 纯函数，无需 DB / 网络，安全阀 SKIP 不影响它。
// 反向验证：把 pickFlowCreator 第一行的 `reporterOpenID != ""` 改成恒假
// ⇒ 第 1/2 个用例（报修人有 openId）会红；改成恒真 ⇒ 第 3 个（兜底）会红。
func TestPickFlowCreator(t *testing.T) {
	cases := []struct {
		name           string
		reporterOpenID string
		senderID       string
		wantCreator    string
		wantSource     string
	}{
		{"报修人有 openId 优先于 sender_id", "open-reporter", "open-system", "open-reporter", "reporter"},
		{"报修人有 openId 且 sender_id 空", "open-reporter", "", "open-reporter", "reporter"},
		{"报修人无 openId 兜底 sender_id", "", "open-system", "open-system", "sender_id"},
		{"两者都空", "", "", "", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCreator, gotSource := pickFlowCreator(c.reporterOpenID, c.senderID)
			if gotCreator != c.wantCreator || gotSource != c.wantSource {
				t.Fatalf("pickFlowCreator(%q, %q) = (%q, %q), want (%q, %q)",
					c.reporterOpenID, c.senderID, gotCreator, gotSource, c.wantCreator, c.wantSource)
			}
		})
	}
}

// TestSendFlowTodoCarriesResolvedCreator 验证 sendFlowTodo 里的 creator
// 来自 resolveCreatorOpenID（不是硬编码的 s.senderID）。
//
// 用 httptest 桩捕获 /gateway/flowcenter/native/todo/send 的请求体做断言，
// **不依赖真实云之家，也不依赖 DB**（reporterEmpID=0 时跳过查库，直接走 sender_id 兜底）。
//
// 为什么测这条 wiring：代码里有条「容易改错且改错了会让所有待办静默失败」的接线
// ——Creator: creator（解析结果） vs Creator: s.senderID（旧值）。
// 本用例钉住「必须是前者」。
func TestSendFlowTodoCarriesResolvedCreator(t *testing.T) {
	f := newFakeYzj(t)

	yzj := yunzhijia.NewClient(f.srv.URL, "500000084", "test-secret", 5*time.Second)
	yzj.SetEID("26527676")

	svc := &NotifyService{
		yzj:               yzj,
		senderID:          "open-system",      // ← sender_id 兜底值
		todoAppID:         "500000084",
		flowCenterTypeID:  "WXSQD",
		portalBaseURL:     "https://fams.example.com",
		flowCenterEnabled: true,
		// reporterEmpID=0 时 resolveCreatorOpenID 不碰 s.st，故不需要真实 store。
	}

	job := &notifyJob{
		repairID:      2019,
		code:          "WX20260923TEST",
		sourceID:      "repair-2019",
		reporterEmpID: 0, // 报修人 emp=0 → 直接走 sender_id 兜底，不查库
	}
	op := notifyOp{
		kind:     opEnsure,
		empID:    127,
		openID:   "open-assignee",
		purpose:  "dispatch",
		title:    "测试标题",
		content:  "测试正文",
		withRead: false,
	}

	_, _ = svc.sendFlowTodo(job, op) // 即使出错，请求已发出，下面断言请求体。

	// 找到最后一条 /gateway/flowcenter/native/todo/send 请求。
	var sendReq *recordedReq
	for _, r := range f.snapshot() {
		if r.path == "/gateway/flowcenter/native/todo/send" {
			sendReq = &r
		}
	}
	if sendReq == nil {
		paths := []string{}
		for _, r := range f.snapshot() {
			paths = append(paths, r.path)
		}
		t.Fatalf("没有捕获到 /gateway/flowcenter/native/todo/send 请求，stub 收到的路径：%v", paths)
	}

	// 断言 1：creator 必须是 resolveCreatorOpenID 的返回值（sender_id 兜底），
	// 而不是 s.senderID 被硬编码进请求体。
	creator, ok := sendReq.body["creator"].(string)
	if !ok {
		t.Fatalf("请求体里没有 creator 字段，body=%+v", sendReq.body)
	}
	if creator != "open-system" {
		t.Fatalf("creator 期望为 sender_id 兜底值 \"open-system\"，实际 \"%s\"", creator)
	}

	// 断言 2：顺带验证 bizId 是按 job.sourceID + empID + purpose 拼的
	// （防止「顺手把 creator 改成 s.senderID」时连带改了别的接线）。
	todoArr, ok := sendReq.body["todo"].([]any)
	if !ok || len(todoArr) == 0 {
		t.Fatalf("请求体里没有 todo 数组，body=%+v", sendReq.body)
	}
	first := todoArr[0].(map[string]any)
	if first["bizId"] != "repair-2019-127-dispatch" {
		t.Fatalf("bizId 拼接错误，期望 \"repair-2019-127-dispatch\"，实际 \"%v\"", first["bizId"])
	}
}
