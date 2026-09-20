package store

import (
	"database/sql"
	"fmt"
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
	return s.migrateOrg()
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
// 用它而不是「每次启动都 UPDATE ... WHERE col = ”」：后者会在将来某次
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
		role VARCHAR(16) NOT NULL DEFAULT 'viewer',
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
}
