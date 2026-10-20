package handler

import (
	"log"
	"net/http"
	"strconv"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ComponentList 列出全部组件。
func ComponentList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Component.List(r.Context(), reqScope(r))
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// ComponentGet 获取单个组件。
func ComponentGet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		c, err := s.Component.Get(r.Context(), reqScope(r), id)
		if err != nil {
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		httpx.OK(w, c)
	}
}

// ComponentCreate 新建组件。
func ComponentCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c component.Component
		if err := httpx.DecodeJSON(r, &c); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if c.Name == "" {
			httpx.Fail(w, http.StatusBadRequest, "name 必填")
			return
		}
		id, err := s.Component.Create(r.Context(), reqScope(r), &c)
		auditOp(s, r, audit.ActionComponentCreate, "component", strconv.FormatInt(id, 10), c.Name, err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// ComponentUpdate 更新组件。
func ComponentUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var c component.Component
		if err := httpx.DecodeJSON(r, &c); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		c.ID = id
		err := s.Component.Update(r.Context(), reqScope(r), &c)
		auditOp(s, r, audit.ActionComponentUpdate, "component", strconv.FormatInt(id, 10), c.Name, err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// ComponentDelete 删除组件。
func ComponentDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		// 先清理制品磁盘文件，再删组件；否则文件会永久残留（DB 行由 CASCADE 删除）。
		if perr := s.Artifact.PurgeByComponent(r.Context(), reqScope(r), id); perr != nil {
			log.Printf("⚠️ 清理组件 %d 的制品文件失败: %v", id, perr)
		}
		err := s.Component.Delete(r.Context(), reqScope(r), id)
		auditOp(s, r, audit.ActionComponentDelete, "component", strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}
