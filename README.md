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
# 只比对不写库，看这次同步会影响多少张卡
go run ./cmd/dryrun -config config.yaml -mode full -dry-run=true
# 去掉 -dry-run=false 才真的写
```

> 注意：dry-run 的计数可能和实跑不一致（dry-run 不写库、状态不推进，
> 同编码重复行的第二次比对会重复计数）。别拿 dry-run 的数字当预期值。

### 星瀚接口字段扫描

```bash
# 扫描，并和上次的基线对比（基线不存在时自动创建）
go run ./cmd/kdscan -config config.yaml

# 确认变化无误后，把本次结果存为新基线
go run ./cmd/kdscan -config config.yaml -update

# 连"仅数量波动"的字段一起看
go run ./cmd/kdscan -config config.yaml -v
```

拉全量、列出所有字段与取值分布、展开 `finentry` 明细子表的全部键、统计同编码重复行，
**并和上次扫描的字段指纹逐字段对比**。怀疑「字段取不到值 / 金额对不上」时先跑它。

指纹基线存在 `docs/kingdee-fields-baseline.json`。指纹只记「字段在不在、有没有非零值」，
**不记具体值**——具体值天天在变，记进去只会把 diff 淹没在噪声里。

对比结果按处理优先级分五级：

| 标记 | 含义 | 要处理吗 |
|---|---|---|
| `✗ 消失` | 投影列被删了 | 要。代码不会报错，只会从此静默读到零值 |
| `! 开始有值` | 字段一直恒 0，现在有数据了 | 要。通常正意味着可以接入了 |
| `! 变全零` | 原本有值，现在全归零 | 要。被撤了，或者对方改了列名 |
| `+ 新增*有值` | 多出来的键，且带值 | 要 |
| `· 计数变化` | 仅数量波动（新建卡、金额变动） | 不用，加 `-v` 才显示 |

计数波动默认不报警：本库天天有卡在增减，若 227 行变 228 行也报 ⚠，很快就没人看了。
告警一旦开始"狼来了"，就等于没有。

> **为什么需要它**：星瀚侧改接口投影是**静默的**。2026-09-17 当天 19:18 扫到
> `finentry` 只有 2 个键且全库恒为 0，20:07 再扫已变成 39 个键、真实取值挂在
> `originalfincard_*` 前缀上——两次之间没有任何通知。
>
> 更坑的是：那两个旧键**一直都在**，只是永远为 0。**「键在」和「有数据」是两回事**，
> 只对比字段名的做法抓不到这个区别，所以指纹必须记 `nonzero`。
>
> 所以「字段取不到值」的第一反应是**重扫**，而不是翻旧结论。
> 详见 `docs/星瀚接口字段扩展需求.md`。

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
