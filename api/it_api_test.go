package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"asset-mgr/model"
)

// —— IT 信息设备台账的 HTTP 层端到端测试（2026-10-10 需求）——
//
// 复用 repair_qa_api_test.go 的 qaH 脚手架：连真库 + 真路由 + 真 token 签发。
// 走 HTTP 而不是直接调 handler，是为了把「路由有没有挂」「requirePerm 有没有包上」
// 这两件最容易漏的事一并覆盖（单元测试测不到它们）。

// itCleanup 注册 IT 探针记录的清理。前缀统一 QA-IT-。
func (h *qaH) itCleanup() {
	h.t.Helper()
	h.t.Cleanup(func() {
		h.st.DB().Exec("DELETE FROM it_equipment WHERE asset_code LIKE 'QA-IT-%'")
	})
}

// newITRecord 直接经 store 建一条 IT 探针记录（不走 HTTP，供权限/范围用例造数据）。
func (h *qaH) newITRecord(code string, cardID, empID, deptID int64) int64 {
	h.t.Helper()
	id, err := h.st.CreateITEquipment(&model.ITEquipment{
		CardID: cardID, AssetCode: code, DeviceType: "笔记本",
		UserEmpID: empID, DeptID: deptID, Status: model.ITStatusInUse,
	}, "qa")
	if err != nil {
		h.t.Fatalf("建 IT 探针记录失败: %v", err)
	}
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM it_equipment WHERE id=?", id) })
	return id
}

// TestITEquipmentAPIPermGate 钉死写接口的权限门控。
//
// 只有 it_admin（与 admin）持有 it.manage。asset_manager 持有 asset.manage 等一堆权限位，
// 但**刻意没有** it.manage —— 本用例对它测一次，正是为了证明「有别的管理权限 ≠ 能改 IT 台账」。
func TestITEquipmentAPIPermGate(t *testing.T) {
	h := qaSetup(t)
	h.itCleanup()

	itAdmin := model.User{ID: 11, Username: "qa-it-admin", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}, EmployeeID: 0}
	assetMgr := model.User{ID: 12, Username: "qa-it-am", RealName: "资产管理员",
		Role: model.RoleAssetManager, Roles: []string{model.RoleAssetManager}, EmployeeID: 12}
	emp := model.User{ID: 13, Username: "qa-it-emp", RealName: "员工",
		Role: model.RoleEmployee, Roles: []string{model.RoleEmployee}, EmployeeID: 13}
	admin := model.User{ID: 14, Username: "qa-it-super", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 0}

	body := map[string]any{
		"asset_code": fmt.Sprintf("QA-IT-PERM-%d", time.Now().UnixNano()),
		"status":     model.ITStatusInUse,
	}

	// 未登录 → 401（鉴权中间件先拦）。
	if code, _, _ := h.do("POST", "/api/it-equipment", "", body); code != http.StatusUnauthorized {
		t.Errorf("未登录应 401，实际 %d", code)
	}

	// 有权限位但没 it.manage 的角色 → 403。
	for _, u := range []model.User{assetMgr, emp} {
		code, resp, _ := h.do("POST", "/api/it-equipment", h.token(u), body)
		if code != http.StatusForbidden {
			t.Errorf("%s 无 it.manage 应被拒 403，实际 %d %v", u.Username, code, resp)
		}
	}

	// it_admin / admin → 放行。
	for _, u := range []model.User{itAdmin, admin} {
		code, resp, _ := h.do("POST", "/api/it-equipment", h.token(u), body)
		if code != http.StatusOK {
			t.Fatalf("%s 应放行，实际 %d %v", u.Username, code, resp)
		}
		id, _ := resp["id"].(float64)
		if id <= 0 {
			t.Fatalf("%s 应返回新 id，实际 %v", u.Username, resp)
		}
		// 用同一个用户验证 PUT / DELETE 同样受门控（能过）。
		code, _, _ = h.do("PUT", fmt.Sprintf("/api/it-equipment/%d", int64(id)), h.token(u),
			map[string]any{"asset_code": body["asset_code"], "status": model.ITStatusIdle})
		if code != http.StatusOK {
			t.Errorf("%s PUT 应放行，实际 %d", u.Username, code)
		}
		code, _, _ = h.do("DELETE", fmt.Sprintf("/api/it-equipment/%d", int64(id)), h.token(u), nil)
		if code != http.StatusOK {
			t.Errorf("%s DELETE 应放行，实际 %d", u.Username, code)
		}
	}

	// 反向：employee 对 PUT / DELETE 也必须 403（不能只在 POST 上挂 requirePerm）。
	probe := h.newITRecord(fmt.Sprintf("QA-IT-PERM2-%d", time.Now().UnixNano()), 0, 0, 0)
	if code, _, _ := h.do("PUT", fmt.Sprintf("/api/it-equipment/%d", probe), h.token(emp),
		map[string]any{"status": model.ITStatusIdle}); code != http.StatusForbidden {
		t.Errorf("employee PUT 应 403，实际 %d", code)
	}
	if code, _, _ := h.do("DELETE", fmt.Sprintf("/api/it-equipment/%d", probe), h.token(emp), nil); code != http.StatusForbidden {
		t.Errorf("employee DELETE 应 403，实际 %d", code)
	}
	// 被拒后记录必须还在。
	if got, _ := h.st.GetITEquipment(probe); got == nil {
		t.Errorf("被拒的删除不该真的删掉记录")
	}
}

// TestITEquipmentAPICardlessMultipleAndDuplicateCard 覆盖两个核心业务约束：
//  ① 「资产卡上没有的设备」可以有多条（card_id 为空）；
//  ② 同一张资产卡最多一条 IT 记录（第二次关联返回 409，且文案可操作）。
func TestITEquipmentAPICardlessMultipleAndDuplicateCard(t *testing.T) {
	h := qaSetup(t)
	h.itCleanup()

	itAdmin := model.User{ID: 21, Username: "qa-it-admin2", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}}
	tok := h.token(itAdmin)

	// ① 三条无卡设备都要能建。
	for i := 0; i < 3; i++ {
		body := map[string]any{
			"asset_code": fmt.Sprintf("QA-IT-CARDLESS-API-%d-%d", time.Now().UnixNano(), i),
			"device_type": "网络设备",
			"status":      model.ITStatusIdle,
		}
		code, resp, _ := h.do("POST", "/api/it-equipment", tok, body)
		if code != http.StatusOK {
			t.Fatalf("第 %d 条无卡设备应建成功，实际 %d %v", i+1, code, resp)
		}
	}

	// ② 同一张卡关联两次 → 409。
	cardID := h.newCard(fmt.Sprintf("QA-IT-LINK-%d", time.Now().UnixNano()), 0)
	first := map[string]any{"card_id": cardID, "status": model.ITStatusInUse}
	code, resp, _ := h.do("POST", "/api/it-equipment", tok, first)
	if code != http.StatusOK {
		t.Fatalf("首次关联资产卡应成功，实际 %d %v", code, resp)
	}
	firstID, _ := resp["id"].(float64)
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM it_equipment WHERE id=?", int64(firstID)) })

	code, resp, _ = h.do("POST", "/api/it-equipment", tok, first)
	if code != http.StatusConflict {
		t.Fatalf("同一张卡第二次关联应 409，实际 %d %v", code, resp)
	}
	// 文案必须可操作：告诉用户「已登记过，请直接编辑那条」。
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "已登记过") {
		t.Errorf("409 文案应说明已登记过并指向编辑，实际 %q", msg)
	}

	// ③ 资产编码以资产台账为准（服务端覆盖前端传的值）。
	card2 := h.newCard(fmt.Sprintf("QA-IT-LINK2-%d", time.Now().UnixNano()), 0)
	code, resp, _ = h.do("POST", "/api/it-equipment", tok, map[string]any{
		"card_id": card2, "asset_code": "乱填的编码", "status": model.ITStatusInUse,
	})
	if code != http.StatusOK {
		t.Fatalf("关联第二张卡应成功，实际 %d %v", code, resp)
	}
	id2, _ := resp["id"].(float64)
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM it_equipment WHERE id=?", int64(id2)) })
	got, err := h.st.GetITEquipment(int64(id2))
	if err != nil || got == nil {
		t.Fatalf("回读失败: %v %v", got, err)
	}
	var wantCode string
	if err := h.st.DB().QueryRow("SELECT asset_code FROM asset_card WHERE id=?", card2).Scan(&wantCode); err != nil {
		t.Fatalf("读探针卡编码失败: %v", err)
	}
	if got.AssetCode != wantCode {
		t.Errorf("关联卡的记录资产编码应取自资产台账 %q，实际 %q（前端传的是「乱填的编码」）", wantCode, got.AssetCode)
	}

	// ④ 「从资产台账关联生成」的判重入口：GET /api/it-equipment?card_id=N 应命中一条。
	code, resp, _ = h.do("GET", fmt.Sprintf("/api/it-equipment?card_id=%d", card2), tok, nil)
	if code != http.StatusOK {
		t.Fatalf("按 card_id 查询应 200，实际 %d", code)
	}
	if n, _ := resp["total"].(float64); int(n) != 1 {
		t.Errorf("按 card_id 应命中 1 条，实际 %v", resp["total"])
	}
}

// TestITEquipmentAPIValidation 校验门：枚举、内存范围、不存在的关联卡。
func TestITEquipmentAPIValidation(t *testing.T) {
	h := qaSetup(t)
	h.itCleanup()
	tok := h.token(model.User{ID: 31, Username: "qa-it-v", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}})

	cases := []struct {
		name string
		body map[string]any
		want string // 期望错误文案里的关键词
	}{
		{"电脑类型非法", map[string]any{"device_type": "蒸汽机"}, "电脑类型"},
		{"用途非法", map[string]any{"purpose": "打游戏"}, "用途"},
		{"状态非法", map[string]any{"status": "已飞升"}, "状态"},
		{"内存超上界", map[string]any{"memory_gb": 99999}, "内存容量"},
		{"内存为负", map[string]any{"memory_gb": -1}, "内存容量"},
		{"关联卡不存在", map[string]any{"card_id": 999999999}, "不存在"},
	}
	for _, c := range cases {
		code, resp, _ := h.do("POST", "/api/it-equipment", tok, c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d %v", c.name, code, resp)
			continue
		}
		msg, _ := resp["error"].(string)
		if !strings.Contains(msg, c.want) {
			t.Errorf("%s 的错误文案应含 %q，实际 %q", c.name, c.want, msg)
		}
	}

	// 空 body 是合法的：全部字段可空（状态会补默认值「在用」）。
	code, resp, _ := h.do("POST", "/api/it-equipment", tok, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("空 body 应可建（状态补默认值），实际 %d %v", code, resp)
	}
	id, _ := resp["id"].(float64)
	got, err := h.st.GetITEquipment(int64(id))
	if err != nil || got == nil {
		t.Fatalf("回读失败: %v %v", got, err)
	}
	if got.Status != model.ITStatusInUse {
		t.Errorf("状态为空时应补默认值 %q，实际 %q", model.ITStatusInUse, got.Status)
	}
}

// TestITEquipmentAPIListScopeNarrowed 可见范围在**数据行上**收窄：
// 员工只看自己名下的，IT 管理员不限。读接口本身不挂 requirePerm。
func TestITEquipmentAPIListScopeNarrowed(t *testing.T) {
	h := qaSetup(t)
	h.itCleanup()

	ns := time.Now().UnixNano()
	myEmp := int64(980001)
	otherEmp := int64(980002)
	mine := h.newITRecord(fmt.Sprintf("QA-IT-API-SCOPE-MINE-%d", ns), 0, myEmp, 0)
	h.newITRecord(fmt.Sprintf("QA-IT-API-SCOPE-OTHER-%d", ns), 0, otherEmp, 0)

	emp := model.User{ID: 41, Username: "qa-it-scope-emp", RealName: "员工",
		Role: model.RoleEmployee, Roles: []string{model.RoleEmployee}, EmployeeID: myEmp}
	itAdmin := model.User{ID: 42, Username: "qa-it-scope-admin", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}}

	// 员工：只看到自己那条。
	code, resp, raw := h.do("GET", "/api/it-equipment?page_size=500", h.token(emp), nil)
	if code != http.StatusOK {
		t.Fatalf("员工读列表应 200（读接口不门控），实际 %d %s", code, raw)
	}
	items, _ := resp["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("员工应至少看到自己名下那条，实际 0 条")
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if empID, _ := m["user_emp_id"].(float64); int64(empID) != myEmp {
			t.Errorf("员工看到了别人名下的记录 emp=%v（应只见自己）", m["user_emp_id"])
		}
	}
	if int64(len(items)) == 0 {
		t.Errorf("员工应看到自己那条 id=%d", mine)
	}

	// IT 管理员：不限，能看到两条以上。
	code, resp, _ = h.do("GET", "/api/it-equipment?page_size=500", h.token(itAdmin), nil)
	if code != http.StatusOK {
		t.Fatalf("IT 管理员读列表应 200，实际 %d", code)
	}
	if n, _ := resp["total"].(float64); int(n) < 2 {
		t.Errorf("IT 管理员应看到至少 2 条（不限范围），实际 %v", resp["total"])
	}
}

// TestITEquipmentAPIEnumsExposed 三组枚举必须从服务端下发（前端不另抄一份）。
func TestITEquipmentAPIEnumsExposed(t *testing.T) {
	h := qaSetup(t)
	tok := h.token(model.User{ID: 51, Username: "qa-it-enum", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}})

	code, resp, _ := h.do("GET", "/api/enums", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("/api/enums 应 200，实际 %d", code)
	}
	for _, key := range []string{"it_device_types", "it_purposes", "it_statuses"} {
		arr, ok := resp[key].([]any)
		if !ok || len(arr) == 0 {
			t.Errorf("/api/enums 应下发 %s（非空数组），实际 %v", key, resp[key])
		}
	}
	// 角色列表里必须有 it_admin，否则前端「用户管理」页的下拉拿不到它。
	roles, _ := resp["roles"].([]any)
	found := false
	for _, r := range roles {
		if s, _ := r.(string); s == model.RoleITAdmin {
			found = true
		}
	}
	if !found {
		t.Errorf("/api/enums 的 roles 应含 %q，实际 %v", model.RoleITAdmin, roles)
	}
}

// TestITEquipmentAPIExportAndImport 导出 / 模板 / 导入的端到端。
//
// 导入用一个内存里现造的 xlsx（表头取自 itImportHeaders，避免与实现漂移），
// 覆盖：① 无卡设备按自有编码新增；② 同一份文件再导一次是**更新**而不是重复。
func TestITEquipmentAPIExportAndImport(t *testing.T) {
	h := qaSetup(t)
	h.itCleanup()
	tok := h.token(model.User{ID: 61, Username: "qa-it-io", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin}})

	// 导出：应返回 xlsx 且非空。
	code, _, raw := h.do("GET", "/api/it-equipment/export", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d %s", code, raw)
	}
	if len(raw) == 0 {
		t.Fatalf("导出内容不应为空")
	}
	// xlsx 是 zip，头两字节应为 PK。
	if len(raw) < 2 || raw[0] != 'P' || raw[1] != 'K' {
		t.Errorf("导出内容不像 xlsx（应以 PK 开头），实际前 4 字节 % x", raw[:min(4, len(raw))])
	}

	// 模板：同样应返回 xlsx。
	code, _, raw = h.do("GET", "/api/it-equipment/import-template", tok, nil)
	if code != http.StatusOK || len(raw) < 2 || raw[0] != 'P' {
		t.Errorf("模板下载应返回 xlsx，实际 code=%d len=%d", code, len(raw))
	}

	// 导入：造一份含 1 行无卡设备的 xlsx。
	impCode := fmt.Sprintf("QA-IT-IMP-%d", time.Now().UnixNano())
	rows := [][]string{
		itImportHeaders,
		{impCode, "", "", "台式机", "机房", "备用", "闲置", "是", "华为 MateStation", "麒麟 9006C", "16", "SSD 512GB", "统信 UOS 20", "导入用例"},
	}
	first := h.uploadITXLSX(t, tok, rows)
	if first != http.StatusOK {
		t.Fatalf("首次导入应 200，实际 %d", first)
	}
	list, err := h.st.FindITEquipmentByAssetCode(impCode)
	if err != nil || len(list) != 1 {
		t.Fatalf("导入后应查到 1 条，实际 %d（err=%v）", len(list), err)
	}
	if !list[0].IsXinchuang || list[0].MemoryGB != 16 || list[0].DeviceType != "台式机" {
		t.Errorf("导入字段未正确落库：%+v", list[0])
	}
	if list[0].CardID != 0 {
		t.Errorf("资产台账里没有这个编码，card_id 应为 0，实际 %d", list[0].CardID)
	}

	// 同一份文件再导一次：必须是**更新**（改内存），不是新增。
	rows[1][10] = "32"
	if c := h.uploadITXLSX(t, tok, rows); c != http.StatusOK {
		t.Fatalf("二次导入应 200，实际 %d", c)
	}
	again, err := h.st.FindITEquipmentByAssetCode(impCode)
	if err != nil || len(again) != 1 {
		t.Fatalf("二次导入后仍应只有 1 条（不能重复），实际 %d（err=%v）", len(again), err)
	}
	if again[0].MemoryGB != 32 {
		t.Errorf("二次导入应更新内存为 32，实际 %d", again[0].MemoryGB)
	}

	// 资产编码留空的行必须被拒（否则反复导会重复）。
	bad := [][]string{
		itImportHeaders,
		{"", "", "", "笔记本", "", "", "在用", "否", "无编码设备", "", "8", "SSD 256GB", "", ""},
	}
	if c := h.uploadITXLSX(t, tok, bad); c != http.StatusBadRequest {
		t.Errorf("资产编码留空应 400（导入靠它判重），实际 %d", c)
	}
}

// uploadITXLSX 把行数据打成 xlsx 并以 multipart 上传到导入接口，返回状态码。
func (h *qaH) uploadITXLSX(t *testing.T, tok string, rows [][]string) int {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Sheet1"
	for i, row := range rows {
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+1)
			f.SetCellValue(sheet, cell, v)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成 xlsx 失败: %v", err)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "it-import.xlsx")
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(buf.Bytes()); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/it-equipment/import", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		t.Logf("导入返回 %d: %v", rec.Code, m)
	}
	return rec.Code
}
