package store

import "fmt"

// Seed 只在对应表为空时写入，重复启动不会覆盖用户维护的数据。
func (s *Store) Seed(codePrefix string, seqWidth int) error {
	if err := s.seedIfEmpty("company", []string{
		`INSERT INTO company (name, code, sort_index) VALUES ('XXX公司', 'SJ', 1)`,
	}); err != nil {
		return err
	}

	if err := s.seedIfEmpty("asset_category", []string{
		// repairable 必须显式写死，不能省：migrate() 先于 Seed() 执行，全新空库部署时
		// 迁移里的 backfill（UPDATE ... WHERE code IN ('0203'..'0206')）跑在空表上是 no-op，
		// 随后这里的 INSERT 若取列默认 0，那 4 个本该可维修的分类会被静默置为不可维修。
		// 取值与迁移 backfill 逐字对齐：机器设备/运输工具/电子设备/办公设备 = 1，其余 = 0。
		`INSERT INTO asset_category (name, code, use_months, residual_rate, sort_index, repairable) VALUES
			('土地资产', '0201', 0, 0, 1, 0),
			('房屋及建筑物', '0202', 240, 5, 2, 0),
			('机器设备', '0203', 120, 5, 3, 1),
			('运输工具', '0204', 48, 5, 4, 1),
			('电子设备', '0205', 36, 5, 5, 1),
			('办公设备', '0206', 60, 5, 6, 1),
			('其他', '0299', 60, 5, 7, 0)`,
	}); err != nil {
		return err
	}

	if err := s.seedIfEmpty("asset_area", []string{
		`INSERT INTO asset_area (name, short_name, code, sort_index) VALUES ('本地', '本地', 'BD', 1)`,
	}); err != nil {
		return err
	}

	if err := s.seedIfEmpty("code_rule", []string{
		fmt.Sprintf(`INSERT INTO code_rule (category_id, prefix, seq_width, current_seq) VALUES (0, '%s', %d, 0)`,
			codePrefix, seqWidth),
	}); err != nil {
		return err
	}

	return nil
}

func (s *Store) seedIfEmpty(table string, stmts []string) error {
	var n int64
	// table 只来自本文件的字面量，不含外部输入
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		return fmt.Errorf("count %s: %w", table, err)
	}
	if n > 0 {
		return nil
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("seed %s: %w", table, err)
		}
	}
	return nil
}
