package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"asset-mgr/model"
)

// itImportHeaders 是 IT 台账导入模板的列顺序，导出复用同一套表头。
//
// 列顺序**逐字对应用户提出的字段清单**（资产编码 使用人 部门 电脑类型 使用地点 用途 状态
// 是否信创 品牌.型号 CPU型号 内存容量(GB) 硬盘类型及容量 系统版本 备注），
// 这样现场拿到纸质/电子表格时列序一致，不必来回对。
//
// ⚠️ 与 importHeaders 同一条纪律：**新增列一律追加在末尾**。
// 导入是按列下标取值的，插在中间会把后面所有列错位（而错位不会报错，只会写错数据）。
var itImportHeaders = []string{
	"资产编码", "使用人", "部门", "电脑类型", "使用地点", "用途", "状态",
	"是否信创", "品牌.型号", "CPU型号", "内存容量(GB)", "硬盘类型及容量", "系统版本", "备注",
}

func (s *Server) handleITImportTemplate(w http.ResponseWriter, r *http.Request) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "IT设备导入"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成模板失败")
		return
	}
	f.SetActiveSheet(idx)
	f.DeleteSheet("Sheet1")

	for i, h := range itImportHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	// 两行示例：一行「有资产卡」（编码来自资产台账），一行「资产卡上没有的设备」
	// （编码是人工编的自有编码）——后者正是用户明确提出的真实场景，
	// 放在模板里比写在文档里更容易被看见。
	f.SetCellValue(sheet, "A2", "GCGS-DN-2024-0001")
	f.SetCellValue(sheet, "B2", "张三")
	f.SetCellValue(sheet, "D2", "笔记本")
	f.SetCellValue(sheet, "E2", "总部 3 楼办公区")
	f.SetCellValue(sheet, "F2", "办公")
	f.SetCellValue(sheet, "G2", "在用")
	f.SetCellValue(sheet, "H2", "是")
	f.SetCellValue(sheet, "I2", "联想 ThinkPad T14")
	f.SetCellValue(sheet, "J2", "Intel i7-1260P")
	f.SetCellValue(sheet, "K2", "16")
	f.SetCellValue(sheet, "L2", "SSD 512GB")
	f.SetCellValue(sheet, "M2", "统信 UOS 20")
	f.SetCellValue(sheet, "N2", "信创机型")

	f.SetCellValue(sheet, "A3", "IT-2026-0007")
	f.SetCellValue(sheet, "D3", "网络设备")
	f.SetCellValue(sheet, "E3", "机房")
	f.SetCellValue(sheet, "F3", "备用")
	f.SetCellValue(sheet, "G3", "闲置")
	f.SetCellValue(sheet, "H3", "否")
	f.SetCellValue(sheet, "I3", "华为 S5700")
	f.SetCellValue(sheet, "N3", "资产台账里没有的设备，编码为人工自编")

	writeXLSX(w, f, "IT设备台账导入模板.xlsx")
}

// handleITExport 导出当前筛选条件下的 IT 台账。
//
// 与 handleExport 同一约定：**没有第二条取数路径**，全程走 ListITEquipment，
// 所以跟着列表一起受可见范围收窄 —— 受限账号导出的就是他自己那几条，
// 不会绕过列表把全量导走。
func (s *Server) handleITExport(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.itScope(w, r)
	if !ok {
		return
	}
	q := parseITListQuery(r)
	q.Page = 1
	q.PageSize = 500

	f := excelize.NewFile()
	defer f.Close()
	sheet := "IT设备台账"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成导出文件失败")
		return
	}
	f.SetActiveSheet(idx)
	f.DeleteSheet("Sheet1")
	for i, h := range itImportHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}

	row := 2
	for {
		res, err := s.st.ListITEquipment(q, sc)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询 IT 设备失败")
			return
		}
		for i := range res.Items {
			e := res.Items[i]
			vals := []any{
				e.AssetCode, e.UserEmpName, e.DeptName, e.DeviceType, e.Location,
				e.Purpose, e.Status, boolCN(e.IsXinchuang), e.BrandModel, e.CPU,
				e.MemoryGB, e.Disk, e.OSVersion, e.Remark,
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

	writeXLSX(w, f, fmt.Sprintf("IT设备台账_%s.xlsx", time.Now().Format("20060102")))
}

// boolCN 把布尔值写成中文，供导出与模板统一口径。
// 不用 true/false：现场表格里这一列写的是「是/否」，导出成 true/false 后
// 用户改完再导回来时，parseITBool 也得跟着认两种写法——不如一开始就只写中文。
func boolCN(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// parseITBool 解析「是否信创」列。容忍现场常见的几种写法。
// 空串返回 false（未勾选即非信创），不是错误——这一列漏填很常见。
func parseITBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "否", "n", "no", "false", "×", "x":
		return false, true
	case "1", "是", "y", "yes", "true", "√", "✓":
		return true, true
	}
	return false, false
}

// parseITImportRows 把 Excel 行解析成「已解析、待写入」的条目，并同时完成判重。
//
// # 判重规则（导入的核心，必须能反复导同一份文件而不产生重复）
//
//	① 资产编码**必填** —— 这是导入专用的约束，比页面表单更严，理由见下。
//	② 文件内编码不得重复。
//	③ 编码命中 asset_card.asset_code → 关联该卡（card_id），且 asset_code 以卡为准。
//	④ 编码命中 it_equipment.asset_code，或 ③ 关联到的卡已有 IT 记录 → **更新**那一条。
//	⑤ 都没命中 → 新增。
//
// 为什么导入要求编码必填，而页面表单允许留空（用户已确认「可留空，也支持手工编码」）：
// 导入是**批量、可重复**的动作，而 asset_code 是唯一的匹配键。编码允许为空的话，
// 同一份文件导两次就会把每台无卡设备复制一遍，而且**不会有任何报错**——
// 静默重复是导入最危险的失败方式（对照 api/importexport.go 里 financeHeaderAliases
// 注释中「静默忽略」的同款判断）。所以这里选择「当场报错」：
// 无卡设备请在这列填一个自有编码（如 IT-2026-0007），页面表单则仍然可以留空。
func (s *Server) parseITImportRows(rows [][]string) ([]model.ITImportItem, []importError) {
	var out []model.ITImportItem
	var errs []importError
	seenCode := map[string]int{}

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

		var e model.ITEquipment
		e.AssetCode = get(0)
		if e.AssetCode == "" {
			errs = append(errs, importError{rowNo, "资产编码", "必填（无卡设备也请填一个自有编码，否则无法判重）"})
		} else if prev, dup := seenCode[e.AssetCode]; dup {
			errs = append(errs, importError{rowNo, "资产编码", fmt.Sprintf("与第 %d 行重复", prev)})
		} else {
			seenCode[e.AssetCode] = rowNo
		}

		e.UserEmpID = s.lookupOrErr(&errs, rowNo, "使用人", get(1), s.st.LookupEmployeeByName)
		e.DeptID = s.lookupOrErr(&errs, rowNo, "部门", get(2), s.st.LookupDepartmentByName)

		e.DeviceType = get(3)
		if !model.IsValidITDeviceType(e.DeviceType) {
			errs = append(errs, importError{rowNo, "电脑类型", "取值非法：" + e.DeviceType})
		}
		e.Location = get(4)

		e.Purpose = get(5)
		if !model.IsValidITPurpose(e.Purpose) {
			errs = append(errs, importError{rowNo, "用途", "取值非法：" + e.Purpose})
		}

		e.Status = get(6)
		if e.Status == "" {
			e.Status = model.ITStatusInUse
		} else if !model.IsValidITStatus(e.Status) {
			errs = append(errs, importError{rowNo, "状态", "取值非法：" + e.Status})
		}

		if b, ok := parseITBool(get(7)); ok {
			e.IsXinchuang = b
		} else {
			errs = append(errs, importError{rowNo, "是否信创", "取值非法（应为 是/否）：" + get(7)})
		}

		e.BrandModel, e.CPU, e.Disk, e.OSVersion = get(8), get(9), get(11), get(12)
		if v := get(10); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, importError{rowNo, "内存容量(GB)", "不是整数：" + v})
			} else if n < 0 || n > itMaxMemoryGB {
				errs = append(errs, importError{rowNo, "内存容量(GB)", "需在 0 ~ 10240 之间：" + v})
			} else {
				e.MemoryGB = n
			}
		}
		e.Remark = get(13)

		// —— 判重：先看资产台账里有没有这张卡 ——
		if e.AssetCode != "" {
			card, err := s.st.GetCardByCode(e.AssetCode)
			if err != nil {
				errs = append(errs, importError{rowNo, "资产编码", "查询资产台账失败：" + err.Error()})
			} else if card != nil {
				e.CardID = card.ID
				// asset_code 以卡为准（与 api/it.go 的 resolveITCardLink 同一口径）：
				// 冗余快照的真相在 asset_card。
				e.AssetCode = card.AssetCode
			}
		}

		// —— 判重：再看 IT 台账里有没有已登记的记录 ——
		existing, err := s.findExistingIT(rowNo, &e, &errs)
		if err != nil {
			errs = append(errs, importError{rowNo, "资产编码", "查询 IT 台账失败：" + err.Error()})
		} else {
			out = append(out, model.ITImportItem{ExistingID: existing, Equipment: e})
		}
	}
	return out, errs
}

// findExistingIT 找出这一行应当**更新**的那条 IT 记录 id（0 = 新增）。
//
// 优先按关联卡匹配（卡是强关联、更可靠），再按资产编码匹配（无卡设备只有编码可用）。
// 编码匹配到多条时**报错而不是随便取一条**：asset_code 没有唯一约束，
// 静默取第一条会把数据写到错误的行上，而这在导入里是最难发现的错误。
func (s *Server) findExistingIT(rowNo int, e *model.ITEquipment, errs *[]importError) (int64, error) {
	if e.CardID > 0 {
		dup, err := s.st.GetITEquipmentByCardID(e.CardID)
		if err != nil {
			return 0, err
		}
		if dup != nil {
			return dup.ID, nil
		}
	}
	if e.AssetCode == "" {
		return 0, nil
	}
	list, err := s.st.FindITEquipmentByAssetCode(e.AssetCode)
	if err != nil {
		return 0, err
	}
	switch len(list) {
	case 0:
		return 0, nil
	case 1:
		return list[0].ID, nil
	default:
		*errs = append(*errs, importError{rowNo, "资产编码",
			fmt.Sprintf("IT 台账里有 %d 条同编码记录，无法判定更新哪一条，请先手工合并", len(list))})
		return 0, nil
	}
}

func (s *Server) handleITImport(w http.ResponseWriter, r *http.Request) {
	rows, ok := s.openUploadedSheet(w, r)
	if !ok {
		return
	}

	items, errs := s.parseITImportRows(rows)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  fmt.Sprintf("共 %d 处问题，未写入任何数据", len(errs)),
			"errors": errs,
		})
		return
	}

	created, updated, err := s.st.ImportITEquipment(items, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "导入失败，已回滚："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated})
}
