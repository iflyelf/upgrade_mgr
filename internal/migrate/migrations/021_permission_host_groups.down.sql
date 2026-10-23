-- 回滚团队授权的主机分组范围
DROP INDEX IF EXISTS idx_tgp_team;
ALTER TABLE team_group_permissions DROP COLUMN IF EXISTS host_groups;
