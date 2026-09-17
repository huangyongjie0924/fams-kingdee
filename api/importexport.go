package api

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"asset-mgr/model"
)

// importHeaders 是导入模板的列顺序，导出也复用同一套表头
var importHeaders = []string{
	"资产编码", "资产名称", "资产类别", "规格型号", "设备序列号", "计量单位", "状态", "金额",
	"使用公司", "使用部门", "使用人", "管理人", "所属公司", "区域", "存放地点",
	"购入日期", "使用期限(月)", "来源", "备注",
	"原值", "累计折旧", "残值率(%)", "财务使用期限(月)", "供应商",
	// 数量必须追加在末尾：导入是按列下标取值的，插在中间会把后面所有列错位
	"数量",
}

func (s *Server) handleImportTemplate(w http.ResponseWriter, r *http.Request) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "资产导入"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成模板失败")
		return
	}
	f.SetActiveSheet(idx)
	f.DeleteSheet("Sheet1")

	for i, h := range importHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	f.SetCellValue(sheet, "A2", "")
	f.SetCellValue(sheet, "B2", "示例：戴尔笔记本电脑")
	f.SetCellValue(sheet, "C2", "电子产品及通信设备")
	f.SetCellValue(sheet, "P2", "2026-01-15")
	// 数量是最后一列（第 25 列 = Y），放个样例提示格式
	f.SetCellValue(sheet, "Y2", "194.52")

	writeXLSX(w, f, "资产导入模板.xlsx")
}

func writeXLSX(w http.ResponseWriter, f *excelize.File, filename string) {
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	if err := f.Write(w); err != nil {
		// 响应头已发出，只能记录
		fmt.Printf("write xlsx: %v\n", err)
	}
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	// 导出没有第二条取数路径，全程走 ListCards，所以跟着列表一起受限：
	// 只读账号导出的就是他自己那几张，不会绕过列表把全量导走。
	sc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	q := parseListQuery(r)
	q.Page = 1
	q.PageSize = 500

	f := excelize.NewFile()
	defer f.Close()
	sheet := "资产清单"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成导出文件失败")
		return
	}
	f.SetActiveSheet(idx)
	f.DeleteSheet("Sheet1")
	for i, h := range importHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}

	row := 2
	for {
		res, err := s.st.ListCards(q, sc)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询资产失败")
			return
		}
		for i := range res.Items {
			c := res.Items[i]
			vals := []any{
				c.AssetCode, c.Name, c.CategoryName, c.Spec, c.SerialNo, c.Unit, c.Status, c.Amount,
				c.UseCompanyName, c.UseDeptName, c.UserEmpName, c.ManagerEmpName, c.OwnerCompanyName,
				c.AreaName, c.Location, c.PurchaseDate, c.UseMonths, c.Source, c.Remark,
				c.FinOriginalValue, c.FinAccumDepreciaton, c.FinResidualRate, c.FinUseMonths, c.VendorName,
				c.Quantity,
			}
			for j, v := range vals {
				cell, _ := excelize.CoordinatesToCellName(j+1, row)
				f.SetCellValue(sheet, cell, v)
			}
			row++
		}
		if int64(q.Page*q.PageSize) >= res.Total {
			break
		}
		q.Page++
	}

	writeXLSX(w, f, fmt.Sprintf("资产清单_%s.xlsx", time.Now().Format("20060102")))
}

type importError struct {
	Row     int    `json:"row"`
	Column  string `json:"column"`
	Message string `json:"message"`
}

// openUploadedSheet 取上传 .xlsx 的第一张工作表。
// 「新增导入」和「批量更新财务信息」共用同一套入口校验，报错文案保持一致。
func (s *Server) openUploadedSheet(w http.ResponseWriter, r *http.Request) ([][]string, bool) {
	if err := r.ParseMultipartForm(s.cfg.Server.MaxUploadMB << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传解析失败，文件可能超出大小限制")
		return nil, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少上传文件")
		return nil, false
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".xlsx") {
		writeErr(w, http.StatusBadRequest, "只支持 .xlsx 格式")
		return nil, false
	}

	f, err := excelize.OpenReader(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Excel 解析失败："+err.Error())
		return nil, false
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		writeErr(w, http.StatusBadRequest, "文件里没有工作表")
		return nil, false
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取工作表失败")
		return nil, false
	}
	if len(rows) < 2 {
		writeErr(w, http.StatusBadRequest, "没有数据行")
		return nil, false
	}
	return rows, true
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	rows, ok := s.openUploadedSheet(w, r)
	if !ok {
		return
	}

	cards, errs := s.parseImportRows(rows)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  fmt.Sprintf("共 %d 处问题，未写入任何数据", len(errs)),
			"errors": errs,
		})
		return
	}

	n, err := s.st.ImportCards(cards, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "导入失败，已回滚："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": n})
}

// financeHeaderAliases 把表头写法归一到内部列名。
// 主用法是「导出的整表改完导回」，导出用的就是 importHeaders 里的写法；
// 别名是为了手工精简成「资产编码 / 原值 / 累计折旧」三列时，不至于因为少了
// 「(%)」「(月)」就整列被静默忽略 —— 静默忽略是这个功能最危险的失败方式。
var financeHeaderAliases = map[string]string{
	"资产编码":      "asset_code",
	"原值":        "original_value",
	"累计折旧":      "accum_depreciation",
	"残值率":       "residual_rate",
	"残值率(%)":    "residual_rate",
	"财务使用期限":    "fin_use_months",
	"财务使用期限(月)": "fin_use_months",
}

// financeUpdatable 是批量更新真正会写库的列（不含「资产编码」这个定位键）。
// 只列这四列是刻意的：文件里出现的其他列（名称、部门、状态、数量…）一律不读，
// 这样「导出整表 → 改金额 → 导回」不会顺手覆盖别的字段。
var financeUpdatable = []struct{ key, label string }{
	{"original_value", "原值"},
	{"accum_depreciation", "累计折旧"},
	{"residual_rate", "残值率(%)"},
	{"fin_use_months", "财务使用期限(月)"},
}

// handleImportFinance 批量更新财务信息。与 handleImport 是两件不同的事，别混：
// handleImport 是「新增」，编码已存在就报错；这里是「更新」，编码必须已存在，
// 且只改财务列。
//
// 存在的原因是星瀚的 Select_AssetCard 不返回资产原值 / 累计折旧（全库恒为 0），
// 227 张卡的金额只能从星瀚导出后人工回填 —— 单卡表单填 200 多次不现实。
//
// 支持 ?dry_run=1：只算不写，把「哪张卡、哪个字段、从多少改到多少」先回给前端确认。
func (s *Server) handleImportFinance(w http.ResponseWriter, r *http.Request) {
	rows, ok := s.openUploadedSheet(w, r)
	if !ok {
		return
	}

	ups, errs := s.parseFinanceRows(rows)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  fmt.Sprintf("共 %d 处问题，未更新任何数据", len(errs)),
			"errors": errs,
		})
		return
	}
	if len(ups) == 0 {
		writeErr(w, http.StatusBadRequest, "没有数据行")
		return
	}

	dryRun := r.URL.Query().Get("dry_run") == "1"
	results, err := s.st.UpdateCardsFinance(ups, operatorOf(r), dryRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "批量更新失败，已回滚："+err.Error())
		return
	}

	// updated 在 dry_run 下是「将更新」的意思，前端按 dry_run 决定文案。
	writeJSON(w, http.StatusOK, map[string]any{
		"dry_run": dryRun,
		"rows":    len(ups),
		"updated": len(results),
		"skipped": len(ups) - len(results),
		"changes": results,
	})
}

func (s *Server) parseFinanceRows(rows [][]string) ([]model.FinanceUpdate, []importError) {
	col := map[string]int{}
	for i, h := range rows[0] {
		key, ok := financeHeaderAliases[strings.TrimSpace(h)]
		if !ok {
			continue
		}
		// 同名列出现两次时以第一列为准，避免后面一列空着就把前面有值的挤掉
		if _, dup := col[key]; !dup {
			col[key] = i
		}
	}
	if _, ok := col["asset_code"]; !ok {
		return nil, []importError{{Row: 1, Column: "资产编码",
			Message: "表头缺少「资产编码」列，无法确定要更新哪张卡"}}
	}
	present := 0
	for _, f := range financeUpdatable {
		if _, ok := col[f.key]; ok {
			present++
		}
	}
	if present == 0 {
		return nil, []importError{{Row: 1, Column: "表头",
			Message: "没有任何可更新的财务列（原值 / 累计折旧 / 残值率(%) / 财务使用期限(月)）"}}
	}

	var ups []model.FinanceUpdate
	var errs []importError
	seen := map[string]int{}
	codes := []string{}

	for i, row := range rows[1:] {
		rowNo := i + 2
		get := func(key string) string {
			idx, ok := col[key]
			if !ok || idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}

		var up model.FinanceUpdate
		up.AssetCode = get("asset_code")
		if up.AssetCode == "" {
			errs = append(errs, importError{rowNo, "资产编码", "必填"})
		} else if prev, dup := seen[up.AssetCode]; dup {
			errs = append(errs, importError{rowNo, "资产编码", fmt.Sprintf("与第 %d 行重复", prev)})
		} else {
			seen[up.AssetCode] = rowNo
			codes = append(codes, up.AssetCode)
		}

		// 空单元格 = 这一列不动；写 0 是「改成 0」，是有效输入。两者必须分开：
		// 导出文件里没填的金额就是 0，不能让「导出 → 只改一张 → 导回」把别人的卡清零。
		for _, f := range []struct {
			key   string
			label string
			dst   **float64
		}{
			{"original_value", "原值", &up.OriginalValue},
			{"accum_depreciation", "累计折旧", &up.AccumDepreciation},
			{"residual_rate", "残值率(%)", &up.ResidualRate},
		} {
			v := get(f.key)
			if v == "" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				errs = append(errs, importError{rowNo, f.label, "不是数字：" + v})
				continue
			}
			if n < 0 {
				errs = append(errs, importError{rowNo, f.label, "不能为负数：" + v})
				continue
			}
			*f.dst = &n
		}
		if up.ResidualRate != nil && *up.ResidualRate > 100 {
			errs = append(errs, importError{rowNo, "残值率(%)", "应在 0~100 之间：" + get("residual_rate")})
		}
		if v := get("fin_use_months"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, importError{rowNo, "财务使用期限(月)", "不是整数：" + v})
			} else if n < 0 {
				errs = append(errs, importError{rowNo, "财务使用期限(月)", "不能为负数：" + v})
			} else {
				up.FinUseMonths = &n
			}
		}

		ups = append(ups, up)
	}

	if len(codes) > 0 {
		existing, err := s.st.ExistingCodes(codes)
		if err != nil {
			errs = append(errs, importError{0, "资产编码", "查询失败：" + err.Error()})
		} else {
			for code, rowNo := range seen {
				if !existing[code] {
					errs = append(errs, importError{rowNo, "资产编码", "系统里没有这张卡：" + code})
				}
			}
		}
	}

	// 上面查存在性是按 map 遍历追加的，顺序随机；按行号排一下，
	// 用户看到的报错列表才和 Excel 里的行序一致。
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Row < errs[j].Row })
	return ups, errs
}

func (s *Server) parseImportRows(rows [][]string) ([]model.AssetCard, []importError) {
	var cards []model.AssetCard
	var errs []importError
	seenCode := map[string]int{}
	codes := []string{}

	for i, row := range rows[1:] {
		rowNo := i + 2
		get := func(idx int) string {
			if idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}

		var c model.AssetCard
		c.AssetCode = get(0)
		c.Name = get(1)
		if c.Name == "" {
			errs = append(errs, importError{rowNo, "资产名称", "必填"})
		}
		if c.AssetCode != "" {
			if prev, dup := seenCode[c.AssetCode]; dup {
				errs = append(errs, importError{rowNo, "资产编码",
					fmt.Sprintf("与第 %d 行重复", prev)})
			} else {
				seenCode[c.AssetCode] = rowNo
				codes = append(codes, c.AssetCode)
			}
		}

		catName := get(2)
		if catName == "" {
			errs = append(errs, importError{rowNo, "资产类别", "必填"})
		} else {
			id, months, rate, err := s.st.LookupCategoryByName(catName)
			if err != nil {
				errs = append(errs, importError{rowNo, "资产类别", "查询失败"})
			} else if id == 0 {
				errs = append(errs, importError{rowNo, "资产类别", "系统里不存在：" + catName})
			} else {
				c.CategoryID = id
				c.UseMonths = months
				c.FinResidualRate = rate
			}
		}

		c.Spec, c.SerialNo, c.Unit = get(3), get(4), get(5)
		c.Status = get(6)
		if c.Status == "" {
			c.Status = model.StatusIdle
		} else if !contains(model.AssetStatuses, c.Status) {
			errs = append(errs, importError{rowNo, "状态", "取值非法：" + c.Status})
		}
		if v := get(7); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err != nil {
				errs = append(errs, importError{rowNo, "金额", "不是数字：" + v})
			} else {
				c.Amount = f
			}
		}
		c.UseCompanyID = s.lookupOrErr(&errs, rowNo, "使用公司", get(8), s.st.LookupCompanyByName)
		c.UseDeptID = s.lookupOrErr(&errs, rowNo, "使用部门", get(9), s.st.LookupDepartmentByName)
		c.UserEmpID = s.lookupOrErr(&errs, rowNo, "使用人", get(10), s.st.LookupEmployeeByName)
		c.ManagerEmpID = s.lookupOrErr(&errs, rowNo, "管理人", get(11), s.st.LookupEmployeeByName)
		c.OwnerCompanyID = s.lookupOrErr(&errs, rowNo, "所属公司", get(12), s.st.LookupCompanyByName)
		c.AreaID = s.lookupOrErr(&errs, rowNo, "区域", get(13), s.st.LookupAreaByName)
		c.Location = get(14)

		if v := get(15); v != "" {
			if d, ok := parseDate(v); ok {
				c.PurchaseDate = d
			} else {
				errs = append(errs, importError{rowNo, "购入日期", "日期格式应为 2026-01-15，实际：" + v})
			}
		}
		if v := get(16); v != "" {
			if n, err := strconv.Atoi(v); err != nil {
				errs = append(errs, importError{rowNo, "使用期限(月)", "不是整数：" + v})
			} else {
				c.UseMonths = n
			}
		}
		if v := get(17); v != "" {
			if !model.IsValidSource(v) {
				errs = append(errs, importError{rowNo, "来源", "取值非法：" + v})
			} else {
				c.Source = v
			}
		}
		c.Remark = get(18)

		for _, f := range []struct {
			idx   int
			label string
			dst   *float64
		}{
			{19, "原值", &c.FinOriginalValue},
			{20, "累计折旧", &c.FinAccumDepreciaton},
			{21, "残值率(%)", &c.FinResidualRate},
		} {
			if v := get(f.idx); v != "" {
				if n, err := strconv.ParseFloat(v, 64); err != nil {
					errs = append(errs, importError{rowNo, f.label, "不是数字：" + v})
				} else {
					*f.dst = n
				}
			}
		}
		if v := get(22); v != "" {
			if n, err := strconv.Atoi(v); err != nil {
				errs = append(errs, importError{rowNo, "财务使用期限(月)", "不是整数：" + v})
			} else {
				c.FinUseMonths = n
			}
		}
		c.VendorID = s.lookupOrErr(&errs, rowNo, "供应商", get(23), s.st.LookupVendorByName)

		// 数量列（第 25 列）是后加的：老模板没有这一列时 get 返回空串，数量保持 0，
		// 不报错——导入模板的向后兼容就靠这一点。
		if v := get(24); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err != nil {
				errs = append(errs, importError{rowNo, "数量", "不是数字：" + v})
			} else {
				c.Quantity = n
			}
		}

		c.FinStatus = "未入账"
		c.FinNetValue = c.FinOriginalValue - c.FinAccumDepreciaton

		cards = append(cards, c)
	}

	if len(codes) > 0 {
		existing, err := s.st.ExistingCodes(codes)
		if err != nil {
			errs = append(errs, importError{0, "资产编码", "查重失败：" + err.Error()})
		} else {
			for code, rowNo := range seenCode {
				if existing[code] {
					errs = append(errs, importError{rowNo, "资产编码", "系统里已存在：" + code})
				}
			}
		}
	}
	return cards, errs
}

func (s *Server) lookupOrErr(errs *[]importError, rowNo int, label, name string,
	lookup func(string) (int64, error)) int64 {
	if name == "" {
		return 0
	}
	id, err := lookup(name)
	if err != nil {
		*errs = append(*errs, importError{rowNo, label, "查询失败"})
		return 0
	}
	if id == 0 {
		*errs = append(*errs, importError{rowNo, label, "系统里不存在：" + name})
		return 0
	}
	return id
}

// parseDate 容忍 Excel 里常见的几种写法
func parseDate(v string) (string, bool) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"2006-01-02", "2006/1/2", "2006.01.02", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}
