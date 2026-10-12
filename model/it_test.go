package model

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// —— IT 信息设备台账的模型层测试（2026-10-10 需求）——

func TestITEnumValidationAllowsEmpty(t *testing.T) {
	// 空串必须放行：IT 记录最常见的产生方式是「从资产台账关联生成」，
	// 那一刻类型/用途还是空的，等着人来补。空串非法的话主路径第一步就走不通。
	if !IsValidITDeviceType("") || !IsValidITPurpose("") || !IsValidITStatus("") {
		t.Fatalf("空串必须被视为合法（未填），见 model/it.go 的 IsValidITDeviceType 注释")
	}
	// 枚举内合法。
	for _, v := range ITDeviceTypes {
		if !IsValidITDeviceType(v) {
			t.Errorf("ITDeviceTypes 里的 %q 应合法", v)
		}
	}
	for _, v := range ITPurposes {
		if !IsValidITPurpose(v) {
			t.Errorf("ITPurposes 里的 %q 应合法", v)
		}
	}
	for _, v := range ITStatuses {
		if !IsValidITStatus(v) {
			t.Errorf("ITStatuses 里的 %q 应合法", v)
		}
	}
	// 枚举外非法。
	if IsValidITDeviceType("蒸汽机") {
		t.Errorf("枚举外的电脑类型不应合法")
	}
	if IsValidITPurpose("打游戏") {
		t.Errorf("枚举外的用途不应合法")
	}
	// ⚠️ 资产台账的「调拨中」**不属于** IT 状态枚举：IT 台账没有调拨概念。
	// 这条断言钉死「两套枚举不共用」，防止有人图省事把 ITStatuses 换成 AssetStatuses。
	if IsValidITStatus(StatusTransfer) {
		t.Errorf("资产状态「调拨中」不应是合法的 IT 设备状态（两套枚举刻意不共用）")
	}
	// 反过来，IT 的「待分配」也不在资产状态里。
	if contains(AssetStatuses, ITStatusPending) {
		t.Errorf("「待分配」是 IT 台账专有状态，不应出现在资产状态枚举里")
	}
}

func TestITScopeAllowsUnion(t *testing.T) {
	// 零值 = 不限。
	var zero ITScope
	if zero.Restricted() {
		t.Fatalf("零值 ITScope 应不限")
	}
	if !zero.Allows(&ITEquipment{UserEmpID: 1, DeptID: 2}) {
		t.Errorf("不限 scope 应放行一切")
	}

	// 两维取**并集**：命中任一即可见。
	// ⚠️ 这里必须用「同时非零」的 scope 才测得出并集——单维度时 OR 与 AND 结果相同，
	// 而多角色（如 [部门负责人, 员工]）会让两维同时非零（与 AssetScope 同一个坑，
	// 见 model.AssetScope.Allows 的注释）。
	sc := ITScope{SelfEmpID: 6001, DeptIDs: []int64{1201, 1202}}
	if !sc.Restricted() {
		t.Fatalf("两维非零应视为受限")
	}
	// 只命中 self 维度（部门不在列表里）→ 可见。
	if !sc.Allows(&ITEquipment{UserEmpID: 6001, DeptID: 9999}) {
		t.Errorf("命中 SelfEmpID 维度即应可见（并集），实际被拒")
	}
	// 只命中 dept 维度（使用人不是他）→ 可见。
	if !sc.Allows(&ITEquipment{UserEmpID: 9999, DeptID: 1202}) {
		t.Errorf("命中 DeptIDs 维度即应可见（并集），实际被拒")
	}
	// 两维都不命中 → 不可见。
	if sc.Allows(&ITEquipment{UserEmpID: 9999, DeptID: 9999}) {
		t.Errorf("两维都不命中不应可见")
	}
	// 空 DeptIDs 只有 self 维度。
	onlySelf := ITScope{SelfEmpID: 6001}
	if onlySelf.Allows(&ITEquipment{UserEmpID: 9999}) {
		t.Errorf("只有 self 维度时，别人名下的记录不应可见")
	}
}

// TestITAdminRoleRegistration 钉死 it_admin 的角色与权限登记（2026-10-10 需求）。
//
// 用户明确选择了「新增独立角色 it_admin」而不是复用 asset.manage，故：
//   - 角色必须在 model.Roles 里（否则 NormalizeRoles 会把它当非法值过滤掉 ⇒
//     EffectiveRoles() 变空 ⇒ api 层 fail-closed 403，账号直接不可用）；
//   - 权限**只有** it.manage：它需要「看」全量资产台账来找设备做关联（那是可见范围），
//     但**不该能改**资产卡（那是 asset.manage）。读与写分开是这里唯一的判断依据。
func TestITAdminRoleRegistration(t *testing.T) {
	if !isKnownRole(RoleITAdmin) {
		t.Fatalf("%q 必须在 model.Roles 里，否则会被 NormalizeRoles 当非法值过滤", RoleITAdmin)
	}
	// 必须在**末尾**：NormalizeRoles 按 Roles 顺序重排 roles 列，插在中间会让所有
	// 存量账号的 roles 文本在下次保存时被重写（语义不变但凭空产生一批无意义的变更）。
	if last := Roles[len(Roles)-1]; last != RoleITAdmin {
		t.Errorf("新增角色应追加在 model.Roles 末尾，实际末位是 %q", last)
	}

	got := rolePermissions[RoleITAdmin]
	want := map[string]bool{PermITManage: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("it_admin 的权限表应恰为 %v，实际 %v", want, got)
	}
	// 显式反证：不该拿到资产台账的写权限。
	if Can(RoleITAdmin, PermAssetManage) {
		t.Errorf("it_admin 不应持有 asset.manage（它只读资产台账，不写）")
	}
	// 也不该有任何维修侧权限。
	for _, p := range []string{PermRepairReport, PermRepairDispatch, PermRepairHandle, PermRepairApprove, PermRepairManage} {
		if Can(RoleITAdmin, p) {
			t.Errorf("it_admin 不应持有 %s", p)
		}
	}
	// admin 恒真（Can 里兜底），故 admin 一定能维护 IT 台账。
	if !Can(RoleAdmin, PermITManage) {
		t.Errorf("admin 应恒有 it.manage（Can 的 admin 兜底）")
	}
	// 归一化：it_admin 参与排序且不被过滤。
	if got := NormalizeRoles([]string{RoleITAdmin, RoleEmployee}); len(got) != 2 {
		t.Errorf("NormalizeRoles 应保留 it_admin，实际 %v", got)
	}
}

// TestFrontendRoleTablesInSync 是**前后端角色/权限表一致性**的守卫。
//
// # 为什么必须有它
//
// web/src/stores/auth.ts 的 rolePermissions 是后端 model.rolePermissions 的**手抄副本**，
// 两者漂移会产生两类真实故障（本仓已经发生过一次）：
//   - 前端多给了权限 → **按钮可见但点击 403**；
//   - 前端少给了权限 → 后端允许但界面上没有入口（功能「消失」）。
//
// 之前的做法是「靠人记得同步改两处」，那不可靠。本测试直接**解析 auth.ts 的
// rolePermissions 对象并逐项比对**，把约定变成可执行断言。
//
// 只比对非 admin 角色：admin 在后端 Can() 里兜底恒真、在前端 can() 里也有 `r === "admin"`
// 短路，两边都刻意不在表里列它。
func TestFrontendRoleTablesInSync(t *testing.T) {
	src, err := os.ReadFile("../web/src/stores/auth.ts")
	if err != nil {
		t.Fatalf("读取前端 auth.ts 失败（路径变了？本测试依赖相对路径 ../web/src/stores/auth.ts）：%v", err)
	}
	text := string(src)

	quoted := regexp.MustCompile(`"([^"]+)"`)
	for _, role := range Roles {
		if role == RoleAdmin {
			continue
		}
		// 匹配 `role: [ ... ]`（跨行）。只有 rolePermissions 用数组，roleLabels / scopeHints
		// 用的是字符串，故不会误匹配。
		re := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(role) + `\s*:\s*\[(.*?)\]`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("前端 auth.ts 的 rolePermissions 里找不到角色 %q —— "+
				"后端 model.Roles 有它，前端没有 ⇒ 该角色账号登录后界面会缺入口。", role)
			continue
		}
		fe := map[string]bool{}
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			fe[q[1]] = true
		}
		be := rolePermissions[role]
		if len(fe) != len(be) {
			t.Errorf("角色 %q 的权限数量不一致：后端 %v，前端 %v", role, sortedKeys(be), sortedKeys(fe))
			continue
		}
		for p := range be {
			if !fe[p] {
				t.Errorf("角色 %q 的权限 %q 后端有、前端没有 ⇒ 「后端允许但界面没入口」", role, p)
			}
		}
		for p := range fe {
			if !be[p] {
				t.Errorf("角色 %q 的权限 %q 前端有、后端没有 ⇒ 「按钮可见但点击 403」", role, p)
			}
		}
	}
}

// TestFrontendRoleLabelsCoverAllRoles 前端 roleLabels 必须覆盖每个角色。
// 缺了不会报错，只会让界面上把角色显示成英文原名（如 it_admin），
// 属于「不影响功能但一眼就能看出没做完」的漏配。
func TestFrontendRoleLabelsCoverAllRoles(t *testing.T) {
	src, err := os.ReadFile("../web/src/stores/auth.ts")
	if err != nil {
		t.Fatalf("读取前端 auth.ts 失败：%v", err)
	}
	text := string(src)
	re := regexp.MustCompile(`(?s)roleLabels\s*:\s*Record<string,\s*string>\s*=\s*\{(.*?)\n\};`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("解析 auth.ts 的 roleLabels 失败（结构改了？请同步更新本测试）")
	}
	for _, role := range Roles {
		// 匹配 `role: "..."`。
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(role) + `\s*:`).MatchString(m[1]) {
			t.Errorf("前端 roleLabels 缺少角色 %q 的中文名，界面上会显示英文原名", role)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 简单插入排序：测试里只用于报错信息，量小。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
