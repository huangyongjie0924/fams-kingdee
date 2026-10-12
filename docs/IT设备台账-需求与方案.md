# IT 信息设备台账 —— 需求与方案

> 需求来源（用户原话）：
> 「重大功能新增：需要新增一个 IT 信息设备台账管理表单，目的是为了更好的管理公司 IT 信息设备，
> 维护设备信息、状态和使用人员。表单设计想法是跟资产台账做关联生成后完善设备属性字段
> （资产编码 使用人 部门 电脑类型 使用地点 用途 状态 是否信创 品牌.型号 CPU型号
> 内存容量(GB) 硬盘类型及容量 系统版本 备注），也可手工维护关联关系。
> 实际管理中存在员工一人多机，或资产卡上无的设备。」
>
> 落地日期：2026-10-10

---

## 1. 为什么必须是独立的一张自有表

**不能往 `asset_card` 加列。** `asset_card` 是**星瀚托管表**：每日 00:00 的定时同步会按
`kingdeeOwnedColumns` 覆盖本地值。全表只有一个本地列 `biz_status` 逃过同步，
而它的**唯一写入者**是维修单据状态机（见 `store/card.go` 的 `scopeConds` 上方注释）。

IT 属性（品牌型号 / CPU / 内存 / 硬盘 / 系统版本 / 是否信创）是纯台账本地数据，
一旦加到 `asset_card` 上，**第二天就会被同步抹掉**。

⇒ 新建 `it_equipment` 表，与资产台账以 `card_id` 建立**可空关联**。

---

## 2. 数据模型

### 2.1 表结构（`store/mysql.go` 的 `schema`）

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT PK | |
| `card_id` | BIGINT **NULL** | 关联 `asset_card.id`。**NULL = 未关联**（资产卡上没有这台设备） |
| `asset_code` | VARCHAR(64) | 冗余快照 + 导入匹配键。**无唯一约束** |
| `device_type` | VARCHAR(32) | 电脑类型（枚举） |
| `user_emp_id` | BIGINT | 使用人。**无唯一约束** |
| `dept_id` | BIGINT | 使用部门**快照** |
| `location` | VARCHAR(128) | 使用地点 |
| `purpose` | VARCHAR(32) | 用途（枚举） |
| `status` | VARCHAR(16) | 状态（枚举），默认「在用」 |
| `is_xinchuang` | TINYINT(1) | 是否信创 |
| `brand_model` | VARCHAR(128) | 品牌.型号 |
| `cpu` | VARCHAR(128) | CPU 型号 |
| `memory_gb` | INT | 内存容量（GB） |
| `disk` | VARCHAR(128) | 硬盘类型及容量 |
| `os_version` | VARCHAR(64) | 系统版本 |
| `remark` | VARCHAR(500) | 备注 |
| `created_by` / `created_at` / `updated_at` | | 服务端维护 |

索引：`UNIQUE KEY uk_it_card (card_id)`、`idx_it_user`、`idx_it_dept`、`idx_it_status`、`idx_it_code`。

### 2.2 🔴 三个关键设计决策（都是承重墙，改动前必读）

#### ① `card_id` 必须是**可空列**，不能是 `NOT NULL DEFAULT 0`

`uk_it_card` 唯一索引要**同时**满足两件事：

1. **一张资产卡最多一条 IT 记录**（1:1 —— 一张卡就是一台物理设备）；
2. **任意多条「资产卡上没有的设备」共存**（用户明确提出的真实场景）。

靠的是 MySQL 的一条语义：**唯一索引对 NULL 不去重，对 0 去重**。
若库里存字面量 `0`，第二台无卡设备就会撞唯一键，报
`Duplicate entry '0' for key 'uk_it_card'` —— 这个错误信息与「这张卡已登记过」完全不同，
很难联想到根因。

Go 侧仍然用 `int64` 的 `0` 表示「未关联」（JSON 里 0 与「无」同义、前端 `el-select`
的 `clearable` 天然产出 0）。**双向转换只落在两处**：

- 读：`itSelect` 里的 `COALESCE(c.card_id, 0)`
- 写：`itValueExpr("card_id")` 返回 `NULLIF(?, 0)`

改一处必须改另一处，由 `store/it_test.go` 的 `TestITValueExprCardIDIsNull` 钉死。

#### ② `asset_code` **刻意不加唯一约束**

多条无卡设备的编码都是空串 `''`，加唯一键第二条就插不进去
（与 `employee.open_id` 同一个坑，见 `store/mysql.go` 的 `idx_emp_open_id` 注释）。
它只是冗余快照 + 导入匹配键，不是主键。

#### ③ `user_emp_id` **刻意不加唯一约束**

用户明确提出「员工一人多机」。一个员工对应多条记录是**正常业务状态**，不是脏数据。
`asset_code` 同理不唯一（但业务上应尽量唯一）。

### 2.3 枚举取值（单一事实来源：`model/it.go`）

| 字段 | 取值 |
|---|---|
| 电脑类型 | 台式机 / 笔记本 / 一体机 / 工作站 / 服务器 / 平板 / 打印机 / 网络设备 / 其他 |
| 用途 | 办公 / 研发 / 设计 / 生产 / 测试 / 备用 / 其他 |
| 状态 | 在用 / 闲置 / 维修中 / 借用 / 报废 / 待分配 |

**校验语义**：空串放行、非空必须在枚举内。
空串放行的理由：「从资产台账关联生成」那一刻只有编码/使用人/部门/地点是带出来的，
类型与用途还是空的，等着人来补。若空串非法，这条主路径第一步就走不通。
状态例外：空串会被补成默认值「在用」，保证库里不出现状态为空的行。

**IT 状态枚举与资产状态枚举刻意不共用**：资产状态里的「调拨中」由单据流程驱动，
IT 台账没有调拨概念；而「待分配」（新到货还没派给人）是资产台账没有的。
`model/it_test.go` 有一条断言钉死两者不互相污染。

前端**不另抄一份**，通过 `GET /api/enums` 的 `it_device_types` / `it_purposes` /
`it_statuses` 三个键下发。

---

## 3. 权限矩阵（新增角色 `it_admin`）

> 用户选择：**新增独立角色** `it_admin`，而不是复用 `asset.manage`。

### 3.1 ⚠️ 新增角色的真实成本

本仓里「角色」不是一个字符串，而是**散布在多个决策点**上的语义。
新增一个角色必须逐处登记，否则会**静默落进 `default` 分支** ——
而 `api/scope.go` 的 `default` 返回零值 scope，语义是「**不限**」，即**看到全量数据**。
这是个 fail-open 的危险默认值，从界面上完全看不出来。

登记点清单：

| 位置 | 内容 |
|---|---|
| `model/model.go` | `Roles` 列表（**追加到末尾**）、`rolePermissions` |
| `api/scope.go` | `assetScope` 与 `repairScope` 各一个显式 `case`（**两处都要**） |
| `api/scope.go` 的 `itScopeOf` | 是否映射到 IT 台账范围 |
| `api/dashboard.go` | `dashboardTodos` / `dashboardStats` 的分支 |
| `api/notify_plan.go` | `assetManagerEmpIDs`（是否接管修通知） |
| `api/user.go` | `roleBindingErr`（是否要求绑定员工） |
| `web/src/stores/auth.ts` | `rolePermissions` / `roleLabels` / `scopeHints` |
| `web/src/views/users/UserList.vue` | 角色下拉、`EMP_BOUND_ROLES`、绑定校验 |

### 3.2 最终矩阵

| 角色 | `it.manage`（写 IT 台账） | `asset.manage` | 资产台账可见范围 | 维修单可见范围 | **IT 台账可见范围** |
|---|---|---|---|---|---|
| `admin` | ✅（Can 兜底恒真） | ✅ | 不限 | 不限 | 不限 |
| `it_admin` | ✅ | ❌ **刻意不给** | **不限**（显式 case） | **不限**（显式 case） | 不限 |
| `asset_manager` | ❌ | ✅ | 不限 | 不限 | 不限 |
| `counter` | ❌ | ❌ | 不限 | 只看自己提交的 | 不限 |
| `dept_head` | ❌ | ❌ | 本部门（含下级） | 本部门（含下级） | 本部门（含下级） |
| `maintainer` | ❌ | ❌ | 不限 | 派给我的 + 我报修的 | 不限 |
| `employee` | ❌ | ❌ | 自己名下 + 报修中的 | 只看自己提交的 | **自己名下** |

### 3.3 `it_admin` 的取舍（用户明确确认）

- **不给 `asset.manage`**：它需要「**看**」全量资产台账来检索设备做关联（那是**可见范围**，
  不是**权限位**），但不该能改资产卡。读与写分开，是这里唯一的判断依据。
- **在资产台账 / 维修模块「不限」**：代价是它同时能看到全量资产的金额与财务字段
  （原值 / 净值 / 折旧）。这是明确接受的，**不是遗漏** —— 所以在 `scope.go` 里写了
  显式的 `case model.RoleITAdmin:` 而不是让它落 `default`，这样它在 diff 里是可见的。
- **不要求绑定员工**：它在 `assetScope` / `itScope` 里都是「不限」，不靠 `employee_id` 收窄范围
  （对照 `employee` / `counter` / `maintainer` 三者的范围都依赖 `employee_id`）。

---

## 4. 可见范围：`itScope` **从 `assetScope` 派生**，不另写一份 switch

用户要求「IT 台账的可见范围复用资产台账的范围」。

```go
// api/scope.go
func itScopeOf(sc model.AssetScope) model.ITScope {
	return model.ITScope{SelfEmpID: sc.SelfEmpID, DeptIDs: sc.DeptIDs}
}
func (s *Server) itScope(w, r) (model.ITScope, bool) {
	sc, ok := s.assetScope(w, r)
	if !ok { return model.ITScope{}, false }
	return itScopeOf(sc), true
}
```

**为什么派生而不是复制**：本仓已有一次真实教训 —— 列表的 scope 与首页的 scope
各写一份、随时间分叉（见 `store/card.go` 的 `scopeConds` 上方注释）。
派生则**从构造上不可能漂移**。

**代价**：`employee` 账号会多跑一次 `ListOpenRepairCardIDs`（`assetScope` 的第三维），
结果被 `itScopeOf` 丢弃。一次带索引的点查，换「两个模块的范围永不分叉」，值得。

**⚠️ 刻意丢弃 `AssetScope.RepairingCardIDs`**：那一维的语义是「有未结单维修单的资产对员工可见」，
属维修模块语义；照搬会让员工看到**别人名下设备的 IT 配置**（CPU / 内存 / 系统版本）。
由 `api/role_scope_guard_test.go` 的 `TestITScopeOfDropsRepairingDimension` 钉死。

**`itScope` 与 `assetScope` 一样 fail-closed**：解析不出角色 → 403，绝不退化成「不限」。

---

## 5. 接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/it-equipment` | 不门控 | 列表。范围在**数据行上**收窄 |
| GET | `/api/it-equipment/{id}` | 不门控 | 单条。`denyITOutOfScope` 收口 |
| POST | `/api/it-equipment` | `it.manage` | 新增 |
| PUT | `/api/it-equipment/{id}` | `it.manage` | 编辑 |
| DELETE | `/api/it-equipment/{id}` | `it.manage` | 物理删除 |
| GET | `/api/it-equipment/export` | 不门控 | 导出（受范围收窄） |
| GET | `/api/it-equipment/import-template` | 不门控 | 模板 |
| POST | `/api/it-equipment/import` | `it.manage` | 导入 |

### 5.1 为什么读接口不挂 `requirePerm`

与资产台账 / 维修单**完全同一套约定**：能进这个页面就该能看，范围在数据行上收窄。
若读接口门控 `it.manage`，员工就完全进不去这个页面，
「复用资产台账范围」这条需求等于没落地。

### 5.2 为什么写接口用 `requirePerm` 而不是 `requireAdmin`

IT 台账是**可恢复的常规主数据**（删错了重录一条即可），不需要「硬判角色」这道闸门。
对照：维修单物理删除用 `requireAdmin`，因为它**不可恢复**
（见 `api/server.go` 的 `requireAdmin` 注释）。

### 5.3 判重复用列表接口，不新增端点

「从资产台账关联生成」需要判重。用 `GET /api/it-equipment?card_id=N`，
**不另开 `/by-card` 端点** —— 本仓已经踩过 ServeMux 模式冲突
（见 `api/server.go` 里 `by-code` 走查询串的注释）。能不加路由就不加。

### 5.4 资产编码以**库**为准

`resolveITCardLink`：`card_id > 0` 时校验卡存在，并**用 `asset_card.asset_code` 覆盖**请求里的值。
理由：`asset_code` 是这条 IT 记录与资产台账之间的冗余快照，它的真相在 `asset_card`。
若允许前端随意填，就会出现「关联了卡 A，却显示卡 B 的编码」这种自相矛盾的数据。

---

## 6. 「跟资产台账做关联生成」的用户路径

```
资产台账列表 → 点资产名称 → 详情抽屉 → 右侧操作栏「生成 IT 设备记录」
   ↓ 跳转 /it-equipment?from_card=<资产卡 id>
IT 台账页 resolveFromCard():
   ├─ GET /api/assets/{id}                   取卡上的编码/使用人/部门/地点
   ├─ GET /api/it-equipment?card_id={id}     判重
   ├─ 已有记录 → 直接打开**编辑**那条（并提示「已登记过」）
   └─ 没有记录 → 打开**新增**，预填卡上的四个字段
        ↓
   人只需补 IT 属性：电脑类型 / 用途 / 品牌型号 / CPU / 内存 / 硬盘 / 系统版本 / 是否信创
```

**「手工维护关联关系」**：表单里的「关联资产」是一个可搜索、可清空的 `el-select`：
- 选中一张资产卡 → 自动带出编码/使用人/部门/地点（只填空字段，不覆盖已填的值）；
- 清空 → `card_id = 0`，表示「资产台账里没有这台设备」，此时资产编码可自由填写。

---

## 7. 导入 / 导出

### 7.1 列顺序（与用户提出的字段清单逐字一致）

`资产编码 使用人 部门 电脑类型 使用地点 用途 状态 是否信创 品牌.型号 CPU型号 内存容量(GB) 硬盘类型及容量 系统版本 备注`

⚠️ 与 `api/importexport.go` 同一条纪律：**新增列一律追加在末尾**。
导入按列下标取值，插在中间会把后面所有列错位（而错位不会报错，只会写错数据）。

### 7.2 🔴 导入要求「资产编码必填」（比页面表单更严）

页面表单允许编码留空（用户已确认「可留空，也支持手工编码」），但**导入必须填**。

理由：导入是**批量、可重复**的动作，而 `asset_code` 是唯一的匹配键。
编码允许为空的话，同一份文件导两次就会把每台无卡设备复制一遍，**而且不会有任何报错**。
静默重复是导入最危险的失败方式（对照 `api/importexport.go` 里 `financeHeaderAliases`
注释中「静默忽略」的同款判断）。

⇒ 无卡设备请在这列填一个自有编码（如 `IT-2026-0007`）。**页面表单则仍然可以留空。**

### 7.3 判重规则（保证「导出 → 改 → 导回」可反复执行）

| 顺序 | 条件 | 结果 |
|---|---|---|
| ① | 编码命中 `asset_card.asset_code` | 关联该卡（`card_id`），`asset_code` **以卡为准** |
| ② | 编码命中 `it_equipment.asset_code`，或①关联到的卡已有 IT 记录 | **更新**那一条 |
| ③ | 都没命中 | 新增 |
| ④ | 编码在 IT 台账里匹配到**多条** | **报错**（不随便取一条） |

第 ④ 条很重要：`asset_code` 没有唯一约束，静默取第一条会把数据写到错误的行上，
而这在导入里是最难发现的错误。

### 7.4 整批事务

`ImportITEquipment` 在一个事务里写入，整批成功或整批回滚。
部分写入会留下一个「改了一半」的台账，比完全不写更难收拾
（与 `handleImport` 同一约定：有错就整体不写）。

### 7.5 「是否信创」的往返

导出写中文「是 / 否」（不是 `true/false`）；导入 `parseITBool` 容忍
`是/否/1/0/y/n/yes/no/true/false/√/✓/×/x` 与空串（空串视为「否」，不报错）。

---

## 8. 前端

| 文件 | 内容 |
|---|---|
| `web/src/api/it.ts` | 列元数据、提交字段白名单、状态标签映射 |
| `web/src/views/it/List.vue` | 列表 + 筛选 + 表单对话框 + 删除 |
| `web/src/views/it/Import.vue` | 导入页 |
| `web/src/router/index.ts` | 新增 `/it-equipment`、`/it-equipment/import`（**既有 23 条路由一条未动**） |
| `web/src/composables/useNav.ts` | 导航项（**不加权限门**，与资产台账一致）+ 面包屑规则 |
| `web/src/views/asset/CardDetail.vue` | 「生成 IT 设备记录」入口（`auth.can('it.manage')` 门控） |

**硬约束遵守情况**：**零路由改动**（只新增，既有 23 条未动）、业务逻辑不动、不新增权限位之外的权限。

**列配置**：14 列全开会把表格挤到横向滚动，故默认只显示 9 列，其余可在「列配置」里打开。

---

## 9. 测试

### 9.1 守卫测试（`api/role_scope_guard_test.go`）—— 本轮最高价值

| 用例 | 断言 |
|---|---|
| `TestScopeRoleConstNamesCoverAllRoles` | 角色→常量映射必须覆盖 `model.Roles` 全部项（加角色忘更新映射 → 红） |
| `TestScopeHandlesEveryRoleExplicitly` | 每个角色要么有显式 `case`，要么在「承认落 default」名单里（**防静默 fail-open**） |
| `TestScopeITAdminExplicitInBothScopes` | `case model.RoleITAdmin:` 必须出现 **≥2** 次（`assetScope` + `repairScope` 各一次） |
| `TestITAdminScopeUnrestricted` | it_admin 在三个 scope 里都不限，且 `employee_id=0` 不被 403 误伤 |
| `TestITScopeOfDropsRepairingDimension` | `itScopeOf` 必须丢弃 `RepairingCardIDs` |
| `TestITScopeEmployeeNarrowed` / `TestITScopeFailClosed` | 员工收窄到本人；空角色 403 |

**这些守卫都做过「注入漂移验证能红」**：把 `case model.RoleITAdmin:` 改成无法命中的字符串字面量、
把 `auth.ts` 的 `it_admin: ["it.manage"]` 清空 —— 两个守卫都如期变红，然后恢复。

### 9.2 前后端权限表一致性（`model/it_test.go`）

`TestFrontendRoleTablesInSync` **解析** `web/src/stores/auth.ts` 的 `rolePermissions` 对象
并逐项比对后端 `model.rolePermissions`（而非字符串包含）。漂移的两个方向都被点名：

- 前端多给 → 「**按钮可见但点击 403**」
- 前端少给 → 「后端允许但界面没入口」

`TestFrontendRoleLabelsCoverAllRoles` 保证 `roleLabels` 覆盖每个角色
（缺了会把角色显示成英文原名）。

### 9.3 数据层（`store/it_test.go`）

- `TestITEquipmentCardlessMultipleCoexist` —— **最关键的真库断言**：三条无卡设备必须都能落库
- `TestITEquipmentOnePerCard` —— 同一张卡第二条必须撞唯一键（`Duplicate entry`）
- `TestITEquipmentSameEmployeeMultiple` —— 一人多机
- `TestITEquipmentScopeNarrowedInSQL` —— 范围进 WHERE，且 `ITEquipmentCount` 与列表口径一致
- `TestITEquipmentImportUpsert` —— 反复导同一份不重复
- `TestITValueExprCardIDIsNull` / `TestITScopeCondsUnion` / `TestITBuildWhereOptionalFilters`
  —— 纯函数级（含「nil 不筛选 vs &false 要筛选」的区分）

### 9.4 HTTP 端到端（`api/it_api_test.go`）

权限门控（含「未登录 401 / asset_manager 403 / it_admin 200」三态）、
无卡多条 + 同卡 409 文案、编码以库为准、判重查询、校验门、
范围收窄、枚举下发、导出/模板/导入（内存里现造 xlsx 上传，含「二次导入是更新不是新增」）。

---

## 10. 回滚

纯新增，**没有改动任何既有表结构、既有路由、既有权限位**，故回滚极简：

```bash
cd /opt/asset-mgr && mv asset-mgr.bak-<TS> asset-mgr && systemctl restart asset-mgr
```

- `it_equipment` 表**不需要删**：旧二进制不认识它，留着不影响任何行为
  （且将来重新上线时数据还在）。
- `sys_user.roles` 里已写入的 `it_admin` **不需要清理**：旧二进制的 `NormalizeRoles`
  会把它当非法值过滤掉 ⇒ `EffectiveRoles()` 可能变空 ⇒ **api 层 fail-closed 403**。
  即：旧版本下 `it_admin` 账号会**登录后看不到任何数据**（而不是看到全量）—— 这是安全的降级方向。
  ⚠️ 但若该账号**同时**还持有别的角色（如 `it_admin,employee`），过滤后仍剩 `employee`，
  行为正常。故回滚前请确认 `it_admin` 账号是否单独持有该角色。
- 前端无需回滚：旧前端没有 `/it-equipment` 入口，用户不会走到那个页面。
  （但 `web/dist` 是嵌进二进制的，换回旧二进制即等于换回旧前端。）

---

## 11. 已知限制与后续扩展点

1. **IT 台账与资产状态不联动**：IT 记录里的「状态」是**独立**字段，
   不会随资产台账的 `status` / `biz_status` 变化，反之亦然。
   若需要联动（如资产卡报废 → IT 记录自动报废），需要新增同步逻辑并明确定义谁是真相。
2. **不产生云之家待办**：IT 台账的维护没有时限、没有「球在谁手上」的概念，
   故 `dashboardTodos` 里 it_admin 刻意没有任何分支（待办为空）。
   若将来要加（如「无卡设备待补登记」），必须在那里显式加分支并配守卫测试。
3. **物理删除无审计表**：删除只写 `log.Printf` 审计日志（走 journald），
   没有独立的删除记录表。若需要「谁在什么时候删了哪台设备」的可查询留痕，
   需要新增一张 `it_equipment_log`（可复用 `doc_status_log` 的形态）。
4. **`asset_code` 不唯一**：手工编码可能重复。导入时会因「匹配到多条」报错，
   但页面表单不会拦。若需要强唯一，要先决定「无卡设备的空编码怎么处理」。
5. **没有附件/照片**：IT 台账不支持上传照片（资产台账有 `asset_attachment`）。
   若需要，可复用 `asset_attachment` 的存储层，但要先想清楚归属语义（按 IT 记录 id 收敛）。
6. **IT 管理员首页指标卡只有 3 张**（总数 / 信创 / 无卡），
   按权限位 `it.manage` 门控 —— 把 `it.manage` 授予别的角色时卡片会自动跟上。
