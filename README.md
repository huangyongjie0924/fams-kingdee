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
# 扫描资产卡，并和上次的基线对比（基线不存在时自动创建）
go run ./cmd/kdscan -config config.yaml

# 扫描人员接口
go run ./cmd/kdscan -config config.yaml -target personnel

# 两个接口一起扫
go run ./cmd/kdscan -config config.yaml -target all

# 确认变化无误后，把本次结果存为新基线
go run ./cmd/kdscan -config config.yaml -update

# 连"仅数量波动"的字段一起看
go run ./cmd/kdscan -config config.yaml -v
```

拉全量、列出所有字段与取值分布、展开明细子表（资产卡 `finentry` / 人员 `entryentity`）的全部键、
统计同编码重复行，**并和上次扫描的字段指纹逐字段对比**。怀疑「字段取不到值 / 金额对不上」时先跑它。

指纹基线：资产卡 `docs/kingdee-fields-baseline.json`，人员 `docs/kingdee-personnel-baseline.json`
（`-baseline-dir` 可改目录）。指纹只记「字段在不在、有没有非零值、服务端生效的过滤条件」，
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
> 所以「字段取不到值」的第一反应是**重扫**，而不是翻旧结论。
> 资产卡侧的投影变更详见 `docs/星瀚接口字段扩展需求.md`。

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
