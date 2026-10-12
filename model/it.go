package model

import "time"

// —— IT 信息设备台账（2026-10-10 新增需求）——
//
// # 为什么必须是一张**独立的自有表**，而不是往 asset_card 上加列
//
// asset_card 是**星瀚托管表**：每日 00:00 的定时同步会按 kingdeeOwnedColumns 覆盖本地值。
// 全表只有一个本地列 biz_status 逃过同步，而它的唯一写入者是维修单据状态机
// （见 store/card.go 的 scopeConds 上方注释与 model.AssetCard.BizStatus 的说明），
// 用户永远不能手填。IT 属性（品牌型号 / CPU / 内存 / 硬盘 / 系统版本 / 是否信创）
// 是纯台账本地数据，一旦加到 asset_card 上，**第二天就会被同步抹掉**。
// 所以 IT 台账必须有自己的表、自己的写入路径，与资产台账以 card_id 建立**可空关联**。
//
// # 两条现实约束（用户明确提出，直接决定主键/索引设计）
//
//  1. **员工一人多机** —— 一个人名下可能同时有台式机 + 笔记本 + 平板。
//     故 user_emp_id **不是唯一键**，一个员工对应多条 IT 设备记录。
//  2. **资产卡上没有的设备确实存在**（外购路由器、自购配件、耗材类设备）。
//     故 card_id 必须**可空**，且允许多条 —— 靠「MySQL 唯一索引对 NULL 不去重」这一语义实现：
//     UNIQUE KEY uk_it_card (card_id) 保证「一张资产卡最多一条 IT 记录」（1:1），
//     同时不影响任意多条 card_id IS NULL 的无卡设备。见 store/it.go 的写列注释。

// ITDeviceTypes 是「电脑类型」的可选值。
//
// 这是**唯一事实来源**：后端校验（model.IsValidITDeviceType）与前端下拉都读它，
// 前端通过 GET /api/enums 拿，**不另抄一份**（本仓踩过「前后端两份表漂移」的真实 bug，
// 见 model.rolePermissions 的注释）。要加取值，改这一行即可。
var ITDeviceTypes = []string{
	"台式机", "笔记本", "一体机", "工作站", "服务器", "平板", "打印机", "网络设备", "其他",
}

// ITPurposes 是「用途」的可选值。
var ITPurposes = []string{
	"办公", "研发", "设计", "生产", "测试", "备用", "其他",
}

// IT 设备状态。与资产台账的 AssetStatuses 是**两套**枚举，刻意不共用：
// 资产状态里「调拨中」由单据流程驱动，IT 台账没有调拨概念；
// 而 IT 台账需要「待分配」（新到货还没派给人）这个资产台账没有的状态。
const (
	ITStatusInUse     = "在用"
	ITStatusIdle      = "闲置"
	ITStatusRepairing = "维修中"
	ITStatusBorrowed  = "借用"
	ITStatusScrapped  = "报废"
	ITStatusPending   = "待分配"
)

var ITStatuses = []string{
	ITStatusInUse, ITStatusIdle, ITStatusRepairing,
	ITStatusBorrowed, ITStatusScrapped, ITStatusPending,
}

// IsValidITDeviceType / IsValidITPurpose / IsValidITStatus 三个校验的语义统一为
// 「空串放行，非空必须在枚举内」。
//
// ⚠️ 为什么空串要放行：IT 记录最常见的产生方式是「从资产台账关联生成」——
// 那一刻只有资产编码/使用人/部门/地点是从卡上带出来的，IT 属性（类型、用途）还是空的，
// 等着人来补。若空串非法，这条「先生成后完善」的主路径第一步就走不通。
// 这与 api/card.go 里 Source 的写法一致（`c.Source != "" && !IsValidSource(...)`）。
//
// 写入侧另有兜底：状态为空时落默认值「在用」（见 api 层 validateITEquipment），
// 所以「空串放行」不会让库里出现状态为空的行。
func IsValidITDeviceType(s string) bool { return s == "" || contains(ITDeviceTypes, s) }
func IsValidITPurpose(s string) bool    { return s == "" || contains(ITPurposes, s) }
func IsValidITStatus(s string) bool     { return s == "" || contains(ITStatuses, s) }

// ITEquipment 是一条 IT 信息设备台账记录。
type ITEquipment struct {
	ID int64 `json:"id"`

	// CardID 关联资产台账 asset_card.id。
	//
	// **0 = 未关联**（这台设备在资产卡上不存在），是本字段唯一合法的「空」表示。
	//
	// ⚠️ 库里这一列是 **NULL 而不是 0**，靠 store 层的 `NULLIF(?, 0)` / `COALESCE(card_id, 0)`
	// 在两个方向做转换。原因只有一个：UNIQUE KEY uk_it_card (card_id) 要同时满足
	// 「一卡一条记录」与「多条无卡设备共存」——而 MySQL 唯一索引对 NULL 不去重，
	// 对 0 去重。若库里存 0，第二台无卡设备就会被唯一键挡住。
	//
	// 为什么 Go 侧用 0 而不是 *int64：JSON 里 0 与「未关联」同义、前端 el-select 的
	// clearable 天然产出 0，指针会让「0 / null」两种空值在前后端各解释一遍。
	CardID int64 `json:"card_id"`

	// AssetCode 是**冗余快照 + 导入匹配键**。
	//
	// 有卡时由服务端从 asset_card 带出（前端也会带，但服务端以库为准）；
	// 无卡时可留空，也可由人工编一个自有编码（如外购设备的资产标签号）。
	//
	// ⚠️ **刻意不加唯一约束**：多条无卡设备的编码都是空串 ''，加唯一键第二条就插不进去
	// （与 employee.open_id 同一个坑，见 store/mysql.go 的 idx_emp_open_id 注释）。
	// 卡被删掉后这一列仍保留，是「这条 IT 记录原本对应哪台资产」的唯一线索。
	AssetCode string `json:"asset_code"`

	// AssetName 是**派生字段，不落库**：由 itSelect 从 asset_card LEFT JOIN 带出。
	// 卡被删 / 未关联时为对应空值。只用于列表展示，绝不进任何写列清单。
	AssetName string `json:"asset_name,omitempty"`

	DeviceType string `json:"device_type"`

	// UserEmpID 是使用人。**不是唯一键**——见本文件顶部「员工一人多机」。
	UserEmpID  int64  `json:"user_emp_id"`
	UserEmpName string `json:"user_emp_name,omitempty"`

	// DeptID 是使用人所属部门的**快照**。不靠 employee.dept_id 实时推——
	// 员工主数据里这一列基本是空的（见 model.User.DeptID 的注释），且人调岗后
	// 这台设备当时属于哪个部门是历史事实，不该跟着人变。
	DeptID   int64  `json:"dept_id"`
	DeptName string `json:"dept_name,omitempty"`

	Location string `json:"location"`
	Purpose  string `json:"purpose"`
	Status   string `json:"status"`

	// IsXinchuang 是否信创设备（信息技术应用创新，即国产 CPU/OS 的那批）。
	// 用户明确提出要这个字段，是筛选与统计的主要维度之一（列表页会出「信创设备 N 台」）。
	IsXinchuang bool `json:"is_xinchuang"`

	BrandModel string `json:"brand_model"`
	CPU        string `json:"cpu"`
	// MemoryGB 内存容量，单位 GB。用整数而不是字符串：要能按容量筛选/排序，
	// 「8G」「8GB」「8 GB」这种自由文本没法排序也没法筛。非整数的特殊情况（如 1.5G）
	// 记在 Remark 里——现实里没有这种内存条。
	MemoryGB int    `json:"memory_gb"`
	Disk     string `json:"disk"`
	OSVersion string `json:"os_version"`
	Remark   string `json:"remark"`

	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ITScope 描述「这个账号能看哪些 IT 设备记录」，零值 = 不限制。
//
// 与 model.AssetScope 的关系：**口径一致、维度更少**。
// 用户明确要求「IT 台账的可见范围复用资产台账的范围」，故维度照搬
// （自己名下 / 管辖部门），但**刻意不含** AssetScope 的第三维 RepairingCardIDs ——
// 那一维的语义是「有未结单维修单的资产对员工可见」，是维修模块的需要，
// 与 IT 设备台账无关，照抄过来会让员工莫名其妙看到别人的设备。
//
// 与 AssetScope 一样只是数据：由 api 层按角色解析出来，store 只认数字不认角色。
// 多角色语义同样是**并集**：两个维度同时非零时命中任一即可见。
type ITScope struct {
	SelfEmpID int64   // >0：可见「这个使用人名下」的 IT 设备
	DeptIDs   []int64 // 非空：可见「使用部门落在这些部门里」的 IT 设备
}

func (s ITScope) Restricted() bool {
	return s.SelfEmpID > 0 || len(s.DeptIDs) > 0
}

// Allows 判断单条记录是否在范围内。列表侧走 SQL（store/it.go 的 itScopeConds），
// 这里供点查使用。两维取**并集**（命中任一即可见），与 model.AssetScope.Allows 同构。
func (s ITScope) Allows(e *ITEquipment) bool {
	if !s.Restricted() {
		return true
	}
	if s.SelfEmpID > 0 && e.UserEmpID == s.SelfEmpID {
		return true
	}
	for _, id := range s.DeptIDs {
		if id == e.DeptID {
			return true
		}
	}
	return false
}

// ITListQuery 是 IT 台账列表查询条件。
// 与 model.ListQuery 同一约定：排序字段与筛选列都经过白名单映射，不直接拼进 SQL。
type ITListQuery struct {
	Keyword    string
	DeviceType string
	Purpose    string
	Status     []string
	// IsXinchuang 按「是否信创」筛选，nil = 不筛选。
	// 用指针而不是 bool，理由与 ListQuery.Repairable 完全相同：bool 零值 false
	// 无法区分「不筛选」和「只要非信创」，而后者是真实存在的查询（例如导出非信创清单）。
	IsXinchuang *bool
	// Linked 按「是否已关联资产卡」筛选，nil = 不筛选。
	// 指向 true = 只要已关联的；指向 false = 只要资产卡上没有的设备
	// ——后者正是用户提出的核心场景之一，必须能一键筛出来。
	Linked    *bool
	DeptID    int64
	UserEmpID int64
	// CardID 按「关联的资产卡」精确筛选，0 = 不筛选。
	//
	// 它的真实用途是**判重**：「从资产台账关联生成」时前端先带 card_id 打一次列表接口，
	// 有结果就说明这张卡已登记过 IT 信息，直接跳到编辑那条记录。
	// 之所以复用列表接口而不另开 `/by-card` 端点：本仓已经踩过 ServeMux 模式冲突
	// （见 api/server.go 里 by-code 走查询串的注释），能不加路由就不加。
	CardID     int64
	AssetCode  string
	BrandModel string
	SortBy     string
	SortDesc   bool
	Page       int
	PageSize   int
}

// ITListResult 是 IT 台账列表的响应体。
type ITListResult struct {
	Items []ITEquipment `json:"items"`
	Total int64         `json:"total"`
	// XinchuangTotal 是**当前筛选条件下**的信创设备台数（不落库，随 Total 一起算）。
	// 用户要「是否信创」这个字段，最自然的用法就是看「这批里有多少台是信创」，
	// 让服务端一次算好，前端不必再拉一遍全量去数。
	XinchuangTotal int64 `json:"xinchuang_total"`
	// UnlinkedTotal 是当前筛选条件下「资产卡上没有的设备」台数。
	// 与 XinchuangTotal 同理：这是用户明确点出的管理盲区，应当一眼可见。
	UnlinkedTotal int64 `json:"unlinked_total"`
	Page          int   `json:"page"`
	PageSize      int   `json:"page_size"`
}

// ITImportItem 是一行「已解析、待写入」的 IT 台账导入数据。
//
// ExistingID > 0 表示**更新**这条已存在的记录；0 表示**新增**。
//
// 为什么把「新增还是更新」的结论放在这里、由 api 层算好：
// 判定要查两类库（asset_card 按编码、it_equipment 按编码或关联卡），属于「解析」而不是「写入」。
// 把结论显式传下来，store 层就只负责写，判定规则**只有一处实现**
// （api/it_importexport.go 的 parseITImportRows），不会出现两套判重口径。
type ITImportItem struct {
	ExistingID int64
	Equipment  ITEquipment
}
