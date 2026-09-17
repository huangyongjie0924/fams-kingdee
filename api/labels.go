package api

import (
	"net/http"
	"strings"

	"asset-mgr/model"
)

// 二维码标签相关的三个只读查询。都刻意不加 requirePerm —— 与
// GET /api/assets/{id}/history、GET /api/assets/export 一致：能看到资产列表的角色
// 就应该能扫码、能打标签。

// handleGetCardByCode 扫码后按编码定位唯一一台资产。
// 路径用查询串而不是 /by-code/{code}：后者会和已有的 /api/assets/{id}/history、
// /api/assets/{id}/attachments 在 ServeMux 里构成模式冲突，注册时直接 panic。
func (s *Server) handleGetCardByCode(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		writeErr(w, http.StatusBadRequest, "缺少资产编码")
		return
	}
	sc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	c, err := s.st.GetCardByCode(code)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产失败")
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "资产编码不存在："+code)
		return
	}
	if denyOutOfScope(w, c, sc) {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleListCardsByIDs 标签批量打印取数。ids 形如 "1,2,3"，上限 200 张一页。
func (s *Server) handleListCardsByIDs(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		writeErr(w, http.StatusBadRequest, "缺少 ids")
		return
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		if id := atoi64(p); id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "ids 里没有合法的资产 ID")
		return
	}
	if len(ids) > 200 {
		writeErr(w, http.StatusBadRequest, "一次最多打印 200 张标签")
		return
	}
	sc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	cards, err := s.st.GetCardsByIDs(ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产失败")
		return
	}
	// 范围外的静默丢掉：批量打印时混进一两条只该少印一张，
	// 整批 403 会把看得见的那些也一起挡掉。GetCardsByIDs 的入参序保持不变。
	inScope := cards[:0]
	for i := range cards {
		if sc.Allows(&cards[i]) {
			inScope = append(inScope, cards[i])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": inScope})
}

// handleCardCountItems 这台资产当前落在哪些「盘点中」的计划里，供扫码后给「去盘点」入口。
// 盘点员只能看到指派给自己的明细，与 handleListCountItems 一个口径。
func (s *Server) handleCardCountItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	if !s.checkCardScope(w, r, id) {
		return
	}
	me := userFrom(r)
	var assigneeID int64
	if me.Role == model.RoleCounter {
		if me.EmployeeID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法盘点")
			return
		}
		assigneeID = me.EmployeeID
	}
	items, err := s.st.CountItemsByCard(id, assigneeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
