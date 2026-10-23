-- 回滚主机字段定义的 builtin 列
ALTER TABLE host_field_defs DROP COLUMN IF EXISTS builtin;
