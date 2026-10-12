package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"asset-mgr/model"
)

// 本文件是「新增角色必须显式登记到每一个 scope 分支」的守卫（2026-10-10 新增 it_admin 时补）。
//
// # 为什么需要它
//
// api/scope.go 的 assetScope / repairScope 都是「逐角色 switch + default 返回零值 scope」的结构，
// 而零值 scope 的语义是「**不限**」。也就是说：**新增角色忘登记的默认后果是看到全量数据**。
// 这是个 fail-open 的危险默认值，而且从界面上完全看不出来——
// 账号能正常登录、页面正常渲染，只是数据比别人多。
//
// 本次新增 it_admin 时，用户明确选择了「IT 管理员在资产台账与维修模块不限」，
// 于是我们写了显式的 `case model.RoleITAdmin:`。但**行为测试无法区分「显式写了 case」
// 和「落到 default」**——两者返回的 scope 一模一样。所以必须扫源码：
// 断言「每一个角色要么有显式 case，要么在下面这份『承认它落 default』的名单里」。
// 将来有人删掉那两行 case，本测试立刻变红。

// scopeDefaultRoles 是**明确承认**「落 default 分支 = 不限范围」的角色集合。
//
// 加进这里等于在代码评审里说一句「我知道它不限，这是有意的」。
// 新角色若不属于这一类，必须在 scope.go 里写显式 case，否则本测试会红。
var scopeDefaultRoles = map[string]bool{
	model.RoleAdmin:        true,
	model.RoleAssetManager: true,
	model.RoleCounter:      true,
	model.RoleMaintainer:   true,
}

// roleConstNames 把角色字符串映射到 model 包里的常量名。
//
// ⚠️ 这份映射必须与 model.Roles **完全一一对应**（下面有断言）。
// 它是本文件的核心机关：新增角色时忘了更新它 → 测试红 → 强制人回来看一眼 scope.go。
var roleConstNames = map[string]string{
	model.RoleAdmin:        "RoleAdmin",
	model.RoleAssetManager: "RoleAssetManager",
	model.RoleCounter:      "RoleCounter",
	model.RoleDeptHead:     "RoleDeptHead",
	model.RoleMaintainer:   "RoleMaintainer",
	model.RoleEmployee:     "RoleEmployee",
	model.RoleITAdmin:      "RoleITAdmin",
}

// TestScopeRoleConstNamesCoverAllRoles 前置守卫：映射表必须覆盖 model.Roles 的每一项。
// 它红了说明「有人加了角色但没更新本文件的映射表」——这正是要拦住的时刻。
func TestScopeRoleConstNamesCoverAllRoles(t *testing.T) {
	for _, r := range model.Roles {
		if roleConstNames[r] == "" {
			t.Fatalf("角色 %q 在 model.Roles 里，但 api/role_scope_guard_test.go 的 "+
				"roleConstNames 没有登记它。请在映射表里补一行，并确认 scope.go 里"+
				"assetScope / repairScope 对该角色有显式处理（或把它加进 scopeDefaultRoles 并说明理由）。", r)
		}
	}
	if len(roleConstNames) != len(model.Roles) {
		t.Fatalf("roleConstNames 有 %d 项，model.Roles 有 %d 项 —— 两者必须一一对应"+
			"（多出来的项说明角色被删了但映射没删）", len(roleConstNames), len(model.Roles))
	}
}

// TestScopeHandlesEveryRoleExplicitly 核心守卫：每个角色都必须被 scope.go 显式处理。
//
// 判定：要么源码里有 `case model.Role<X>:`，要么它在 scopeDefaultRoles 里被明确承认。
// 两者都不是 ⇒ 该角色会静默落 default ⇒ 读到全量数据。
func TestScopeHandlesEveryRoleExplicitly(t *testing.T) {
	src, err := os.ReadFile("scope.go")
	if err != nil {
		t.Fatalf("读取 scope.go 失败：%v", err)
	}
	// 归一化空白：把 "case\n\t\tmodel.RoleXxx:" 也压成可匹配的一行。
	norm := strings.Join(strings.Fields(string(src)), " ")

	for _, role := range model.Roles {
		constName := roleConstNames[role]
		explicit := strings.Contains(norm, "case model."+constName+":")
		if explicit {
			continue
		}
		if scopeDefaultRoles[role] {
			// 已在名单里明确承认「落 default = 不限」。
			continue
		}
		t.Errorf("角色 %q（model.%s）在 api/scope.go 里既没有显式 case，"+
			"也不在 scopeDefaultRoles 名单里 —— 它会静默落进 default 分支，"+
			"而 default 返回零值 scope 的语义是「不限」，即**看到全量数据**。"+
			"请二选一：① 在 assetScope 与 repairScope 各加一个显式 case 并说明取舍；"+
			"② 把它加进 scopeDefaultRoles 并说明为什么它就该不限。", role, constName)
	}
}

// TestScopeITAdminExplicitInBothScopes 把 it_admin 的显式登记钉死。
//
// 与上一个测试互补：上一个检查「有没有登记」，这一个检查「登记在**两个**函数里」。
// 只加一处是最容易犯的错——assetScope 加了、repairScope 忘了，而两个函数的测试
// 分别看都「有 it_admin 的字样」。
func TestScopeITAdminExplicitInBothScopes(t *testing.T) {
	src, err := os.ReadFile("scope.go")
	if err != nil {
		t.Fatalf("读取 scope.go 失败：%v", err)
	}
	norm := strings.Join(strings.Fields(string(src)), " ")
	// assetScope 与 repairScope 各需要一个 case，故至少要出现两次。
	if n := strings.Count(norm, "case model.RoleITAdmin:"); n < 2 {
		t.Fatalf("api/scope.go 里 `case model.RoleITAdmin:` 只出现 %d 次，应至少 2 次"+
			"（assetScope 与 repairScope 各一次）。少的那一处会让 IT 管理员在该模块"+
			"静默落 default = 不限——行为恰好相同，所以只有本测试能发现。", n)
	}
}

// TestITAdminScopeUnrestricted 行为断言：it_admin 在三个 scope 里都是「不限」。
//
// 这是用户明确确认的取舍：IT 管理员要能检索全库资产来挑设备做关联，
// 也要能看到全公司所有 IT 设备。用零值 Server 即可 —— 不限路径在任何 store 查询之前返回。
func TestITAdminScopeUnrestricted(t *testing.T) {
	srv := &Server{}
	u := model.User{
		ID: 9001, Username: "itadmin", RealName: "IT 管理员",
		Role: model.RoleITAdmin, Roles: []string{model.RoleITAdmin},
		// 刻意给 0：it_admin 不依赖 employee_id，不该被「未绑定员工」403 误伤
		// （与 admin / asset_manager 同一条约束，见 TestScopeSingleRoleRegression）。
		EmployeeID: 0,
	}

	rec := httptest.NewRecorder()
	sc, ok := srv.assetScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("it_admin assetScope 应放行（employee_id=0 不应 403），实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sc.Restricted() {
		t.Errorf("it_admin 的 assetScope 应不限（用户明确确认），实际 %+v", sc)
	}

	rec = httptest.NewRecorder()
	rsc, ok := srv.repairScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("it_admin repairScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rsc.Restricted() {
		t.Errorf("it_admin 的 repairScope 应不限，实际 %+v", rsc)
	}

	rec = httptest.NewRecorder()
	isc, ok := srv.itScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("it_admin itScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if isc.Restricted() {
		t.Errorf("it_admin 的 itScope 应不限，实际 %+v", isc)
	}
}

// TestITScopeOfDropsRepairingDimension 钉死 itScopeOf 的**唯一**映射语义。
//
// itScope 是「从 assetScope 派生」而不是另写一份 switch（防两个模块的范围随时间分叉）。
// 代价是必须显式丢弃 AssetScope 的第三维 RepairingCardIDs ——
// 那一维是「有未结单维修单的资产对员工可见」，属维修模块语义；
// 照搬会让员工看到别人名下设备的 IT 配置（CPU/内存/系统版本）。
//
// 本测试是纯函数级的：一旦有人「顺手」把第三维也映射过去，立刻红。
func TestITScopeOfDropsRepairingDimension(t *testing.T) {
	sc := model.AssetScope{
		SelfEmpID:        6001,
		DeptIDs:          []int64{1201, 1202},
		RepairingCardIDs: []int64{77, 88, 99},
	}
	got := itScopeOf(sc)

	if got.SelfEmpID != 6001 {
		t.Errorf("SelfEmpID 应原样映射，实际 %d", got.SelfEmpID)
	}
	if len(got.DeptIDs) != 2 || got.DeptIDs[0] != 1201 || got.DeptIDs[1] != 1202 {
		t.Errorf("DeptIDs 应原样映射，实际 %v", got.DeptIDs)
	}
	// 关键断言：第三维必须被丢弃。ITScope 结构体里根本没有这个字段，
	// 所以这里验的是「映射后 Restricted() 的判定不受第三维影响」——
	// 用一个只有 RepairingCardIDs 非零的 scope 来验。
	onlyRepairing := itScopeOf(model.AssetScope{RepairingCardIDs: []int64{77}})
	if onlyRepairing.Restricted() {
		t.Errorf("只含 RepairingCardIDs 的 AssetScope 导出后应**不限**"+
			"（该维度属维修模块语义，不适用于 IT 台账），实际 %+v", onlyRepairing)
	}
}

// TestITScopeEmployeeNarrowed 行为断言：employee 的 itScope 被收窄到本人（真库）。
//
// itScope 内部走 assetScope，而 employee 分支会查 ListOpenRepairCardIDs，故需要连库。
func TestITScopeEmployeeNarrowed(t *testing.T) {
	st, cfg := mustTestStore(t)
	srv := NewServer(st, cfg)

	u := model.User{
		ID: 9101, Username: "it-emp", RealName: "员工",
		Role: model.RoleEmployee, Roles: []string{model.RoleEmployee},
		EmployeeID: 6101,
	}
	rec := httptest.NewRecorder()
	isc, ok := srv.itScope(rec, scopeReqFor(u))
	if !ok {
		t.Fatalf("employee itScope 应放行，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !isc.Restricted() {
		t.Fatalf("employee 的 itScope 必须收窄到本人，实际 %+v", isc)
	}
	if isc.SelfEmpID != 6101 {
		t.Errorf("itScope.SelfEmpID 应为 6101，实际 %d", isc.SelfEmpID)
	}
	// 允许：自己名下的记录。
	if !isc.Allows(&model.ITEquipment{UserEmpID: 6101, DeptID: 0}) {
		t.Errorf("自己名下的 IT 设备应对员工可见")
	}
	// 拒绝：别人名下、且不在自己部门的记录。
	if isc.Allows(&model.ITEquipment{UserEmpID: 9999, DeptID: 0}) {
		t.Errorf("别人名下的 IT 设备不应对员工可见，实际放行")
	}
}

// TestITScopeFailClosed 与 assetScope / repairScope 同一套 fail-closed 语义：
// 解析不出角色时 403，绝不退化成「不限」。
func TestITScopeFailClosed(t *testing.T) {
	srv := &Server{}
	u := model.User{ID: 99, Username: "bogus", Role: "bogus", Roles: []string{"bogus"}, EmployeeID: 127}
	if n := len(u.EffectiveRoles()); n != 0 {
		t.Fatalf("前置条件不成立：期望 EffectiveRoles() 为空，实际 %v", u.EffectiveRoles())
	}
	rec := httptest.NewRecorder()
	if _, ok := srv.itScope(rec, scopeReqFor(u)); ok {
		t.Errorf("空角色集合 itScope 必须 fail-closed，实际放行")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("空角色集合 itScope 必须写 403，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
}
