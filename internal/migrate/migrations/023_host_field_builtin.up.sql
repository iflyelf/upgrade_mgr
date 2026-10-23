-- 主机字段定义补 builtin 列
--
-- 020 初次创建 host_field_defs 时遗漏了 builtin，而通用字段仓储
-- （internal/logic/fielddef）对 user/host 两种表使用同一套 SQL，
-- 导致主机字段列表报错 `column "builtin" does not exist`。
-- 本迁移为已升级环境补列（新装环境由 020 直接带上）。

ALTER TABLE host_field_defs
    ADD COLUMN IF NOT EXISTS builtin BOOLEAN NOT NULL DEFAULT FALSE;
