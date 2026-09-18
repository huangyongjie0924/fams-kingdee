package kingdee

import (
	"context"
	"errors"
	"fmt"
)

// 本文件承载「组织主数据」两类接口：部门（行政组织）与人员。
//
// 两个接口与资产卡的差异（实测，2026-09-18）：
//   - 请求体必须带 data（内容空对象即可），缺了报「请求参数没有 data 数据」
//   - 部门接口的 pageSize 必填，缺了报「页大小pageSize不能为空」
//   - 两者都带服务端过滤条件（响应 data.filter 回显），请求体覆盖不了；
//     人员接口的过滤条件当天被改过一次，直接导致返回 0 行
//
// 类型以实测为准，不照抄文档：文档把 id 标成 Long，实际是 String；
// totalCount 文档标 String，实际是 Integer。

// DepartmentNode 是 Department.Structure 里的一条「组织视图」记录。
//
// ⚠ 一个组织节点会带着多套视图（实测单行最多 21 条），
// level / longnumber / viewparent_number 都随视图不同而不同。
// 取组织树前必须先按 ViewID 筛，默认方案是 "1"（行政组织视图默认方案）——
// 这个值可以直接从接口过滤条件的回显 `[structure.view.id = 1]` 里读到。
type DepartmentNode struct {
	ViewID           string `json:"view_id"`
	ViewName         string `json:"view_name"`
	Level            int    `json:"level"`
	LongNumber       string `json:"longnumber"`
	IsLeaf           bool   `json:"isleaf"`
	Seq              int    `json:"seq"`
	ViewParentNumber string `json:"viewparent_number"`
	ViewParentName   string `json:"viewparent_name"`
	ViewOrgNumber    string `json:"vieworg_number"`
}

// Department 是 query-department 返回的一个组织节点（29 个顶层字段中的一部分）。
type Department struct {
	ID               string           `json:"id"`
	Number           string           `json:"number"`
	Name             string           `json:"name"`
	SimpleName       string           `json:"simplename"`
	OrgPatternName   string           `json:"orgpattern_name"`
	OrgPatternNumber string           `json:"orgpattern_number"`
	OrgPatternType   string           `json:"orgpattern_patterntype"`
	Enable           string           `json:"enable"`
	Status           string           `json:"status"`
	CreateTime       string           `json:"createtime"`
	ModifyTime       string           `json:"modifytime"`
	DisableDate      string           `json:"disabledate"`
	Structure        []DepartmentNode `json:"structure"`
}

// DefaultOrgViewID 是行政组织视图的默认方案 ID。
const DefaultOrgViewID = "1"

// View 返回指定视图下的层级信息；该节点没有这套视图时返回 nil。
func (d *Department) View(viewID string) *DepartmentNode {
	for i := range d.Structure {
		if d.Structure[i].ViewID == viewID {
			return &d.Structure[i]
		}
	}
	return nil
}

// Level 是默认视图下的层级；没有该视图时返回 0。
func (d *Department) Level() int {
	if v := d.View(DefaultOrgViewID); v != nil {
		return v.Level
	}
	return 0
}

// LongNumber 是默认视图下的完整路径（用 ! 分隔），例如
// GCGS!GCGSHB!12!1201!120108。范围判定与归属上溯都靠它。
func (d *Department) LongNumber() string {
	if v := d.View(DefaultOrgViewID); v != nil {
		return v.LongNumber
	}
	return ""
}

// ParentNumber 是默认视图下的父节点编码。
func (d *Department) ParentNumber() string {
	if v := d.View(DefaultOrgViewID); v != nil {
		return v.ViewParentNumber
	}
	return ""
}

// IsCompany 判断这个节点是不是「公司」。
//
// ⚠ 只能用组织形态判断，不能看层级、编码位数或名称：
//   - 公司节点层级实测散在 L2~L8，编码位数 2~12 位都有
//   - 名字里带「公司」的节点有 753 个其实是部门（如「某电器公司」120128）
func (d *Department) IsCompany() bool {
	return d.OrgPatternName == "公司"
}

// Enabled 判断节点是否启用。金蝶用 enable='1' 表示启用。
func (d *Department) Enabled() bool {
	return d.Enable == "1"
}

// PersonnelEntry 是 Personnel.EntryEntity 的一条「部门分录」。
//
// ⚠ 这不是历史调岗记录，而是「同一职位在每个法人主体下各挂一条」：
// 例如某人在某电器公司（1201）和某家电销售公司（1202）各有一条同名职位。
// Seq 是分录行号（实测到 165），**不是主职标志**，别拿它排序取第一条当主部门。
//
// dpt_* 三个字段是 2026-09-18 13:20 星瀚补进投影的，实测 10633/10633
// 命中组织树。orgstructure_number / orgstructure_name 至今恒为空。
type PersonnelEntry struct {
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	IsInCharge bool   `json:"isincharge"`
	Position   string `json:"position"`
	DeptID     string `json:"dpt_id"`
	DeptNumber string `json:"dpt_number"`
	DeptName   string `json:"dpt_name"`
}

// Personnel 是 query-personnel 返回的一名人员（27 个顶层字段中的一部分）。
type Personnel struct {
	ID          string           `json:"id"`
	Number      string           `json:"number"`
	Name        string           `json:"name"`
	Username    string           `json:"username"`
	Status      string           `json:"status"`
	Enable      string           `json:"enable"`
	Phone       string           `json:"phone"`
	Email       string           `json:"email"`
	Gender      string           `json:"gender"`
	IDCard      string           `json:"idcard"`
	Birthday    string           `json:"birthday"`
	IsForbidden bool             `json:"isforbidden"`
	DisableDate string           `json:"disabledate"`
	CreateTime  string           `json:"createtime"`
	ModifyTime  string           `json:"modifytime"`
	EntryEntity []PersonnelEntry `json:"entryentity"`
}

// Enabled 判断人员是否启用。
func (p *Personnel) Enabled() bool {
	return p.Enable == "1"
}

// Active 是「在职」口径：启用、未被禁用、且没有离职日期。
//
// 三个字段会互相矛盾（实测 5038 人里 isforbidden=129、有离职日=189），
// 所以口径必须写在一处，避免各调用点各判一套。
func (p *Personnel) Active() bool {
	return p.Enabled() && !p.IsForbidden && p.DisableDate == ""
}

// orgPage 是部门/人员接口的分页响应体。
// Filter 是服务端生效的过滤条件回显——请求体覆盖不了它，只能观测。
type orgPage[T any] struct {
	Rows       []T    `json:"rows"`
	LastPage   bool   `json:"lastPage"`
	PageNo     int    `json:"pageNo"`
	PageSize   int    `json:"pageSize"`
	TotalCount int64  `json:"totalCount"`
	Filter     string `json:"filter"`
}

// orgReq 是部门/人员接口的请求体。
// Data 必须存在（空对象即可），否则报「请求参数没有 data 数据」。
type orgReq struct {
	Data     map[string]any `json:"data"`
	PageNo   int            `json:"pageNo"`
	PageSize int            `json:"pageSize"`
}

// QueryDepartments 拉取部门（行政组织）全量。
// 返回的第二个值是服务端生效的过滤条件，用于诊断与告警。
func (c *Client) QueryDepartments(ctx context.Context) ([]Department, string, error) {
	if c.cfg.DepartmentQueryPath == "" {
		return nil, "", errors.New("kingdee department_query_path is not configured")
	}
	return queryOrgAll[Department](ctx, c, c.cfg.DepartmentQueryPath, "部门")
}

// QueryPersonnel 拉取人员全量。返回的第二个值是服务端生效的过滤条件。
func (c *Client) QueryPersonnel(ctx context.Context) ([]Personnel, string, error) {
	if c.cfg.PersonnelQueryPath == "" {
		return nil, "", errors.New("kingdee personnel_query_path is not configured")
	}
	return queryOrgAll[Personnel](ctx, c, c.cfg.PersonnelQueryPath, "人员")
}

// queryOrgAll 分页拉全量。返回 rows 与服务端生效的过滤条件。
func queryOrgAll[T any](ctx context.Context, c *Client, path, label string) ([]T, string, error) {
	pageSize := c.cfg.PageSize
	if pageSize <= 0 {
		// 部门接口的 pageSize 必填，缺了直接报错，这里兜一个合理值
		pageSize = 200
	}

	var out []T
	var filter string
	pageNo := 1
	for {
		req := orgReq{Data: map[string]any{}, PageNo: pageNo, PageSize: pageSize}
		var resp apiResponse[orgPage[T]]
		if err := c.postJSON(ctx, joinURL(c.cfg.BaseURL, path), req, &resp); err != nil {
			return nil, filter, err
		}
		// errorCode 成功时是字符串 "0"，不是空串——按空串判会把成功当失败
		if !resp.Status || (resp.ErrorCode != "" && resp.ErrorCode != "0") {
			return nil, filter, fmt.Errorf("kingdee %s api error: code=%s message=%s",
				label, resp.ErrorCode, resp.Message)
		}
		if pageNo == 1 {
			filter = resp.Data.Filter
		}
		out = append(out, resp.Data.Rows...)

		// 空页也要当结束：lastPage 偶尔不可靠时，空页是更硬的信号
		if resp.Data.LastPage || len(resp.Data.Rows) == 0 {
			break
		}
		pageNo++
		if pageNo > 1000 {
			return nil, filter, fmt.Errorf("%s 分页超过安全上限，可能陷入死循环", label)
		}
	}
	return out, filter, nil
}
