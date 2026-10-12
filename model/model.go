package model

import (
	"strings"
	"time"
)

// 资产状态枚举，与参考系统保持一致
const (
	StatusIdle     = "闲置"
	StatusInUse    = "在用"
	StatusBorrowed = "借用"
	StatusRepair   = "维修中"
	StatusTransfer = "调拨中"
	StatusScrapped = "报废"
)

var AssetStatuses = []string{StatusIdle, StatusInUse, StatusBorrowed, StatusRepair, StatusTransfer, StatusScrapped}

// 来源枚举
var AssetSources = []string{"购入", "自建", "捐赠", "调入", "租入", "其他"}

// SourceKingdeeSync 是同步写入的来源标记。它**故意不在 AssetSources 里**：
// 这是系统写的值，不该出现在人工可选的来源下拉里。但校验必须放行它——
// 否则同步卡在表单里改任何字段都会被「来源取值非法」挡回去。
const SourceKingdeeSync = "金蝶同步"

// IsValidSource 判断来源是否合法：人工可选的枚举 + 系统写入的同步标记
func IsValidSource(s string) bool {
	return contains(AssetSources, s) || s == SourceKingdeeSync
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// 财务资产类型
var FinAssetTypes = []string{"固定资产", "低值易耗品", "无形资产"}

// 财务信息状态
var FinStatuses = []string{"未入账", "已入账", "已处置"}

type AssetCard struct {
	ID        int64  `json:"id"`
	AssetCode string `json:"asset_code"`
	Name      string `json:"name"`

	CategoryID   int64  `json:"category_id"`
	CategoryName string `json:"category_name,omitempty"`

	Spec     string `json:"spec"`
	SerialNo string `json:"serial_no"`
	Unit     string `json:"unit"`
	Status   string `json:"status"`
	// BizStatus 是**台账本地维护的业务状态**（维修中 / 未来的调拨中 / 领用中…），
	// 与星瀚托管的生命周期状态 Status 物理分离。存在的唯一理由：Status 是星瀚托管列，
	// 每天 00:00 的定时同步会把本地置的「维修中」重置回「在用」（见 docs/维修流程模块架构建议.md §1）。
	//
	// 硬约束：BizStatus **只有一个写入者**——维修单据状态机，且在单据事务内写；
	// 绝不加入 kingdeeOwnedColumns / cardInitColumns / cardWriteColumns 任一清单，
	// 也不出现在卡片编辑表单里（用户永远不能手填「维修中」）。
	BizStatus string `json:"biz_status"`
	// DisplayStatus 是**派生字段，不落库**：有效状态 = biz_status 非空 ? biz_status : status。
	// 所有展示 / 筛选 / 导出只认它，优先级只有这一处实现（见 §1.5）。
	DisplayStatus string  `json:"display_status"`
	Amount        float64 `json:"amount"`
	// Quantity 是金蝶的 assetamount：带计量单位的数量——房屋按平方米、设备按台/辆。
	// 与 Amount（台账自填的金额）完全是两回事。
	// 金蝶同步来的卡由同步托管（星瀚 >0 时覆盖，见 store.mergeOwnedFields）；
	// 手工新建的卡不在金蝶里，可以自己填。金蝶返回 10 位小数（如 194.5200000000），
	// 库里按 DECIMAL(18,4) 存。
	Quantity float64 `json:"quantity"`

	// Synced 是**派生字段，不落库**：表示这张卡是否由外部系统（金蝶）同步托管，
	// 由 api 层按 external_asset_map 是否有关联记录算出来，只用于前端判断
	// 「数量」这类托管字段能不能编辑。不要在 cardWriteColumns 里加它。
	Synced bool `json:"synced,omitempty"`

	// CategoryRepairable 是**派生字段，不落库**：所属分类是否允许提交维修单。
	// 由 cardSelect 的 COALESCE(cat.repairable, 0) 带出（LEFT JOIN 未匹配或
	// category_id 为哨兵值 0 时为 0 = 不可维修），故不进入任何写列清单。
	// 刻意不用 omitempty：false 也必须序列化到前端，前端靠它决定是否隐藏报修按钮。
	CategoryRepairable bool `json:"category_repairable"`

	UseCompanyID   int64  `json:"use_company_id"`
	UseCompanyName string `json:"use_company_name,omitempty"`
	UseDeptID      int64  `json:"use_dept_id"`
	UseDeptName    string `json:"use_dept_name,omitempty"`
	UserEmpID      int64  `json:"user_emp_id"`
	UserEmpName    string `json:"user_emp_name,omitempty"`
	// UseStatus 是金蝶的「使用状态」（usestatus_name），与本地维护的 Status 生命周期状态无关
	UseStatus string `json:"use_status"`

	ManagerEmpID   int64  `json:"manager_emp_id"`
	ManagerEmpName string `json:"manager_emp_name,omitempty"`

	OwnerCompanyID   int64  `json:"owner_company_id"`
	OwnerCompanyName string `json:"owner_company_name,omitempty"`
	AreaID           int64  `json:"area_id"`
	AreaName         string `json:"area_name,omitempty"`
	Location         string `json:"location"`

	PurchaseDate string `json:"purchase_date"`
	// CardCreatedAt 是金蝶的建卡时间（createtime），只读展示，不在台账里编辑
	CardCreatedAt string `json:"card_created_at"`
	UseMonths     int    `json:"use_months"`
	Source        string `json:"source"`
	InStockNo     string `json:"in_stock_no"`
	RFID          string `json:"rfid"`
	Remark        string `json:"remark"`

	FinAssetType        string  `json:"fin_asset_type"`
	FinShareDeptID      int64   `json:"fin_share_dept_id"`
	FinShareDeptName    string  `json:"fin_share_dept_name,omitempty"`
	VendorID            int64   `json:"vendor_id"`
	VendorName          string  `json:"vendor_name,omitempty"`
	FinAmountWithTax    float64 `json:"fin_amount_with_tax"`
	FinTax              float64 `json:"fin_tax"`
	FinOriginalValue    float64 `json:"fin_original_value"`
	FinNetValue         float64 `json:"fin_net_value"`
	FinAccumDepreciaton float64 `json:"fin_accum_depreciation"`
	FinResidualRate     float64 `json:"fin_residual_rate"`
	FinUseMonths        int     `json:"fin_use_months"`
	FinPeriod           string  `json:"fin_period"`
	FinEntryDate        string  `json:"fin_entry_date"`
	FinStatus           string  `json:"fin_status"`

	// FinEntryPresent 表示这次同步的金蝶财务明细子表里**确实带了**财务信息。
	// 不是数据库列，只服务于同步合并，用来区分「星瀚给了值」和「星瀚没给」——
	// 光看值看不出来：mapCard 会用类别默认值把使用期限/残值率填上，
	// 于是「没给」的那份重复行带着 240 期 / 5% 的默认值，正好骗过「大于 0 才覆盖」的闸门，
	// 把前一行刚同步下来的真实值（如 107 期 / 3%）又盖回去。
	FinEntryPresent bool `json:"-"`

	MtVendorID     int64  `json:"mt_vendor_id"`
	MtVendorName   string `json:"mt_vendor_name,omitempty"`
	MtContact      string `json:"mt_contact"`
	MtPhone        string `json:"mt_phone"`
	MtOwnerEmpID   int64  `json:"mt_owner_emp_id"`
	MtOwnerEmpName string `json:"mt_owner_emp_name,omitempty"`
	MtExpireDate   string `json:"mt_expire_date"`
	MtRemark       string `json:"mt_remark"`

	TagIDs []int64  `json:"tag_ids,omitempty"`
	Tags   []string `json:"tags,omitempty"`

	// AttachmentIDs 只在提交时使用：照片/附件先上传拿到 id，保存卡片时回填 card_id
	AttachmentIDs []int64 `json:"attachment_ids,omitempty"`

	ExtJSON string `json:"ext_json,omitempty"`

	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FinanceUpdate 是「批量更新财务信息」里的一行输入。
//
// 可选字段一律用指针，用来区分「这一列要改」和「这一列没填」：
// 主用法是把导出的整表改完再导回，文件里没填的列绝不能被当成 0 把库里已有值抹掉。
//
// 覆盖范围是「财务信息里的金额与数值字段」：含税金额、税额、原值、累计折旧、
// 残值率、财务使用期限。入账期间/入账时间/财务信息状态不在内（不是金额，且批量改状态
// 需要单独的确认语义）。数量也不在内：它由星瀚同步托管，手工改了下次同步就没了。
//
// 净值不在这里 —— 它是 原值 − 累计折旧 的派生值，由 store 层统一重算，
// 否则三个数各改各的迟早对不上（这正是本需求要解决的问题）。
type FinanceUpdate struct {
	AssetCode         string
	AmountWithTax     *float64
	Tax               *float64
	OriginalValue     *float64
	AccumDepreciation *float64
	ResidualRate      *float64
	FinUseMonths      *int
}

// ListQuery 列表查询条件。Sort 与筛选字段都经过白名单映射，不直接拼进 SQL。
type ListQuery struct {
	Keyword    string
	Status     []string
	CategoryID int64
	// Repairable 按「分类是否允许报修（asset_category.repairable）」筛选，0 值表示不筛选。
	// 用指针而不是 bool：bool 的零值 false 无法区分「不筛选」和「只要不可维修」，
	// 而「只要不可维修」是真实存在的查询（例如导出不可报修清单）。
	// nil = 不筛选；指向 true = 只要可维修的；指向 false = 只要不可维修的。
	// 条件在 store 层写成 COALESCE(cat.repairable, 0) = ?，与列表返回的
	// AssetCard.CategoryRepairable 同源同口径，避免出现「筛出来但显示的标志不一致」。
	Repairable   *bool
	AreaID       int64
	UseDeptID    int64
	UseCompanyID int64
	Source       string
	FinStatus    string
	AssetCode    string
	Name         string
	SerialNo     string
	ManagerName  string
	UserName     string
	Location     string
	PurchaseFrom string
	PurchaseTo   string
	AmountMin    *float64
	AmountMax    *float64
	SortBy       string
	SortDesc     bool
	Page         int
	PageSize     int
}

type ListResult struct {
	Items       []AssetCard `json:"items"`
	Total       int64       `json:"total"`
	AmountTotal float64     `json:"amount_total"`
	Page        int         `json:"page"`
	PageSize    int         `json:"page_size"`
}

// TreeNode 供分类与区域两棵树复用
type TreeNode struct {
	ID           int64   `json:"id"`
	ParentID     int64   `json:"parent_id"`
	Name         string  `json:"name"`
	Code         string  `json:"code"`
	ShortName    string  `json:"short_name,omitempty"`
	UseMonths    int     `json:"use_months,omitempty"`
	ResidualRate float64 `json:"residual_rate,omitempty"`
	SortIndex    int     `json:"sort_index"`
	// Repairable 仅对分类（asset_category）有意义：该分类下的资产是否允许提交维修单。
	// 它是**台账本地列**——同步链路对分类只有 INSERT、没有 UPDATE，故不会被每日同步抹掉
	// （依据见 docs/增量架构-可维修标签与首页.md §1.1，由 store/category_guard_test.go 钉死）。
	// asset_area 复用本结构但不使用该字段，omitempty 让区域 JSON 与从前逐字节一致。
	Repairable bool        `json:"repairable,omitempty"`
	Children   []*TreeNode `json:"children,omitempty"`
}

type Company struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	TaxNo     string `json:"tax_no"`
	SortIndex int    `json:"sort_index"`
	// Source 标记数据来源：kingdee 表示由星瀚同步托管，空串表示本地手工维护。
	// 同步只覆盖 kingdee 的行，手工建的行保留本地值。
	Source string `json:"source,omitempty"`
}

type Department struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	ParentID    int64  `json:"parent_id"`
	CompanyID   int64  `json:"company_id"`
	CompanyName string `json:"company_name,omitempty"`
	SortIndex   int    `json:"sort_index"`
	// LongNumber 是星瀚侧的组织路径（如 GCGS!GCGSHB!12!1201!120108）。
	// 保留它是为了让「这个部门为什么挂到这家公司」在库里能自证：
	// 归属判定完全靠它，出问题时不用再去翻接口。
	LongNumber string `json:"longnumber,omitempty"`
	Level      int    `json:"level,omitempty"`
	Enabled    bool   `json:"enabled"`
	Source     string `json:"source,omitempty"`
}

type Employee struct {
	ID          int64  `json:"id"`
	EmpNo       string `json:"emp_no"`
	Name        string `json:"name"`
	DeptID      int64  `json:"dept_id"`
	DeptName    string `json:"dept_name,omitempty"`
	CompanyID   int64  `json:"company_id"`
	CompanyName string `json:"company_name,omitempty"`
	Phone       string `json:"phone"`
	Active      bool   `json:"active"`
	Source      string `json:"source,omitempty"`
}

type Vendor struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Contact string `json:"contact"`
	Phone   string `json:"phone"`
	Remark  string `json:"remark"`
}

type Tag struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type HistoryEntry struct {
	ID        int64     `json:"id"`
	CardID    int64     `json:"card_id"`
	Action    string    `json:"action"`
	Field     string    `json:"field"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	Operator  string    `json:"operator"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	RealName string `json:"real_name"`
	// Role 是**主角色**（= Roles[0]），保留两个用途：① 旧前端 / 旧 token 仍读它；
	// ② 回滚时把二进制换回旧版本，旧版本读到的仍是主角色，不会变成「无权限」。
	// ⚠️ 判断权限请用 CanAny(u.EffectiveRoles(), perm)，判断角色请用 HasRole，
	// 不要直接比 u.Role —— 多角色账号下 u.Role 只是角色集合里的第一个。
	Role string `json:"role"`
	// Roles 是账号的**全部角色**（一人可多角色，2026-09-24 需求）。
	// 空 slice 表示「旧数据 / 未迁移」，此时 EffectiveRoles() 回落到 []string{Role}，
	// 因此存量行不迁移也能正确工作。
	Roles []string `json:"roles,omitempty"`
	// EmployeeID 把登录账号绑定到员工主数据：盘点模块据此判断「谁是盘点人」，
	// 「员工」账号也靠它确定「我名下的资产」
	EmployeeID   int64  `json:"employee_id"`
	EmployeeName string `json:"employee_name,omitempty"`
	// DeptID 是「部门负责人」账号管辖的部门。不走 employee.dept_id——
	// 员工主数据里这一列基本是空的，推不出部门。
	DeptID   int64  `json:"dept_id"`
	DeptName string `json:"dept_name,omitempty"`
	Enabled  bool   `json:"enabled"`
}

const (
	RoleAdmin        = "admin"
	RoleAssetManager = "asset_manager"
	RoleCounter      = "counter"
	// RoleDeptHead 部门负责人：只看本部门（含下级）资产，只读
	RoleDeptHead = "dept_head"
	// RoleMaintainer 维护人员：看指派给自己的维修单 + 自己提交的维修单，接单 / 报完工。
	// 「自己提交的」这一维是为角色变更兜底：账号被从 employee 改成 maintainer 后，
	// 那些以员工身份报的单不该从他眼前消失（见 api/scope.go 的 repairScope）。
	RoleMaintainer = "maintainer"
	// RoleEmployee 员工：看自己名下的资产 + 自己提交的维修单，可报修。
	RoleEmployee = "employee"
	// RoleITAdmin IT 管理员：维护 IT 信息设备台账（新增 2026-10-10 需求）。
	//
	// ⚠️ 它的**权限**只有 it.manage 一个；但它的**可见范围**是「不限」——
	// 在 assetScope / repairScope 里都走与 asset_manager 同等的分支，在 itScope 里也不限。
	// 这是**有意为之、且经用户确认**的取舍：IT 管理员要能检索全库资产才能挑出设备做关联，
	// 也应当能看到全公司所有 IT 设备。代价是它同时能看到全量资产台账（含金额与财务字段）。
	//
	// ⚠️⚠️ **新增角色必须显式登记到每一个按角色分支的地方**，否则会静默落进
	// `default` 分支 = 「不限范围」= 看到全量数据（api/scope.go 的 assetScope / repairScope
	// 都是这个结构）。本项目已有的登记点见 docs/IT设备台账-需求与方案.md 的权限矩阵。
	RoleITAdmin = "it_admin"
)

// Roles 供前端下拉展示，顺序即展示顺序。
// 这个顺序同时是 NormalizeRoles 的归一化顺序，也是 roles 列里逗号分隔的书写顺序。
//
// ⚠️ 新增角色一律**追加到末尾**：NormalizeRoles 按本切片顺序重排 roles 列，
// 插在中间会让所有存量账号的 roles 文本在下次保存时被重写（虽不改变语义，但会让
// diff 与按角色名的排查凭空多出一批无意义的变更）。
var Roles = []string{RoleAdmin, RoleAssetManager, RoleCounter, RoleDeptHead, RoleMaintainer, RoleEmployee, RoleITAdmin}

// 权限名：前后端共用同一套字符串（前端 stores/auth.ts 有一份等价的角色映射）
const (
	PermAssetManage  = "asset.manage"
	PermMasterManage = "master.manage"
	PermSyncManage   = "sync.manage"
	PermCountManage  = "count.manage"
	PermCountEnter   = "count.enter"
	PermUserManage   = "user.manage"

	// —— 维修流程权限位（P0 即定义，repair.approve / repair.manage 属 P1 用）——
	// PermRepairReport：提交报修 + 对自己单据的确认 / 退回 / 撤单。
	//   ⚠️ 它同时授予 employee，是**有意为之**：打破「员工纯只读」的既有不变量。
	//   报修正是「服务到每一位员工」的核心动作，且报修天然自收窄（只看得到自己提交的单）。
	PermRepairReport   = "repair.report"
	PermRepairDispatch = "repair.dispatch"
	PermRepairHandle   = "repair.handle"
	PermRepairApprove  = "repair.approve"
	PermRepairManage   = "repair.manage"

	// —— IT 信息设备台账权限位（2026-10-10 需求）——
	// PermITManage：维护 IT 台账（新增 / 编辑 / 删除 / 导入）。当前只有 it_admin 持有。
	//   ⚠️ 它**不**授予资产台账的写权限（asset.manage）：IT 管理员看得到资产台账，
	//   但不该能改资产卡上的数据——那是资产管理员的事。读与写在这里是分开的。
	PermITManage = "it.manage"
)

// rolePermissions 固定角色的权限表。admin 在 Can 里兜底放行，不必列出；
// 表里没有的角色（含未知/拼错的值）一律按无权限处理。
//
// ⚠️ 这份表与前端 web/src/stores/auth.ts 的 rolePermissions 必须**逐项一致**，
// 否则会出现「按钮可见但点击 403」或「后端允许但界面没入口」（已发生过一次真实 bug）。
var rolePermissions = map[string]map[string]bool{
	RoleAssetManager: {
		PermAssetManage:  true,
		PermMasterManage: true,
		PermCountManage:  true,
		PermCountEnter:   true,
		// 维修：管理员受理 / 驳回 / 派工 / 取消，并登记费用
		PermRepairReport:   true,
		PermRepairDispatch: true,
		PermRepairApprove:  true,
		PermRepairManage:   true,
	},
	RoleCounter: {
		PermCountEnter:   true,
		PermRepairReport: true,
	},
	// dept_head 对台账是纯只读；维修侧可报修、可审批（P1）。
	RoleDeptHead: {
		PermRepairReport:  true,
		PermRepairApprove: true,
	},
	// 维护人员：只负责接单 / 报完工
	RoleMaintainer: {
		PermRepairHandle: true,
	},
	// employee 唯一的写权限是 repair.report（有意为之，见 PermRepairReport 注释）
	RoleEmployee: {
		PermRepairReport: true,
	},
	// IT 管理员：只有 it.manage。**刻意不给 asset.manage**——
	// 他需要「看」全量资产台账来找设备做关联（那是可见范围，不是权限位），
	// 但不该能改资产卡。读与写分开，是这里唯一的判断依据。
	RoleITAdmin: {
		PermITManage: true,
	},
}

func Can(role, perm string) bool {
	if role == RoleAdmin {
		return true
	}
	return rolePermissions[role][perm]
}

// —— 多角色（一人可多角色）支持 ——
//
// 语义约定（api 层与前端必须一致）：
//   - 权限 = **并集**：任一角色持有该权限即放行（CanAny）。
//   - 数据可见范围 = **并集**：任一角色允许看见的行都算可见（api/scope.go 里逐角色算完再合并）。
//     admin / asset_manager 这类「不限」的角色会把整个范围放大成全量，这是有意的：
//     并集语义下「不限」就是全集，不给它开特例，否则「A 角色不限、B 角色受限」的行为无法解释。
//   - 角色列表为空（存量数据）时一律回落到 []string{Role}，见 EffectiveRoles。

// LegacyRoleRenames 是已废弃角色名 → 现行角色名的映射。
//
// 存在的唯一理由：改名（viewer→employee、repair_tech→maintainer）之后，**存量库里的行仍带旧名**，
// 而旧名已不在 Roles 里。若不映射，NormalizeRoles 会把旧名当非法值**过滤掉** ⇒ EffectiveRoles()
// 变成空集合 ⇒ 在 api/scope.go 里零值 scope 被当成「不限范围」，即**读到全量资产 / 全量维修单**。
// 这不是假设：生产库有 3 个 role='viewer' 的账号，2026-09-24 用真实数据模拟迁移时实测到
// `assetScope Restricted=false`（看全部）。所以映射必须做，且数据侧也要就地改名
// （见 store/mysql.go 的 migrateLegacyRoleNames）。
var LegacyRoleRenames = map[string]string{
	"viewer":      RoleEmployee,
	"repair_tech": RoleMaintainer,
}

// NormalizeRoleName 把（可能是废弃名的）单个角色名映射成现行名；无法识别时返回空串。
func NormalizeRoleName(name string) string {
	name = strings.TrimSpace(name)
	if renamed, ok := LegacyRoleRenames[name]; ok {
		return renamed
	}
	if isKnownRole(name) {
		return name
	}
	return ""
}

// NormalizeRoles 归一化角色集合：**先映射废弃名**，再去空白、去重、剔除非法值，
// 并按 Roles 的声明顺序排序。
// 排序是为了让「同一组角色」在任何写入路径下都得到同一份字符串，
// 否则 roles 列的文本会随勾选顺序抖动，diff 与去重都会变得不可靠。
func NormalizeRoles(in []string) []string {
	set := map[string]bool{}
	for _, r := range in {
		if n := NormalizeRoleName(r); n != "" {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for _, r := range Roles { // 按声明顺序输出，保证稳定
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

func isKnownRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// ParseRoles 解析 sys_user.roles 列（逗号分隔）。空串返回 nil。
func ParseRoles(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return NormalizeRoles(strings.Split(s, ","))
}

// FormatRoles 把角色集合序列化成 roles 列的值：逗号分隔、无空格。
// ⚠️ 不能带空格：SQL 侧的 FIND_IN_SET 依赖「无空格」这一约定
// （见 api/notify_plan.go 的 assetManagerEmpIDs）。
func FormatRoles(roles []string) string {
	return strings.Join(NormalizeRoles(roles), ",")
}

// EffectiveRoles 返回账号实际生效的角色集合。Roles 为空（存量行 / 旧 token）时回落到 Role。
// 返回的 slice 保证非空且已归一化——除非 Role 本身也是空串（理论上不该出现）。
func (u *User) EffectiveRoles() []string {
	if len(u.Roles) > 0 {
		if n := NormalizeRoles(u.Roles); len(n) > 0 {
			return n
		}
	}
	if strings.TrimSpace(u.Role) == "" {
		return nil
	}
	return NormalizeRoles([]string{u.Role})
}

// HasRole 判断角色集合里是否含某个角色。
func HasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// CanAny 多角色版权限判定：任一角色持有该权限即放行。
// admin 仍然恒真（与 Can 一致，admin 在 rolePermissions 里不列）。
func CanAny(roles []string, perm string) bool {
	for _, r := range roles {
		if Can(r, perm) {
			return true
		}
	}
	return false
}

type ExternalAssetMap struct {
	ID              int64      `json:"id"`
	Source          string     `json:"source"`
	ExternalID      string     `json:"external_id"`
	ExternalCode    string     `json:"external_code"`
	CardID          int64      `json:"card_id"`
	ExternalVersion string     `json:"external_version"`
	PayloadHash     string     `json:"payload_hash"`
	LastSyncRunID   int64      `json:"last_sync_run_id"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

type ExternalMasterMap struct {
	ID           int64     `json:"id"`
	Source       string    `json:"source"`
	Kind         string    `json:"kind"`
	ExternalID   string    `json:"external_id"`
	ExternalCode string    `json:"external_code"`
	ExternalName string    `json:"external_name"`
	LocalID      int64     `json:"local_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type SyncRun struct {
	ID           int64      `json:"id"`
	Source       string     `json:"source"`
	Resource     string     `json:"resource"`
	Mode         string     `json:"mode"`
	TriggeredBy  string     `json:"triggered_by"`
	CursorValue  string     `json:"cursor_value"`
	Status       string     `json:"status"`
	TotalCount   int        `json:"total_count"`
	CreatedCount int        `json:"created_count"`
	UpdatedCount int        `json:"updated_count"`
	SkippedCount int        `json:"skipped_count"`
	DeletedCount int        `json:"deleted_count"`
	FailedCount  int        `json:"failed_count"`
	ErrorSummary string     `json:"error_summary"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type SyncRunError struct {
	ID           int64     `json:"id"`
	RunID        int64     `json:"run_id"`
	ExternalID   string    `json:"external_id"`
	Stage        string    `json:"stage"`
	Field        string    `json:"field"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
	RawValue     string    `json:"raw_value"`
	Retryable    bool      `json:"retryable"`
	CreatedAt    time.Time `json:"created_at"`
}

type SyncState struct {
	ID            int64      `json:"id"`
	Source        string     `json:"source"`
	Resource      string     `json:"resource"`
	CursorValue   string     `json:"cursor_value"`
	LastRunID     int64      `json:"last_run_id"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// SyncMode 同步模式
const (
	SyncModeIncremental = "incremental"
	SyncModeFull        = "full"
)

// SyncStatus 运行状态
const (
	SyncStatusRunning = "running"
	SyncStatusSuccess = "success"
	SyncStatusFailed  = "failed"
	SyncStatusPartial = "partial"
)

// 同步资源类型。sync_run.resource 用它区分一次跑批同步的是什么——
// 资产卡和组织主数据的跑批混在一张列表里，不区分就看不出某次失败是同步什么。
const (
	ResourceAssetCard = "asset_card"
	ResourceOrg       = "org"
	// ResourceYzjOrg 是云之家通讯录增量同步（openId 回填路径 B）的资源标识。
	//
	// 它复用 sync_run / sync_state / sync_run_error，但**不属于**星瀚同步链路：
	// 其 source 为 "yunzhijia"（而非星瀚的 "kingdee"），两者的游标与失败记录彼此独立。
	// 见 docs/云之家待办通知集成方案.md §6.5。
	ResourceYzjOrg = "yzj_org"
)

// ExternalMapStatus 外部映射状态
const (
	ExternalMapStatusActive  = "active"
	ExternalMapStatusDeleted = "deleted"
)

// MasterKind 外部主数据类别
const (
	MasterKindCategory   = "category"
	MasterKindDepartment = "department"
	MasterKindArea       = "area"
	MasterKindEmployee   = "employee"
	MasterKindVendor     = "vendor"
	MasterKindCompany    = "company"
)

// 盘点计划状态
const (
	CountPlanDraft     = "draft"
	CountPlanCounting  = "counting"
	CountPlanDone      = "done"
	CountPlanCancelled = "cancelled"
)

// 盘点结果。空字符串表示「未盘点」。
const (
	CountResultNormal  = "在库"
	CountResultLoss    = "盘亏"
	CountResultGain    = "盘盈"
	CountResultDamaged = "损毁"
)

var CountResults = []string{CountResultNormal, CountResultLoss, CountResultGain, CountResultDamaged}

// CountScope 盘点范围，零值字段表示不限；全零即「全部资产」
type CountScope struct {
	UseDeptID    int64 `json:"use_dept_id"`
	CategoryID   int64 `json:"category_id"`
	AreaID       int64 `json:"area_id"`
	UseCompanyID int64 `json:"use_company_id"`
}

func (c CountScope) Empty() bool {
	return c.UseDeptID == 0 && c.CategoryID == 0 && c.AreaID == 0 && c.UseCompanyID == 0
}

// AssetScope 描述「这个账号能看哪些资产」，零值 = 不限制。
// 和 CountScope 一样只是数据，由 api 层按角色解析出来，store 只认数字不认角色。
// AssetScope 资产可见范围。零值表示不限。
//
// 多角色（一人可多角色，2026-09-24）：多个维度**同时非零**是合法状态（如 [部门负责人, 员工]），
// 此时按**并集**判定——命中任一维度即可见。列表侧的 SQL 由 store/card.go 的 scopeConds 同口径产出。
type AssetScope struct {
	SelfEmpID int64   // >0：可见「这个使用人名下」的资产
	DeptIDs   []int64 // 非空：可见「使用部门落在这些部门里」的资产

	// RepairingCardIDs 非空表示可见「当前有**未到终态**维修单的资产」。
	//
	// 「未到终态」= 状态 NOT IN (done, rejected, cancelled)，即待受理 / 已受理 / 待审批 /
	// 已派工 / 维修中 / 待确认 都算未结单（见 model.RepairTerminalStatuses）。
	//
	// ⚠️ **不限报修人**——这是**全公司**口径，不是「我报修的」。任何人的报修单，只要还没结单，
	// 对应的资产就进入可见范围。理由：员工报修入口（/api/assets?repairable=1、/api/assets/by-code、
	// 扫码）本身全部经过 assetScope 门控（api/labels.go → denyOutOfScope），员工根本没法对
	// 看不见的资产报修；故「只看我报修的」等价于现状、是个空改动，用户明确选了「所有未结单的资产」。
	//
	// ⚠️ 只由 employee 角色产生（见 api/scope.go 的 RoleEmployee 分支）。维护人员 / dept_head /
	// counter / admin / asset_manager 一律不变（维护人员维持「看全量台账」）。
	//
	// 与 SelfEmpID / DeptIDs 取**并集**：命中任一维度即可见。
	RepairingCardIDs []int64
}

func (s AssetScope) Restricted() bool {
	return s.SelfEmpID > 0 || len(s.DeptIDs) > 0 || len(s.RepairingCardIDs) > 0
}

// Allows 判断单张卡是否在范围内。列表侧走 SQL，这里供点查使用。
//
// ⚠️ 各维度必须取**并集**（命中任一即可见）。原实现写成
// `if s.SelfEmpID > 0 { return c.UserEmpID == s.SelfEmpID }` 的短路形式——
// 单角色时各维度只会有一个被设，所以看不出问题；但并集下 [部门负责人, 员工]
// 会让 DeptIDs 被整个吃掉（部门负责人看不到本部门的资产）。
// 与之对称的 model.RepairScope.Allows 从一开始就是 OR 写法，本函数此前是不对称的那一侧。
func (s AssetScope) Allows(c *AssetCard) bool {
	if !s.Restricted() {
		return true
	}
	if s.SelfEmpID > 0 && c.UserEmpID == s.SelfEmpID {
		return true
	}
	for _, id := range s.DeptIDs {
		if id == c.UseDeptID {
			return true
		}
	}
	// 第三维（员工）：有未结单维修单的资产。不限报修人，全公司口径——见字段注释。
	for _, id := range s.RepairingCardIDs {
		if id == c.ID {
			return true
		}
	}
	return false
}

type CountPlan struct {
	ID         int64      `json:"id"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	Scope      CountScope `json:"scope"`
	Status     string     `json:"status"`
	Remark     string     `json:"remark"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// 以下为列表页的派生统计，不落库
	ItemCount    int `json:"item_count,omitempty"`
	CountedCount int `json:"counted_count,omitempty"`
}

// CountItem 是生成盘点表时的资产快照：账面字段定格，事后资产变更不影响已生成的盘点表。
type CountItem struct {
	ID           int64   `json:"id"`
	PlanID       int64   `json:"plan_id"`
	CardID       int64   `json:"card_id"`
	AssetCode    string  `json:"asset_code"`
	Name         string  `json:"name"`
	CategoryName string  `json:"category_name"`
	UseDeptName  string  `json:"use_dept_name"`
	UserName     string  `json:"user_name"`
	Location     string  `json:"location"`
	UseStatus    string  `json:"use_status"`
	BookAmount   float64 `json:"book_amount"`

	AssigneeID   int64      `json:"assignee_id"`
	AssigneeName string     `json:"assignee_name"`
	Result       string     `json:"result"`
	Note         string     `json:"note"`
	CountedBy    string     `json:"counted_by"`
	CountedAt    *time.Time `json:"counted_at,omitempty"`
}

// CountSummary 盘点汇总
type CountSummary struct {
	Total     int `json:"total"`
	Counted   int `json:"counted"`
	Uncounted int `json:"uncounted"`
	Normal    int `json:"normal"`
	Loss      int `json:"loss"`
	Gain      int `json:"gain"`
	Damaged   int `json:"damaged"`
	Assigned  int `json:"assigned"`
}

type CountReport struct {
	Plan    *CountPlan   `json:"plan"`
	Summary CountSummary `json:"summary"`
	Items   []CountItem  `json:"items"`
}

// —— 首页（需求 B）DTO ——
//
// DashboardTodo 是「待我处理」的一项。哪些项出现、count 多少全部由服务端按角色算好，
// 前端只渲染（避免把「角色→待办」的判断复制到前端形成第二套规则）。
type DashboardTodo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int64  `json:"count"`
	Link  string `json:"link"`
	// Level 是紧急程度的语义色提示（danger/warning/info，空串=不强调）。
	// 与 count 一样由服务端按角色算好：前端只把值映射到配色，不按 key 猜颜色，
	// 否则「同一个 key 在不同角色下紧急程度不同」的事实会在前端复制成第二套规则。
	Level string `json:"level,omitempty"`
}

// DashboardStatCard 是首页指标卡，与 DashboardTodo 同形（key/label/count/link）。
// 语义区别：todo 是「要你去做的事」（count=0 不出），stat 是「与你相关的量」（该角色适用就出，0 也出）。
type DashboardStatCard struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int64  `json:"count"`
	Link  string `json:"link"`
}

// DashboardStatusCount 是「状态 + 数量」的通用分组计数（资产状态分布 / 维修单状态分布共用）。
// 资产状态分布里 Status 直接是中文有效状态（display_status），Label 留空；
// 维修单状态分布里 Status 是英文状态码、Label 是中文白话（见 store/dashboard.go）。
type DashboardStatusCount struct {
	Status string `json:"status"`
	Label  string `json:"label,omitempty"`
	Count  int64  `json:"count"`
}

// DashboardCategoryCount 是「分类名 + 数量」。
type DashboardCategoryCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// DashboardOverview 是首页概览区块。字段分组即未来的扩展点：P1 的分布/趋势直接往这里加键，
// 前端增量渲染，结构不返工。
type DashboardOverview struct {
	AssetTotal      int64                    `json:"asset_total"`
	AssetStatus     []DashboardStatusCount   `json:"asset_status"`
	AssetByCategory []DashboardCategoryCount `json:"asset_by_category"`
	RepairStatus    []DashboardStatusCount   `json:"repair_status"`
}

// Dashboard 是 GET /api/dashboard 的响应体：一次请求返回「待办 + 指标卡 + 概览」三块。
// Stats 是「与我相关」的量：设计宗旨要求第一屏内容 = 与我相关的事，缺了它前端只能拿
// overview 的全量概览冒充员工首页。哪些卡出现由服务端按角色算好，前端只渲染。
type Dashboard struct {
	Todo     []DashboardTodo     `json:"todo"`
	Stats    []DashboardStatCard `json:"stats"`
	Overview DashboardOverview   `json:"overview"`
}
