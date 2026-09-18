package syncer

import (
	"errors"
	"strings"
	"testing"

	"asset-mgr/integration/kingdee"
)

// ---- 测试夹具：按实测结构造节点 ----

// mkDept 造一个组织节点。longNumber 是默认视图下的完整路径，
// 层级/父节点都从它派生，避免测试里手写一堆互相矛盾的字段。
func mkDept(number, name, longNumber, orgPattern string) kingdee.Department {
	segs := strings.Split(longNumber, "!")
	parent := ""
	if len(segs) > 1 {
		parent = segs[len(segs)-2]
	}
	return kingdee.Department{
		ID:               "id-" + number,
		Number:           number,
		Name:             name,
		OrgPatternName:   orgPattern,
		OrgPatternNumber: "Orgform06",
		Enable:           "1",
		Status:           "C",
		Structure: []kingdee.DepartmentNode{{
			ViewID:           kingdee.DefaultOrgViewID,
			ViewName:         "行政组织视图默认方案",
			Level:            len(segs),
			LongNumber:       longNumber,
			ViewParentNumber: parent,
			Seq:              len(segs),
		}},
	}
}

func mkPerson(number, name string, deptNumbers ...string) kingdee.Personnel {
	p := kingdee.Personnel{Number: number, Name: name, Enable: "1", Status: "C"}
	for i, d := range deptNumbers {
		p.EntryEntity = append(p.EntryEntity, kingdee.PersonnelEntry{
			ID: "e" + d, Seq: i + 1, DeptNumber: d, DeptName: "部门" + d,
		})
	}
	return p
}

// scopeFixture 造一份覆盖范围的完整组织树：
//
//	GCGS!GCGSHB!12!1201            XXX公司（公司）
//	  GCGS!GCGSHB!12!1201!120101   公司领导
//	    ...!120101!12010101        公司领导一组
//	  GCGS!GCGSHB!12!1201!120108   品牌资产管理部
//	  GCGS!GCGSHB!12!1201!1200151  业务支持中心二组（⚠ 编码"跨支"，不以 1201 开头）
//	GCGS!GCGSHB!12!1202            YYY公司（公司）
//	  GCGS!GCGSHB!12!1202!120210   品牌资产管理部
//	GCGS!GCGSHB!12                  XXX板块（合并）—— 范围外
//	GCGS!GCGSHB!12!1203             范围外的兄弟公司
//	GCGS!GCGSHB!0101!010104         范围外的部门
func scopeFixture() []kingdee.Department {
	return []kingdee.Department{
		mkDept("GCGS", "ZZZ集团（集团）", "GCGS", "集团"),
		mkDept("GCGSHB", "ZZZ集团（合并）", "GCGS!GCGSHB", "集团"),
		mkDept("12", "XXX公司（合并）", "GCGS!GCGSHB!12", "公司"),
		mkDept("1201", "XXX公司", "GCGS!GCGSHB!12!1201", "公司"),
		mkDept("1202", "YYY公司", "GCGS!GCGSHB!12!1202", "公司"),
		mkDept("1203", "范围外的兄弟公司", "GCGS!GCGSHB!12!1203", "公司"),
		mkDept("120101", "公司领导", "GCGS!GCGSHB!12!1201!120101", "部门"),
		mkDept("120108", "品牌资产管理部", "GCGS!GCGSHB!12!1201!120108", "部门"),
		mkDept("120121", "业务支持中心", "GCGS!GCGSHB!12!1201!120121", "部门"),
		mkDept("1200151", "业务支持中心二组", "GCGS!GCGSHB!12!1201!120121!1200151", "部门"),
		mkDept("12010101", "公司领导一组", "GCGS!GCGSHB!12!1201!120101!12010101", "部门"),
		mkDept("120210", "品牌资产管理部", "GCGS!GCGSHB!12!1202!120210", "部门"),
		mkDept("0101", "WWW公司", "GCGS!GCGSHB!0101", "公司"),
		mkDept("010104", "财务部", "GCGS!GCGSHB!0101!010104", "部门"),
	}
}

func TestInOrgScope(t *testing.T) {
	cases := []struct {
		longNumber string
		want       bool
		why        string
	}{
		{"GCGS!GCGSHB!12!1201", true, "范围根节点自身"},
		{"GCGS!GCGSHB!12!1202", true, "范围根节点自身"},
		{"GCGS!GCGSHB!12!1201!120108", true, "1201 的下级部门"},
		{"GCGS!GCGSHB!12!1201!120121!1200151", true, "⚠ 编码跨支，但路径在范围内"},
		{"GCGS!GCGSHB!12!1202!120210", true, "1202 的下级部门"},
		{"GCGS!GCGSHB!12", false, "12 是合并口径虚拟节点，不同步"},
		{"GCGS!GCGSHB!12!1203", false, "12 下的另一个兄弟公司"},
		{"GCGS!GCGSHB!0101!010104", false, "集团别处的部门"},
		{"GCGS!GCGSHB!12!12011", false, "前缀像但不是一个节点（12011 ≠ 1201 的子节点）"},
		{"", false, "空路径"},
	}
	for _, c := range cases {
		if got := InOrgScope(c.longNumber); got != c.want {
			t.Errorf("InOrgScope(%q) = %v，期望 %v（%s）", c.longNumber, got, c.want, c.why)
		}
	}
}

func TestOwnerCompanyOf(t *testing.T) {
	cases := []struct {
		longNumber string
		want       string
	}{
		{"GCGS!GCGSHB!12!1201", "1201"},
		{"GCGS!GCGSHB!12!1202", "1202"},
		{"GCGS!GCGSHB!12!1201!120108", "1201"},
		{"GCGS!GCGSHB!12!1201!120121!1200151", "1201"},
		{"GCGS!GCGSHB!12!1202!120210", "1202"},
		{"GCGS!GCGSHB!0101!010104", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := ownerCompanyOf(c.longNumber); got != c.want {
			t.Errorf("ownerCompanyOf(%q) = %q，期望 %q", c.longNumber, got, c.want)
		}
	}
}

func TestBuildOrgPlanKeepsOnlyScope(t *testing.T) {
	people := []kingdee.Personnel{
		mkPerson("000001", "范围内的", "120108", "120210"),
		mkPerson("000002", "只在范围外", "010104"),
		mkPerson("000003", "没有部门"),
	}
	plan, err := BuildOrgPlan(scopeFixture(), "[structure.view.id = 1]", people, "[null]")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}

	// 部门：范围外的 12 / 1203 / 0101 / 010104 都要被丢掉
	got := map[string]bool{}
	for _, d := range plan.Departments {
		got[d.Code] = true
	}
	for _, want := range []string{"120101", "120108", "120121", "1200151", "12010101", "120210"} {
		if !got[want] {
			t.Errorf("范围内部门 %s 应当保留，实际没有", want)
		}
	}
	for _, unwanted := range []string{"12", "1203", "0101", "010104", "1201", "1202"} {
		if got[unwanted] {
			t.Errorf("%s 不该出现在部门列表里（范围外，或本身是公司）", unwanted)
		}
	}

	// 公司：只留 1201 / 1202
	var compCodes []string
	for _, c := range plan.Companies {
		compCodes = append(compCodes, c.Code)
	}
	if strings.Join(compCodes, ",") != "1201,1202" {
		t.Errorf("公司列表 = %v，期望 [1201 1202]", compCodes)
	}

	// 人员：只留范围内那个
	if len(plan.Employees) != 1 || plan.Employees[0].EmpNo != "000001" {
		t.Fatalf("人员列表 = %+v，期望只留 000001", plan.Employees)
	}
	if plan.PeopleInScope != 1 || plan.PeopleRows != 3 {
		t.Errorf("PeopleInScope=%d PeopleRows=%d，期望 1 / 3", plan.PeopleInScope, plan.PeopleRows)
	}
}

// 公司判定必须用组织形态。层级、编码位数、名称都不可靠。
func TestBuildOrgPlanCompanyDetectedByOrgPattern(t *testing.T) {
	depts := []kingdee.Department{
		// L4 的公司（常见形态）
		mkDept("1201", "XXX公司", "GCGS!GCGSHB!12!1201", "公司"),
		// ⚠ 名字里带「公司」但形态是部门——不能当公司
		mkDept("120128", "XXX公司", "GCGS!GCGSHB!12!1201!120128", "部门"),
		// ⚠ 层级不在 L4 的公司
		mkDept("120199", "深层的公司", "GCGS!GCGSHB!12!1201!120128!120199", "公司"),
	}
	people := []kingdee.Personnel{mkPerson("000001", "甲", "120128")}

	plan, err := BuildOrgPlan(depts, "", people, "")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}

	codes := map[string]bool{}
	for _, c := range plan.Companies {
		codes[c.Code] = true
	}
	if !codes["1201"] {
		t.Error("1201 是公司，应当收进公司列表")
	}
	if codes["120128"] {
		t.Error("120128 形态是「部门」，不该被当成公司（名称里有「公司」二字不算）")
	}
	if !codes["120199"] {
		t.Error("120199 形态是「公司」，层级深浅不影响判定，应当收进公司列表")
	}
}

// 一个人挂多个部门时，主部门必须确定且可解释：1201 优先。
func TestBuildOrgPlanPrimaryDeptPrefers1201(t *testing.T) {
	depts := scopeFixture()
	people := []kingdee.Personnel{
		// 两个法人各一条（2026-09-18 快照：136 人里 118 人如此）——应选 1201 那条
		mkPerson("000001", "甲", "120108", "120210"),
		// 只有 1202 的——只能选 1202（实测 0 人如此，但回退分支必须对）
		mkPerson("000002", "乙", "120210"),
		// 1201 下有两条——取编码最小的，保证结果可复现
		mkPerson("000003", "丙", "120108", "120101"),
	}
	plan, err := BuildOrgPlan(depts, "", people, "")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}

	byEmp := map[string]OrgEmployee{}
	for _, e := range plan.Employees {
		byEmp[e.EmpNo] = e
	}

	if got := byEmp["000001"]; got.DeptCode != "120108" || got.CompanyCode != "1201" {
		t.Errorf("000001 主部门 = %s/%s，期望 120108/1201", got.DeptCode, got.CompanyCode)
	}
	if got := byEmp["000002"]; got.DeptCode != "120210" || got.CompanyCode != "1202" {
		t.Errorf("000002 主部门 = %s/%s，期望 120210/1202", got.DeptCode, got.CompanyCode)
	}
	if got := byEmp["000003"]; got.DeptCode != "120101" {
		t.Errorf("000003 主部门 = %s，期望 120101（编码最小，结果可复现）", got.DeptCode)
	}

	// 全部候选要如实保留，不能被主部门规则吃掉
	if got := byEmp["000001"]; strings.Join(got.DeptCodes, ",") != "120108,120210" {
		t.Errorf("000001 候选 = %v，期望 [120108 120210]", got.DeptCodes)
	}
}

// 部门列表必须按层级从浅到深排好，落库才能一遍串起 parent_id。
func TestBuildOrgPlanDepartmentsOrderedParentFirst(t *testing.T) {
	plan, err := BuildOrgPlan(scopeFixture(), "", []kingdee.Personnel{mkPerson("000001", "甲", "120108")}, "")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}

	inPlan := map[string]bool{}
	for _, d := range plan.Departments {
		inPlan[d.Code] = true
	}

	seen := map[string]bool{}
	for _, d := range plan.Departments {
		// 不变量：父节点只要也在部门列表里，就必须排在子节点前面。
		// （父节点是公司、或不在范围内的，落库时 parent_id 落 0，不在此列。）
		if d.ParentCode != "" && inPlan[d.ParentCode] && !seen[d.ParentCode] {
			t.Errorf("部门 %s 的父节点 %s 排在它后面，落库时 parent_id 会串不上", d.Code, d.ParentCode)
		}
		seen[d.Code] = true
	}

	// 层级必须单调不减
	for i := 1; i < len(plan.Departments); i++ {
		if plan.Departments[i-1].Level > plan.Departments[i].Level {
			t.Errorf("第 %d 项 L%d 排在第 %d 项 L%d 前面，层级不是从浅到深",
				i-1, plan.Departments[i-1].Level, i, plan.Departments[i].Level)
		}
	}
}

// 人员引用了组织树里没有的部门编码时，宁可判成"不在范围"，也不能瞎挂。
func TestBuildOrgPlanDropsUnresolvableDept(t *testing.T) {
	people := []kingdee.Personnel{
		mkPerson("000001", "甲", "999999"), // 组织树里没有
		mkPerson("000002", "乙", "120108"), // 正常
	}
	plan, err := BuildOrgPlan(scopeFixture(), "", people, "")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}
	if len(plan.Employees) != 1 || plan.Employees[0].EmpNo != "000002" {
		t.Fatalf("只应留下能解析部门的 000002，实际 %+v", plan.Employees)
	}
}

// 在职口径：enable / isforbidden / disabledate 三者共同决定。
func TestBuildOrgPlanActiveFlag(t *testing.T) {
	mk := func(no string, enable string, forbidden bool, disableDate string) kingdee.Personnel {
		p := mkPerson(no, "人"+no, "120108")
		p.Enable = enable
		p.IsForbidden = forbidden
		p.DisableDate = disableDate
		return p
	}
	people := []kingdee.Personnel{
		mk("000001", "1", false, ""),
		mk("000002", "0", false, ""),
		mk("000003", "1", true, ""),
		mk("000004", "1", false, "2025-02-24 10:38:02"),
	}
	plan, err := BuildOrgPlan(scopeFixture(), "", people, "")
	if err != nil {
		t.Fatalf("BuildOrgPlan 失败: %v", err)
	}
	want := map[string]bool{"000001": true, "000002": false, "000003": false, "000004": false}
	for _, e := range plan.Employees {
		if e.Active != want[e.EmpNo] {
			t.Errorf("%s Active = %v，期望 %v", e.EmpNo, e.Active, want[e.EmpNo])
		}
	}
}

// ⚠ 最重要的护栏：源返回 0 行必须中止，不能当成"真的没人了"。
// 否则服务端过滤条件一变，整张员工表就被清空。
func TestBuildOrgPlanAbortsOnEmptySource(t *testing.T) {
	people := []kingdee.Personnel{mkPerson("000001", "甲", "120108")}
	depts := scopeFixture()

	cases := []struct {
		name   string
		depts  []kingdee.Department
		people []kingdee.Personnel
	}{
		{"部门接口 0 行", nil, people},
		{"人员接口 0 行", depts, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildOrgPlan(c.depts, "[(1 = 1)]", c.people, "[(1 = 1)]")
			if err == nil {
				t.Fatal("应当返回错误并中止同步，实际成功了")
			}
			if !errors.Is(err, ErrOrgSourceEmpty) {
				t.Errorf("错误应当是 ErrOrgSourceEmpty，实际 %v", err)
			}
			// 错误信息里要带过滤条件——排查时第一个要看的就是它
			if !strings.Contains(err.Error(), "[(1 = 1)]") {
				t.Errorf("错误信息里应当带上服务端过滤条件，实际: %v", err)
			}
		})
	}
}

// 组织树结构变了（longnumber 前缀对不上）也要中止，而不是同步出 0 个部门。
func TestBuildOrgPlanAbortsWhenScopeMatchesNothing(t *testing.T) {
	// 整棵树搬到了别的前缀下，范围里一个节点都没有
	depts := []kingdee.Department{
		mkDept("1201", "XXX公司", "GCGS!GCGSHB!99!1201", "公司"),
		mkDept("120108", "品牌资产管理部", "GCGS!GCGSHB!99!1201!120108", "部门"),
	}
	_, err := BuildOrgPlan(depts, "", []kingdee.Personnel{mkPerson("000001", "甲", "120108")}, "")
	if !errors.Is(err, ErrOrgSourceEmpty) {
		t.Fatalf("范围匹配不到节点时应当中止，实际 err = %v", err)
	}
	if !strings.Contains(err.Error(), "longnumber") {
		t.Errorf("错误信息应当提示 longnumber 可能变了，实际: %v", err)
	}
}

// 人员接口有行、但没一个人在范围内 —— 同样是过滤条件被改的信号，要中止。
func TestBuildOrgPlanAbortsWhenNoPersonInScope(t *testing.T) {
	people := []kingdee.Personnel{mkPerson("000001", "甲", "010104")}
	_, err := BuildOrgPlan(scopeFixture(), "", people, "[(entryentity.dpt_number like '0101%')]")
	if !errors.Is(err, ErrOrgSourceEmpty) {
		t.Fatalf("范围内 0 人时应当中止，实际 err = %v", err)
	}
}

// 范围内一个公司节点都没有时，公司归属会全部落空，要中止。
func TestBuildOrgPlanAbortsWhenNoCompanyInScope(t *testing.T) {
	depts := []kingdee.Department{
		mkDept("120108", "品牌资产管理部", "GCGS!GCGSHB!12!1201!120108", "部门"),
	}
	_, err := BuildOrgPlan(depts, "", []kingdee.Personnel{mkPerson("000001", "甲", "120108")}, "")
	if !errors.Is(err, ErrOrgSourceEmpty) {
		t.Fatalf("范围内没有公司节点时应当中止，实际 err = %v", err)
	}
}

// 范围口径是业务固定约定，改动必须是有意识的。
// 这条测试把当前口径钉住，避免以后重构顺手改了没人发现。
func TestOrgScopeRootsArePinned(t *testing.T) {
	want := "GCGS!GCGSHB!12!1201,GCGS!GCGSHB!12!1202"
	if got := strings.Join(OrgScopeRoots, ","); got != want {
		t.Errorf("同步范围口径被改了：\n  当前 %s\n  期望 %s\n"+
			"这是业务固定约定（只同步 12 下的 1201、1202 两个法人），改之前请确认。", got, want)
	}
	if got := strings.Join(orgScopeRootCodes, ","); got != "1201,1202" {
		t.Errorf("从路径派生的公司编码 = %s，期望 1201,1202", got)
	}
}
