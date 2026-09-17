package kingdee

import "encoding/json"

type Config struct {
	BaseURL        string
	ClientID       string
	ClientSecret   string
	Username       string
	AccountID      string
	RequestTimeout int // 秒
	PageSize       int
	MaxRetries     int
	QueryPath      string
}

type AssetCard struct {
	ID                  string      `json:"id"`
	BillNo              string      `json:"billno"`
	Number              string      `json:"number"`
	AssetName           string      `json:"assetname"`
	Model               string      `json:"model"`
	AssetAmount         json.Number `json:"assetamount"`
	Price               json.Number `json:"price"`
	Remark              string      `json:"remark"`
	BizStatus           string      `json:"bizstatus"`
	BizStatusTitle      string      `json:"bizstatus_title"`
	BillStatus          string      `json:"billstatus"`
	BillStatusTitle     string      `json:"billstatus_title"`
	AuditDate           string      `json:"auditdate"`
	ModifyTime          string      `json:"modifytime"`
	CreateTime          string      `json:"createtime"`
	Barcode             string      `json:"barcode"`
	RealAccountDate     string      `json:"realaccountdate"`
	UsedDate            string      `json:"usedate"`
	MergedCard          bool        `json:"mergedcard"`
	SupplierNumber      string      `json:"supplier_number"`
	SupplierName        string      `json:"supplier_name"`
	AssetCategoryNumber string      `json:"assetcat_number"`
	AssetCategoryName   string      `json:"assetcat_name"`
	UnitName            string      `json:"unit_name"`
	UseDepartmentName   string      `json:"headusedept_name"`
	UseDepartmentNumber string      `json:"headusedept_number"`
	HeadUsePersonID     string      `json:"headuseperson_id"`
	HeadUsePersonNumber string      `json:"headuseperson_number"`
	HeadUsePersonName   string      `json:"headuseperson_name"`
	UseStatusID         string      `json:"usestatus_id"`
	UseStatusNumber     string      `json:"usestatus_number"`
	UseStatusName       string      `json:"usestatus_name"`
	StorePlaceName      string      `json:"storeplace_name"`
	StorePlaceNumber    string      `json:"storeplace_number"`
	AssetUnitName       string      `json:"assetunit_name"`
	AssetUnitNumber     string      `json:"assetunit_number"`
	FinEntry            []FinEntry  `json:"finentry"`
}

// FinEntry 是金蝶的「财务信息」明细子表 —— 星瀚侧资产原值 / 累计折旧 / 净值 / 含税金额 /
// 税额都落在明细表上，finentry 是 Select_AssetCard 返回里唯一承载它的出口。
//
// 实测（2026-09-17，全量 227 行原始响应）：
//   - finentry 是 48 个返回字段里唯一的嵌套结构，只投影了 fin_originalval / fin_networth 两列
//   - 有 finentry 的 200 行里，这两列**全部**是 0.0000000000（10 位小数字面量）
//   - 另外 27 行 finentry 直接是 null（见下方"同编码两行"说明）
//
// 也就是说：明细子表的投影列不全、且已有两列的取值没接上真实数据。
// 因此台账里的金额只能走人工维护（卡片编辑 / Excel 导入），同步不会覆盖。
// 需求文档见 docs/星瀚接口字段扩展需求.md。
//
// 用 json.Number 而不是 float64：星瀚的数值是 10 位小数字面量（如 194.5200000000），
// 一旦星瀚侧补上真实取值，直接解析成 float64 会先丢一次精度。
//
// 同编码两行：227 行只对应 200 个资产编码 —— 有 27 个编码被返回了两行，两行 id 不同、
// createtime/modifytime 相同，差异只在 id、finentry（一行有、一行 null）、
// 以及 5 个编码的 bizstatus（READY 与 ADD 各一行）。我方按 asset_code 落库，重复行会收敛
// 成一张卡，所以对台账无影响；但这说明接口取数很可能是主表与明细表 join 出来的，需要星瀚侧确认。
type FinEntry struct {
	FinOriginalVal json.Number `json:"fin_originalval"`
	FinNetWorth    json.Number `json:"fin_networth"`
}

type AssetPage struct {
	Rows       []AssetCard `json:"rows"`
	LastPage   bool        `json:"lastPage"`
	PageNo     int         `json:"pageNo"`
	PageSize   int         `json:"pageSize"`
	TotalCount int64       `json:"totalCount"`
}

type apiResponse[T any] struct {
	Data      T      `json:"data"`
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
	Status    bool   `json:"status"`
}

type tokenData struct {
	AccessToken string `json:"access_token"`
}
