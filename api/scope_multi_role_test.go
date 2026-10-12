package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"asset-mgr/model"
)

// 本文件锁住「一人可多角色」的**数据可见范围并集**语义（docs/多角色改造方案.md §2 / §4.2）。
//
// 设计要点：
//   - 逐角色算出各自的 scope 后合并；任一角色「不限」则整体不限（「不限」即全集）。
//   - 判断角色用 HasRole / EffectiveRoles，不再直接比 u.Role（多角色下它只是主角色）。
//
// 为什么直接调 assetScope / repairScope 而不走 HTTP：
//   这两个函数的入参只有「请求上下文里的 User」。走 HTTP 要经 JWT 往返，而 token 是否已携带
//   roles 由另一位工程师并行改造；直接注入 User 能把断言钉在**角色集合**这一层，不受 token
//   序列化进度影响，也更贴合「并集」这一单元语义。

// scopeReqFor 把用户直接放进请求上下文，构造出 assetScope / repairScope 需要的请求。
func scopeReqFor(u model.User) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	return req.WithContext(context.WithValue(req.Context(), userKey, u))
}

// TestScopeMultiRoleRepairUnion 验证维修单可见范围的并集：任一角色允许的维度都并入。
func TestScopeMultiRoleRepairUnion(t *testing.T) {
	// 无 dept_head 的多角色用例不触库，用零值 Server 即可。
	srv := &Server{}

	// [employee, maintainer]：并集应**同时**含 reporter 与 assignee 两个维度。
	u := model.User{
		ID: 1, Username: "mr", RealName: "员工兼维护人员",
		Role:       model.RoleEmployee,
		Roles:      []string{model.RoleEmployee, model.RoleMaintainer},
		EmployeeID: 6001,
	}
	rec := httptest.NewRecorder()
	sc, ok := srv.repairScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("[employee,maintainer] repairScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sc.ReporterEmpID != 6001 {
		t.Errorf("并集应含 employee 的 reporter 维度 6001，实际 %+v", sc)
	}
	if sc.AssigneeEmpID != 6001 {
		t.Errorf("并集应含 maintainer 的 assignee 维度 6001，实际 %+v", sc)
	}

	// [asset_manager, maintainer]：asset_manager 不限 → 整体不限（并集里「不限」吃掉受限维度）。
	u2 := model.User{
		ID: 2, Username: "amm", RealName: "资产管理员兼维护人员",
		Role:       model.RoleAssetManager,
		Roles:      []string{model.RoleAssetManager, model.RoleMaintainer},
		EmployeeID: 6002,
	}
	rec = httptest.NewRecorder()
	sc2, ok := srv.repairScope(rec, scopeReqFor(u2))
	if !ok {
		t.Fatalf("[asset_manager,maintainer] repairScope 应放行，实际 code=%d", rec.Code)
	}
	if sc2.Restricted() {
		t.Errorf("并集里 asset_manager 不限应吃掉 maintainer 的受限维度，实际 %+v", sc2)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("不限路径不应写任何错误响应，实际 code=%d", rec.Code)
	}
}

// TestScopeMultiRoleUnrestrictedWins 验证资产可见范围里「不限」吃掉受限维度，且归一化顺序无关。
func TestScopeMultiRoleUnrestrictedWins(t *testing.T) {
	srv := &Server{}

	// 故意把 employee 写在前面：NormalizeRoles 会按声明顺序重排，asset_manager 先命中并直接不限。
	u := model.User{
		ID: 3, Username: "ame", RealName: "员工兼资产管理员",
		Role:       model.RoleEmployee,
		Roles:      []string{model.RoleEmployee, model.RoleAssetManager},
		EmployeeID: 6003,
	}
	rec := httptest.NewRecorder()
	sc, ok := srv.assetScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("[employee,asset_manager] assetScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sc.Restricted() {
		t.Errorf("并集里 asset_manager 不限应吃掉 employee 的受限维度，实际 %+v", sc)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("不限路径不应写任何错误响应，实际 code=%d", rec.Code)
	}
}

// TestScopeSingleRoleRegression 回归保护：单角色行为与改造前逐条一致，
// 尤其 admin / asset_manager 的 employee_id=0 不被「未绑定员工」403 误伤。
func TestScopeSingleRoleRegression(t *testing.T) {
	srv := &Server{}

	// admin / asset_manager：employee_id=0，两个 scope 都必须放行且不限。
	for _, role := range []string{model.RoleAdmin, model.RoleAssetManager} {
		u := model.User{ID: 10, Username: role, RealName: role, Role: role, EmployeeID: 0}

		rec := httptest.NewRecorder()
		sc, ok := srv.assetScope(rec, scopeReqFor(u))
		if !ok {
			t.Errorf("%s assetScope 应放行（employee_id=0 不应 403），实际 code=%d", role, rec.Code)
		}
		if sc.Restricted() {
			t.Errorf("%s assetScope 应不限，实际 %+v", role, sc)
		}

		rec = httptest.NewRecorder()
		rsc, ok := srv.repairScope(rec, scopeReqFor(u))
		if !ok {
			t.Errorf("%s repairScope 应放行（employee_id=0 不应 403），实际 code=%d", role, rec.Code)
		}
		if rsc.Restricted() {
			t.Errorf("%s repairScope 应不限，实际 %+v", role, rsc)
		}
	}

	// employee 单角色：只含 reporter 维度。
	emp := model.User{ID: 11, Username: "e", RealName: "员工", Role: model.RoleEmployee, EmployeeID: 6011}
	rec := httptest.NewRecorder()
	sc, ok := srv.repairScope(rec, scopeReqFor(emp))
	if !ok {
		t.Fatalf("employee repairScope 应放行，实际 code=%d", rec.Code)
	}
	if sc.ReporterEmpID != 6011 || sc.AssigneeEmpID != 0 {
		t.Errorf("employee 单角色应只含 reporter 维度，实际 %+v", sc)
	}

	// maintainer 单角色：同时含 reporter 与 assignee（改造前既有行为）。
	mt := model.User{ID: 12, Username: "m", RealName: "维护人员", Role: model.RoleMaintainer, EmployeeID: 6012}
	rec = httptest.NewRecorder()
	sc, ok = srv.repairScope(rec, scopeReqFor(mt))
	if !ok {
		t.Fatalf("maintainer repairScope 应放行，实际 code=%d", rec.Code)
	}
	if sc.ReporterEmpID != 6012 || sc.AssigneeEmpID != 6012 {
		t.Errorf("maintainer 单角色应同时含 reporter 与 assignee，实际 %+v", sc)
	}

	// employee 未绑定员工 → 403（该账号确实需要 employee 维度，保持单角色行为）。
	unbound := model.User{ID: 13, Username: "u", RealName: "未绑定", Role: model.RoleEmployee, EmployeeID: 0}
	rec = httptest.NewRecorder()
	if _, ok := srv.repairScope(rec, scopeReqFor(unbound)); ok {
		t.Errorf("未绑定员工的 employee 应被拒，实际放行")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("未绑定员工的 employee 应写 403，实际 code=%d", rec.Code)
	}
}

// TestScopeMultiRoleAssetUnionWithDept 验证含 dept_head 的并集需要连库（部门子树查询）。
// 断言 [dept_head, employee] 的 assetScope 同时含 DeptIDs 与 SelfEmpID。
//
// 用**探针部门 id** 而非真实部门：deptSubtree(root) 即使部门表为空也会返回 [root]，
// 故不依赖部门主数据存在——测试库为空时也能真跑，不会被 Skip 成「假绿」。
func TestScopeMultiRoleAssetUnionWithDept(t *testing.T) {
	st, cfg := mustTestStore(t)
	srv := NewServer(st, cfg)

	const probeDeptID = int64(880002)

	// 直接插一行 sys_user 承载 UserDeptID 查询。刻意不写 roles 列：
	// 该列存在时取默认空串、不存在时也不影响本 INSERT，两种迁移进度下都能跑。
	ns := time.Now().UnixNano()
	res, err := st.DB().Exec(
		`INSERT INTO sys_user (username, password_hash, real_name, role, employee_id, dept_id, enabled)
		 VALUES (?, '', 'QA多角色部门负责人', ?, ?, ?, 1)`,
		fmt.Sprintf("qa-mr-depthead-%d", ns), model.RoleDeptHead, int64(7001), probeDeptID)
	if err != nil {
		t.Fatalf("建探针账号失败: %v", err)
	}
	uid, _ := res.LastInsertId()
	t.Cleanup(func() { st.DB().Exec("DELETE FROM sys_user WHERE id=?", uid) })

	u := model.User{
		ID: uid, Username: "qa-mr-dh", RealName: "多角色部门负责人",
		Role:       model.RoleDeptHead,
		Roles:      []string{model.RoleDeptHead, model.RoleEmployee},
		EmployeeID: 7001,
	}

	rec := httptest.NewRecorder()
	sc, ok := srv.assetScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("[dept_head,employee] assetScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sc.DeptIDs) == 0 {
		t.Errorf("并集应含 dept_head 的 DeptIDs，实际 %+v", sc)
	}
	if sc.SelfEmpID != 7001 {
		t.Errorf("并集应含 employee 的 SelfEmpID=7001，实际 %+v", sc)
	}

	// 顺带验证 [dept_head, maintainer] 的 repairScope 并集：DeptIDs 与 assignee/reporter 同时存在。
	u2 := model.User{
		ID: uid, Username: "qa-mr-dh2", RealName: "部门负责人兼维护人员",
		Role:       model.RoleDeptHead,
		Roles:      []string{model.RoleDeptHead, model.RoleMaintainer},
		EmployeeID: 7001,
	}
	rec = httptest.NewRecorder()
	rsc, ok := srv.repairScope(rec, scopeReqFor(u2))
	if !ok {
		t.Fatalf("[dept_head,maintainer] repairScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(rsc.DeptIDs) == 0 {
		t.Errorf("并集应含 dept_head 的 DeptIDs，实际 %+v", rsc)
	}
	if rsc.ReporterEmpID != 7001 || rsc.AssigneeEmpID != 7001 {
		t.Errorf("并集应含 maintainer 的 reporter/assignee 维度，实际 %+v", rsc)
	}
}

// TestScopeEmptyRolesFailClosed 是 P0 回归：**解析不出任何角色**时必须 fail-closed。
//
// 背景（team-lead 用生产真实数据实测到）：生产库有 3 个 role='viewer' 的账号，改名后旧名已不在
// model.Roles 里。若角色名映射缺失，NormalizeRoles 会把非法名**过滤成空集合**，而
// assetScope / repairScope 是「逐角色累加」的循环——空集合 = 循环体一次都不执行 ⇒ 直接返回
// 零值 scope ⇒ Restricted()==false ⇒ **读到全量资产 / 全量维修单**。这是典型的 fail-open：
// 「解析不出角色」绝不能等价于「不限范围」。
//
// ⚠️ 本断言在 api/scope.go 加 fail-closed 前**必须红**（ok==true、code==200、Restricted==false），
// 加后**必须绿**。用零值 Server 即可：空角色在触库前就应被拦下，不该走到任何 store 查询。
func TestScopeEmptyRolesFailClosed(t *testing.T) {
	srv := &Server{}

	// 脏数据：Role 与 Roles 都是非法值 → EffectiveRoles() 为空集合。
	u := model.User{
		ID: 99, Username: "bogus", RealName: "脏角色",
		Role: "bogus", Roles: []string{"bogus"}, EmployeeID: 127,
	}
	if n := len(u.EffectiveRoles()); n != 0 {
		t.Fatalf("前置条件不成立：期望 EffectiveRoles() 为空，实际 %v", u.EffectiveRoles())
	}

	rec := httptest.NewRecorder()
	sc, ok := srv.assetScope(rec, scopeReqFor(u))
	if ok {
		t.Errorf("空角色集合 assetScope 必须 fail-closed，实际放行：scope=%+v Restricted=%v", sc, sc.Restricted())
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("空角色集合 assetScope 必须写 403，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	rsc, ok := srv.repairScope(rec, scopeReqFor(u))
	if ok {
		t.Errorf("空角色集合 repairScope 必须 fail-closed，实际放行：scope=%+v Restricted=%v", rsc, rsc.Restricted())
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("空角色集合 repairScope 必须写 403，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestEffectiveRolesLegacyRename 是 model 视角的**对照说明**，与 fail-closed 互补成两层：
//
//   - 模型层（model/model.go 的 LegacyRoleRenames）：把废弃名 viewer→employee、repair_tech→maintainer
//     **先映射再归一化**，让存量 3 个 role='viewer' 的账号仍能拿到 employee 的可见范围；
//   - api 层（api/scope.go 的 fail-closed）：万一将来出现「连映射都认不出」的脏数据，一律 403 兜底。
//
// 断言刻意写在 api 包内（**不改 model/role_test.go**，那是同事的文件）。
func TestEffectiveRolesLegacyRename(t *testing.T) {
	cases := []struct {
		name string
		u    model.User
		want []string
	}{
		{"viewer→employee", model.User{Role: "viewer", Roles: []string{"viewer"}}, []string{model.RoleEmployee}},
		{"repair_tech→maintainer", model.User{Role: "repair_tech", Roles: []string{"repair_tech"}}, []string{model.RoleMaintainer}},
		{"仅 Role 字段为废弃名", model.User{Role: "viewer"}, []string{model.RoleEmployee}},
		{"无法识别的脏名仍归一为空", model.User{Role: "bogus", Roles: []string{"bogus"}}, nil},
	}
	for _, tc := range cases {
		u := tc.u
		got := u.EffectiveRoles()
		if len(got) != len(tc.want) {
			t.Errorf("%s: EffectiveRoles()=%v，期望 %v", tc.name, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: EffectiveRoles()=%v，期望 %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestScopeEmployeeIncludesOpenRepairAssets 验证员工可见范围第三维（真库）：
// employee 的 assetScope 里 RepairingCardIDs 非空，且一张「有未结单、但 user_emp_id 不是他」的
// 资产对他可见（Allows 为 true）。
//
// 这是第三维的端到端证明：api 层从库里取到未结单 card_id 并填入 scope，model.Allows 据此放行。
// 安全性：走 mustTestStore 的安全阀（库名含 test 才真跑），自建自清，只碰带唯一前缀的行。
func TestScopeEmployeeIncludesOpenRepairAssets(t *testing.T) {
	st, cfg := mustTestStore(t)
	srv := NewServer(st, cfg)

	ns := time.Now().UnixNano()
	prefix := fmt.Sprintf("QA-EMP-SCOPE-REPAIR-%d", ns)
	// repair_order.code 是 VARCHAR(32)，上面这个前缀太长，维修单用短码。
	repairCode := fmt.Sprintf("QAER%d", ns)

	// 造一张「有未结单、但 user_emp_id=0（不是该员工）」的资产 + 一条 pending 维修单。
	res, err := st.DB().Exec(
		`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, user_emp_id, use_dept_id, created_by)
		 VALUES (?, ?, ?, '', 0, 0, 0, 'qa')`,
		prefix, "QA员工可见探针卡", model.StatusInUse)
	if err != nil {
		t.Fatalf("建探针卡失败: %v", err)
	}
	cardID, _ := res.LastInsertId()
	t.Cleanup(func() { st.DB().Exec("DELETE FROM asset_card WHERE id=?", cardID) })

	if _, err := st.DB().Exec(
		`INSERT INTO repair_order (code, card_id, reporter_emp_id, status, created_by)
		 VALUES (?, ?, 0, ?, 'qa')`, repairCode, cardID, model.RepairPending); err != nil {
		t.Fatalf("建维修单失败: %v", err)
	}
	t.Cleanup(func() { st.DB().Exec("DELETE FROM repair_order WHERE code=?", repairCode) })

	u := model.User{
		ID: 555, Username: "qa-emp-scope", RealName: "员工可见范围探针",
		Role: model.RoleEmployee, EmployeeID: 5501,
	}
	rec := httptest.NewRecorder()
	sc, ok := srv.assetScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("employee assetScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sc.RepairingCardIDs) == 0 {
		t.Fatalf("employee 的 assetScope 应含非空 RepairingCardIDs，实际 %+v", sc)
	}

	// 有未结单、但 user_emp_id 不是他 —— 应因第三维而可见。
	probe := &model.AssetCard{ID: cardID, UserEmpID: 0, UseDeptID: 0}
	if !sc.Allows(probe) {
		t.Errorf("有未结单维修单的资产（card_id=%d）应对员工可见，实际 scope=%+v", cardID, sc)
	}
}
