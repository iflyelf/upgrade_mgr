-- 团队授权增加主机分组范围
--
-- host_groups 限定该团队可操作的主机业务分组（对应 hosts.app_code / biz_group 值）。
-- 空数组表示不限主机范围（默认，保持既有行为）。

ALTER TABLE team_group_permissions
    ADD COLUMN IF NOT EXISTS host_groups TEXT[] DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_tgp_team ON team_group_permissions(team_id);
