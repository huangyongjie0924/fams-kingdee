package api

import (
	"net/http"
)

// handleRepairUpload 维修附件上传。**独立路由，不放宽 /api/upload**。
//
// 为什么必须独立（见 docs/维修流程模块架构建议.md §4.4）：现有 /api/upload 直接信任
// 表单里的 card_id 调 SaveAttachment，没有对象级鉴权；它现在靠 requirePerm(asset.manage)
// 挡着低权限用户。一旦放宽，任何登录用户就能给任意资产挂附件（IDOR）。
//
// 三层防护：
//  1. 认证：Authenticate 中间件统一强制（无 token → 401）。
//  2. 授权（功能级）：路由上 requireAnyPerm(repair.report, repair.handle)。
//  3. 授权（对象级）：本 handler 校验「当前用户是否有权给目标单据 / 资产上传」。
//
// 存储复用 uploads/ 目录、随机名规则、扩展名白名单、大小上限 —— 不引入新攻击面。
func (s *Server) handleRepairUpload(w http.ResponseWriter, r *http.Request) {
	// 先解析表单拿 repair_id / card_id，做完对象级鉴权再落盘，避免无权的上传留下孤儿文件。
	if err := r.ParseMultipartForm(s.cfg.Server.MaxUploadMB << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传解析失败，文件可能超出大小限制")
		return
	}
	me := userFrom(r)
	repairID := atoi64(r.FormValue("repair_id"))
	cardID := atoi64(r.FormValue("card_id"))

	if repairID > 0 {
		// 挂到已有单据：报修人 / 该单指派维修工 / 管理员才有权上传
		o, err := s.st.GetRepair(repairID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询维修单失败")
			return
		}
		if o == nil {
			writeErr(w, http.StatusNotFound, "维修单不存在")
			return
		}
		if !isRepairReporter(me, o) && !isRepairAssignee(me, o) && !canManageRepair(me.Role) {
			writeErr(w, http.StatusForbidden, "无权给该维修单上传附件")
			return
		}
		// 归属以单据为准，忽略表单里可能被篡改的 card_id，避免张冠李戴
		cardID = o.CardID
	} else {
		// 报修前预上传：repair_id=0 暂存，提交单据时回填（两段式上传）。
		// 此时没有单据可校验对象级权限，只要求资产真实存在；提交时才会真正绑定。
		if cardID <= 0 {
			writeErr(w, http.StatusBadRequest, "缺少 card_id")
			return
		}
		card, err := s.st.GetCard(cardID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询资产失败")
			return
		}
		if card == nil {
			writeErr(w, http.StatusNotFound, "资产不存在")
			return
		}
	}

	name, origin, ext, size, ok := s.saveUploadedFile(w, r)
	if !ok {
		return
	}
	kind := "file"
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		kind = "photo"
	}
	id, err := s.st.SaveRepairAttachment(repairID, cardID, kind, origin, name, size, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "登记附件失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "url": "/uploads/" + name, "kind": kind, "size": size,
	})
}
