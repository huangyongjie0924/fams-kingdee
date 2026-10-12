-- ============================================================================
-- 回滚脚本：把角色名从新名改回旧名
--   配套变更：2026-09-24 多角色改造（viewer→employee / repair_tech→maintainer）
--   正向前置：deploy/migrations/20260924_multi_role.sql（手工路径）
--             或 store/mysql.go 的 migrate()（运行时权威路径，二者等价）
--
-- 【什么时候需要跑】
--   只有在「把 /opt/asset-mgr/asset-mgr 换回 2026-09-23 23:00 那版旧二进制
--   （提交 b7b7a3e）」的时候才需要。单独换二进制而不跑本脚本 = 原 viewer 账号会 fail-open。
--
-- 【为什么必须跑（这不是洁癖，是实打实的安全问题）】
--   新二进制启动时的 migrateLegacyRoleNames() 会把 sys_user.role / roles 里的
--   'viewer' 就地改写成 'employee'、'repair_tech' 改写成 'maintainer'。
--   而旧二进制（b7b7a3e）的 api/scope.go 是：
--       switch u.Role {
--       case model.RoleViewer:   // 常量值 = "viewer"
--           ... SelfEmpID 收窄 ...
--       case model.RoleDeptHead: ...
--       }
--       return model.AssetScope{}, true   // ← 函数末尾的兜底：不限范围
--   'employee' 匹配不到任何 case ⇒ 落到末尾 ⇒ **不限 = 读全量资产台账**。
--   权限侧（model.Can）对未知角色返回 false，于是变成
--   「丢写权限 + 拿全量读权限」—— 与 2026-09-24 在 api/scope.go 里拦下的
--   空角色 fail-open 完全同型，只是这次发生在旧代码侧。
--
-- 【实测记录】
--   2026-09-24 14:15 生产部署后，本脚本的「正向」结果已在生产库确认：
--     id=3  huangyongjie  role viewer→employee  roles viewer→employee
--     id=4  005331        role viewer→employee  roles viewer→employee
--     id=10 测试           role viewer→employee  roles viewer→employee
--   即：一旦回滚二进制，这 3 行必须先用本脚本改回去。
-- ============================================================================

-- 先看清楚现状，别盲改
SELECT id, username, real_name, role, roles FROM sys_user ORDER BY id;

-- 反向改名：新名 → 旧名。
-- 只涉及这两个角色，admin / asset_manager / counter / dept_head 不受影响。
UPDATE sys_user SET role = 'viewer'      WHERE role = 'employee';
UPDATE sys_user SET role = 'repair_tech' WHERE role = 'maintainer';

-- 多角色列一并回退。用逗号边界替换，避免 'employee2' 之类的子串被误伤。
UPDATE sys_user
   SET roles = TRIM(BOTH ',' FROM REPLACE(CONCAT(',', roles, ','), ',employee,', ',viewer,'))
 WHERE CONCAT(',', roles, ',') LIKE '%,employee,%';

UPDATE sys_user
   SET roles = TRIM(BOTH ',' FROM REPLACE(CONCAT(',', roles, ','), ',maintainer,', ',repair_tech,'))
 WHERE CONCAT(',', roles, ',') LIKE '%,maintainer,%';

-- 复核：应恢复成 admin / viewer 两种值（本次改造前生产只有这两种）
SELECT id, username, real_name, role, roles FROM sys_user ORDER BY id;

-- 【注意】roles 列本身（VARCHAR(128) NOT NULL DEFAULT ''）**不需要**删除。
-- 旧二进制不读它，留着无害；删列反而会在下次升级时让回填逻辑重跑一次。
-- 若坚持要删：ALTER TABLE sys_user DROP COLUMN roles;
