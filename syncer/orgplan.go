package syncer

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"asset-mgr/integration/kingdee"
)

// 本文件把星瀚返回的原始组织数据算成一份「落库计划」。
//
// 刻意做成纯函数：不碰数据库、不发请求，于是「范围怎么定」「主部门怎么选」
// 「哪些情况必须中止」这些判断都能直接单测，不用起库也不用连星瀚。
// 真正的落库在 org.go。

// OrgScopeRoots 是组织同步范围的根节点路径（longnumber）。
//
// 口径由业务固定：只同步「12」（某电器股份有限公司（合并））下面的
// 1201、1202 两个法人，及其全部下级组织。
//
// 两个刻意的取舍：
//   - 「12」本身是合并口径的虚拟节点，不是实际法人，不同步
//   - 范围判定用 longnumber 前缀，不用编码前缀：编码会"跨支"
//     （如 1200151 业务支持中心二组挂在 1201 下，但编码不以 1201 开头）
var OrgScopeRoots = []string{
	"GCGS!GCGSHB!12!1201",
	"GCGS!GCGSHB!12!1202",
}

// orgScopeRootCodes 从 OrgScopeRoots 派生公司编码（1201 / 1202）。
// 派生而不是各写一遍：路径和编码是同一件事的两种表示，写两处必然改漏一处。
var orgScopeRootCodes = func() []string {
	out := make([]string, 0, len(OrgScopeRoots))
	for _, r := range OrgScopeRoots {
		segs := strings.Split(r, "!")
		out = append(out, segs[len(segs)-1])
	}
	return out
}()

// ErrOrgSourceEmpty 表示源数据不健康（返回 0 行 / 范围内 0 条），同步必须中止。
//
// 这是本模块最重要的护栏。星瀚侧的服务端过滤条件会被静默改动——2026-09-18
// 人员接口就被加过一条匹配不到任何人的过滤条件，直接返回 0 行。
// 若那时照常走「源里没有 = 台账里停用」的逻辑，会把整张员工表清空。
// 所以 0 行一律当中止，不当作"真的没人了"。
var ErrOrgSourceEmpty = errors.New("星瀚组织数据为空，同步中止")

// OrgCompany 是计划里的一家公司（组织形态为「公司」的节点）。
type OrgCompany struct {
	Code      string // 星瀚编码，如 1201
	Name      string
	ShortName string // simplename，如「某电器公司」
	Level     int
	Enabled   bool
}

// OrgDepartment 是计划里的一个部门。
type OrgDepartment struct {
	Code        string // 星瀚编码，如 120108
	Name        string
	ParentCode  string // 星瀚父节点编码（可能是公司编码，如 1201）
	CompanyCode string // 归属公司编码（1201 / 1202）
	Level       int
	LongNumber  string
	Enabled     bool
	SortIndex   int
}

// OrgEmployee 是计划里的一名人员。
type OrgEmployee struct {
	EmpNo       string
	Name        string
	DeptCode    string // 主部门编码，可能为空
	CompanyCode string // 主部门归属公司
	Phone       string
	Active      bool
	// DeptCodes 是这名人员在范围内的全部候选部门（已去重排序）。
	// 保留它是为了如实反映「一人挂多个法人」这件事——
	// 2026-09-18 快照：136 名范围内人员里，118 人「1201 + 1202」双挂、
	// 18 人只在 1201、0 人只在 1202。这个比例会随星瀚数据变化。
	DeptCodes []string
}

// OrgPlan 是一次组织同步要落库的全部内容，附源数据诊断信息。
type OrgPlan struct {
	Companies   []OrgCompany
	Departments []OrgDepartment
	Employees   []OrgEmployee

	DeptRows      int    // 星瀚返回的部门总行数
	DeptFilter    string // 部门接口服务端生效的过滤条件
	DeptInScope   int    // 落在范围内的部门节点数（含两个公司节点）
	PeopleRows    int    // 星瀚返回的人员总行数
	PeopleFilter  string // 人员接口服务端生效的过滤条件
	PeopleInScope int    // 落在范围内的人员数
}

// BuildOrgPlan 把星瀚原始数据算成落库计划。
//
// 任何"源数据不健康"的情况都返回 ErrOrgSourceEmpty，由调用方中止整次同步。
func BuildOrgPlan(depts []kingdee.Department, deptFilter string,
	people []kingdee.Personnel, peopleFilter string) (*OrgPlan, error) {

	if len(depts) == 0 {
		return nil, fmt.Errorf("%w：部门接口返回 0 行（服务端过滤 %q）", ErrOrgSourceEmpty, deptFilter)
	}
	if len(people) == 0 {
		return nil, fmt.Errorf("%w：人员接口返回 0 行（服务端过滤 %q）", ErrOrgSourceEmpty, peopleFilter)
	}

	plan := &OrgPlan{
		DeptRows:     len(depts),
		DeptFilter:   deptFilter,
		PeopleRows:   len(people),
		PeopleFilter: peopleFilter,
	}

	// ---- 1. 建立编码 → 节点索引，并按范围筛 ----
	byNumber := make(map[string]*kingdee.Department, len(depts))
	for i := range depts {
		if n := strings.TrimSpace(depts[i].Number); n != "" {
			byNumber[n] = &depts[i]
		}
	}

	inScope := make([]*kingdee.Department, 0, 128)
	for i := range depts {
		d := &depts[i]
		if InOrgScope(d.LongNumber()) {
			inScope = append(inScope, d)
		}
	}
	plan.DeptInScope = len(inScope)
	if len(inScope) == 0 {
		return nil, fmt.Errorf("%w：部门接口返回 %d 行，但没有一个节点落在同步范围内。"+
			"组织树的 longnumber 可能变了（期望前缀 %s）",
			ErrOrgSourceEmpty, len(depts), strings.Join(OrgScopeRoots, " / "))
	}

	// ---- 2. 公司节点 ----
	// 只收「公司」形态的节点；1201/1202 各一个。
	// 判定用组织形态，不能用层级/编码位数/名称——见 kingdee.Department.IsCompany 的注释。
	for _, d := range inScope {
		if !d.IsCompany() {
			continue
		}
		plan.Companies = append(plan.Companies, OrgCompany{
			Code:      d.Number,
			Name:      d.Name,
			ShortName: d.SimpleName,
			Level:     d.Level(),
			Enabled:   d.Enabled(),
		})
	}
	sort.Slice(plan.Companies, func(i, j int) bool { return plan.Companies[i].Code < plan.Companies[j].Code })
	if len(plan.Companies) == 0 {
		return nil, fmt.Errorf("%w：范围内 %d 个节点里没有「公司」形态的节点，"+
			"公司归属将全部落空", ErrOrgSourceEmpty, len(inScope))
	}

	// ---- 3. 部门节点 ----
	// 按层级从浅到深输出，落库时父节点一定已经先建好，
	// 于是 parent_id 可以一遍串起来，不需要第二轮更新。
	for _, d := range inScope {
		if d.IsCompany() {
			continue
		}
		plan.Departments = append(plan.Departments, OrgDepartment{
			Code:        d.Number,
			Name:        d.Name,
			ParentCode:  d.ParentNumber(),
			CompanyCode: ownerCompanyOf(d.LongNumber()),
			Level:       d.Level(),
			LongNumber:  d.LongNumber(),
			Enabled:     d.Enabled(),
			SortIndex:   viewSeq(d),
		})
	}
	sort.SliceStable(plan.Departments, func(i, j int) bool {
		a, b := plan.Departments[i], plan.Departments[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return a.Code < b.Code
	})

	// ---- 4. 人员 ----
	for i := range people {
		p := &people[i]
		cands := scopedDeptCodes(p, byNumber)
		if len(cands) == 0 {
			continue // 不在范围内
		}
		deptCode, companyCode := pickPrimaryDept(cands, byNumber)
		plan.Employees = append(plan.Employees, OrgEmployee{
			EmpNo:       strings.TrimSpace(p.Number),
			Name:        p.Name,
			DeptCode:    deptCode,
			CompanyCode: companyCode,
			Phone:       p.Phone,
			Active:      p.Active(),
			DeptCodes:   cands,
		})
	}
	plan.PeopleInScope = len(plan.Employees)
	sort.Slice(plan.Employees, func(i, j int) bool {
		return plan.Employees[i].EmpNo < plan.Employees[j].EmpNo
	})
	if len(plan.Employees) == 0 {
		return nil, fmt.Errorf("%w：人员接口返回 %d 行，但没有一个人的任职组织落在同步范围内。"+
			"接口的服务端过滤条件可能又被改了（当前回显 %q）",
			ErrOrgSourceEmpty, len(people), peopleFilter)
	}

	return plan, nil
}

// InOrgScope 判断一个 longnumber 是否落在同步范围内（含范围根节点自身）。
func InOrgScope(longNumber string) bool {
	if longNumber == "" {
		return false
	}
	for _, root := range OrgScopeRoots {
		if longNumber == root || strings.HasPrefix(longNumber, root+"!") {
			return true
		}
	}
	return false
}

// ownerCompanyOf 沿 longnumber 从近到远找第一个范围根节点，返回其编码（1201 / 1202）。
func ownerCompanyOf(longNumber string) string {
	segs := strings.Split(longNumber, "!")
	for i := len(segs) - 1; i >= 0; i-- {
		for _, code := range orgScopeRootCodes {
			if segs[i] == code {
				return code
			}
		}
	}
	return ""
}

// scopedDeptCodes 取这名人员在范围内的全部候选部门编码（去重、升序）。
//
// 查不到组织节点的部门编码一律丢弃：宁可判成"不在范围"，
// 也不要把一个无法归属的部门写进台账。
func scopedDeptCodes(p *kingdee.Personnel, byNumber map[string]*kingdee.Department) []string {
	seen := make(map[string]bool, len(p.EntryEntity))
	out := make([]string, 0, len(p.EntryEntity))
	for _, e := range p.EntryEntity {
		code := strings.TrimSpace(e.DeptNumber)
		if code == "" || seen[code] {
			continue
		}
		d, ok := byNumber[code]
		if !ok || !InOrgScope(d.LongNumber()) {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// pickPrimaryDept 从候选里选一个主部门。
//
// 规则：优先取归属「1201」（某电器公司）的那条；没有则取编码最小的。
//
// 为什么必须定这条规则：多数范围内人员在两个法人下各挂一条同一职位的记录。
// 但台账 employee.dept_id 是单值，必须有个确定的、可解释的选择，
// 否则同一个人两次同步可能落到不同部门。
//
// 选 1201 的理由：台账资产的权属公司分布是 1201 下 130 张、1202 下 70 张，
// 且 1201 的 simplename「某电器公司」正是台账 company_name 里已经在用的写法。
//
// ⚠ 必须记住的副作用：因为固定优先 1201，**1202 子树的部门在台账里会显示 0 员工**。
// 这不是漏同步，也不是主数据残缺——那些人的主职被统一归到了 1201。
// 只有「在 1202 有任职、且在 1201 没有任何候选」的人才会落到 1202（下方回退分支）。
//
// 注意：这**不是**"人员只有一个部门"，只是给单值列一个确定答案。
// 全部候选保留在 OrgEmployee.DeptCodes 里。
//
// 上面两个分布数字是 2026-09-18 的快照，会随星瀚数据变化，别当成常量。
func pickPrimaryDept(cands []string, byNumber map[string]*kingdee.Department) (deptCode, companyCode string) {
	if len(cands) == 0 {
		return "", ""
	}
	if len(orgScopeRootCodes) > 0 {
		primary := orgScopeRootCodes[0] // 1201
		for _, c := range cands {       // cands 已升序，结果确定
			d, ok := byNumber[c]
			if ok && ownerCompanyOf(d.LongNumber()) == primary {
				return c, primary
			}
		}
	}
	// 没有 1201 的候选，退到编码最小的那条
	c := cands[0]
	if d, ok := byNumber[c]; ok {
		return c, ownerCompanyOf(d.LongNumber())
	}
	return c, ""
}

// viewSeq 取默认视图下的排序号，用于 department.sort_index。
func viewSeq(d *kingdee.Department) int {
	if v := d.View(kingdee.DefaultOrgViewID); v != nil {
		return v.Seq
	}
	return 0
}
