-- 回滚人员组织、字段自定义与 FlyIAM 集成基础表
DROP TABLE IF EXISTS host_field_defs;
ALTER TABLE hosts DROP COLUMN IF EXISTS custom_fields;

DROP TABLE IF EXISTS team_group_permissions;
DROP TABLE IF EXISTS service_group_components;
DROP TABLE IF EXISTS service_groups;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS roles;

DROP TABLE IF EXISTS user_field_values;
DROP TABLE IF EXISTS user_field_defs;
