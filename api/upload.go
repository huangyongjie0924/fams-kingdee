package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 只允许这些扩展名，落盘用随机名，不使用上传的原始文件名
var allowedUploadExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".pdf": true, ".xlsx": true,
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.Server.MaxUploadMB << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传解析失败，文件可能超出大小限制")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少上传文件")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedUploadExt[ext] {
		writeErr(w, http.StatusBadRequest, "不支持的文件类型："+ext)
		return
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		writeErr(w, http.StatusInternalServerError, "生成文件名失败")
		return
	}
	name := hex.EncodeToString(buf) + ext

	if err := os.MkdirAll(s.cfg.Server.UploadDir, 0o750); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建上传目录失败")
		return
	}
	dst, err := os.Create(filepath.Join(s.cfg.Server.UploadDir, name))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存文件失败")
		return
	}
	defer dst.Close()

	limited := io.LimitReader(file, s.cfg.Server.MaxUploadMB<<20)
	size, err := io.Copy(dst, limited)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入文件失败")
		return
	}

	cardID := atoi64(r.FormValue("card_id"))
	kind := "file"
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		kind = "photo"
	}
	id, err := s.st.SaveAttachment(cardID, kind, header.Filename, name, size, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "登记附件失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "url": "/uploads/" + name, "kind": kind, "size": size,
	})
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	if !s.checkCardScope(w, r, id) {
		return
	}
	list, err := s.st.ListAttachments(id)
	s.listJSON(w, list, err, "附件")
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	stored, err := s.st.DeleteAttachment(id)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "附件不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "删除附件失败")
		return
	}
	// stored 是上传时生成的随机名，不含用户输入，仍用 Base 兜一层防目录穿越
	if stored != "" {
		os.Remove(filepath.Join(s.cfg.Server.UploadDir, filepath.Base(stored)))
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}
