-- 20260924 多角色改造：sys_user 新增 roles 列（一人可多角色）+ 就地改写废弃角色名。
--
-- 背景：见 docs/多角色改造方案.md §1。原 sys_user.role 是单角色列；本次新增 roles 列承载
-- 「账号的全部角色」，逗号分隔、**无空格**、去重、按 model.Roles 声明顺序排序。
--   - roles 是**唯一事实来源**（api/notify_plan.go 的 FIND_IN_SET 依赖它）。
--   - role 列**保留**，写入时恒等于 roles 的第一个角色（主角色）：① 旧 token / 旧前端仍读它；
--     ② 回滚安全——把二进制换回旧版本，旧版本读到的仍是主角色，不会变成「无权限」。
--
-- ⚠️ 可重复执行说明：MySQL 8 的 ALTER TABLE ... ADD COLUMN **不支持** IF NOT EXISTS，
--    重跑本文件会报 1060（Duplicate column name 'roles'），属**预期**，忽略即可。
--    下面所有 UPDATE 均幂等（只补空行 / 只改含旧名的行），可安全重跑。
--
-- ⚠️ 与运行时迁移的关系：本仓库运行时的自动迁移在 store/mysql.go（store.New 启动时执行 DDL）。
--    那里已有等价的 addColumnIfMissingBackfill(roles) 与 migrateLegacyRoleNames()。
--    本文件是**手工执行**路径的副本；两条路径幂等，先后执行都安全。
--    （若先手工执行本文件建出 roles 列，运行时那步「建列+回填」会被跳过，但
--      migrateLegacyRoleNames() 每次启动都跑，仍会把旧名改掉——所以手工路径也不会残留旧名。）
--
-- 迁移前请备份 sys_user 表（生产库只有一份：10.0.0.120/asset）。

-- 1) 新增 roles 列（role 之后）。
ALTER TABLE sys_user ADD COLUMN roles varchar(128) NOT NULL DEFAULT '' AFTER role;

-- 2) 存量行回填：把原单角色写进 roles，使 roles 成为完整事实来源。
-- （Go 侧 EffectiveRoles() 在 roles 为空时会回落到 role，所以本 UPDATE 不是正确性前提，
--  但 FIND_IN_SET 查询依赖它，故必须执行。）
UPDATE sys_user SET roles = role WHERE roles = '' OR roles IS NULL;

-- 3) 就地改写**废弃角色名**：viewer→employee、repair_tech→maintainer。
--    与 store/mysql.go 的 migrateLegacyRoleNames() 同义。
--    为什么必须做：旧名已不在 model.Roles 里；代码层虽会映射（model.LegacyRoleRenames），
--    但 roles 列里留旧名是脏数据，任何按角色名的查询（FIND_IN_SET('employee', roles)）都会漏掉这些账号。

-- 3a) 单角色行（生产存量即此类：role='viewer' / 'repair_tech'）：role 与 roles **两列一起改**，
--     保持 role == roles 的第一个角色。幂等：只改 role 仍是旧名的行。
UPDATE sys_user SET role = 'employee',   roles = 'employee'   WHERE role = 'viewer';
UPDATE sys_user SET role = 'maintainer', roles = 'maintainer' WHERE role = 'repair_tech';

-- 3b) 兜底：roles 列里含旧名、但 role 列已不是旧名（多角色行）。
--     按逗号边界替换（CONCAT 补首尾逗号，避免 'viewerx' 这类子串被误伤），再裁掉首尾逗号。
--     ⚠️ 多角色行若同时含旧名与映射后的同名，理论上可能出现重复项；生产无此类数据，
--     且运行时 migrateLegacyRoleNames() 会按 FormatRoles 归一化去重，故此处不额外处理。
UPDATE sys_user
   SET roles = TRIM(BOTH ',' FROM REPLACE(CONCAT(',', roles, ','), ',viewer,', ',employee,'))
 WHERE FIND_IN_SET('viewer', roles) > 0;
UPDATE sys_user
   SET roles = TRIM(BOTH ',' FROM REPLACE(CONCAT(',', roles, ','), ',repair_tech,', ',maintainer,'))
 WHERE FIND_IN_SET('repair_tech', roles) > 0;
