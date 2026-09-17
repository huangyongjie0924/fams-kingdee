package api

import (
	"database/sql"
	"net/http"

	"asset-mgr/model"
)

func (s *Server) listJSON(w http.ResponseWriter, v any, err error, what string) {
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询"+what+"失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) saved(w http.ResponseWriter, id int64, err error, what string) {
	if err != nil {
		if isDuplicate(err) {
			writeErr(w, http.StatusConflict, what+"名称已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "保存"+what+"失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) deleted(w http.ResponseWriter, err error, what string) {
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, what+"不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListCategories()
	s.listJSON(w, list, err, "资产分类")
}

func (s *Server) handleSaveCategory(w http.ResponseWriter, r *http.Request) {
	var n model.TreeNode
	if err := readJSON(w, r, &n); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&n.Name, &n.Code)
	if n.Name == "" {
		writeErr(w, http.StatusBadRequest, "分类名称必填")
		return
	}
	id, err := s.st.SaveCategory(&n)
	s.saved(w, id, err, "资产分类")
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteCategory(id), "资产分类")
}

func (s *Server) handleListAreas(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListAreas()
	s.listJSON(w, list, err, "区域")
}

func (s *Server) handleSaveArea(w http.ResponseWriter, r *http.Request) {
	var n model.TreeNode
	if err := readJSON(w, r, &n); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&n.Name, &n.ShortName, &n.Code)
	if n.Name == "" {
		writeErr(w, http.StatusBadRequest, "区域名称必填")
		return
	}
	id, err := s.st.SaveArea(&n)
	s.saved(w, id, err, "区域")
}

func (s *Server) handleDeleteArea(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteArea(id), "区域")
}

func (s *Server) handleListCompanies(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListCompanies()
	s.listJSON(w, list, err, "公司")
}

func (s *Server) handleSaveCompany(w http.ResponseWriter, r *http.Request) {
	var c model.Company
	if err := readJSON(w, r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&c.Name, &c.Code, &c.TaxNo)
	if c.Name == "" {
		writeErr(w, http.StatusBadRequest, "公司名称必填")
		return
	}
	id, err := s.st.SaveCompany(&c)
	s.saved(w, id, err, "公司")
}

func (s *Server) handleDeleteCompany(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteCompany(id), "公司")
}

func (s *Server) handleListDepartments(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListDepartments()
	s.listJSON(w, list, err, "部门")
}

func (s *Server) handleSaveDepartment(w http.ResponseWriter, r *http.Request) {
	var d model.Department
	if err := readJSON(w, r, &d); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&d.Name, &d.Code)
	if d.Name == "" {
		writeErr(w, http.StatusBadRequest, "部门名称必填")
		return
	}
	id, err := s.st.SaveDepartment(&d)
	s.saved(w, id, err, "部门")
}

func (s *Server) handleDeleteDepartment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteDepartment(id), "部门")
}

func (s *Server) handleListEmployees(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListEmployees(r.URL.Query().Get("keyword"))
	s.listJSON(w, list, err, "员工")
}

func (s *Server) handleSaveEmployee(w http.ResponseWriter, r *http.Request) {
	var e model.Employee
	if err := readJSON(w, r, &e); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&e.Name, &e.EmpNo, &e.Phone)
	if e.Name == "" {
		writeErr(w, http.StatusBadRequest, "员工姓名必填")
		return
	}
	id, err := s.st.SaveEmployee(&e)
	s.saved(w, id, err, "员工")
}

func (s *Server) handleDeleteEmployee(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteEmployee(id), "员工")
}

func (s *Server) handleListVendors(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListVendors()
	s.listJSON(w, list, err, "供应商")
}

func (s *Server) handleSaveVendor(w http.ResponseWriter, r *http.Request) {
	var v model.Vendor
	if err := readJSON(w, r, &v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&v.Name, &v.Code, &v.Contact, &v.Phone, &v.Remark)
	if v.Name == "" {
		writeErr(w, http.StatusBadRequest, "供应商名称必填")
		return
	}
	id, err := s.st.SaveVendor(&v)
	s.saved(w, id, err, "供应商")
}

func (s *Server) handleDeleteVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteVendor(id), "供应商")
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListTags()
	s.listJSON(w, list, err, "标签")
}

func (s *Server) handleSaveTag(w http.ResponseWriter, r *http.Request) {
	var t model.Tag
	if err := readJSON(w, r, &t); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&t.Name, &t.Color)
	if t.Name == "" {
		writeErr(w, http.StatusBadRequest, "标签名称必填")
		return
	}
	id, err := s.st.SaveTag(&t)
	s.saved(w, id, err, "标签")
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	s.deleted(w, s.st.DeleteTag(id), "标签")
}
