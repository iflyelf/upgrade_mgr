package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ArtifactList 列出制品（?component_id= 可选）。
func ArtifactList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.ParseInt(r.URL.Query().Get("component_id"), 10, 64)
		list, err := s.Artifact.List(r.Context(), reqScope(r), cid)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// ArtifactUpload 上传制品（multipart: file + 表单字段）。
func ArtifactUpload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 防滥用：限制单文件大小，避免填满磁盘或消耗带宽。
		maxSize := s.Config.Artifact.MaxSize
		if maxSize <= 0 {
			maxSize = 2 << 30 // 默认 2GB
		}
		if err := r.ParseMultipartForm(maxSize); err != nil {
			httpx.FailErr(w, http.StatusBadRequest, fmt.Errorf("解析上传失败: %w", err))
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, "缺少 file 字段")
			return
		}
		defer file.Close()
		// 二次校验：header.Size 可能被伪造，但 ParseMultipartForm 已限制总量，此处拒绝明显超标的。
		if header.Size > maxSize {
			httpx.Fail(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("文件超过限制（%d MB）", maxSize>>20))
			return
		}

		cid, _ := strconv.ParseInt(r.FormValue("component_id"), 10, 64)
		meta := &artifact.Artifact{
			ComponentID: cid,
			Version:     r.FormValue("version"),
			OSFamily:    r.FormValue("os_family"),
			Arch:        r.FormValue("arch"),
			Filename:    header.Filename,
			InnerPath:   r.FormValue("inner_path"),
			Notes:       r.FormValue("notes"),
		}
		saved, err := s.Artifact.Save(r.Context(), reqScope(r), meta, file)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, saved)
	}
}

// ArtifactDownload 下载制品（支持 Range 断点续传，供 Agent 与 wget 续传）。
func ArtifactDownload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		// Agent 凭据决定可见租户；未认证或归属不明则 fail-close（查不到制品）。
		sc := agentScope(s, r)
		a, err := s.Artifact.Get(r.Context(), sc, id)
		if err != nil {
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		// 经存储后端读取（local 直读文件，s3/oss 落临时文件），统一支持 Range。
		f, err := s.Artifact.Open(a)
		if err != nil {
			httpx.Fail(w, http.StatusNotFound, "制品文件丢失")
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-MD5", a.MD5)
		w.Header().Set("X-SHA256", a.SHA256)
		// 文件名已在上传时经 SanitizeFilename 白名单清洗，此处仍转义引号防响应头注入。
		w.Header().Set("Content-Disposition",
			"attachment; filename=\""+strings.ReplaceAll(a.Filename, "\"", "")+"\"")
		http.ServeContent(w, r, a.Filename, time.Time{}, f) // 自动处理 Range
	}
}

// ArtifactListPaged 制品分页列表（组件过滤 + 关键字搜索）。
func ArtifactListPaged(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.ParseInt(r.URL.Query().Get("component_id"), 10, 64)
		page := atoiDefault(r.URL.Query().Get("page"), 1)
		size := atoiDefault(r.URL.Query().Get("size"), 50)
		list, total, err := s.Artifact.ListPaged(r.Context(), reqScope(r), cid,
			r.URL.Query().Get("q"), page, size)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if page < 1 {
			page = 1
		}
		if size < 1 {
			size = 50
		}
		if size > 200 {
			size = 200
		}
		pages := (total + size - 1) / size
		if pages < 1 {
			pages = 1
		}
		if page > pages {
			page = pages
		}
		httpx.OK(w, map[string]any{
			"list": list, "total": total, "page": page, "size": size, "pages": pages,
		})
	}
}
