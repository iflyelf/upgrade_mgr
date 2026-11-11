-- 主机批量删除作业：记录是否强制删除在线主机。
-- 普通删除保护在线主机；只有发起人显式勾选强制删除时才允许。
ALTER TABLE host_delete_jobs ADD COLUMN IF NOT EXISTS force BOOLEAN NOT NULL DEFAULT FALSE;
