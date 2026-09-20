package model

import "time"

// DocTypeRepair 是 doc_status_log 的判别式：单据类型。doc_status_log 建成通用表，
// 后续的调拨单 / 领用单复用同一张表，只换这个值（见架构建议 D2）。
const DocTypeRepair = "repair"

// 维修单状态码。**存储用英文码**（对齐 count_plan.status = draft/counting/done/cancelled），
// **展示用中文白话**（对齐 count_item.result）。中文标签见 RepairStatusLabels。
//
// approving / scrapping 属 P1/P2，P0 不产生这两个跃迁，但码位从 P0 就定义，
// 避免 P1 启用时改词表导致前端映射返工。
const (
	RepairPending    = "pending"    // 待受理
	RepairAccepted   = "accepted"   // 已受理
	RepairApproving  = "approving"  // 待审批（P1）
	RepairDispatched = "dispatched" // 已派工
	RepairRepairing  = "repairing"  // 维修中 —— 唯一会写 asset_card.biz_status='维修中' 的状态
	RepairConfirming = "confirming" // 待确认
	RepairDone       = "done"       // 已完工（终态）
	RepairRejected   = "rejected"   // 已驳回（终态）
	RepairCancelled  = "cancelled"  // 已撤单（终态）
	RepairScrapping  = "scrapping"  // 报废评估（P1/P2）
)

// RepairStatuses 是全部状态码，顺序即业务推进顺序（供前端下拉/统计使用）。
var RepairStatuses = []string{
	RepairPending, RepairAccepted, RepairApproving, RepairDispatched, RepairRepairing,
	RepairConfirming, RepairDone, RepairRejected, RepairCancelled, RepairScrapping,
}

// RepairTerminalStatuses 是三个终态：已完工 / 已驳回 / 已撤单。
// 「我的报修」「我的维修」这类在途计数一律 NOT IN 它——已结束的单不该再挂在员工首页。
// 加新终态时改这里，而不是在各处散落写死状态列表。
var RepairTerminalStatuses = []string{RepairDone, RepairRejected, RepairCancelled}

// RepairStatusLabels 状态码 → 业务白话。前端展示一律走它，不暴露内部码。
var RepairStatusLabels = map[string]string{
	RepairPending:    "待受理",
	RepairAccepted:   "已受理",
	RepairApproving:  "待审批",
	RepairDispatched: "已派工",
	RepairRepairing:  "维修中",
	RepairConfirming: "待确认",
	RepairDone:       "已完工",
	RepairRejected:   "已驳回",
	RepairCancelled:  "已撤单",
	RepairScrapping:  "报废评估",
}

// RepairStatusLabel 取中文白话；未知状态原样返回，便于排障时看出库里的怪值。
func RepairStatusLabel(code string) string {
	if l, ok := RepairStatusLabels[code]; ok {
		return l
	}
	return code
}

// RepairDoneStatuses 是终态集合：进入这些状态后单据不再流转。
var RepairDoneStatuses = []string{RepairDone, RepairRejected, RepairCancelled}

// 维修单动作码。写进 doc_status_log.action，供时间线展示。
const (
	RepairActionSubmit   = "submit"   // 提交报修
	RepairActionAccept   = "accept"   // 受理
	RepairActionReject   = "reject"   // 驳回
	RepairActionDispatch = "dispatch" // 派工
	RepairActionTake     = "take"     // 接单
	RepairActionFinish   = "finish"   // 报完工
	RepairActionConfirm  = "confirm"  // 确认完工
	RepairActionReturn   = "return"   // 退回
	RepairActionCancel   = "cancel"   // 撤单 / 取消
	RepairActionApprove  = "approve"  // 审批通过（P1）
	RepairActionScrap    = "scrap"    // 转报废评估（P1/P2）
)

// RepairActionLabels 动作码 → 业务白话。
var RepairActionLabels = map[string]string{
	RepairActionSubmit:   "提交报修",
	RepairActionAccept:   "受理",
	RepairActionReject:   "驳回",
	RepairActionDispatch: "派工",
	RepairActionTake:     "接单",
	RepairActionFinish:   "报完工",
	RepairActionConfirm:  "确认完工",
	RepairActionReturn:   "退回",
	RepairActionCancel:   "撤单",
	RepairActionApprove:  "审批通过",
	RepairActionScrap:    "转报废评估",
}

// RepairActionLabel 取中文白话；未知动作原样返回。
func RepairActionLabel(code string) string {
	if l, ok := RepairActionLabels[code]; ok {
		return l
	}
	return code
}

// 紧急程度。存储用英文码，展示用中文。
const (
	RepairUrgencyLow    = "low"
	RepairUrgencyNormal = "normal"
	RepairUrgencyHigh   = "high"
)

// RepairUrgencies 全部紧急程度码，顺序即展示顺序。
var RepairUrgencies = []string{RepairUrgencyLow, RepairUrgencyNormal, RepairUrgencyHigh}

// RepairUrgencyLabels 紧急程度码 → 中文。
var RepairUrgencyLabels = map[string]string{
	RepairUrgencyLow:    "低",
	RepairUrgencyNormal: "一般",
	RepairUrgencyHigh:   "紧急",
}

// RepairUrgencyLabel 取中文；未知码原样返回。
func RepairUrgencyLabel(code string) string {
	if l, ok := RepairUrgencyLabels[code]; ok {
		return l
	}
	return code
}

// IsValidRepairUrgency 校验紧急程度码是否合法。
func IsValidRepairUrgency(v string) bool {
	for _, u := range RepairUrgencies {
		if u == v {
			return true
		}
	}
	return false
}

// RepairOrder 维修单主表（repair_order）的领域模型。
//
// 关联约定：外键一律 BIGINT，0 = 「未设置」哨兵值；单据靠终态归档，无 deleted_at。
// asset_code / asset_name / use_dept_name / location 是提交时的**快照**，
// 主数据仍是引用（参照 count_item 的做法），事后资产变更不影响已生成的单据。
type RepairOrder struct {
	ID   int64  `json:"id"`
	Code string `json:"code"` // WX<yyyymmdd><0001>，先插后回填

	CardID    int64  `json:"card_id"`
	AssetCode string `json:"asset_code"`
	AssetName string `json:"asset_name"`

	ReporterEmpID int64  `json:"reporter_emp_id"`
	ReporterName  string `json:"reporter_name"`
	UseDeptID     int64  `json:"use_dept_id"`
	UseDeptName   string `json:"use_dept_name"`
	Location      string `json:"location"`

	FaultDesc string `json:"fault_desc"`
	Urgency   string `json:"urgency"`
	Status    string `json:"status"`
	// StatusLabel 是派生字段（不落库）：status 的中文白话，供前端直接展示。
	StatusLabel string `json:"status_label"`

	AssigneeEmpID int64  `json:"assignee_emp_id"`
	AssigneeName  string `json:"assignee_name"`
	VendorID      int64  `json:"vendor_id"`
	VendorName    string `json:"vendor_name"`

	HandlerDesc  string `json:"handler_desc"`
	RejectReason string `json:"reject_reason"`

	// Cost / CostDeptID 是 P1-4 费用登记字段。P0 恒 0，但列从 P0 就存在（D4）：
	// 一单对应一费用，独立 repair_cost 表是 1:1 冗余；生产库上后补列比预留两列风险大。
	Cost       float64 `json:"cost"`
	CostDeptID int64   `json:"cost_dept_id"`

	AcceptBy string `json:"accept_by"` // 受理人（审计）

	AcceptAt  *time.Time `json:"accept_at,omitempty"`
	AssignAt  *time.Time `json:"assign_at,omitempty"`
	StartAt   *time.Time `json:"start_at,omitempty"`   // 进入维修中
	FinishAt  *time.Time `json:"finish_at,omitempty"`  // 进入待确认
	ConfirmAt *time.Time `json:"confirm_at,omitempty"` // 已完工
	CloseAt   *time.Time `json:"close_at,omitempty"`   // 终态（驳回/撤单/报废）

	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RepairAttachment 维修附件（repair_attachment）。归属语义与资产档案照（asset_attachment）不同：
// 按 repair_id 精确归属到某一张维修单，不混进资产卡的照片墙。
// 存储层复用 uploads/ 目录与随机名规则（见 api/upload.go）。
type RepairAttachment struct {
	ID       int64  `json:"id"`
	RepairID int64  `json:"repair_id"` // 0 = 先上传待绑定（对齐 asset_attachment 的 card_id=0 约定）
	CardID   int64  `json:"card_id"`   // 冗余，便于按卡查维修照
	Kind     string `json:"kind"`      // photo / invoice / file
	OriginName string `json:"origin_name"`
	// URL 是派生字段（不落库）：/uploads/<随机名>，供前端直接展示。
	URL        string `json:"url"`
	SizeBytes  int64  `json:"size_bytes"`
	UploadedBy string `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// DocStatusLog 是通用单据状态流转记录（doc_status_log，带 doc_type 判别式）。
// 纯 append-only 审计表、零业务逻辑；当前只有 doc_type='repair'，后续单据流复用。
type DocStatusLog struct {
	ID         int64  `json:"id"`
	DocType    string `json:"doc_type"`
	DocID      int64  `json:"doc_id"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	// FromLabel / ToLabel 是派生字段（不落库）：状态码的中文白话。
	FromLabel   string    `json:"from_label"`
	ToLabel     string    `json:"to_label"`
	Action      string    `json:"action"`
	ActionLabel string    `json:"action_label"` // 派生
	Operator    string    `json:"operator"`
	Remark      string    `json:"remark"`
	CreatedAt   time.Time `json:"created_at"`
}

// RepairListQuery 维修单列表查询条件。所有筛选字段都经白名单映射，不直接拼进 SQL。
type RepairListQuery struct {
	Status    []string // 多选状态（英文码）
	UseDeptID int64
	CardID    int64
	AssetCode string
	Keyword   string // 单号 / 资产编码 / 资产名称 / 故障描述 / 报修人
	From      string // created_at 下界（含），格式 "2006-01-02 15:04:05"
	To        string // created_at 上界（含）
	Page      int
	PageSize  int
}

// RepairListResult 维修单列表结果。
type RepairListResult struct {
	Items    []RepairOrder `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// RepairScope 描述「这个账号能看哪些维修单」，零值 = 不限制。
// 与 AssetScope 一样只是数据，由 api 层按角色解析出来，store 只认数字不认角色。
//
// 三个字段对应三种收窄口径，命中其一即可见（见 RepairScope.Allows）：
//   - ReporterEmpID：报修人（员工看自己提交的单）
//   - AssigneeEmpID：被指派的维修工（维修工看派给自己的单）
//   - DeptIDs：使用部门落在这些部门里的单（部门主管看本部门及下级）
type RepairScope struct {
	ReporterEmpID int64
	AssigneeEmpID int64
	DeptIDs       []int64
}

// Restricted 是否有限制（零值表示不限）。
func (s RepairScope) Restricted() bool {
	return s.ReporterEmpID > 0 || s.AssigneeEmpID > 0 || len(s.DeptIDs) > 0
}

// Allows 判断单张维修单是否在范围内。列表侧走 SQL，这里供点查（详情/日志/附件）使用。
func (s RepairScope) Allows(o *RepairOrder) bool {
	if !s.Restricted() {
		return true
	}
	if s.ReporterEmpID > 0 && o.ReporterEmpID == s.ReporterEmpID {
		return true
	}
	if s.AssigneeEmpID > 0 && o.AssigneeEmpID == s.AssigneeEmpID {
		return true
	}
	for _, id := range s.DeptIDs {
		if id == o.UseDeptID {
			return true
		}
	}
	return false
}
