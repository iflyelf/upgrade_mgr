-- 组件的 script 模式自定义下载地址。
--
-- 非空时 Agent 直接从该绝对 http/https 地址下载安装脚本，不再查找/上传制品；
-- 为空时沿用原有逻辑，从制品库下载。
ALTER TABLE components ADD COLUMN IF NOT EXISTS script_url TEXT NOT NULL DEFAULT '';
