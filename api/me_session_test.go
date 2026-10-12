package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"asset-mgr/model"
)

// —— Q16 方案 D：只读会话的**客户端自省**（/api/me 与 deeplink-login 暴露 readonly）——
//
// 见 docs/云之家待办通知集成方案.md §12-Q16 方案 D。第 1 层 JS 桥已判定不可实现，网页端用户
// 点待办 100% 落到只读的第 2 层（rsc>0）。前端必须能自省「我是只读会话」，才能隐藏写按钮、
// 改显示「登录后处理」入口，而不是渲染一个点了就 403 的按钮。

// TestMeReportsReadonlyForScopedSession：收窄会话（rsc>0）⇒ /api/me 必须回 readonly=true。
//
// 变异验证：把 api/auth.go 的 handleMe 里 `scope > 0` 改成 `false` ⇒ 本用例变红。
func TestMeReportsReadonlyForScopedSession(t *testing.T) {
	srv := NewServer(nil, deeplinkTestConfig())
	h := newRealHandler(srv)

	user := &model.User{ID: 7, Username: "u", RealName: "只读", Role: model.RoleEmployee, EmployeeID: 127, Enabled: true}
	tok, err := srv.issueScopedToken(user, 123)
	if err != nil {
		t.Fatalf("签发收窄会话失败: %v", err)
	}

	code, body := doReq(t, h, http.MethodGet, "/api/me", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("/api/me：期望 200，实际 %d，body=%s", code, body)
	}
	var m struct {
		User        *model.User `json:"user"`
		Readonly    bool        `json:"readonly"`
		RepairScope int64       `json:"repair_scope"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, body)
	}
	if !m.Readonly {
		t.Errorf("收窄会话 readonly 应为 true，实际 false（body=%s）", body)
	}
	if m.RepairScope != 123 {
		t.Errorf("repair_scope 应为 123，实际 %d", m.RepairScope)
	}
	if m.User == nil || m.User.ID != 7 {
		t.Errorf("user 缺失或 id 不对：%+v", m.User)
	}
}

// TestMeReportsWritableForNormalSession：普通会话（无 rsc）⇒ readonly=false、repair_scope=0。
func TestMeReportsWritableForNormalSession(t *testing.T) {
	srv := NewServer(nil, deeplinkTestConfig())
	h := newRealHandler(srv)

	user := &model.User{ID: 9, Username: "admin", RealName: "管理员", Role: model.RoleAdmin, Enabled: true}
	tok, err := srv.issueToken(user)
	if err != nil {
		t.Fatalf("签发普通会话失败: %v", err)
	}

	code, body := doReq(t, h, http.MethodGet, "/api/me", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("/api/me：期望 200，实际 %d，body=%s", code, body)
	}
	var m struct {
		User        *model.User `json:"user"`
		Readonly    bool        `json:"readonly"`
		RepairScope int64       `json:"repair_scope"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, body)
	}
	if m.Readonly {
		t.Errorf("普通会话 readonly 应为 false，实际 true")
	}
	if m.RepairScope != 0 {
		t.Errorf("普通会话 repair_scope 应为 0，实际 %d", m.RepairScope)
	}
	if m.User == nil || m.User.ID != 9 {
		t.Errorf("user 缺失或 id 不对：%+v", m.User)
	}
}

// TestDeeplinkLoginResponseCarriesReadonly：deeplink-login 成功响应必须带 readonly=true
// （前端拿到 token 的同一刻就知道自己只读，省掉一次 /api/me 往返）。
//
// 数据策略：自建**探针**员工 + 探针账号（用户名带 DL-ME-PROBE 前缀 + 纳秒后缀），
// 不依赖库里现成数据——否则空库上会静默 Skip，而被跳过的用例等于没有用例。
// 清理按用户名与员工 id 双删，避免上次异常退出残留的 username 撞 uk_user_name。
func TestDeeplinkLoginResponseCarriesReadonly(t *testing.T) {
	st, cfg := mustTestStore(t)
	cfg.Yunzhijia.LoginTokenSecret = testDeeplinkSecret

	empNo := fmt.Sprintf("DL-ME-PROBE-%d", time.Now().UnixNano())
	res, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, ?, 1)`, empNo, "深链只读探针")
	if err != nil {
		t.Fatalf("建探针员工失败: %v", err)
	}
	empID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取探针员工 id 失败: %v", err)
	}
	t.Cleanup(func() {
		st.DB().Exec("DELETE FROM sys_user WHERE username=?", empNo)
		st.DB().Exec("DELETE FROM employee WHERE id=?", empID)
	})

	srv := NewServer(st, cfg)
	h := newRealHandler(srv)

	tok, err := srv.MintDeeplinkToken(empID, 123)
	if err != nil {
		t.Fatalf("签发免登 token 失败: %v", err)
	}
	code, body := doReq(t, h, http.MethodPost, "/api/sso/deeplink-login", "", map[string]string{"token": tok})
	if code != http.StatusOK {
		t.Fatalf("deeplink-login：期望 200，实际 %d，body=%s", code, body)
	}
	var m struct {
		Token    string      `json:"token"`
		User     *model.User `json:"user"`
		Readonly bool        `json:"readonly"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, body)
	}
	if m.Token == "" || m.User == nil {
		t.Fatalf("响应缺 token/user：%s", body)
	}
	if !m.Readonly {
		t.Errorf("deeplink-login 响应 readonly 应为 true，实际 false（body=%s）", body)
	}
}
