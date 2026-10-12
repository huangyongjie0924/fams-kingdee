package model

import (
	"reflect"
	"strings"
	"testing"
)

// —— 多角色（一人可多角色）核心逻辑的单元测试 ——
//
// 覆盖 model/model.go 里 NormalizeRoles / ParseRoles / FormatRoles / EffectiveRoles /
// HasRole / CanAny 的边界。这些是 api 层与 store 层共用的地基，出问题会同时污染
// 写入（roles 列文本）与鉴权（并集判定），故单测必须钉死。

func TestNormalizeRoles(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"去重", []string{"employee", "employee"}, []string{"employee"}},
		{"剔非法", []string{"employee", "bogus", "admin"}, []string{"admin", "employee"}},
		{"按声明顺序排序（稳定）", []string{"employee", "admin", "maintainer"}, []string{"admin", "maintainer", "employee"}},
		{"trim 前后空白", []string{" employee ", "admin"}, []string{"admin", "employee"}},
		{"空串元素被剔除", []string{"", "employee", "  "}, []string{"employee"}},
		{"全非法返回空切片", []string{"x", "y"}, []string{}},
		{"nil 返回空切片", nil, []string{}},
		{"全量角色保持声明顺序", []string{"employee", "maintainer", "dept_head", "counter", "asset_manager", "admin"},
			[]string{"admin", "asset_manager", "counter", "dept_head", "maintainer", "employee"}},
	}
	for _, c := range cases {
		got := NormalizeRoles(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: NormalizeRoles(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
	// 归一化结果必须非 nil（api 层靠 len() 判断，nil 与空切片对 len 等价，但显式钉住避免误解）。
	if NormalizeRoles(nil) == nil {
		t.Errorf("NormalizeRoles(nil) 应返回非 nil 空切片")
	}
}

func TestFormatRolesNoSpace(t *testing.T) {
	// FormatRoles 输出必须**无空格**：api/notify_plan.go 的 FIND_IN_SET 依赖这一约定。
	if got := FormatRoles([]string{"employee", "admin"}); got != "admin,employee" {
		t.Fatalf("FormatRoles 应为 admin,employee，实际 %q", got)
	}
	if strings.Contains(FormatRoles([]string{"admin", "maintainer", "employee"}), " ") {
		t.Fatalf("FormatRoles 不得含空格")
	}
	// 非法 / 空白输入被归一化掉，不产出多余逗号。
	if got := FormatRoles([]string{"employee", " ", "bogus"}); got != "employee" {
		t.Fatalf("FormatRoles 应剔除非法与空白，实际 %q", got)
	}
	if got := FormatRoles(nil); got != "" {
		t.Fatalf("FormatRoles(nil) 应为空串，实际 %q", got)
	}
}

func TestParseFormatRolesRoundTrip(t *testing.T) {
	// 往返：ParseRoles(FormatRoles(x)) == NormalizeRoles(x)。
	in := []string{"employee", "admin", "maintainer"}
	roles := NormalizeRoles(in)
	s := FormatRoles(roles)
	back := ParseRoles(s)
	if !reflect.DeepEqual(back, roles) {
		t.Fatalf("往返失败：%v -> %q -> %v", roles, s, back)
	}
	// 带空格的输入应被 ParseRoles trim 后归一化。
	if got := ParseRoles("admin, employee"); !reflect.DeepEqual(got, []string{"admin", "employee"}) {
		t.Fatalf("ParseRoles 应 trim 空格，实际 %v", got)
	}
	// 空串返回 nil（区别于空切片）。
	if got := ParseRoles(""); got != nil {
		t.Fatalf("ParseRoles(\"\") 应返回 nil，实际 %v", got)
	}
	if got := ParseRoles("   "); got != nil {
		t.Fatalf("ParseRoles(空白) 应返回 nil，实际 %v", got)
	}
}

func TestEffectiveRolesFallback(t *testing.T) {
	// Roles 非空 → 用它（并归一化 / 排序）。
	u := &User{Role: "admin", Roles: []string{"employee", "maintainer"}}
	if got, want := u.EffectiveRoles(), []string{"maintainer", "employee"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roles 非空时应用 Roles：got %v, want %v", got, want)
	}
	// Roles 空（存量行 / 旧 token）→ 回落 [Role]。
	u2 := &User{Role: "employee"}
	if got, want := u2.EffectiveRoles(), []string{"employee"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roles 空时应回落 [Role]：got %v, want %v", got, want)
	}
	// Roles 全是非法值 → 也回落 [Role]。
	u3 := &User{Role: "employee", Roles: []string{"bogus"}}
	if got, want := u3.EffectiveRoles(), []string{"employee"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roles 全非法时应回落 [Role]：got %v, want %v", got, want)
	}
	// Role 也空 → nil。
	if got := (&User{}).EffectiveRoles(); got != nil {
		t.Errorf("Role 与 Roles 都空时应返回 nil，实际 %v", got)
	}
}

func TestHasRole(t *testing.T) {
	roles := []string{"employee", "maintainer"}
	if !HasRole(roles, "maintainer") {
		t.Errorf("应含 maintainer")
	}
	if HasRole(roles, "admin") {
		t.Errorf("不应含 admin")
	}
	if HasRole(nil, "admin") {
		t.Errorf("空集合不应含任何角色")
	}
}

func TestCanAnyUnion(t *testing.T) {
	// admin 恒真：即使它不在 rolePermissions 表里。
	if !CanAny([]string{"admin"}, PermSyncManage) {
		t.Errorf("admin 应恒有全部权限（含 sync.manage）")
	}
	// 并集：maintainer 有 repair.handle，employee 有 repair.report；
	// 单角色各自拿不到对方的权限，合起来两个都能拿到。
	both := []string{"employee", "maintainer"}
	if !CanAny(both, PermRepairReport) {
		t.Errorf("[employee,maintainer] 应含 repair.report（employee 维度）")
	}
	if !CanAny(both, PermRepairHandle) {
		t.Errorf("[employee,maintainer] 应含 repair.handle（maintainer 维度）")
	}
	if CanAny([]string{"employee"}, PermRepairHandle) {
		t.Errorf("单 employee 不应有 repair.handle")
	}
	if CanAny([]string{"maintainer"}, PermRepairReport) {
		t.Errorf("单 maintainer 不应有 repair.report")
	}
	// 空集合为假。
	if CanAny(nil, PermRepairReport) {
		t.Errorf("空集合不应有任何权限")
	}
	if CanAny([]string{}, PermRepairReport) {
		t.Errorf("空切片不应有任何权限")
	}
	// 未知角色为假。
	if CanAny([]string{"bogus"}, PermRepairReport) {
		t.Errorf("未知角色不应有任何权限")
	}
}

// TestCanAndCanAnyConsistent 单角色 Can 与多角色 CanAny 在单元素集合下必须一致。
func TestCanAndCanAnyConsistent(t *testing.T) {
	perms := []string{PermAssetManage, PermRepairReport, PermRepairHandle, PermSyncManage, PermUserManage}
	for _, role := range Roles {
		for _, p := range perms {
			if Can(role, p) != CanAny([]string{role}, p) {
				t.Errorf("Can(%q,%q)=%v 与 CanAny([%q],%q)=%v 不一致",
					role, p, Can(role, p), role, p, CanAny([]string{role}, p))
			}
		}
	}
}
