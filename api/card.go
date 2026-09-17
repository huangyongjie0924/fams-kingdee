package api

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"asset-mgr/model"
)

func parseListQuery(r *http.Request) model.ListQuery {
	v := r.URL.Query()
	q := model.ListQuery{
		Keyword:      strings.TrimSpace(v.Get("keyword")),
		Source:       v.Get("source"),
		FinStatus:    v.Get("fin_status"),
		AssetCode:    strings.TrimSpace(v.Get("asset_code")),
		Name:         strings.TrimSpace(v.Get("name")),
		SerialNo:     strings.TrimSpace(v.Get("serial_no")),
		ManagerName:  strings.TrimSpace(v.Get("manager_name")),
		UserName:     strings.TrimSpace(v.Get("user_name")),
		Location:     strings.TrimSpace(v.Get("location")),
		PurchaseFrom: v.Get("purchase_from"),
		PurchaseTo:   v.Get("purchase_to"),
		SortBy:       v.Get("sort_by"),
		SortDesc:     v.Get("sort_desc") == "1" || v.Get("sort_desc") == "true",
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
	q.CategoryID = atoi64(v.Get("category_id"))
	q.AreaID = atoi64(v.Get("area_id"))
	q.UseDeptID = atoi64(v.Get("use_dept_id"))
	q.UseCompanyID = atoi64(v.Get("use_company_id"))
	if f, err := strconv.ParseFloat(v.Get("amount_min"), 64); err == nil {
		q.AmountMin = &f
	}
	if f, err := strconv.ParseFloat(v.Get("amount_max"), 64); err == nil {
		q.AmountMax = &f
	}
	q.Page = int(atoi64(v.Get("page")))
	q.PageSize = int(atoi64(v.Get("page_size")))
	return q
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func (s *Server) handleListCards(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	res, err := s.st.ListCards(parseListQuery(r), sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产列表失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	sc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	c, err := s.st.GetCard(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产失败")
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "资产不存在")
		return
	}
	if denyOutOfScope(w, c, sc) {
		return
	}
	// 单卡详情额外告诉前端这张卡是否由金蝶托管，用于决定「数量」能不能编辑。
	// 列表接口不需要，所以不放进 cardSelect，避免每次列表查询都多一个子查询。
	if synced, err := s.st.IsCardSynced(id); err == nil {
		c.Synced = synced
	}
	writeJSON(w, http.StatusOK, c)
}

// validateCard 只校验必填与枚举，业务规则（净值计算）由前端联动 + 这里兜底
func validateCard(c *model.AssetCard) string {
	trimAll(&c.AssetCode, &c.Name, &c.Spec, &c.SerialNo, &c.Unit, &c.Location, &c.RFID)
	if c.Name == "" {
		return "资产名称必填"
	}
	if c.CategoryID <= 0 {
		return "资产类别必填"
	}
	if c.Status == "" {
		c.Status = model.StatusIdle
	}
	if !contains(model.AssetStatuses, c.Status) {
		return "状态取值非法"
	}
	if c.Source != "" && !model.IsValidSource(c.Source) {
		return "来源取值非法"
	}
	// 资产类型不做枚举白名单校验，只挡异常长度：
	// 表单里这个字段是 allow-create（用户可以自己敲新值），而且同步会把金蝶的资产类别名
	// （如「房屋及建筑物」）直接填进来，本来就超出 FinAssetTypes 那三个值。
	// 卡着枚举校验的话，同步卡在表单里改任何字段都会被挡回去。
	if len([]rune(c.FinAssetType)) > 32 {
		return "资产类型过长"
	}
	if c.FinStatus == "" {
		c.FinStatus = "未入账"
	}
	if !contains(model.FinStatuses, c.FinStatus) {
		return "财务信息状态取值非法"
	}
	if c.Amount < 0 || c.FinOriginalValue < 0 {
		return "金额不能为负数"
	}
	if c.Quantity < 0 {
		return "数量不能为负数"
	}
	if c.FinNetValue == 0 && c.FinOriginalValue > 0 {
		c.FinNetValue = c.FinOriginalValue - c.FinAccumDepreciaton
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var c model.AssetCard
	if err := readJSON(w, r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := validateCard(&c); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.st.CreateCard(&c, operatorOf(r))
	if err != nil {
		if isDuplicate(err) {
			writeErr(w, http.StatusConflict, "资产编码已存在："+c.AssetCode)
			return
		}
		writeErr(w, http.StatusInternalServerError, "新增资产失败："+err.Error())
		return
	}
	if err := s.st.BindAttachments(id, c.AttachmentIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "关联附件失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "asset_code": c.AssetCode})
}

func (s *Server) handleUpdateCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	var c model.AssetCard
	if err := readJSON(w, r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := validateCard(&c); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// 金蝶托管的卡，「数量」以星瀚为准：忽略请求里的值，沿用库里现有的。
	// 前端已经置灰了，这层是防止绕过界面直接调接口把托管字段改脏。
	if synced, err := s.st.IsCardSynced(id); err == nil && synced {
		if old, err := s.st.GetCard(id); err == nil && old != nil {
			c.Quantity = old.Quantity
		}
	}
	if err := s.st.UpdateCard(id, &c, operatorOf(r)); err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "资产不存在")
			return
		}
		if isDuplicate(err) {
			writeErr(w, http.StatusConflict, "资产编码已存在："+c.AssetCode)
			return
		}
		writeErr(w, http.StatusInternalServerError, "更新资产失败："+err.Error())
		return
	}
	if err := s.st.BindAttachments(id, c.AttachmentIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "关联附件失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	if err := s.st.DeleteCard(id, operatorOf(r)); err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "资产不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除资产失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "请先勾选资产")
		return
	}
	if !contains(model.AssetStatuses, req.Status) {
		writeErr(w, http.StatusBadRequest, "状态取值非法："+req.Status)
		return
	}
	n, err := s.st.UpdateCardStatus(req.IDs, req.Status, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "调整状态失败，已回滚："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n, "skipped": len(req.IDs) - n})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	if !s.checkCardScope(w, r, id) {
		return
	}
	list, err := s.st.ListHistory(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询履历失败")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleNextCode(w http.ResponseWriter, r *http.Request) {
	code, err := s.st.NextCode(atoi64(r.URL.Query().Get("category_id")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成编码失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"asset_code": code})
}

func isDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}
