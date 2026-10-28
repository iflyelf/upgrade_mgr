package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
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
		// 限制本实例并发上传数：每个上传会持有一条 advisory lock 连接，
		// 槽位在解析 multipart 之前获取，避免排队请求先把临时文件写满磁盘。
		release, err := s.Artifact.AcquireUploadSlot(r.Context())
		if err != nil {
			httpx.Fail(w, http.StatusServiceUnavailable, "上传请求已取消或等待超时，请稍后重试")
			return
		}
		defer release()
		// 防滥用：限制单文件大小，避免填满磁盘或消耗带宽。
		maxSize := s.ConfigSnapshot().Artifact.MaxSize
		if maxSize <= 0 {
			maxSize = 2 << 30 // 默认 2GB
		}
		// ParseMultipartForm 的参数是「内存中保留的最大字节数」，不是请求体上限：
		// 直接传 maxSize 会让最大 2GB 的文件全部驻留内存，构成内存 DoS。
		// 这里用 MaxBytesReader 限制请求体总量，并只保留 32MB 在内存中，
		// 其余落临时文件；处理结束后统一删除临时文件。
		bodyLimit := maxSize
		if bodyLimit < (1 << 62) {
			bodyLimit += 1 << 20 // multipart 头部/字段开销
		}
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large") {
				httpx.Fail(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("文件超过限制（%d MB）", maxSize>>20))
				return
			}
			httpx.Fail(w, http.StatusBadRequest, "解析上传失败")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, "缺少 file 字段")
			return
		}
		defer file.Close()
		// 二次校验：header.Size 是解析后的真实文件大小，比请求体更精确。
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

// maxArtifactBatchDelete 单次批量删除的制品数上限（与分页最大条数一致）。
const maxArtifactBatchDelete = 200

// artifactDeleteStatus 把删除错误映射为 HTTP 状态与面向用户的消息。
func artifactDeleteStatus(err error) (int, string, bool) {
	switch {
	case errors.Is(err, artifact.ErrArtifactInUse):
		return http.StatusConflict, err.Error(), false
	case err.Error() == "制品不存在":
		return http.StatusNotFound, "制品不存在或无权访问", false
	case strings.Contains(err.Error(), "等待其他实例上传/删除同一组件制品超时"):
		return http.StatusConflict, "该组件正在上传或删除制品，请稍后重试", false
	}
	return http.StatusInternalServerError, "删除失败（服务端错误）", true
}

// artifactAuditName 审计里记录的制品名称：组件 ID / 版本 / 架构 / 文件名。
func artifactAuditName(a *artifact.Artifact) string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("component=%d %s/%s %s", a.ComponentID, a.Version, a.Arch, a.Filename)
}

// ArtifactDelete 删除单个制品：DELETE /api/artifacts/{id}（管理员，租户隔离）。
func ArtifactDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "制品 ID 无效")
			return
		}
		a, err := s.Artifact.Delete(r.Context(), reqScope(r), id)
		auditOp(s, r, audit.ActionArtifactDelete, "artifact", strconv.FormatInt(id, 10), artifactAuditName(a), err)
		if err != nil {
			status, msg, internal := artifactDeleteStatus(err)
			if internal {
				httpx.FailErr(w, status, err)
			} else {
				httpx.Fail(w, status, msg)
			}
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// ArtifactBatchDelete 批量删除制品：POST /api/artifacts/batch-delete {"ids":[1,2,3]}。
//
// 逐个复用单个删除的校验（租户隔离、组件锁、使用中拒绝），每个单独写审计；
// 某个失败不影响其他制品，返回成功列表与逐个失败原因。
func ArtifactBatchDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		ids := uniquePositiveIDs(req.IDs)
		if len(ids) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "请选择要删除的制品")
			return
		}
		if len(ids) > maxArtifactBatchDelete {
			httpx.Fail(w, http.StatusBadRequest, fmt.Sprintf("单次最多删除 %d 个制品", maxArtifactBatchDelete))
			return
		}
		type failure struct {
			ID    int64  `json:"id"`
			Error string `json:"error"`
		}
		deleted := make([]int64, 0, len(ids))
		failed := []failure{}
		sc := reqScope(r)
		for _, id := range ids {
			if r.Context().Err() != nil {
				failed = append(failed, failure{ID: id, Error: "请求已取消"})
				continue
			}
			a, err := s.Artifact.Delete(r.Context(), sc, id)
			auditOp(s, r, audit.ActionArtifactDelete, "artifact", strconv.FormatInt(id, 10), artifactAuditName(a), err)
			if err != nil {
				_, msg, _ := artifactDeleteStatus(err)
				failed = append(failed, failure{ID: id, Error: msg})
				continue
			}
			deleted = append(deleted, id)
		}
		httpx.OK(w, map[string]any{"deleted": deleted, "failed": failed})
	}
}
