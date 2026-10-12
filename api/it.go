package api

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"

	"asset-mgr/model"
)

// —— IT 信息设备台账接口（2026-10-10 需求）——
//
// 权限与范围的分工，与资产台账 / 维修单完全一致：
//   - **读接口不挂 requirePerm**（能进这个页面就该能看），可见范围在**数据行上**收窄
//     （列表走 store/it.go 的 itScopeConds，点查走 denyITOutOfScope）。
//   - **写接口挂 requirePerm(model.PermITManage)**，当前只有 it_admin 与 admin 持有。
//
// 为什么不给写接口挂 requireAdmin：IT 台账是**可恢复的常规主数据**（删错了重录一条即可），
// 不需要「硬判角色」这道闸门。对照：维修单物理删除用的是 requireAdmin，因为它不可恢复
// （见 api/server.go 的 requireAdmin 注释）。

func parseITListQuery(r *http.Request) model.ITListQuery {
	v := r.URL.Query()
	q := model.ITListQuery{
		Keyword:    strings.TrimSpace(v.Get("keyword")),
		DeviceType: strings.TrimSpace(v.Get("device_type")),
		Purpose:    strings.TrimSpace(v.Get("purpose")),
		AssetCode:  strings.TrimSpace(v.Get("asset_code")),
		BrandModel: strings.TrimSpace(v.Get("brand_model")),
		SortBy:     v.Get("sort_by"),
		SortDesc:   v.Get("sort_desc") == "1" || v.Get("sort_desc") == "true",
	}
	if raw := v["status"]; len(raw) > 0 {
		for _, s := range raw {
			for _, one := range strings.Split(s, ",") {
				if one = strings.TrimSpace(one); one != "" {
					q.Status = append(q.Status, one)
				}
			}
		}
	}
	q.DeptID = atoi64(v.Get("dept_id"))
	q.UserEmpID = atoi64(v.Get("user_emp_id"))
	q.CardID = atoi64(v.Get("card_id"))
	// 三个可选布尔筛选用指针，理由与 parseListQuery 的 repairable 完全相同：
	// 参数缺失或空串一律当「不筛选」，绝不默认成 false —— 否则 GET /api/it-equipment
	// 会被静默收窄成「只要非信创 / 只要无卡的」，那是数据范围的破坏性变更。
	switch v.Get("is_xinchuang") {
	case "1", "true":
		t := true
		q.IsXinchuang = &t
	case "0", "false":
		f := false
		q.IsXinchuang = &f
	}
	switch v.Get("linked") {
	case "1", "true":
		t := true
		q.Linked = &t
	case "0", "false":
		f := false
		q.Linked = &f
	}
	q.Page = int(atoi64(v.Get("page")))
	q.PageSize = int(atoi64(v.Get("page_size")))
	return q
}

func (s *Server) handleListITEquipment(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.itScope(w, r)
	if !ok {
		return
	}
	res, err := s.st.ListITEquipment(parseITListQuery(r), sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询 IT 设备台账失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetITEquipment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	sc, ok := s.itScope(w, r)
	if !ok {
		return
	}
	e, err := s.st.GetITEquipment(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询 IT 设备失败")
		return
	}
	if e == nil {
		writeErr(w, http.StatusNotFound, "IT 设备记录不存在")
		return
	}
	if denyITOutOfScope(w, e, sc) {
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// itMaxMemoryGB 是内存容量的上界，纯属**防手滑**：把「16」敲成「1600」或把容量填成
// 硬盘的 GB 数（如 1024）都会落进这个区间之外或边缘。
// 上界取 10240（10 TB）—— 现实中的服务器内存远达不到，但留足余量，
// 不会误伤将来真出现的超大内存机型。
const itMaxMemoryGB = 10240

// validateITEquipment 校验并**就地补全** IT 记录。返回非空即校验失败（文案直接给用户看）。
//
// 语义：三个枚举字段一律「空串放行、非空必须在枚举内」——理由见 model/it.go 的
// IsValidITDeviceType 注释（「从资产台账关联生成」时这些字段本来就是空的，等着人来补）。
// 状态是例外：空串会被补成默认值「在用」，保证库里不出现状态为空的行。
//
// 返回 (bool, string) 的第二项表示「card_id 有变化，调用方需要据此判重 / 回滚」，
// 这里不返回 —— 判重放在 handler 里，因为那需要查库。
func validateITEquipment(e *model.ITEquipment) string {
	trimAll(&e.AssetCode, &e.DeviceType, &e.Location, &e.Purpose, &e.Status,
		&e.BrandModel, &e.CPU, &e.Disk, &e.OSVersion, &e.Remark)

	if !model.IsValidITDeviceType(e.DeviceType) {
		return "电脑类型取值非法：" + e.DeviceType
	}
	if !model.IsValidITPurpose(e.Purpose) {
		return "用途取值非法：" + e.Purpose
	}
	if e.Status == "" {
		e.Status = model.ITStatusInUse
	}
	if !model.IsValidITStatus(e.Status) {
		return "状态取值非法：" + e.Status
	}
	if e.MemoryGB < 0 || e.MemoryGB > itMaxMemoryGB {
		return "内存容量需在 0 ~ 10240 GB 之间"
	}
	// 只挡异常长度，不做枚举白名单：品牌型号是自由文本（现场叫法五花八门），
	// 与 api/card.go 里 FinAssetType 的处理同理。
	if len([]rune(e.BrandModel)) > 128 {
		return "品牌型号过长"
	}
	if len([]rune(e.CPU)) > 128 {
		return "CPU 型号过长"
	}
	if len([]rune(e.Disk)) > 128 {
		return "硬盘类型及容量过长"
	}
	if len([]rune(e.OSVersion)) > 64 {
		return "系统版本过长"
	}
	if len([]rune(e.Remark)) > 500 {
		return "备注过长"
	}
	return ""
}

// resolveITCardLink 在 card_id > 0 时校验关联的资产卡存在，并**以库为准**回填资产编码。
//
// 为什么资产编码以库为准而不是信前端：asset_code 是这条 IT 记录与资产台账之间的
// 冗余快照，它的真相在 asset_card。若允许前端随意填，就会出现「关联了卡 A，却显示卡 B 的编码」
// 这种自相矛盾的数据，而这正是导入匹配与人工核对时的依据。
//
// 返回 (ok, errMsg)。errMsg 非空表示应当以 400 返回给用户。
func (s *Server) resolveITCardLink(e *model.ITEquipment) (bool, string) {
	if e.CardID <= 0 {
		// 未关联：asset_code 保持用户填的值（可留空，也可手工编一个自有编码）。
		return true, ""
	}
	card, err := s.st.GetCard(e.CardID)
	if err != nil {
		return false, "查询关联资产失败：" + err.Error()
	}
	if card == nil {
		return false, "关联的资产不存在（card_id=" + strconv.FormatInt(e.CardID, 10) + "）"
	}
	e.AssetCode = card.AssetCode
	return true, ""
}

func (s *Server) handleCreateITEquipment(w http.ResponseWriter, r *http.Request) {
	var e model.ITEquipment
	if err := readJSON(w, r, &e); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 主键与派生字段由服务端决定，请求里带了也忽略：
	//   - ID 是自增主键；
	//   - AssetName / UserEmpName / DeptName 是 LEFT JOIN 派生字段，不落库（见 model/it.go）。
	// 这里显式清零而不是靠「反正不写库」——避免它们出现在响应里造成误解。
	e.ID = 0
	e.AssetName, e.UserEmpName, e.DeptName = "", "", ""

	if msg := validateITEquipment(&e); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if ok, msg := s.resolveITCardLink(&e); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// 判重放在 INSERT 之前：一张资产卡最多一条 IT 记录（uk_it_card）。
	// 先查一次能给出「已登记过、id 是几」这种可操作的信息，
	// 而不是把唯一键冲突的原始报错抛给用户。
	if e.CardID > 0 {
		if dup, err := s.st.GetITEquipmentByCardID(e.CardID); err != nil {
			writeErr(w, http.StatusInternalServerError, "校验关联失败："+err.Error())
			return
		} else if dup != nil {
			writeErr(w, http.StatusConflict,
				"这张资产（"+dup.AssetCode+"）已登记过 IT 设备信息，请直接编辑那条记录")
			return
		}
	}
	id, err := s.st.CreateITEquipment(&e, operatorOf(r))
	if err != nil {
		if isDuplicate(err) {
			// 兜底：并发下两个请求可能同时通过上面的判重。
			writeErr(w, http.StatusConflict, "这张资产已登记过 IT 设备信息")
			return
		}
		writeErr(w, http.StatusInternalServerError, "新增 IT 设备失败："+err.Error())
		return
	}
	log.Printf("审计: 操作人=%s 新增 IT 设备记录 id=%d 资产编码=%q 关联卡=%d 使用人=%d",
		operatorOf(r), id, e.AssetCode, e.CardID, e.UserEmpID)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleUpdateITEquipment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	var e model.ITEquipment
	if err := readJSON(w, r, &e); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	e.ID = id
	e.AssetName, e.UserEmpName, e.DeptName = "", "", ""

	if msg := validateITEquipment(&e); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if ok, msg := s.resolveITCardLink(&e); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// 判重只针对「关联到另一条记录已占用的卡」。指向自己那条是合法的（改属性时 card_id 不变），
	// 故必须比较 id，不能只判断「有没有记录」。
	if e.CardID > 0 {
		dup, err := s.st.GetITEquipmentByCardID(e.CardID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验关联失败："+err.Error())
			return
		}
		if dup != nil && dup.ID != id {
			writeErr(w, http.StatusConflict,
				"这张资产（"+dup.AssetCode+"）已被另一条 IT 记录关联，请先解除那一条的关联")
			return
		}
	}
	if err := s.st.UpdateITEquipment(id, &e, operatorOf(r)); err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "IT 设备记录不存在")
			return
		}
		if isDuplicate(err) {
			writeErr(w, http.StatusConflict, "这张资产已被另一条 IT 记录关联")
			return
		}
		writeErr(w, http.StatusInternalServerError, "更新 IT 设备失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleDeleteITEquipment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	// 删除前先把记录读出来：审计日志要留下「删掉的是哪台、谁在用」。
	// 这是本仓审计日志的一贯做法（对照维修单物理删除逐单记 code+状态）。
	e, err := s.st.GetITEquipment(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询 IT 设备失败")
		return
	}
	if e == nil {
		writeErr(w, http.StatusNotFound, "IT 设备记录不存在")
		return
	}
	if err := s.st.DeleteITEquipment(id); err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "IT 设备记录不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除 IT 设备失败："+err.Error())
		return
	}
	log.Printf("审计: 操作人=%s 物理删除 IT 设备记录 id=%d 资产编码=%q 关联卡=%d 使用人=%d 品牌型号=%q",
		operatorOf(r), id, e.AssetCode, e.CardID, e.UserEmpID, e.BrandModel)
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}
