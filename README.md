# 固定资产台账管理系统

内部自用的固定资产台账，第一期实现资产卡片：列表、新增/编辑/详情、字段级履历、Excel 导入导出。字段参照易点易动资产模块实地核对。

## 技术栈

- 后端：Go 1.25，`database/sql` + stdlib `net/http`，无 ORM 无 Web 框架
- 前端：Vue 3 + Element Plus + Vite，产物用 `go:embed` 打进二进制
- 数据库：MySQL 8.0，库名 `asset`，13 张表启动时自动建

## 首次拉取后

仓库不含凭据与构建产物，克隆后需补三步：

```bash
# 1. 配置：模板 → 实际配置，填入数据库密码和 jwt_secret
cp config.example.yaml config.yaml
#    （config.yaml / .env / cert/*.key 均被 .gitignore 排除，不会误提交）

# 2. 前端依赖
cd web && npm install

# 3. 证书：cert/server.crt 与 cert/server.key（HTTPS 用，key 权限 600）
```

注意 `go build` 依赖 `web/dist`（`main.go` 里是 `go:embed all:web/dist`），
**必须先 `npm run build` 再编译**，否则报 `pattern all:web/dist: no matching files found`。

## 本地开发

```bash
# 后端（自动建表 + 写种子数据 + 首次创建管理员）
go run . -config config.yaml

# 前端（dev 代理 /api 到 :8080）
cd web && npm install && npm run dev
```

前端 dev 地址 http://localhost:5173，后端 http://localhost:8080。

## 诊断命令

### 金蝶同步预检

```bash
# 只比对不写库，看这次同步会影响多少张卡（默认就是 dry-run）
go run ./cmd/dryrun -config config.yaml -mode full

# 真的要写的时候显式关掉
go run ./cmd/dryrun -config config.yaml -mode full -dry-run=false
```

dry-run 的数字与实跑一致。它内部维护一份「模拟落库后」的 overlay，
同编码的多张合并卡不会重复计数。

> 此前没有这层 overlay：同一编码的两行会各自与**同一份**旧数据比对，
> 每比一次都算一次 affected，实测 2 倍高估（预测 `updated=30` / 实际 `15`）。
> 那个版本作为「上线闸门」是安全的（只会高估不会低估），但作为预检就没用了
> ——预检的价值全在于数字可信。修复只落在 dry-run 分支内，实跑路径未改动。

### 星瀚接口字段扫描

```bash
# ⚠ 凭据从 .env 读入环境变量（config.go 只认 os.Getenv，不解析 .env）
set -a && . ./.env && set +a

# 扫描资产卡，并和上次的基线对比（基线不存在时自动创建）
go run ./cmd/kdscan -config config.yaml

# 扫描人员接口
go run ./cmd/kdscan -config config.yaml -target personnel

# 扫描部门（行政组织）接口
go run ./cmd/kdscan -config config.yaml -target dept

# 三个接口一起扫（资产卡 / 人员 / 部门）
go run ./cmd/kdscan -config config.yaml -target all

# 展开某个组织节点（-code 传组织编码）
go run ./cmd/kdscan -config config.yaml -target dept -code 120108

# 确认变化无误后，把本次结果存为新基线（-target all 时不能配 -update）
go run ./cmd/kdscan -config config.yaml -update

# 连"仅数量波动"的字段一起看
go run ./cmd/kdscan -config config.yaml -v
```

拉全量、列出所有字段与取值分布、展开明细子表（资产卡 `finentry` / 人员 `entryentity` /
部门 `structure`）的全部键、统计同编码重复行，**并和上次扫描的字段指纹逐字段对比**。
怀疑「字段取不到值 / 金额对不上」时先跑它。

指纹基线：资产卡 `docs/kingdee-fields-baseline.json`、
人员 `docs/kingdee-personnel-baseline.json`、
部门 `docs/kingdee-dept-baseline.json`（`-baseline-dir` 可改目录）。
指纹只记「字段在不在、有没有非零值、服务端生效的过滤条件」，
**不记具体值**——具体值天天在变，记进去只会把 diff 淹没在噪声里。

对比结果按处理优先级分级：

| 标记 | 含义 | 要处理吗 |
|---|---|---|
| `✗ 消失` | 投影列被删了 | 要。代码不会报错，只会从此静默读到零值 |
| `! 开始有值` | 字段一直恒 0，现在有数据了 | 要。通常正意味着可以接入了 |
| `! 变全零` | 原本有值，现在全归零 | 要。被撤了，或者对方改了列名 |
| `+ 新增*有值` | 多出来的键，且带值 | 要 |
| `! 过滤条件变更` | 服务端生效的查询口径变了 | 要。行数会跟着变，且请求体覆盖不了 |
| `· 计数变化` | 仅数量波动（新建卡、金额变动） | 不用，加 `-v` 才显示 |

**返回 0 行会单独走一个分支**：只报「过滤条件/行数变了」，
**不会**顺势报「所有字段消失」——0 行时字段本来就都不出现，
把它当成"投影被清空"会把排查带偏（详见 `docs/星瀚人员接口字段探测.md` 第四节）。

退出码：`0` 无重要变化 / `1` 检测到重要变化 / `2` 参数错误 / `3` 扫描失败。
`1` 和 `3` 分开是有意的——`1` 是「接口变了，人来看看」，`3` 是「这次没扫成，结果不可信」。

计数波动默认不报警：本库天天有卡在增减，若 227 行变 228 行也报 ⚠，很快就没人看了。
告警一旦开始"狼来了"，就等于没有。

> **为什么需要它**：星瀚侧改接口是**静默的**，而且改的不只是投影。
>
> 改投影：2026-09-17 当天 19:18 扫到 `finentry` 只有 2 个键且全库恒为 0，
> 20:07 再扫已变成 39 个键、真实取值挂在 `originalfincard_*` 前缀上——两次之间没有任何通知。
> 更坑的是那两个旧键**一直都在**，只是永远为 0。**「键在」和「有数据」是两回事**，
> 只对比字段名的做法抓不到这个区别，所以指纹必须记 `nonzero`。
>
> 改口径：2026-09-18 人员接口被加了一条**服务端过滤条件**，请求体里传 `filter` /
> `name` / `number` / `condition` 等 16 种写法全部无效（连 `[(1=1)]` 都不生效），
> 行数从 5038 直接变 0。**过滤条件既不在请求体里、也不在返回数据里**，
> 唯一能看见它的地方是响应里 `data.filter` 的回显——所以指纹必须把它记下来。
> 详见 `docs/星瀚人员接口字段探测.md`。
>
> 同一天 13:20 再扫，人员接口已恢复成 `[null]` / 5038 行，**并且投影被补齐了**：
> `entryentity` 从 4 个子字段变成 11 个，多了 `dpt_id` / `dpt_name` / `dpt_number`。
> 同一次里资产卡的过滤条件也被改了（去掉了 `billstatus = 'C'`），
> 而**行数仍是 227，字段也没变**——只比行数或只比字段名，这次变化完全看不见。
> **这就是为什么指纹要同时记「过滤条件 + 字段 + 非零数」三样。**
>
> 所以「字段取不到值」的第一反应是**重扫**，而不是翻旧结论。
> 资产卡侧的投影变更详见 `docs/星瀚接口字段扩展需求.md`，
> 部门接口与组织树详见 `docs/星瀚部门接口字段探测.md`。

### 星瀚 ↔ 台账 财务字段核对

```bash
# 逐张比对「星瀚接口值」与「台账落库值」
go run ./cmd/kdverify -config config.yaml
```

逐字段比对原值 / 累计折旧 / 净值 / 税额 / 含税金额 / 财务使用期限 / 残值率，
并做库内勾稽验证（`净值 == 原值 - 累计折旧`、`含税金额 == 原值 + 税额`）。
退出码 `0` = 全部一致、`1` = 有问题，可以直接用在脚本或 CI 里。

只对**星瀚确实提供了值**的字段做断言；星瀚没给值时台账保留的是人工维护值或
类别默认值，本来就不该等于 0，硬断言会制造大量假警。

> **为什么需要它**：同步跑完看到 `updated=200` **不代表**对齐了。
> 上一轮就是靠逐条比对才抓出「22 张卡残值率停在 5%、4 张卡使用期限停在 240」，
> 而当时汇总数字 `updated=200` 看起来完全正常。
>
> 它和 `kdscan` 是一对：`kdscan` 回答"接口有什么"（探测投影结构），
> `kdverify` 回答"落库对不对"（核对同步结果）。
> 解析规则直接复用 `syncer.ApplyFinEntry`，**不重新实现一遍**——
> 核对工具自己算错的话，报出来的"不一致"就是假警，而假警比不报更糟。

### 组织主数据同步（公司 / 部门 / 员工）

台账的部门与人员主数据来自星瀚，不再是手工维护。范围**固定**在「12」下的
`1201`、`1202` 两个法人及其全部下级组织，写在 `syncer.OrgScopeRoots`。

```bash
# 默认就是 dry-run：只算不写
go run ./cmd/orgsync -config config.yaml

# 确认影响面后再真写
go run ./cmd/orgsync -config config.yaml -dry-run=false
```

界面上是「金蝶同步」页的**同步部门与人员**按钮（`POST /api/sync/org`，
需要 `sync.manage` 权限）。它和资产卡同步分开：组织同步只做全量、不认 mode，
返回体是「范围 / 计划 / 同名冲突」这套结构，合成一个接口会得到一半字段恒为空的响应。

几个不能改的判断：

- **范围判定用 `longnumber` 前缀，不用编码前缀。** 编码会「跨支」——
  `1200151`（业务支持中心二组）挂在 `1201` 下，但编码不以 `1201` 开头。
  按编码前缀筛会漏掉它，按编码等值匹配更会漏掉 88% 的挂载点。
- **`12` 本身不同步。** 它是合并口径的虚拟节点、不是实际法人，
  两个儿子都是公司。同步它对台账没有意义。
- **公司只认 `orgpattern_name = 公司`。** 层级不固定（L2~L8 都有）、
  编码位数不固定（2~12 位），753 个名字带「公司」的其实是部门。
- **落库顺序：公司 → 部门（浅到深）→ 员工。** 部门必须父先于子，
  `parent_id` 才能一遍串起来；员工最后，因为它要引用部门 ID。
  落库后可用「部门表里 `parent_id <> 0` 但父节点不存在」的条数自检，应为 0。
- **不按名称匹配。** 5203 个组织节点只有 1333 个唯一名称，「财务部」一个名字挂了 78 个节点。
  按名称兜底会让 `120102` 和 `120214` 认领同一行本地数据，两个星瀚部门塌成一条。
  解析顺序只有：外部映射表 → 按编码 → 新建。
- **0 行必须中止**（`syncer.ErrOrgSourceEmpty`）。资产卡返回 0 行最多是「没变化」，
  组织主数据返回 0 行若照常执行，会把整张部门表/员工表判成「源里没有了」。
  五处护栏：部门 0 行 / 人员 0 行 / 范围内 0 节点 / 范围内 0 公司 / 范围内 0 人。
- **dry-run 与真跑口径必须一致。** 两边都用「解析到已有行 = 更新，新建 = 新建」，
  所以空库上 dry-run 报「会新建 226」、真跑就报「新建 226」，再跑一次报「更新 226」。
  这条不变量是预检有用的前提，改动任一分支都要重新验一遍。

落库后每行带 `source = 'kingdee'`，前端主数据页标为**星瀚托管**；手工建的行 `source` 为空、
标为**手工维护**，同步不碰它们。两者同名时会并列存在并在同步结果里报成「同名冲突」
（带手工行 ID 与同步行 ID），由人决定手工行去留——同步不会替你删。

> **为什么 dry-run 要在运行记录里标出来**：dry-run 也把「会新建 / 会更新」记进统计列
> （这样历史里能看到影响面的变化趋势），但这会让演练和真写在记录里长得一样。
> 所以演练批次的摘要固定写「演练模式（dry_run）：未写库，新增/更新为预估数」。

### 每日定时同步（先组织，后资产卡）

```yaml
sync:
  enable: true
  daily_at: "00:00"   # 本地时区，格式必须是 HH:MM
  dry_run: false
```

每天 `sync.daily_at`（默认 `00:00`）跑**一个批次**，批次内两个阶段**有序**：

```
阶段一  组织主数据（公司 / 部门 / 员工）   POST /api/sync/org 语义
阶段二  资产卡                            增量
```

顺序不是风格问题。资产卡同步只会建「卡片引用到的」主数据，而且只写编码和名称、
不写组织关系。空库上实测（227 张卡）：

| 跑法 | 部门 | 员工 |
|---|---|---|
| 只跑资产卡 | 29 个（**0 个**有归属公司 / 上级 / 路径） | 65 名（**0 名**挂到部门） |
| 先组织再跑资产卡 | 88 个（88 个有公司、40 个有上级、88 个有路径） | 136 名（全部挂到部门与公司） |

顺序反了不会产生重复行（组织同步按编码能把资产卡建的行接管过来，下一轮补齐），
但当天的台账里「使用部门」只有一个名字、没有归属公司，按公司或部门筛选资产、
划盘点范围都会漏掉——这正是要避免的「资产信息不全」。

两条相关规则：

- **组织阶段失败则不执行资产卡阶段。** 批次的意义就是让资产卡落在主数据完整的库上。
  组织同步没跑成还继续跑资产卡，得到的就是上表第一行那份不完整数据。代价是漏跑一天
  资产卡，可恢复（次日或人工点一次）。
- **启动补跑。** 进程在 `daily_at` 时刻处于停机状态时这一批会丢。启动时判断一次：
  只有「**调度器触发的、成功的、今天的**资产卡批次」才认为已跑过、跳过补跑。
  两个限定词都有用——手工点「立即增量同步」只跑资产卡不刷组织，不能拿它当整批跑过，
  否则组织主数据会整天不更新。距下一个定时点不足 10 分钟时也不补跑，避免紧接着
  又跑一轮真正的定时批次。

界面「金蝶同步」页的**定时同步**卡片显示每日时刻、下次自动同步时间与上次调度结果。
同步记录的「触发人」一列会区分 `scheduler` 与 `manual`。

## 构建与部署

```bash
cd web && npm run build          # 产物落到 web/dist，被 go:embed 打包
cd .. && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o asset-mgr-linux .
```

推到服务器：

```bash
scp asset-mgr-linux config.yaml asset-mgr.service root@<host>:/opt/asset-mgr/
mv /opt/asset-mgr/asset-mgr-linux /opt/asset-mgr/asset-mgr && chmod +x /opt/asset-mgr/asset-mgr
cp /opt/asset-mgr/asset-mgr.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now asset-mgr
```

除二进制外还需一并放到 `/opt/asset-mgr/`：`.env`（金蝶/云之家凭据）、`cert/server.crt`+`cert/server.key`
（HTTPS，`server.key` 权限 600）。服务器防火墙要放行 `8080/tcp`（HTTP）和 `8443/tcp`（HTTPS）：

```bash
firewall-cmd --permanent --add-port=8080/tcp --add-port=8443/tcp && firewall-cmd --reload
```

日志走 journald（CentOS 7 的 systemd 219 不支持 `append:` 写法）：`journalctl -u asset-mgr -f`。

## 上线前必须改

1. `config.yaml` 的 `auth.jwt_secret` 换成随机值
2. `auth.init_admin_password` 换掉，或首次登录后改密
3. 数据库账号换成只对 `asset` 库有权限的专用账号（需 root 授权）：
   ```sql
   GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,DROP,CREATE TEMPORARY TABLES,REFERENCES
   ON asset.* TO 'assetapp'@'%';
   ```

## 设计取舍

参考系统是元数据驱动的低代码平台（表单定义 + 六个规则引擎 + 工作流设计器），那套复杂度是为服务几万家租户才必要的。这里采用领域固化：卡片、状态机、字段写死在代码里，只把编码规则、扩展字段（`asset_card.ext_json`）、列表列配置做成可配。

单据流（入库单、领用退库、调拨、盘点、折旧计提）留到二期。
