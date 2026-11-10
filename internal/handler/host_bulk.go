package handler

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// hostBuiltinEditable 主机内置可编辑字段（与 HostUpdate 的白名单一致）。
var hostBuiltinEditable = []string{
	"hostname", "account", "app_code", "biz_group", "department", "vendor", "host_type",
}

// hostNetworkEditable 网络类可编辑字段。
var hostNetworkEditable = []string{"tunnel_ip", "server_url_override"}

// hostAllEditable 全部可编辑字段，供批量编辑/导入生成 SQL。
var hostAllEditable = append(append([]string{}, hostBuiltinEditable...), hostNetworkEditable...)

// normalizeBulkHostFields 校验并规范化批量编辑/导入的内置字段。
func normalizeBulkHostFields(fields map[string]string) (map[string]string, error) {
	req := &hostUpdateRequest{}
	pick := func(k string) *string {
		if v, ok := fields[k]; ok {
			return &v
		}
		return nil
	}
	req.Hostname = pick("hostname")
	req.Account = pick("account")
	req.AppCode = pick("app_code")
	req.BizGroup = pick("biz_group")
	req.Department = pick("department")
	req.Vendor = pick("vendor")
	req.HostType = pick("host_type")
	if err := normalizeHostFields(req.fields()); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, p := range map[string]*string{
		"hostname": req.Hostname, "account": req.Account, "app_code": req.AppCode,
		"biz_group": req.BizGroup, "department": req.Department, "vendor": req.Vendor,
		"host_type": req.HostType,
	} {
		if p != nil {
			out[k] = *p
		}
	}
	if v, ok := fields["tunnel_ip"]; ok {
		nv := hostnet.NormalizeExitIP(v)
		if nv == "" && strings.TrimSpace(v) != "" {
			return nil, fmt.Errorf("隧道 IP 不是合法的 IP 地址")
		}
		out["tunnel_ip"] = nv
	}
	if v, ok := fields["server_url_override"]; ok {
		nv, err := hostnet.NormalizeAddr(v)
		if err != nil {
			return nil, err
		}
		out["server_url_override"] = nv
	}
	return out, nil
}

// normalizeBulkCustomFields 按字段定义校验自定义字段。
func normalizeBulkCustomFields(ctx context.Context, s *svc.ServiceContext, custom map[string]string) (map[string]string, error) {
	if len(custom) == 0 {
		return nil, nil
	}
	repo, err := fielddef.NewRepo(s.DB, fielddef.KindHost)
	if err != nil {
		return nil, err
	}
	defs, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	return fielddef.ValidateValues(defs, custom)
}

// hostUpdateSets 生成 UPDATE SET 片段；返回 SQL、参数、下一个占位符序号。
func hostUpdateSets(fields, custom map[string]string, start int) (string, []any, int) {
	sets := make([]string, 0, len(fields)+2)
	args := make([]any, 0, len(fields)+1)
	i := start
	for _, key := range hostAllEditable {
		if v, ok := fields[key]; ok {
			sets = append(sets, key+"=$"+strconv.Itoa(i))
			args = append(args, v)
			i++
		}
	}
	if _, ok := fields["server_url_override"]; ok {
		sets = append(sets, "server_url_auto=FALSE")
	}
	if custom != nil {
		b, _ := json.Marshal(custom)
		sets = append(sets, "custom_fields=COALESCE(custom_fields,'{}'::jsonb)||$"+strconv.Itoa(i)+"::jsonb")
		args = append(args, string(b))
		i++
	}
	sets = append(sets, "updated_at=NOW()")
	return strings.Join(sets, ", "), args, i
}

// HostBatchUpdate 批量编辑主机：POST /api/hosts/batch-update。
func HostBatchUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs          []int64           `json:"ids"`
			Fields       map[string]string `json:"fields"`
			CustomFields map[string]string `json:"custom_fields"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		ids := uniquePositiveIDs(req.IDs)
		if len(ids) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "请选择要编辑的主机")
			return
		}
		if len(ids) > maxHostBatchDelete {
			httpx.Fail(w, http.StatusBadRequest, fmt.Sprintf("单次最多编辑 %d 台主机", maxHostBatchDelete))
			return
		}
		fields, err := normalizeBulkHostFields(req.Fields)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		custom, err := normalizeBulkCustomFields(r.Context(), s, req.CustomFields)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(fields) == 0 && len(custom) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "没有需要修改的字段")
			return
		}

		sets, args, i := hostUpdateSets(fields, custom, 1)
		sc := reqScope(r)
		cond, condArgs := sc.Where("", i+1)
		args = append(args, pq.Array(ids))
		args = append(args, condArgs...)
		q := "UPDATE hosts SET " + sets + " WHERE id = ANY($" + strconv.Itoa(i) + ") AND " + cond
		res, err := s.DB.ExecContext(r.Context(), q, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		n, _ := res.RowsAffected()
		auditOpDetails(s, r, audit.ActionHostUpdate, "host_batch_update", "", "", nil,
			map[string]any{"ids": ids, "fields": keysOf(fields), "custom_fields": keysOf(custom), "updated": n}, nil)
		httpx.OK(w, map[string]any{"updated": n})
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hostCSVColumn 导出列。
type hostCSVColumn struct {
	key    string
	label  string
	custom bool
}

// hostCSVColumns 导出列：IP/出口 IP + 内置可编辑字段 + 自定义可编辑字段。
func hostCSVColumns(ctx context.Context, s *svc.ServiceContext) ([]hostCSVColumn, error) {
	cols := []hostCSVColumn{
		{key: "ip", label: "ip"},
		{key: "exit_ip", label: "exit_ip"},
		{key: "hostname", label: "hostname"},
		{key: "os_family", label: "os_family"},
		{key: "os_type", label: "os_type"},
		{key: "os_version", label: "os_version"},
		{key: "account", label: "account"},
		{key: "app_code", label: "app_code"},
		{key: "biz_group", label: "biz_group"},
		{key: "department", label: "department"},
		{key: "vendor", label: "vendor"},
		{key: "host_type", label: "host_type"},
		{key: "tunnel_ip", label: "tunnel_ip"},
		{key: "server_url_override", label: "server_url_override"},
	}
	repo, err := fielddef.NewRepo(s.DB, fielddef.KindHost)
	if err != nil {
		return nil, err
	}
	defs, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.Editable {
			cols = append(cols, hostCSVColumn{key: d.FieldKey, label: d.Label, custom: true})
		}
	}
	return cols, nil
}

// HostExportCSV 导出主机 CSV（管理员，尊重当前筛选条件）。
func HostExportCSV(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cols, err := hostCSVColumns(r.Context(), s)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		whereSQL, args, _ := hostListFilter(s, r)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT h.ip, COALESCE(h.exit_ip,''), COALESCE(h.hostname,''),
			       COALESCE(h.os_family,''), COALESCE(h.os_type,''), COALESCE(h.os_version,''),
			       COALESCE(h.account,''),
			       COALESCE(h.app_code,''), COALESCE(h.biz_group,''), COALESCE(h.department,''),
			       COALESCE(h.vendor,''), COALESCE(h.host_type,''), COALESCE(h.tunnel_ip,''),
			       COALESCE(h.server_url_override,''), COALESCE(h.custom_fields,'{}'::jsonb)
			  FROM hosts h`+whereSQL+` ORDER BY h.ip LIMIT 50000`, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"hosts_"+time.Now().Format("20060102_150405")+".csv\"")
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		cw := csv.NewWriter(w)
		_ = cw.Write(func() []string {
			out := make([]string, len(cols))
			for i, c := range cols {
				out[i] = c.key
			}
			return out
		}())
		for rows.Next() {
			var ip, exitIP, hostname, osFamily, osType, osVersion string
			var account, appCode, bizGroup, department, vendor, hostType string
			var tunnelIP, override string
			var customRaw []byte
			if err := rows.Scan(&ip, &exitIP, &hostname, &osFamily, &osType, &osVersion,
				&account, &appCode, &bizGroup, &department, &vendor, &hostType,
				&tunnelIP, &override, &customRaw); err != nil {
				return
			}
			var custom map[string]string
			_ = json.Unmarshal(customRaw, &custom)
			base := map[string]string{
				"ip": ip, "exit_ip": exitIP, "hostname": hostname,
				"os_family": osFamily, "os_type": osType, "os_version": osVersion,
				"account":  account,
				"app_code": appCode, "biz_group": bizGroup, "department": department,
				"vendor": vendor, "host_type": hostType, "tunnel_ip": tunnelIP,
				"server_url_override": override,
			}
			rec := make([]string, len(cols))
			for i, c := range cols {
				if c.custom {
					rec[i] = custom[c.key]
				} else {
					rec[i] = base[c.key]
				}
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
	}
}

// hostCSVHeaderAliases 导入表头别名（小写）。
var hostCSVHeaderAliases = map[string]string{
	"ip": "ip", "主机ip": "ip", "ip地址": "ip", "地址": "ip",
	"exit_ip": "exit_ip", "出口ip": "exit_ip", "出口 ip": "exit_ip",
	"hostname": "hostname", "主机名": "hostname",
	"account": "account", "账号": "account",
	"app_code": "app_code", "appcode": "app_code", "app code": "app_code", "应用编码": "app_code",
	"biz_group": "biz_group", "bizgroup": "biz_group", "业务组": "biz_group",
	"department": "department", "部门": "department",
	"vendor": "vendor", "厂商": "vendor",
	"host_type": "host_type", "hosttype": "host_type", "主机类型": "host_type",
	"tunnel_ip": "tunnel_ip", "隧道ip": "tunnel_ip", "主机隧道ip": "tunnel_ip", "主机隧道 ip": "tunnel_ip",
	"server_url_override": "server_url_override", "连接地址覆盖": "server_url_override", "连接覆盖": "server_url_override",
}

// HostImportCSV 从 CSV 导入/更新主机：POST /api/hosts/import。
//
// 以 ip 为唯一键：已存在则更新非空单元格，不存在且 create_missing=true 时创建 manual 主机。
func HostImportCSV(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		var req struct {
			CSV           string `json:"csv"`
			ClearEmpty    bool   `json:"clear_empty"`
			CreateMissing bool   `json:"create_missing"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误或 CSV 超过 10MB")
			return
		}
		if strings.TrimSpace(req.CSV) == "" {
			httpx.Fail(w, http.StatusBadRequest, "CSV 内容为空")
			return
		}
		cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(req.CSV, "\uFEFF")))
		cr.FieldsPerRecord = -1
		cr.LazyQuotes = true
		records, err := cr.ReadAll()
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, "CSV 解析失败: "+err.Error())
			return
		}
		if len(records) < 2 {
			httpx.Fail(w, http.StatusBadRequest, "CSV 至少需要表头和一行数据")
			return
		}
		if len(records) > 5001 {
			httpx.Fail(w, http.StatusBadRequest, "单次最多导入 5000 行")
			return
		}

		repo, err := fielddef.NewRepo(s.DB, fielddef.KindHost)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defs, err := repo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		headerMap := map[string]string{}
		for k, v := range hostCSVHeaderAliases {
			headerMap[k] = v
		}
		for _, d := range defs {
			if !d.Editable {
				continue
			}
			key := "custom:" + d.FieldKey
			headerMap[strings.ToLower(d.FieldKey)] = key
			headerMap[strings.ToLower(d.Label)] = key
		}
		header := records[0]
		idx := make([]string, len(header))
		for i, h := range header {
			idx[i] = headerMap[strings.ToLower(strings.TrimSpace(h))]
		}
		hasIP := false
		for _, k := range idx {
			if k == "ip" {
				hasIP = true
				break
			}
		}
		if !hasIP {
			httpx.Fail(w, http.StatusBadRequest, "CSV 缺少 ip 列")
			return
		}

		sc := reqScope(r)
		tenantID := sc.TenantForWrite()
		created, updated, skipped := 0, 0, 0
		type rowErr struct {
			Row int    `json:"row"`
			IP  string `json:"ip,omitempty"`
			Err string `json:"error"`
		}
		errs := []rowErr{}
		for n, rec := range records[1:] {
			rowNo := n + 2
			values := map[string]string{}
			allEmpty := true
			for i, key := range idx {
				if key == "" || i >= len(rec) {
					continue
				}
				v := strings.TrimSpace(rec[i])
				if v != "" {
					allEmpty = false
				}
				values[key] = v
			}
			if allEmpty {
				continue
			}
			ip := strings.TrimSpace(values["ip"])
			delete(values, "ip")
			delete(values, "exit_ip")
			if ip == "" {
				errs = append(errs, rowErr{Row: rowNo, Err: "ip 不能为空"})
				continue
			}
			if len(ip) > 64 || strings.ContainsAny(ip, "\r\n\x00") {
				errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: "ip 格式非法"})
				continue
			}
			fields := map[string]string{}
			custom := map[string]string{}
			for k, v := range values {
				if strings.HasPrefix(k, "custom:") {
					if v != "" || req.ClearEmpty {
						custom[strings.TrimPrefix(k, "custom:")] = v
					}
				} else if v != "" || req.ClearEmpty {
					fields[k] = v
				}
			}
			cleanFields, err := normalizeBulkHostFields(fields)
			if err != nil {
				errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: err.Error()})
				continue
			}
			cleanCustom, err := normalizeBulkCustomFields(r.Context(), s, custom)
			if err != nil {
				errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: err.Error()})
				continue
			}

			cond, condArgs := sc.Where("", 2)
			var id int64
			err = s.DB.QueryRowContext(r.Context(),
				"SELECT id FROM hosts WHERE ip=$1 AND "+cond,
				append([]any{ip}, condArgs...)...).Scan(&id)
			if err == sql.ErrNoRows {
				if !req.CreateMissing {
					skipped++
					continue
				}
				// 新建 manual 主机：只写入提供的字段。
				cols := []string{"ip", "tenant_id", "source"}
				args := []any{ip, tenantID, "manual"}
				for _, key := range hostAllEditable {
					if v, ok := cleanFields[key]; ok {
						cols = append(cols, key)
						args = append(args, v)
					}
				}
				if len(cleanCustom) > 0 {
					b, _ := json.Marshal(cleanCustom)
					cols = append(cols, "custom_fields")
					args = append(args, string(b))
				}
				ph := make([]string, len(cols))
				for i := range cols {
					ph[i] = "$" + strconv.Itoa(i+1)
				}
				err = s.DB.QueryRowContext(r.Context(),
					"INSERT INTO hosts ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(ph, ",")+") RETURNING id",
					args...).Scan(&id)
				if err != nil {
					errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: "创建失败: " + err.Error()})
					continue
				}
				created++
				continue
			}
			if err != nil {
				errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: err.Error()})
				continue
			}
			if len(cleanFields) == 0 && len(cleanCustom) == 0 {
				skipped++
				continue
			}
			sets, args, i := hostUpdateSets(cleanFields, cleanCustom, 1)
			args = append(args, id)
			if _, err := s.DB.ExecContext(r.Context(),
				"UPDATE hosts SET "+sets+" WHERE id=$"+strconv.Itoa(i), args...); err != nil {
				errs = append(errs, rowErr{Row: rowNo, IP: ip, Err: "更新失败: " + err.Error()})
				continue
			}
			updated++
		}
		auditOpDetails(s, r, audit.ActionHostUpdate, "host_import", "", "", nil,
			map[string]any{"created": created, "updated": updated, "skipped": skipped, "failed": len(errs)}, nil)
		failedTotal := len(errs)
		if len(errs) > 200 {
			errs = errs[:200]
		}
		httpx.OK(w, map[string]any{
			"created": created, "updated": updated, "skipped": skipped,
			"failed": failedTotal, "errors": errs,
		})
	}
}
