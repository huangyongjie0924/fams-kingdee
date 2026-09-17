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

// FinEntry 是金蝶的「财务分录」子表。实测每张卡最多一条。
//
// 重要：Select_AssetCard 返回的 fin_originalval / fin_networth 全库 227 张卡恒为 0，
// 星瀚并没有通过这个接口提供资产原值 / 累计折旧 / 净值。因此台账里的这三个数只能
// 走人工维护（卡片编辑 / Excel 导入），不会由同步覆盖。
// 用 json.Number 而不是 float64：星瀚的数值是 10 位小数字面量（如 194.5200000000），
// 一旦星瀚侧补上真实取值，直接解析成 float64 会先丢一次精度。
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
