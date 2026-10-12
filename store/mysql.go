package store

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"asset-mgr/model"

	_ "github.com/go-sql-driver/mysql"
)

type Store struct {
	db *sql.DB
}

func New(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	for i, ddl := range schema {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("schema[%d]: %w", i, err)
		}
	}
	// 金蝶对同一资产编号可能返回多张卡（合并卡），一个本地卡允许被多个外部卡引用，
	// 因此不能再对 (source, card_id) 建唯一约束。老库需要显式删掉这个索引。
	if err := s.dropIndexIfExists("external_asset_map", "uk_ext_card"); err != nil {
		return err
	}
	// 老库的 asset_card 是 CREATE TABLE IF NOT EXISTS 建的，新加列不会自动出现
	if err := s.addColumnIfMissing("asset_card", "use_status", "VARCHAR(32) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("asset_card", "card_created_at", "DATETIME DEFAULT NULL"); err != nil {
		return err
	}
	// 数量列：金蝶 assetamount（如房屋 194.5200 平方米）。金蝶给 10 位小数，
	// 取 4 位与星瀚界面显示口径一致；金额列维持 2 位不受影响。
	if err := s.addColumnIfMissing("asset_card", "quantity", "DECIMAL(18,4) NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// biz_status 是台账本地业务状态列（维修中/…），与星瀚托管的 status 物理分离。
	// 这是「维修状态不被每日同步抹掉」的唯一前提：只要它不进同步的三个列清单，
	// 同步路径就逐字节不变（见 docs/维修流程模块架构建议.md §1、§1.4）。
	if err := s.addColumnIfMissing("asset_card", "biz_status", "VARCHAR(16) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 「只读」账号按 user_emp_id 收窄列表，这列原本没索引，不加就是全表扫
	if err := s.createIndexIfMissing("asset_card", "idx_card_user", "(user_emp_id)"); err != nil {
		return err
	}
	// 「部门负责人」管辖的部门记在账号上：员工主数据的 dept_id 基本是空的，
	// 拿它推部门等于每个部门负责人都开不起来。
	if err := s.addColumnIfMissing("sys_user", "dept_id", "BIGINT NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// sys_user.roles：账号的全部角色，逗号分隔、无空格（见 docs/多角色改造方案.md）。
	//
	// 必须走 addColumnIfMissingBackfill 而不是裸 ALTER：生产已有存量账号，
	// 回填把原单角色写进 roles，FIND_IN_SET 查询（api/notify_plan.go）才查得到人。
	// 也**不能**用「每次启动都 UPDATE ... WHERE roles 为空」：该函数只在列首次新增时回填一次。
	//
	// ⚠️ store/user.go 的 Authenticate / ListUsers / GetUser / UserByEmployee 都 SELECT 了 roles，
	// 缺这一列会让登录直接 Unknown column 'roles'。deploy/migrations/20260924_multi_role.sql 是
	// 同一件事的手工副本，运行时的权威路径是这里。
	if err := s.addColumnIfMissingBackfill("sys_user", "roles",
		"VARCHAR(128) NOT NULL DEFAULT ''",
		"UPDATE sys_user SET roles = role WHERE roles = '' OR roles IS NULL"); err != nil {
		return err
	}
	// 就地改写**已废弃的角色名**（viewer→employee、repair_tech→maintainer）。
	// 只补 roles 列还不够：存量行两列里留的还是旧名，而旧名已不在 model.Roles 里，
	// 将来任何按角色名的查询（FIND_IN_SET('employee', roles)）都会漏掉这些账号。
	// 放在 roles 回填之后：此时两列都已有值，可一并改名并保持 role == roles 的第一个角色。
	if err := s.migrateLegacyRoleNames(); err != nil {
		return err
	}
	// asset_category.repairable 是台账本地列：该分类下的资产是否允许提交维修单。
	// 之所以敢在分类表加本地列（而不像 status 那样被每日同步抹掉），唯一依据是
	// 「同步链路对 asset_category 只有 INSERT、没有 UPDATE」——由
	// store/category_guard_test.go 的 TestCategorySyncHasNoUpdatePath 长期钉死
	// （见 docs/增量架构-可维修标签与首页.md §1.1）。
	//
	// 用 addColumnIfMissingBackfill：backfill 只在列确实不存在、本次刚加时执行一次，
	// 不会每次启动都把管理员手工改过的值重置回默认值（该函数语义见下方实现）。
	// 老板已拍板：机器设备(0203)/运输工具(0204)/电子设备(0205)/办公设备(0206) = 可维修；
	// 其余（土地资产 0201 / 房屋及建筑物 0202·0302 / 建筑物 0101 / 土地使用权 0102 / 其他 0299）
	// = 不可维修（取列默认 0）。
	//
	// 按 code 而非 id 匹配：seed 只种 7 条，生产库有 10 条（另 3 条来自导入），按 id 写死会在生产错位。
	if err := s.addColumnIfMissingBackfill("asset_category", "repairable",
		"TINYINT(1) NOT NULL DEFAULT 0",
		"UPDATE asset_category SET repairable = 1 WHERE code IN ('0203','0204','0205','0206')"); err != nil {
		return err
	}
	if err := s.migrateOrg(); err != nil {
		return err
	}
	return s.migrateNotify()
}

// migrateOrg 是组织主数据（公司 / 部门 / 员工）从星瀚同步所需的新增列。
//
// 为什么要记 source：这三张表原本是手工维护的，现在改由星瀚托管。
// 不标来源的话，人工建的部门和同步进来的部门混在一起，
// 出了问题分不清「是星瀚数据不对」还是「谁手工改过」。
// 更实际的是：同步只该覆盖 source='kingdee' 的行，
// 手工建的行保留本地值（与资产卡的「托管字段」是同一套思路）。
func (s *Store) migrateOrg() error {
	// department：组织树的位置与状态。parent_id / company_id 是本地 ID，
	// 这两个才是落库真正要用的；longnumber / level 是星瀚侧的原始坐标，
	// 保留下来是为了让「为什么这个部门挂到这家公司」在库里能自证。
	for _, c := range []struct{ col, def string }{
		{"longnumber", "VARCHAR(255) NOT NULL DEFAULT ''"},
		{"level", "INT NOT NULL DEFAULT 0"},
		{"enabled", "TINYINT(1) NOT NULL DEFAULT 1"},
		{"source", "VARCHAR(16) NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing("department", c.col, c.def); err != nil {
			return err
		}
	}
	for _, t := range []string{"employee", "company"} {
		if err := s.addColumnIfMissing(t, "source", "VARCHAR(16) NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	// employee.open_id：云之家 openId，待办通知据此定向投递（见 docs/云之家待办通知集成方案.md §6.3）。
	//
	// 这是**本地维护列**：星瀚同步的 UpdateOrgEmployeeTx（store/orgsync.go）是显式列清单，
	// 不含 open_id，所以每日同步不会抹掉它——该前提由 store/employee_openid_guard_test.go 的
	// TestNoEmployeeWriteTargetsOpenID 长期钉死（一旦有人在同步路径里给 UPDATE employee 加了
	// open_id，本地维护的 openId 会被静默抹掉，且不会让任何别的测试变红）。
	//
	// 写入路径只有两条，且都只写这一列：SSO 回填（路径 A）/ 通讯录同步（路径 B）。
	if err := s.addColumnIfMissing("employee", "open_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// ⚠️ 只能是**非唯一**普通索引，绝不能建 UNIQUE：绝大多数员工没有 openId，
	// 默认空串 '' 会大量重复，唯一约束根本建不起来（第一条 '' 之外的 '' 行全部冲突）。见 §6.3。
	if err := s.createIndexIfMissing("employee", "idx_emp_open_id", "(open_id)"); err != nil {
		return err
	}
	// 同步时按 longnumber 前缀判范围、按 code 找本地行，都要走索引
	if err := s.createIndexIfMissing("department", "idx_dept_code", "(code)"); err != nil {
		return err
	}
	// sync_run 原本只有资产卡一种资源，组织同步的跑批混在里面分不出来。
	//
	// 新增列后必须回填一次：历史行全部是资产卡同步留下的，而新列默认是空串。
	// 不回填的话，`WHERE resource = 'asset_card'` 会把它们全部挡掉——
	// 表现就是前端「最近一次运行」和资源筛选双双变成空的，历史批次看起来凭空消失。
	if err := s.addColumnIfMissingBackfill("sync_run", "resource",
		"VARCHAR(64) NOT NULL DEFAULT ''",
		fmt.Sprintf("UPDATE sync_run SET resource = '%s' WHERE resource = ''", model.ResourceAssetCard)); err != nil {
		return err
	}
	return nil
}

// migrateNotify 是 repair_notify_task 灰度切换（im-todo → 流程中心）所需的新增列与索引。
//
// 为什么必须补 channel 列：没有它，开关一打开就无法区分「切换前发出的存量待办」与
// 「切换后新发的流程中心待办」该走哪个办结接口，切换前已发出的待办在云之家会**永远**
// 转不成「已办」（见 docs/云之家流程中心集成方案.md「必读」第 2 条）。
//
// 走 addColumnIfMissing / createIndexIfMissing 而非裸 ALTER：生产已有存量行
// （active/dealt），迁移必须**幂等可重跑**——这两个 helper 先查 information_schema 再决定是否 ALTER。
func (s *Store) migrateNotify() error {
	// channel：这条待办是用哪个通道发出去的（model.NotifyChannel*）。
	// 默认 'im-todo' 正是要的语义：存量行都是切换前发出的 im-todo 待办。
	if err := s.addColumnIfMissing("repair_notify_task", "channel",
		"VARCHAR(16) NOT NULL DEFAULT '"+model.NotifyChannelIMTodo+"'"); err != nil {
		return err
	}
	// biz_id：流程中心的节点级 bizId；im-todo 通道下为空串（默认值即空串）。
	if err := s.addColumnIfMissing("repair_notify_task", "biz_id",
		"VARCHAR(128) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 消费端主查询「按通道捞待处理任务」走这个复合索引。
	if err := s.createIndexIfMissing("repair_notify_task", "idx_notify_channel_state", "(channel, state)"); err != nil {
		return err
	}
	return nil
}

func (s *Store) createIndexIfMissing(table, index, columns string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, index).Scan(&n); err != nil {
		return fmt.Errorf("check index %s.%s: %w", table, index, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD INDEX %s %s", table, index, columns)); err != nil {
		return fmt.Errorf("add index %s.%s: %w", table, index, err)
	}
	return nil
}

func (s *Store) dropIndexIfExists(table, index string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, index).Scan(&n); err != nil {
		return fmt.Errorf("check index %s.%s: %w", table, index, err)
	}
	if n == 0 {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s DROP INDEX %s", table, index)); err != nil {
		return fmt.Errorf("drop index %s.%s: %w", table, index, err)
	}
	return nil
}

func (s *Store) addColumnIfMissing(table, column, definition string) error {
	return s.addColumnIfMissingBackfill(table, column, definition, "")
}

// addColumnIfMissingBackfill 与 addColumnIfMissing 相同，但在新增列之后立刻执行一条回填 SQL。
//
// 回填只在列确实不存在时执行，所以它天然只跑一次，不会在每次启动时重复改写数据。
// 用它而不是「每次启动都 UPDATE ... WHERE col 为空串」：后者会在将来某次
// 真的插入了空值行时把数据悄悄改成另一个资源，而那种错误比缺一次回填难查得多。
func (s *Store) addColumnIfMissingBackfill(table, column, definition, backfill string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&n); err != nil {
		return fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	if backfill != "" {
		if _, err := s.db.Exec(backfill); err != nil {
			return fmt.Errorf("backfill %s.%s: %w", table, column, err)
		}
	}
	return nil
}

// migrateLegacyRoleNames 把 sys_user 里**已废弃的角色名**就地改写成现行名：
// viewer→employee、repair_tech→maintainer（映射来自 model.LegacyRoleRenames，绝不另写一份映射表）。
//
// 为什么数据侧也要改（而不是只靠 model.NormalizeRoleName 在内存里映射）：
//   - 代码层映射能救「读到旧名」的账号，但 roles 列里留着旧名是脏数据，
//     将来任何按角色名的 SQL（如 FIND_IN_SET('employee', roles)）都会漏掉这些账号。
//
// 语义与幂等性：
//   - 只改「含废弃名」的行，绝不无条件重写全部行（同 addColumnIfMissingBackfill 的注释：
//     每次启动都 UPDATE 会在将来真的插入空值时把数据悄悄改坏）。
//   - role / roles **两列在同一条 UPDATE 里一起改**，保持 role == roles 的第一个角色。
//   - 幂等：改名后再跑，已无废弃名 ⇒ 0 行被改。
//   - 遇到**既非现行角色、也不在 LegacyRoleRenames 里**的垃圾值（如手工写错的 emplyoee）：
//     **只打警告、不自动改写**（静默改或静默不管都不可接受），由人工确认。
func (s *Store) migrateLegacyRoleNames() error {
	rows, err := s.db.Query(`SELECT id, username, role, roles FROM sys_user`)
	if err != nil {
		return fmt.Errorf("query sys_user for legacy roles: %w", err)
	}
	defer rows.Close()

	type roleFix struct {
		id                          int64
		username, oldRole, oldRoles string
		newRole, newRoles           string
	}
	var fixes []roleFix

	for rows.Next() {
		var (
			id                 int64
			username, role, rs string
		)
		if err := rows.Scan(&id, &username, &role, &rs); err != nil {
			return err
		}
		tokens := splitRoleTokens(role, rs)

		hasLegacy, hasUnknown := false, false
		var unknown []string
		for _, tk := range tokens {
			if _, ok := model.LegacyRoleRenames[tk]; ok {
				hasLegacy = true
				continue
			}
			if model.NormalizeRoleName(tk) == "" { // 既非现行角色，也非废弃名
				hasUnknown = true
				unknown = append(unknown, tk)
			}
		}
		// 含无法识别的垃圾值 → 只警告、绝不自动改写（人工确认）。
		if hasUnknown {
			log.Printf("角色名迁移：警告：id=%d username=%q 含无法识别的角色值 %v（既非现行角色也非废弃名），未自动改写，请人工确认",
				id, username, unknown)
			continue
		}
		if !hasLegacy {
			continue
		}
		// 现行名（含已映射的废弃名）归一化后写回：去重、按声明顺序排序、逗号无空格。
		newRoles := model.FormatRoles(tokens)
		newRole := ""
		if parsed := model.ParseRoles(newRoles); len(parsed) > 0 {
			newRole = parsed[0]
		}
		if newRole == role && newRoles == rs {
			continue // 已规范，无需改（幂等的另一半保障）
		}
		fixes = append(fixes, roleFix{id: id, username: username, oldRole: role, oldRoles: rs, newRole: newRole, newRoles: newRoles})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// 先关游标再发 UPDATE：MySQL 上边遍历结果集边写同表可能出问题。
	rows.Close()

	for _, f := range fixes {
		if _, err := s.db.Exec(`UPDATE sys_user SET role=?, roles=? WHERE id=?`, f.newRole, f.newRoles, f.id); err != nil {
			return fmt.Errorf("migrate legacy role id=%d: %w", f.id, err)
		}
		log.Printf("角色名迁移：id=%d username=%q role %q→%q roles %q→%q",
			f.id, f.username, f.oldRole, f.newRole, f.oldRoles, f.newRoles)
	}
	if len(fixes) > 0 {
		log.Printf("角色名迁移：共改写 %d 行（废弃角色名 viewer→employee / repair_tech→maintainer）", len(fixes))
	}
	return nil
}

// splitRoleTokens 把 role 单值列与 roles 逗号分隔列拆成 token 列表（去空白、去空串）。
func splitRoleTokens(role, roles string) []string {
	var toks []string
	if t := strings.TrimSpace(role); t != "" {
		toks = append(toks, t)
	}
	for _, p := range strings.Split(roles, ",") {
		if p = strings.TrimSpace(p); p != "" {
			toks = append(toks, p)
		}
	}
	return toks
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS company (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		code VARCHAR(64) NOT NULL DEFAULT '',
		tax_no VARCHAR(64) NOT NULL DEFAULT '',
		sort_index INT NOT NULL DEFAULT 0,
		source VARCHAR(16) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uk_company_name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS department (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		code VARCHAR(64) NOT NULL DEFAULT '',
		parent_id BIGINT NOT NULL DEFAULT 0,
		company_id BIGINT NOT NULL DEFAULT 0,
		sort_index INT NOT NULL DEFAULT 0,
		longnumber VARCHAR(255) NOT NULL DEFAULT '',
		level INT NOT NULL DEFAULT 0,
		enabled TINYINT(1) NOT NULL DEFAULT 1,
		source VARCHAR(16) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_dept_parent (parent_id),
		KEY idx_dept_company (company_id),
		KEY idx_dept_code (code)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS employee (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		emp_no VARCHAR(64) NOT NULL DEFAULT '',
		name VARCHAR(64) NOT NULL,
		dept_id BIGINT NOT NULL DEFAULT 0,
		company_id BIGINT NOT NULL DEFAULT 0,
		phone VARCHAR(32) NOT NULL DEFAULT '',
		active TINYINT(1) NOT NULL DEFAULT 1,
		source VARCHAR(16) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_emp_dept (dept_id),
		KEY idx_emp_name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS asset_category (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		code VARCHAR(64) NOT NULL DEFAULT '',
		parent_id BIGINT NOT NULL DEFAULT 0,
		use_months INT NOT NULL DEFAULT 0,
		residual_rate DECIMAL(6,3) NOT NULL DEFAULT 0,
		sort_index INT NOT NULL DEFAULT 0,
		-- repairable 是台账本地列：该分类下的资产是否允许提交维修单。0=不可维修，1=可维修。
		-- 同步链路对分类只 INSERT、不 UPDATE，故不会被每日同步抹掉（见 store/category_guard_test.go）。
		repairable TINYINT(1) NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_cat_parent (parent_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS asset_area (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		short_name VARCHAR(64) NOT NULL DEFAULT '',
		code VARCHAR(64) NOT NULL DEFAULT '',
		parent_id BIGINT NOT NULL DEFAULT 0,
		sort_index INT NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_area_parent (parent_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS vendor (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		code VARCHAR(64) NOT NULL DEFAULT '',
		contact VARCHAR(64) NOT NULL DEFAULT '',
		phone VARCHAR(32) NOT NULL DEFAULT '',
		remark VARCHAR(500) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uk_vendor_name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS asset_tag (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(64) NOT NULL,
		color VARCHAR(16) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uk_tag_name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS asset_card_tag (
		card_id BIGINT NOT NULL,
		tag_id BIGINT NOT NULL,
		PRIMARY KEY (card_id, tag_id),
		KEY idx_cardtag_tag (tag_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS code_rule (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		category_id BIGINT NOT NULL DEFAULT 0,
		prefix VARCHAR(16) NOT NULL DEFAULT '',
		date_format VARCHAR(16) NOT NULL DEFAULT '',
		seq_width INT NOT NULL DEFAULT 5,
		current_seq BIGINT NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		UNIQUE KEY uk_rule_category (category_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sys_user (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		username VARCHAR(64) NOT NULL,
		password_hash VARCHAR(255) NOT NULL,
		real_name VARCHAR(64) NOT NULL DEFAULT '',
		-- role 是主角色（= roles 的第一个），保留给旧 token / 旧前端读；默认取现存角色 employee。
		role VARCHAR(16) NOT NULL DEFAULT 'employee',
		-- roles 是账号的全部角色，逗号分隔、无空格；唯一事实来源（见 docs/多角色改造方案.md）。
		roles VARCHAR(128) NOT NULL DEFAULT '',
		employee_id BIGINT NOT NULL DEFAULT 0,
		-- 部门负责人账号管辖的部门，见 model.RoleDeptHead
		dept_id BIGINT NOT NULL DEFAULT 0,
		enabled TINYINT(1) NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uk_user_name (username)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS asset_history (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		card_id BIGINT NOT NULL,
		action VARCHAR(16) NOT NULL,
		field VARCHAR(64) NOT NULL DEFAULT '',
		old_value VARCHAR(500) NOT NULL DEFAULT '',
		new_value VARCHAR(500) NOT NULL DEFAULT '',
		operator VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_hist_card (card_id, id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS asset_attachment (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		card_id BIGINT NOT NULL DEFAULT 0,
		kind VARCHAR(16) NOT NULL DEFAULT 'file',
		origin_name VARCHAR(255) NOT NULL DEFAULT '',
		stored_path VARCHAR(255) NOT NULL,
		size_bytes BIGINT NOT NULL DEFAULT 0,
		uploaded_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_att_card (card_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS asset_card (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		asset_code VARCHAR(64) NOT NULL,
		name VARCHAR(128) NOT NULL,
		category_id BIGINT NOT NULL DEFAULT 0,
		spec VARCHAR(128) NOT NULL DEFAULT '',
		serial_no VARCHAR(64) NOT NULL DEFAULT '',
		unit VARCHAR(16) NOT NULL DEFAULT '',
		status VARCHAR(16) NOT NULL DEFAULT '闲置',
		-- biz_status 是台账本地维护的业务状态（维修中/…），与星瀚托管的 status 物理分离。
		-- 绝不加入 kingdeeOwnedColumns / cardInitColumns / cardWriteColumns（见 store/sync.go 注释）。
		biz_status VARCHAR(16) NOT NULL DEFAULT '',
		amount DECIMAL(14,2) NOT NULL DEFAULT 0,
		quantity DECIMAL(18,4) NOT NULL DEFAULT 0,
		use_company_id BIGINT NOT NULL DEFAULT 0,
		use_dept_id BIGINT NOT NULL DEFAULT 0,
		user_emp_id BIGINT NOT NULL DEFAULT 0,
		use_status VARCHAR(32) NOT NULL DEFAULT '',
		manager_emp_id BIGINT NOT NULL DEFAULT 0,
		owner_company_id BIGINT NOT NULL DEFAULT 0,
		area_id BIGINT NOT NULL DEFAULT 0,
		location VARCHAR(128) NOT NULL DEFAULT '',
		purchase_date DATE DEFAULT NULL,
		card_created_at DATETIME DEFAULT NULL,
		use_months INT NOT NULL DEFAULT 0,
		source VARCHAR(32) NOT NULL DEFAULT '',
		in_stock_no VARCHAR(64) NOT NULL DEFAULT '',
		rfid VARCHAR(64) NOT NULL DEFAULT '',
		remark VARCHAR(500) NOT NULL DEFAULT '',
		fin_asset_type VARCHAR(32) NOT NULL DEFAULT '',
		fin_share_dept_id BIGINT NOT NULL DEFAULT 0,
		vendor_id BIGINT NOT NULL DEFAULT 0,
		fin_amount_with_tax DECIMAL(14,2) NOT NULL DEFAULT 0,
		fin_tax DECIMAL(14,2) NOT NULL DEFAULT 0,
		fin_original_value DECIMAL(14,2) NOT NULL DEFAULT 0,
		fin_net_value DECIMAL(14,2) NOT NULL DEFAULT 0,
		fin_accum_depreciation DECIMAL(14,2) NOT NULL DEFAULT 0,
		fin_residual_rate DECIMAL(6,3) NOT NULL DEFAULT 0,
		fin_use_months INT NOT NULL DEFAULT 0,
		fin_period VARCHAR(7) NOT NULL DEFAULT '',
		fin_entry_date DATE DEFAULT NULL,
		fin_status VARCHAR(16) NOT NULL DEFAULT '未入账',
		mt_vendor_id BIGINT NOT NULL DEFAULT 0,
		mt_contact VARCHAR(64) NOT NULL DEFAULT '',
		mt_phone VARCHAR(32) NOT NULL DEFAULT '',
		mt_owner_emp_id BIGINT NOT NULL DEFAULT 0,
		mt_expire_date DATE DEFAULT NULL,
		mt_remark VARCHAR(500) NOT NULL DEFAULT '',
		ext_json JSON DEFAULT NULL,
		created_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		deleted_at DATETIME DEFAULT NULL,
		UNIQUE KEY uk_card_code (asset_code),
		KEY idx_card_status_cat (status, category_id),
		KEY idx_card_dept (use_dept_id),
		KEY idx_card_purchase (purchase_date),
		KEY idx_card_deleted (deleted_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS external_asset_map (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		source VARCHAR(32) NOT NULL DEFAULT 'kingdee',
		external_id VARCHAR(64) NOT NULL,
		external_code VARCHAR(64) NOT NULL DEFAULT '',
		card_id BIGINT NOT NULL,
		external_version VARCHAR(64) NOT NULL DEFAULT '',
		payload_hash VARCHAR(64) NOT NULL DEFAULT '',
		last_sync_run_id BIGINT NOT NULL DEFAULT 0,
		status VARCHAR(16) NOT NULL DEFAULT 'active',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		deleted_at DATETIME DEFAULT NULL,
		UNIQUE KEY uk_ext_map (source, external_id),
		KEY idx_ext_card (source, card_id),
		KEY idx_ext_run (last_sync_run_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS external_master_map (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		source VARCHAR(32) NOT NULL DEFAULT 'kingdee',
		kind VARCHAR(32) NOT NULL,
		external_id VARCHAR(64) NOT NULL DEFAULT '',
		external_code VARCHAR(64) NOT NULL DEFAULT '',
		external_name VARCHAR(128) NOT NULL DEFAULT '',
		local_id BIGINT NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		UNIQUE KEY uk_ext_master (source, kind, external_id),
		KEY idx_ext_master_kind (source, kind, local_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS sync_run (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		source VARCHAR(32) NOT NULL DEFAULT 'kingdee',
		resource VARCHAR(64) NOT NULL DEFAULT '',
		mode VARCHAR(16) NOT NULL,
		triggered_by VARCHAR(64) NOT NULL DEFAULT '',
		cursor_value VARCHAR(64) NOT NULL DEFAULT '',
		status VARCHAR(16) NOT NULL DEFAULT 'running',
		total_count INT NOT NULL DEFAULT 0,
		created_count INT NOT NULL DEFAULT 0,
		updated_count INT NOT NULL DEFAULT 0,
		skipped_count INT NOT NULL DEFAULT 0,
		deleted_count INT NOT NULL DEFAULT 0,
		failed_count INT NOT NULL DEFAULT 0,
		error_summary VARCHAR(500) NOT NULL DEFAULT '',
		started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		finished_at DATETIME DEFAULT NULL,
		KEY idx_sync_run_started (started_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS sync_run_error (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		run_id BIGINT NOT NULL,
		external_id VARCHAR(64) NOT NULL DEFAULT '',
		stage VARCHAR(32) NOT NULL DEFAULT '',
		field VARCHAR(64) NOT NULL DEFAULT '',
		error_code VARCHAR(64) NOT NULL DEFAULT '',
		error_message VARCHAR(500) NOT NULL DEFAULT '',
		raw_value VARCHAR(500) NOT NULL DEFAULT '',
		retryable TINYINT(1) NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_err_run (run_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS sync_state (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		source VARCHAR(32) NOT NULL DEFAULT 'kingdee',
		resource VARCHAR(64) NOT NULL,
		cursor_value VARCHAR(64) NOT NULL DEFAULT '',
		last_run_id BIGINT NOT NULL DEFAULT 0,
		last_success_at DATETIME DEFAULT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		UNIQUE KEY uk_sync_state (source, resource)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS count_plan (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		code VARCHAR(32) NOT NULL DEFAULT '',
		name VARCHAR(128) NOT NULL,
		scope_json TEXT NOT NULL,
		status VARCHAR(16) NOT NULL DEFAULT 'draft',
		remark VARCHAR(500) NOT NULL DEFAULT '',
		created_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME DEFAULT NULL,
		finished_at DATETIME DEFAULT NULL,
		KEY idx_count_plan_status (status)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS count_item (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		plan_id BIGINT NOT NULL,
		card_id BIGINT NOT NULL,
		asset_code VARCHAR(64) NOT NULL DEFAULT '',
		name VARCHAR(255) NOT NULL DEFAULT '',
		category_name VARCHAR(64) NOT NULL DEFAULT '',
		use_dept_name VARCHAR(64) NOT NULL DEFAULT '',
		user_name VARCHAR(64) NOT NULL DEFAULT '',
		location VARCHAR(255) NOT NULL DEFAULT '',
		use_status VARCHAR(32) NOT NULL DEFAULT '',
		book_amount DECIMAL(18,2) NOT NULL DEFAULT 0,
		assignee_id BIGINT NOT NULL DEFAULT 0,
		assignee_name VARCHAR(64) NOT NULL DEFAULT '',
		result VARCHAR(16) NOT NULL DEFAULT '',
		note VARCHAR(255) NOT NULL DEFAULT '',
		counted_by VARCHAR(64) NOT NULL DEFAULT '',
		counted_at DATETIME DEFAULT NULL,
		KEY idx_count_item_plan (plan_id),
		KEY idx_count_item_assignee (assignee_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// —— 维修流程模块（见 docs/维修流程模块架构建议.md §2）——
	// 维修单主表。与 count_plan 同构：单号 + 状态机 + 关联资产。
	// 快照字段（asset_code/asset_name/use_dept_name/location）参照 count_item 做法；
	// 外键一律 BIGINT，0 = 未设置哨兵值；单据靠终态归档，无 deleted_at。
	`CREATE TABLE IF NOT EXISTS repair_order (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		code VARCHAR(32) NOT NULL DEFAULT '',
		card_id BIGINT NOT NULL,
		asset_code VARCHAR(64) NOT NULL DEFAULT '',
		asset_name VARCHAR(128) NOT NULL DEFAULT '',
		reporter_emp_id BIGINT NOT NULL DEFAULT 0,
		reporter_name VARCHAR(64) NOT NULL DEFAULT '',
		use_dept_id BIGINT NOT NULL DEFAULT 0,
		use_dept_name VARCHAR(64) NOT NULL DEFAULT '',
		location VARCHAR(128) NOT NULL DEFAULT '',
		fault_desc VARCHAR(500) NOT NULL DEFAULT '',
		urgency VARCHAR(16) NOT NULL DEFAULT 'normal',
		status VARCHAR(16) NOT NULL DEFAULT 'pending',
		assignee_emp_id BIGINT NOT NULL DEFAULT 0,
		assignee_name VARCHAR(64) NOT NULL DEFAULT '',
		vendor_id BIGINT NOT NULL DEFAULT 0,
		vendor_name VARCHAR(128) NOT NULL DEFAULT '',
		handler_desc VARCHAR(500) NOT NULL DEFAULT '',
		reject_reason VARCHAR(255) NOT NULL DEFAULT '',
		cost DECIMAL(14,2) NOT NULL DEFAULT 0,
		cost_dept_id BIGINT NOT NULL DEFAULT 0,
		accept_by VARCHAR(64) NOT NULL DEFAULT '',
		accept_at DATETIME DEFAULT NULL,
		assign_at DATETIME DEFAULT NULL,
		start_at DATETIME DEFAULT NULL,
		finish_at DATETIME DEFAULT NULL,
		confirm_at DATETIME DEFAULT NULL,
		close_at DATETIME DEFAULT NULL,
		created_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		KEY idx_repair_card (card_id),
		KEY idx_repair_status (status),
		KEY idx_repair_reporter (reporter_emp_id),
		KEY idx_repair_assignee (assignee_emp_id),
		KEY idx_repair_dept (use_dept_id),
		KEY idx_repair_created (created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// 通用单据状态流转记录：纯 append-only 审计表、零业务逻辑，带 doc_type 判别式，
	// 后续单据流（调拨/领用）直接复用（D2）。
	`CREATE TABLE IF NOT EXISTS doc_status_log (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		doc_type VARCHAR(32) NOT NULL,
		doc_id BIGINT NOT NULL,
		from_status VARCHAR(16) NOT NULL DEFAULT '',
		to_status VARCHAR(16) NOT NULL DEFAULT '',
		action VARCHAR(32) NOT NULL DEFAULT '',
		operator VARCHAR(64) NOT NULL DEFAULT '',
		remark VARCHAR(500) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_doc_log (doc_type, doc_id, id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// 维修附件：归属语义与 asset_attachment 不同（按单据收敛），存储层复用 uploads/ 目录。
	// repair_id=0 表示「先上传待绑定」，对齐 asset_attachment 的 card_id=0 约定。
	`CREATE TABLE IF NOT EXISTS repair_attachment (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		repair_id BIGINT NOT NULL DEFAULT 0,
		card_id BIGINT NOT NULL DEFAULT 0,
		kind VARCHAR(16) NOT NULL DEFAULT 'photo',
		origin_name VARCHAR(255) NOT NULL DEFAULT '',
		stored_path VARCHAR(255) NOT NULL,
		size_bytes BIGINT NOT NULL DEFAULT 0,
		uploaded_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		KEY idx_repair_att (repair_id),
		KEY idx_repair_att_card (card_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// —— 云之家待办通知（见 docs/云之家待办通知集成方案.md §4.3 / 附录 C）——
	// 待办投递的补偿 / 幂等表：**每人每单每 sourceId** 一行（UNIQUE KEY uk_notify）。
	// 它是「本地唯一真相」，用于重试与幂等——绝不依赖云之家 create 报错去重
	// （501 是部分成功、202 是频率限制、sync=false 下重复 create 根本不报错）。
	// state 取值见 model.NotifyState*（pending/active/dealt/deleted/failed）。
	// 不落 created/updated 以外的业务字段：语义比 sync_run（拉数据跑批）完全不同，
	// 故独立建表而不混进 sync_run / sync_run_error。
	//
	// channel / biz_id 是灰度切换（im-todo → 流程中心）的地基，见 docs/云之家流程中心集成方案.md
	// 「必读」第 2 条：每条待办必须记住自己是用哪个通道发出去的，否则切换前发出的存量待办
	// 在云之家永远转不成「已办」。channel 默认 'im-todo'（存量行即切换前的 im-todo 待办）。
	`CREATE TABLE IF NOT EXISTS repair_notify_task (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		repair_id BIGINT NOT NULL,
		emp_id BIGINT NOT NULL,
		open_id VARCHAR(64) NOT NULL DEFAULT '',
		source_id VARCHAR(64) NOT NULL,
		purpose VARCHAR(32) NOT NULL DEFAULT '',
		channel VARCHAR(16) NOT NULL DEFAULT 'im-todo',
		biz_id VARCHAR(128) NOT NULL DEFAULT '',
		state VARCHAR(16) NOT NULL DEFAULT 'pending',
		attempts INT NOT NULL DEFAULT 0,
		last_error VARCHAR(500) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		UNIQUE KEY uk_notify (repair_id, emp_id, source_id),
		KEY idx_notify_repair (repair_id),
		KEY idx_notify_state (state),
		KEY idx_notify_channel_state (channel, state)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// —— IT 信息设备台账（2026-10-10 新增需求，见 docs/IT设备台账-需求与方案.md）——
	//
	// 为什么独立建表而不往 asset_card 加列：asset_card 是星瀚托管表，每日 00:00 同步会覆盖
	// 本地值（全表只有 biz_status 一个本地列逃过，且唯一写入者是维修状态机）。
	// IT 属性是纯本地数据，加过去第二天就没了。见 model/it.go 顶部注释。
	//
	// ⚠️ card_id 是**可空列**（不是 NOT NULL DEFAULT 0），这是本表设计的关键：
	//   - UNIQUE KEY uk_it_card (card_id) 保证「一张资产卡最多一条 IT 记录」（1:1）；
	//   - MySQL 唯一索引**对 NULL 不去重** ⇒ 任意多条「资产卡上没有的设备」（card_id IS NULL）
	//     可以共存。
	//   若改成 NOT NULL DEFAULT 0，第二台无卡设备就会被唯一键挡回去——这正是用户明确
	//   提出的真实场景，不能用 0 当哨兵值。Go 侧仍用 int64 的 0 表示「未关联」，
	//   靠 store/it.go 的 NULLIF(?,0) / COALESCE(card_id,0) 双向转换。
	//
	// ⚠️ asset_code **刻意不加唯一约束**：多条无卡设备的编码都是空串 ''，
	// 加唯一键第二条就插不进去（与 employee.open_id 同一个坑）。
	// 它只是冗余快照 + 导入匹配键，不是主键。
	//
	// ⚠️ user_emp_id **刻意不加唯一约束**：用户明确提出「员工一人多机」，
	// 一个员工对应多条记录是正常业务状态，不是脏数据。
	`CREATE TABLE IF NOT EXISTS it_equipment (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		card_id BIGINT DEFAULT NULL,
		asset_code VARCHAR(64) NOT NULL DEFAULT '',
		device_type VARCHAR(32) NOT NULL DEFAULT '',
		user_emp_id BIGINT NOT NULL DEFAULT 0,
		dept_id BIGINT NOT NULL DEFAULT 0,
		location VARCHAR(128) NOT NULL DEFAULT '',
		purpose VARCHAR(32) NOT NULL DEFAULT '',
		status VARCHAR(16) NOT NULL DEFAULT '在用',
		is_xinchuang TINYINT(1) NOT NULL DEFAULT 0,
		brand_model VARCHAR(128) NOT NULL DEFAULT '',
		cpu VARCHAR(128) NOT NULL DEFAULT '',
		memory_gb INT NOT NULL DEFAULT 0,
		disk VARCHAR(128) NOT NULL DEFAULT '',
		os_version VARCHAR(64) NOT NULL DEFAULT '',
		remark VARCHAR(500) NOT NULL DEFAULT '',
		created_by VARCHAR(64) NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		UNIQUE KEY uk_it_card (card_id),
		KEY idx_it_user (user_emp_id),
		KEY idx_it_dept (dept_id),
		KEY idx_it_status (status),
		KEY idx_it_code (asset_code)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}
