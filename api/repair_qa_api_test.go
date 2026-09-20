package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asset-mgr/config"
	"asset-mgr/model"
	"asset-mgr/store"
)

// QA 独立验证：HTTP 层端到端。连真库，自带清理（探针前缀 QA-REPAIR- / QA 账号）。
type qaH struct {
	t       *testing.T
	st      *store.Store
	cfg     *config.Config
	srv     *Server
	handler http.Handler
}

func qaSetup(t *testing.T) *qaH {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "config.yaml"))
	if err != nil {
		t.Skipf("读不到 config.yaml，跳过 API 集成测试: %v", err)
	}
	st, err := store.New(cfg.DSN())
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	srv := NewServer(st, cfg)
	mux := http.NewServeMux()
	srv.Routes(mux)
	h := &qaH{t: t, st: st, cfg: cfg, srv: srv, handler: srv.Authenticate(mux)}
	t.Cleanup(func() { st.Close() })
	return h
}

func (h *qaH) token(u model.User) string {
	h.t.Helper()
	tok, err := h.srv.issueToken(&u)
	if err != nil {
		h.t.Fatalf("签发 token 失败: %v", err)
	}
	return tok
}

func (h *qaH) do(method, path, token string, body any) (int, map[string]any, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.Bytes()
}

// —— 探针数据 ——

// repairableCategoryID 取一个「可维修」分类。需求 A 落地后，只有被管理员标记为
// 可维修的分类下的资产才能提交报修（handleCreateRepair 的 CategoryRepairable 校验）。
// 本文件的用例验证的是维修状态机 / 权限 / 可见范围，与分类门控正交，故探针卡统一
// 挂到可维修分类下，避免被需求 A 的门控误伤。
func (h *qaH) repairableCategoryID() int64 {
	h.t.Helper()
	var id int64
	if err := h.st.DB().QueryRow(
		"SELECT id FROM asset_category WHERE repairable = 1 ORDER BY id LIMIT 1").Scan(&id); err != nil {
		h.t.Fatalf("取可维修分类失败（需求 A 前置：库里应至少有一个 repairable=1 的分类）: %v", err)
	}
	return id
}

func (h *qaH) newCard(code string, useDeptID int64) int64 {
	h.t.Helper()
	catID := h.repairableCategoryID()
	res, err := h.st.DB().Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, use_dept_id, created_by)
		VALUES (?, ?, ?, '', ?, ?, 'qa')`, code, "QA 探针卡", model.StatusInUse, catID, useDeptID)
	if err != nil {
		h.t.Fatalf("建探针卡失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() {
		rows, _ := h.st.DB().Query("SELECT id FROM repair_order WHERE card_id = ?", id)
		if rows != nil {
			var ids []int64
			for rows.Next() {
				var oid int64
				_ = rows.Scan(&oid)
				ids = append(ids, oid)
			}
			rows.Close()
			for _, oid := range ids {
				h.st.DB().Exec("DELETE FROM doc_status_log WHERE doc_type=? AND doc_id=?", model.DocTypeRepair, oid)
				h.st.DB().Exec("DELETE FROM repair_attachment WHERE repair_id=?", oid)
			}
		}
		h.st.DB().Exec("DELETE FROM repair_order WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_history WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_card WHERE id=?", id)
	})
	return id
}

func (h *qaH) newEmployee(name string) int64 {
	h.t.Helper()
	res, err := h.st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, ?, 1)`,
		fmt.Sprintf("QA-%d", time.Now().UnixNano()), name)
	if err != nil {
		h.t.Fatalf("建探针员工失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM employee WHERE id=?", id) })
	return id
}

func (h *qaH) newUser(username, role string, empID, deptID int64) int64 {
	h.t.Helper()
	id, err := h.st.CreateUser(username, "qa-pass-123", "QA-"+username, role, empID, deptID, true)
	if err != nil {
		h.t.Fatalf("建探针账号失败: %v", err)
	}
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM sys_user WHERE id=?", id) })
	return id
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// TestQARepairAPIEndToEnd 覆盖：决策 1/2、状态机全路径、驳回/撤单/退回分支、非法跃迁。
func TestQARepairAPIEndToEnd(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()
	cardID := h.newCard(fmt.Sprintf("QA-REPAIR-E2E-%d", ns), 0)
	empID := h.newEmployee("QA维修工-端到端")

	viewer := h.token(model.User{ID: 1, Username: "qa-viewer", RealName: "QA报修人", Role: model.RoleViewer, EmployeeID: 9101})
	mgr := h.token(model.User{ID: 2, Username: "qa-mgr", RealName: "QA管理员", Role: model.RoleAssetManager, EmployeeID: 9102})
	tech := h.token(model.User{ID: 3, Username: "qa-tech", RealName: "QA维修工", Role: model.RoleRepairTech, EmployeeID: empID})

	// —— 决策 1：viewer 授予 repair.report，可提交报修 ——
	code, m, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "空调不制冷", "urgency": "high"})
	if code != 200 {
		t.Fatalf("❌ viewer 提交报修应成功，实际 %d：%s", code, str(m, "error"))
	}
	oid := int64(m["id"].(float64))
	if str(m, "status") != model.RepairPending {
		t.Fatalf("初始状态应为 pending，实际 %q", str(m, "status"))
	}
	if !strings.HasPrefix(str(m, "code"), "WX") {
		t.Errorf("单号应以 WX 开头，实际 %q", str(m, "code"))
	}
	if int64(m["reporter_emp_id"].(float64)) != 9101 {
		t.Errorf("报修人应取自登录态（9101），实际 %v", m["reporter_emp_id"])
	}
	t.Logf("viewer 提交成功：code=%s status=%s", str(m, "code"), str(m, "status"))

	// —— 决策 1：viewer 不能受理 / 派工 ——
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), viewer, nil); c != 403 {
		t.Errorf("❌ viewer 受理应 403，实际 %d", c)
	}
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), viewer, map[string]any{"assignee_emp_id": empID}); c != 403 {
		t.Errorf("❌ viewer 派工应 403，实际 %d", c)
	}

	// —— 非法跃迁（API 层）：待受理直接接单 / 直接派工 ——
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/take", oid), tech, nil); c == 200 {
		t.Errorf("❌ 待受理直接接单（跳过受理/派工）应被拒，实际 200")
	} else {
		t.Logf("待受理 + take → 正确拒绝 %d", c)
	}
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), mgr, map[string]any{"assignee_emp_id": empID}); c == 200 {
		t.Errorf("❌ 待受理直接派工（跳过受理）应被拒，实际 200")
	}

	// —— 决策 2：完整闭环 待受理→已受理→已派工→维修中→待确认→已完工 ——
	steps := []struct {
		path string
		body any
		tok  string
		want string
	}{
		{fmt.Sprintf("/api/repairs/%d/accept", oid), nil, mgr, model.RepairAccepted},
		{fmt.Sprintf("/api/repairs/%d/dispatch", oid), map[string]any{"assignee_emp_id": empID}, mgr, model.RepairDispatched},
		{fmt.Sprintf("/api/repairs/%d/take", oid), nil, tech, model.RepairRepairing},
		{fmt.Sprintf("/api/repairs/%d/finish", oid), map[string]any{"handler_desc": "更换压缩机"}, tech, model.RepairConfirming},
		{fmt.Sprintf("/api/repairs/%d/confirm", oid), nil, viewer, model.RepairDone},
	}
	for _, s := range steps {
		c, mm, _ := h.do("POST", s.path, s.tok, s.body)
		if c != 200 {
			t.Fatalf("❌ 流转 %s 失败：%d %s", s.path, c, str(mm, "error"))
		}
		if got := str(mm, "status"); got != s.want {
			t.Fatalf("❌ 流转后状态应为 %s，实际 %s", s.want, got)
		}
		t.Logf("闭环推进 → %s", s.want)
	}

	// —— 终态不可再流转 ——
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), mgr, nil); c == 200 {
		t.Errorf("❌ 已完工后再受理应被拒")
	}

	// —— 驳回必须填原因 ——
	_, m2, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "重复报修"})
	oid2 := int64(m2["id"].(float64))
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/reject", oid2), mgr, map[string]any{}); c != 400 {
		t.Errorf("❌ 驳回不填原因应 400，实际 %d", c)
	}
	if c, mm, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/reject", oid2), mgr, map[string]any{"remark": "重复报修"}); c != 200 || str(mm, "status") != model.RepairRejected {
		t.Errorf("❌ 驳回（带原因）应成功并到 rejected，实际 %d %s", c, str(mm, "status"))
	}

	// —— 员工撤单：待受理可自撤；受理后不可自撤 ——
	_, m3, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "误报"})
	oid3 := int64(m3["id"].(float64))
	if c, mm, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid3), viewer, nil); c != 200 || str(mm, "status") != model.RepairCancelled {
		t.Errorf("❌ 待受理由员工撤单应成功到 cancelled，实际 %d %s", c, str(mm, "status"))
	}
	_, m4, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "要撤但已受理"})
	oid4 := int64(m4["id"].(float64))
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid4), mgr, nil)
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid4), viewer, nil); c != 403 {
		t.Errorf("❌ 受理后员工自撤应 403，实际 %d", c)
	}
	if c, mm, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid4), mgr, nil); c != 200 || str(mm, "status") != model.RepairCancelled {
		t.Errorf("❌ 受理后管理员取消应成功，实际 %d %s", c, str(mm, "status"))
	}

	// —— 员工退回：待确认退回必须填原因，退回后回到维修中 ——
	_, m5, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "要退回的单"})
	oid5 := int64(m5["id"].(float64))
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid5), mgr, nil)
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid5), mgr, map[string]any{"assignee_emp_id": empID})
	h.do("POST", fmt.Sprintf("/api/repairs/%d/take", oid5), tech, nil)
	h.do("POST", fmt.Sprintf("/api/repairs/%d/finish", oid5), tech, map[string]any{"handler_desc": "初修"})
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/return", oid5), viewer, map[string]any{}); c != 400 {
		t.Errorf("❌ 退回不填原因应 400，实际 %d", c)
	}
	if c, mm, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/return", oid5), viewer, map[string]any{"remark": "还没修好"}); c != 200 || str(mm, "status") != model.RepairRepairing {
		t.Errorf("❌ 退回（带原因）应回到维修中，实际 %d %s", c, str(mm, "status"))
	}
	// 退回后 biz_status 应再次为维修中
	if c, mm, _ := h.do("GET", fmt.Sprintf("/api/assets/%d", cardID), viewer, nil); c == 200 {
		if str(mm, "biz_status") != model.StatusRepair {
			t.Errorf("❌ 退回后卡 biz_status 应为「维修中」，实际 %q", str(mm, "biz_status"))
		}
	}
}

// TestQARepairPermissionMatrix 用 5 个角色实测越权动作 403。
func TestQARepairPermissionMatrix(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()
	cardID := h.newCard(fmt.Sprintf("QA-REPAIR-PERM-%d", ns), 0)
	empID := h.newEmployee("QA维修工-权限")

	viewer := h.token(model.User{ID: 1, Username: "v", RealName: "V", Role: model.RoleViewer, EmployeeID: 9201})
	tech := h.token(model.User{ID: 2, Username: "t", RealName: "T", Role: model.RoleRepairTech, EmployeeID: empID})
	mgr := h.token(model.User{ID: 3, Username: "m", RealName: "M", Role: model.RoleAssetManager, EmployeeID: 9203})
	admin := h.token(model.User{ID: 4, Username: "a", RealName: "A", Role: model.RoleAdmin, EmployeeID: 9204})
	deptHead := h.token(model.User{ID: 5, Username: "d", RealName: "D", Role: model.RoleDeptHead, EmployeeID: 9205})

	_, m, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "权限矩阵用例"})
	oid := int64(m["id"].(float64))

	type tc struct {
		name, path, tok string
		body            any
		want            int
	}
	cases := []tc{
		{"viewer 受理", fmt.Sprintf("/api/repairs/%d/accept", oid), viewer, nil, 403},
		{"viewer 派工", fmt.Sprintf("/api/repairs/%d/dispatch", oid), viewer, map[string]any{"assignee_emp_id": empID}, 403},
		{"viewer 接单", fmt.Sprintf("/api/repairs/%d/take", oid), viewer, nil, 403},
		{"viewer 报完工", fmt.Sprintf("/api/repairs/%d/finish", oid), viewer, map[string]any{"handler_desc": "x"}, 403},
		{"repair_tech 受理", fmt.Sprintf("/api/repairs/%d/accept", oid), tech, nil, 403},
		{"repair_tech 派工", fmt.Sprintf("/api/repairs/%d/dispatch", oid), tech, map[string]any{"assignee_emp_id": empID}, 403},
		{"dept_head 受理", fmt.Sprintf("/api/repairs/%d/accept", oid), deptHead, nil, 403},
		{"dept_head 派工", fmt.Sprintf("/api/repairs/%d/dispatch", oid), deptHead, map[string]any{"assignee_emp_id": empID}, 403},
	}
	for _, c := range cases {
		if got, _, _ := h.do("POST", c.path, c.tok, c.body); got != c.want {
			t.Errorf("❌ %s 应 %d，实际 %d", c.name, c.want, got)
		} else {
			t.Logf("✓ %s → %d", c.name, got)
		}
	}
	// 正向：asset_manager / admin 可受理
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), mgr, nil); c != 200 {
		t.Errorf("❌ asset_manager 受理应 200，实际 %d", c)
	}
	_, m2, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardID, "fault_desc": "admin 受理用例"})
	oid2 := int64(m2["id"].(float64))
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid2), admin, nil); c != 200 {
		t.Errorf("❌ admin 受理应 200，实际 %d", c)
	}
}

// TestQARepairVisibilityScope 实测可见范围：viewer 只看自己提交、repair_tech 只看派给自己、
// dept_head 看本部门子树、asset_manager/admin 看全部。
func TestQARepairVisibilityScope(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()

	depts, err := h.st.ListDepartments()
	if err != nil || len(depts) == 0 {
		t.Skipf("库里没有部门，跳过 dept_head 范围验证: %v", err)
	}
	probeDept := depts[0].ID

	cardA := h.newCard(fmt.Sprintf("QA-REPAIR-SCOPE-A-%d", ns), probeDept) // 属 probeDept
	cardB := h.newCard(fmt.Sprintf("QA-REPAIR-SCOPE-B-%d", ns), 0)        // 不属 probeDept
	empID := h.newEmployee("QA维修工-范围")

	viewerA := h.token(model.User{ID: 1, Username: "va", RealName: "VA", Role: model.RoleViewer, EmployeeID: 9301})
	viewerB := h.token(model.User{ID: 2, Username: "vb", RealName: "VB", Role: model.RoleViewer, EmployeeID: 9302})
	tech := h.token(model.User{ID: 3, Username: "t", RealName: "T", Role: model.RoleRepairTech, EmployeeID: empID})
	mgr := h.token(model.User{ID: 4, Username: "m", RealName: "M", Role: model.RoleAssetManager, EmployeeID: 9304})
	admin := h.token(model.User{ID: 5, Username: "a", RealName: "A", Role: model.RoleAdmin, EmployeeID: 9305})

	// A 由 viewerA 提交（属 probeDept）；B 由 viewerB 提交（无部门）
	_, mA, _ := h.do("POST", "/api/repairs", viewerA, map[string]any{"card_id": cardA, "fault_desc": "A 单"})
	_, mB, _ := h.do("POST", "/api/repairs", viewerB, map[string]any{"card_id": cardB, "fault_desc": "B 单"})
	idA := int64(mA["id"].(float64))
	idB := int64(mB["id"].(float64))

	// 把 B 派给 tech（A 保持未派工）
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", idB), mgr, nil)
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", idB), mgr, map[string]any{"assignee_emp_id": empID})

	contains := func(body []byte, id int64) bool {
		var res struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("解析列表失败: %v body=%s", err, string(body))
		}
		for _, it := range res.Items {
			if int64(it["id"].(float64)) == id {
				return true
			}
		}
		return false
	}

	// viewerA 只见 A
	_, _, bA := h.do("GET", "/api/repairs", viewerA, nil)
	if !contains(bA, idA) || contains(bA, idB) {
		t.Errorf("❌ viewerA 应只见自己的 A 单（A=%v B=%v）", contains(bA, idA), contains(bA, idB))
	}
	// viewerB 只见 B
	_, _, bB := h.do("GET", "/api/repairs", viewerB, nil)
	if !contains(bB, idB) || contains(bB, idA) {
		t.Errorf("❌ viewerB 应只见自己的 B 单")
	}
	// repair_tech 只见派给自己的 B
	_, _, bT := h.do("GET", "/api/repairs", tech, nil)
	if !contains(bT, idB) || contains(bT, idA) {
		t.Errorf("❌ repair_tech 应只见派给自己的 B 单")
	}
	// asset_manager 见 A+B
	_, _, bM := h.do("GET", "/api/repairs", mgr, nil)
	if !contains(bM, idA) || !contains(bM, idB) {
		t.Errorf("❌ asset_manager 应见全部")
	}
	// admin 见 A+B
	_, _, bAd := h.do("GET", "/api/repairs", admin, nil)
	if !contains(bAd, idA) || !contains(bAd, idB) {
		t.Errorf("❌ admin 应见全部")
	}
	// dept_head（probeDept）：只见 A（B 无部门）
	dhID := h.newUser(fmt.Sprintf("qa-dh-%d", ns), model.RoleDeptHead, 9306, probeDept)
	deptHead := h.token(model.User{ID: dhID, Username: "dh", RealName: "DH", Role: model.RoleDeptHead, EmployeeID: 9306})
	_, _, bD := h.do("GET", "/api/repairs", deptHead, nil)
	if !contains(bD, idA) || contains(bD, idB) {
		t.Errorf("❌ dept_head 应只见本部门子树内的 A 单（A=%v B=%v）", contains(bD, idA), contains(bD, idB))
	}

	// 越权点查：viewerA 取 B 单详情 → 403
	if c, _, _ := h.do("GET", fmt.Sprintf("/api/repairs/%d", idB), viewerA, nil); c != 403 {
		t.Errorf("❌ viewerA 取他人单据详情应 403，实际 %d", c)
	}
	// mine=1：viewerA 只见自己
	_, _, bMine := h.do("GET", "/api/repairs?mine=1", viewerA, nil)
	if !contains(bMine, idA) || contains(bMine, idB) {
		t.Errorf("❌ mine=1 应只见自己提交的单")
	}
}

// TestQARepairUploadIDOR 实测维修附件上传的对象级鉴权（IDOR 防护）与旧路由不放宽。
func TestQARepairUploadIDOR(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()
	cardA := h.newCard(fmt.Sprintf("QA-REPAIR-UP-A-%d", ns), 0)
	cardB := h.newCard(fmt.Sprintf("QA-REPAIR-UP-B-%d", ns), 0)

	viewerA := h.token(model.User{ID: 1, Username: "va", RealName: "QA-A", Role: model.RoleViewer, EmployeeID: 9401})
	viewerB := h.token(model.User{ID: 2, Username: "vb", RealName: "QA-B", Role: model.RoleViewer, EmployeeID: 9402})
	mgr := h.token(model.User{ID: 3, Username: "m", RealName: "QA-M", Role: model.RoleAssetManager, EmployeeID: 9403})

	_, mA, _ := h.do("POST", "/api/repairs", viewerA, map[string]any{"card_id": cardA, "fault_desc": "A 的单"})
	_, mB, _ := h.do("POST", "/api/repairs", viewerB, map[string]any{"card_id": cardB, "fault_desc": "B 的单"})
	idA := int64(mA["id"].(float64))
	idB := int64(mB["id"].(float64))

	upload := func(tok string, fields map[string]string) (int, map[string]any) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range fields {
			_ = w.WriteField(k, v)
		}
		fw, _ := w.CreateFormFile("file", "qa.png")
		_, _ = fw.Write([]byte("QA fake png bytes"))
		_ = w.Close()
		req := httptest.NewRequest("POST", "/api/repair-upload", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		// 清理落盘文件
		if u := str(m, "url"); strings.HasPrefix(u, "/uploads/") {
			fp := filepath.Join(h.cfg.Server.UploadDir, filepath.Base(u))
			h.t.Cleanup(func() { os.Remove(fp) })
		}
		return rec.Code, m
	}

	// 员工 A 给自己的单上传 → 成功
	if c, m := upload(viewerA, map[string]string{"repair_id": fmt.Sprint(idA)}); c != 200 {
		t.Errorf("❌ 报修人给自己的单上传应成功，实际 %d %s", c, str(m, "error"))
	} else {
		t.Logf("✓ 自己的单上传成功 id=%v", m["id"])
	}
	// 员工 A 给员工 B 的单上传 → 403
	if c, _ := upload(viewerA, map[string]string{"repair_id": fmt.Sprint(idB)}); c != 403 {
		t.Errorf("❌ 给他人单据上传应 403，实际 %d", c)
	} else {
		t.Logf("✓ 他人单据上传被拒 403")
	}
	// 未登录上传 → 401
	if c, _ := upload("", map[string]string{"repair_id": fmt.Sprint(idA)}); c != 401 {
		t.Errorf("❌ 未登录上传应 401，实际 %d", c)
	} else {
		t.Logf("✓ 未登录上传被拒 401")
	}
	// 旧路由 /api/upload 未放宽：viewer 仍被挡（需 asset.manage）
	oldUpload := func(tok string) int {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("card_id", fmt.Sprint(cardA))
		fw, _ := w.CreateFormFile("file", "qa.png")
		_, _ = fw.Write([]byte("x"))
		_ = w.Close()
		req := httptest.NewRequest("POST", "/api/upload", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := oldUpload(viewerA); c != 403 {
		t.Errorf("❌ 旧 /api/upload 对 viewer 应仍 403（未放宽），实际 %d", c)
	} else {
		t.Logf("✓ 旧 /api/upload 对 viewer 仍 403（未放宽）")
	}
	if c := oldUpload(mgr); c != 200 {
		t.Errorf("asset_manager 使用旧 /api/upload 应 200，实际 %d", c)
	}
	// 清理旧路由可能写入的 asset_attachment
	h.t.Cleanup(func() {
		h.st.DB().Exec("DELETE FROM asset_attachment WHERE card_id IN (?,?) AND uploaded_by IN ('QA-M','QA-A')", cardA, cardB)
	})
}

// TestQASyncPermissionNoDrift 决策 4：asset_manager 前后端都没有 sync.manage，
// 后端同步接口对它仍是 403（前端按钮已隐藏，用户不会再撞上这个 403）。
func TestQASyncPermissionNoDrift(t *testing.T) {
	h := qaSetup(t)
	mgr := h.token(model.User{ID: 1, Username: "m", RealName: "M", Role: model.RoleAssetManager, EmployeeID: 9601})
	for _, p := range []string{"/api/sync/run", "/api/sync/test-connect", "/api/sync/org"} {
		if c, _, _ := h.do("POST", p, mgr, nil); c != 403 {
			t.Errorf("❌ asset_manager 调 %s 应 403（无 sync.manage），实际 %d", p, c)
		} else {
			t.Logf("✓ asset_manager %s → 403", p)
		}
	}
	// 前端按钮：SideMenu 的「金蝶同步」以 auth.can('sync.manage') 控制，asset_manager 无此权限 → 不显示。
}

// TestQADisplayStatusViaAPI 决策 3 相关：API 返回的 display_status 正确（列表 + 详情）。
func TestQADisplayStatusViaAPI(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()
	code := fmt.Sprintf("QA-REPAIR-DISP-%d", ns)
	cardID := h.newCard(code, 0)

	// 手工把 biz_status 置为维修中（模拟单据驱动）
	if _, err := h.st.DB().Exec("UPDATE asset_card SET biz_status=? WHERE id=?", model.StatusRepair, cardID); err != nil {
		t.Fatalf("置 biz_status 失败: %v", err)
	}
	admin := h.token(model.User{ID: 1, Username: "a", RealName: "A", Role: model.RoleAdmin, EmployeeID: 9501})

	// 详情
	c, m, _ := h.do("GET", fmt.Sprintf("/api/assets/%d", cardID), admin, nil)
	if c != 200 || str(m, "display_status") != model.StatusRepair {
		t.Errorf("❌ 详情 display_status 应为「维修中」，实际 %q", str(m, "display_status"))
	}
	// 列表
	_, _, body := h.do("GET", "/api/assets?keyword="+code, admin, nil)
	var res struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(body, &res)
	found := false
	for _, it := range res.Items {
		if int64(it["id"].(float64)) == cardID {
			found = true
			if it["display_status"] != model.StatusRepair {
				t.Errorf("❌ 列表 display_status 应为「维修中」，实际 %v", it["display_status"])
			}
		}
	}
	if !found {
		t.Errorf("❌ 列表应能按 keyword 查到探针卡")
	}
	// 按「维修中」筛选
	_, _, body2 := h.do("GET", "/api/assets?status="+model.StatusRepair, admin, nil)
	var res2 struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(body2, &res2)
	hit := false
	for _, it := range res2.Items {
		if int64(it["id"].(float64)) == cardID {
			hit = true
		}
	}
	if !hit {
		t.Errorf("❌ 按「维修中」筛选应能筛到探针卡")
	}
	// 导出接口可用（状态列走 DisplayStatus，见 api/importexport.go）
	if c, _, b := h.do("GET", "/api/assets/export", admin, nil); c != 200 || len(b) < 100 {
		t.Errorf("❌ 导出接口异常：code=%d len=%d", c, len(b))
	} else {
		t.Logf("✓ 导出接口 200，字节数=%d", len(b))
	}
}

// newCardWithCategory 建一张挂指定分类的探针卡（需求 A 分类门控用例专用）。
func (h *qaH) newCardWithCategory(code string, categoryID int64) int64 {
	h.t.Helper()
	res, err := h.st.DB().Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, created_by)
		VALUES (?, ?, ?, '', ?, 'qa')`, code, "QA 分类门控探针", model.StatusInUse, categoryID)
	if err != nil {
		h.t.Fatalf("建分类探针卡失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() {
		rows, _ := h.st.DB().Query("SELECT id FROM repair_order WHERE card_id=?", id)
		if rows != nil {
			var ids []int64
			for rows.Next() {
				var oid int64
				_ = rows.Scan(&oid)
				ids = append(ids, oid)
			}
			rows.Close()
			for _, oid := range ids {
				h.st.DB().Exec("DELETE FROM doc_status_log WHERE doc_type=? AND doc_id=?", model.DocTypeRepair, oid)
				h.st.DB().Exec("DELETE FROM repair_attachment WHERE repair_id=?", oid)
			}
		}
		h.st.DB().Exec("DELETE FROM repair_order WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_history WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_card WHERE id=?", id)
	})
	return id
}

// TestQARepairCategoryGate 需求 A 独立验证：后端分类门控是底线（前端可绕过），
// 只有「可维修」分类的资产能提交报修；不可维修 / category_id=0 哨兵值一律 400，
// 且 category_id=0 走 COALESCE 兜底、绝不 500。
//
// 前置分类缺失时**必须报错**（迁移被回滚 / backfill 没跑，正是最该报警的场景），
// 不能 Skip 成绿。
func TestQARepairCategoryGate(t *testing.T) {
	h := qaSetup(t)

	var repID, nonRepID int64
	if err := h.st.DB().QueryRow("SELECT id FROM asset_category WHERE repairable=1 ORDER BY id LIMIT 1").Scan(&repID); err != nil {
		t.Fatalf("库里无可维修分类（需求 A 前置未就绪：迁移/backfill 可能被回滚）: %v", err)
	}
	var nonRepName string
	if err := h.st.DB().QueryRow("SELECT id, name FROM asset_category WHERE repairable=0 ORDER BY id LIMIT 1").Scan(&nonRepID, &nonRepName); err != nil {
		t.Fatalf("库里无不可维修分类（需求 A 前置未就绪）: %v", err)
	}

	viewer := h.token(model.User{ID: 1, Username: "v", RealName: "QA报修人", Role: model.RoleViewer, EmployeeID: 9701})
	mgr := h.token(model.User{ID: 2, Username: "m", RealName: "QA管理员", Role: model.RoleAssetManager, EmployeeID: 9702})
	ns := time.Now().UnixNano()

	// ① 可维修分类 → 200
	cardRep := h.newCardWithCategory(fmt.Sprintf("QA-REPAIR-CAT-REP-%d", ns), repID)
	if c, m, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardRep, "fault_desc": "可维修分类应放行"}); c != 200 {
		t.Errorf("❌ 可维修分类报修应 200，实际 %d %s", c, str(m, "error"))
	} else {
		t.Logf("✓ 可维修分类 → 200（code=%s）", str(m, "code"))
	}

	// ② 不可维修分类 → 400，且文案须含**该卡真实分类名**（证明非硬编码文案）
	cardNon := h.newCardWithCategory(fmt.Sprintf("QA-REPAIR-CAT-NON-%d", ns), nonRepID)
	c, m, _ := h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardNon, "fault_desc": "不可维修分类应被拦"})
	if c != 400 {
		t.Errorf("❌ 不可维修分类报修应 400，实际 %d", c)
	} else {
		msg := str(m, "error")
		if !strings.Contains(msg, nonRepName) {
			t.Errorf("❌ 门控文案应含该卡真实分类名 %q，实际 %q", nonRepName, msg)
		}
		t.Logf("✓ 不可维修分类 → 400：%s", msg)
	}

	// ③ category_id=0 哨兵值 → 400（COALESCE 兜底，绝不 500）；
	//    文案须含「未设置资产类别」——**不能**用「不支持报修」判，两条文案都含它，会互相掩盖。
	cardZero := h.newCardWithCategory(fmt.Sprintf("QA-REPAIR-CAT-ZERO-%d", ns), 0)
	c, m, _ = h.do("POST", "/api/repairs", viewer, map[string]any{"card_id": cardZero, "fault_desc": "哨兵值分类应被拦且不报错"})
	if c != 400 {
		t.Errorf("❌ category_id=0 报修应 400（COALESCE 兜底），实际 %d", c)
	} else {
		msg := str(m, "error")
		if !strings.Contains(msg, "未设置资产类别") {
			t.Errorf("❌ 空分类文案应含「未设置资产类别」，实际 %q", msg)
		}
		t.Logf("✓ category_id=0 → 400：%s", msg)
	}

	// ④ 角色无关：门控在权限校验之后（api/repair.go），凡持 repair.report 的角色
	//    （viewer 与 asset_manager）对**同一张**不可维修卡都应 400 —— 防将来加角色旁路。
	//    只测 viewer 发现不了「某角色被旁路」的回归。
	for _, rc := range []struct {
		name  string
		token string
	}{{"viewer", viewer}, {"asset_manager", mgr}} {
		if c, m, _ := h.do("POST", "/api/repairs", rc.token, map[string]any{"card_id": cardNon, "fault_desc": "角色无关门控"}); c != 400 {
			t.Errorf("❌ %s 对不可维修卡报修应 400（门控与角色无关），实际 %d %s", rc.name, c, str(m, "error"))
		} else {
			t.Logf("✓ %s 对不可维修卡 → 400", rc.name)
		}
	}
}

// TestQADashboardSixRoles 需求 B 首页：6 角色下 GET /api/dashboard 的渲染与越权检查。
// 断言：① 6 角色均 200 不崩；② 待办项按角色收敛（员工/维修工拿不到管理向待办）；
// ③ sync_failed 仅 admin（有 sync.manage）可见；④ 可见条数随 scope 收窄（viewer/dept_head ≤ admin）。
func TestQADashboardSixRoles(t *testing.T) {
	h := qaSetup(t)
	ns := time.Now().UnixNano()

	depts, _ := h.st.ListDepartments()
	if len(depts) == 0 {
		t.Skip("库里没有部门，跳过 dept_head 首页验证")
	}
	probeDept := depts[0].ID

	// 造一点可见数据：一张归 viewer 名下的卡（user_emp_id=viewer 员工号）+ 一张本部门的卡
	repCat := h.repairableCategoryID()
	mkCard := func(code string, userEmp, deptID int64) {
		if _, err := h.st.DB().Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, user_emp_id, use_dept_id, created_by)
			VALUES (?, ?, ?, '', ?, ?, ?, 'qa')`, code, "QA首页探针", model.StatusInUse, repCat, userEmp, deptID); err != nil {
			t.Fatalf("建首页探针卡失败: %v", err)
		}
		h.t.Cleanup(func() {
			h.st.DB().Exec("DELETE FROM repair_order WHERE asset_code=?", code)
			h.st.DB().Exec("DELETE FROM asset_card WHERE asset_code=?", code)
		})
	}
	const viewerEmp = 9801
	mkCard(fmt.Sprintf("QA-REPAIR-DASH-V-%d", ns), viewerEmp, 0)
	mkCard(fmt.Sprintf("QA-REPAIR-DASH-D-%d", ns), 0, probeDept)

	admin := h.token(model.User{ID: 1, Username: "a", RealName: "A", Role: model.RoleAdmin, EmployeeID: 9901})
	mgr := h.token(model.User{ID: 2, Username: "m", RealName: "M", Role: model.RoleAssetManager, EmployeeID: 9902})
	counter := h.token(model.User{ID: 3, Username: "c", RealName: "C", Role: model.RoleCounter, EmployeeID: 9903})
	tech := h.token(model.User{ID: 4, Username: "t", RealName: "T", Role: model.RoleRepairTech, EmployeeID: 9904})
	viewer := h.token(model.User{ID: 5, Username: "v", RealName: "V", Role: model.RoleViewer, EmployeeID: viewerEmp})
	dhID := h.newUser(fmt.Sprintf("qa-dash-dh-%d", ns), model.RoleDeptHead, 9906, probeDept)
	deptHead := h.token(model.User{ID: dhID, Username: "dh", RealName: "DH", Role: model.RoleDeptHead, EmployeeID: 9906})

	type roleCase struct {
		name       string
		tok        string
		allowTodo  map[string]bool // 该角色允许出现的待办 key
		wantSync   bool            // 是否应看到 sync_failed
	}
	adminTodos := map[string]bool{"pending": true, "unassigned": true, "confirming": true, "sync_failed": true}
	cases := []roleCase{
		{"admin", admin, adminTodos, true},
		{"asset_manager", mgr, map[string]bool{"pending": true, "unassigned": true, "confirming": true}, false},
		{"counter", counter, map[string]bool{"count_pending": true, "confirming": true}, false},
		{"repair_tech", tech, map[string]bool{"to_take": true, "repairing": true}, false},
		{"viewer", viewer, map[string]bool{"confirming": true, "in_progress": true}, false},
		{"dept_head", deptHead, map[string]bool{"pending": true}, false},
	}

	type dashResp struct {
		Todo []struct {
			Key   string `json:"key"`
			Count int64  `json:"count"`
		} `json:"todo"`
		Overview struct {
			AssetTotal int64 `json:"asset_total"`
		} `json:"overview"`
	}
	total := map[string]int64{}
	for _, rc := range cases {
		code, _, body := h.do("GET", "/api/dashboard", rc.tok, nil)
		if code != 200 {
			t.Errorf("❌ %s 访问 /api/dashboard 应 200，实际 %d", rc.name, code)
			continue
		}
		var d dashResp
		if err := json.Unmarshal(body, &d); err != nil {
			t.Errorf("❌ %s 解析 dashboard 失败: %v", rc.name, err)
			continue
		}
		total[rc.name] = d.Overview.AssetTotal
		sawSync := false
		for _, td := range d.Todo {
			if !rc.allowTodo[td.Key] {
				t.Errorf("❌ %s 越权拿到不该有的待办项 %q（count=%d）", rc.name, td.Key, td.Count)
			}
			if td.Key == "sync_failed" {
				sawSync = true
			}
		}
		if sawSync != rc.wantSync {
			t.Errorf("❌ %s 的 sync_failed 可见性错误：want=%v got=%v", rc.name, rc.wantSync, sawSync)
		}
		t.Logf("✓ %s：asset_total=%d todo=%d 项 sync_failed=%v", rc.name, d.Overview.AssetTotal, len(d.Todo), sawSync)
	}

	// 可见条数随 scope 收窄
	if total["admin"] > 0 {
		if total["viewer"] > total["admin"] {
			t.Errorf("❌ viewer 资产数(%d)不应超过 admin(%d)", total["viewer"], total["admin"])
		}
		if total["dept_head"] > total["admin"] {
			t.Errorf("❌ dept_head 资产数(%d)不应超过 admin(%d)", total["dept_head"], total["admin"])
		}
		t.Logf("可见条数：admin=%d asset_manager=%d dept_head=%d viewer=%d repair_tech=%d counter=%d",
			total["admin"], total["asset_manager"], total["dept_head"], total["viewer"], total["repair_tech"], total["counter"])
	}
	// viewer 必须能看到自己名下刚造的那张卡（scope 生效的正向证据）
	if total["viewer"] < 1 {
		t.Errorf("❌ viewer 资产数应 ≥1（已造其名下探针卡），实际 %d", total["viewer"])
	}
}
