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

// FinEntry 是金蝶的「财务信息」明细子表。星瀚侧资产原值 / 累计折旧 / 净值都落在明细表上，
// finentry 是 Select_AssetCard 返回里唯一承载它的出口。
//
// 星瀚侧 2026-09-17 晚间扩过这个子表的投影：当天 19:18 只有 2 个键
// （fin_originalval / fin_networth，且全库恒为 0.0000000000），
// 20:07 再扫已变成 39 个键，真实取值挂在 originalfincard_* 前缀上。
// 旧的那两个 fin_* 键保留着但仍然是 0 —— 是没接上数据的冗余列，不要用。
//
// 实测（227 行 / 200 张有明细的卡，全部勾稽验证过）：
//   - originalfincard_originalval  资产原值   200/200 有值，最小 999.08
//   - originalfincard_accumdepre   累计折旧   200/200 有值
//   - originalfincard_networth     净值       200/200 有值
//     且 networth == originalval - accumdepre，200/200 全部成立（口径与台账一致）
//   - originalfincard_netamount    净额       与 networth 200/200 完全相同（冗余列）
//   - originalfincard_originalamount 原币原值 有值时为 22 行，取值恒等于 originalval（冗余列）
//   - originalfincard_incometax    税额       22 行有值，恒等于 originalval × 13%（进项税额），
//     语义尚未经业务确认，暂不接入
//   - originalfincard_preusingamount 预计使用期数（月），200/200 有值，取值 24~600 的整数
//   - originalfincard_preresidualval 预计残值（金额，非比率），194/200 有值
//
// 用 json.Number 而不是 float64：星瀚的数值是 6~10 位小数字面量（如 359265.080000），
// 直接解析成 float64 会先丢一次精度。
//
// 另有 13 个键在 200 行里恒为空/恒零（fin_depremethod_*、fin_depreuse_*、decval、
// monthorigvalchg、monthworkload、originaldata、sourcetype 等），需要时再逐个确认语义。
//
// 同编码两行：227 行只对应 200 个资产编码 —— 有 27 个编码被返回了两行，两行 id 不同、
// createtime/modifytime 相同，差异只在 id、finentry（一行有明细、一行 null）、
// 以及 5 个编码的 bizstatus（READY 与 ADD 各一行）。带明细的那行永远是 id 较小那行（27/27）。
// 我方按 asset_code 落库，重复行会收敛成一张卡，所以对台账无影响。
type FinEntry struct {
	// 资产原值 / 累计折旧 / 净值：台账财务信息的三个金额，已接入同步托管
	OriginalVal  json.Number `json:"originalfincard_originalval"`
	AccumDepre   json.Number `json:"originalfincard_accumdepre"`
	NetWorth     json.Number `json:"originalfincard_networth"`
	PreUseAmount json.Number `json:"originalfincard_preusingamount"`
	PreResidual  json.Number `json:"originalfincard_preresidualval"`
	IncomeTax    json.Number `json:"originalfincard_incometax"`

	// 明细行自身的标识：realcardmasterid 指回资产卡主表 id（200/200 与行的 id 相同），
	// id 是明细行主键（与行的 id 不同）
	RealCardMasterID string      `json:"originalfincard_realcardmasterid"`
	DetailID         string      `json:"originalfincard_id"`
	FinAccountDate   string      `json:"originalfincard_finaccountdate"`
	IsNeedDepre      bool        `json:"originalfincard_isneeddepre"`
	CurrencyRate     json.Number `json:"originalfincard_currencyrate"`

	// 旧投影列：保留只为看清"字段在但值不对"，实际恒为 0，不要读
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
