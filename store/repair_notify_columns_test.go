package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"asset-mgr/model"
)

// 本文件钉死 repair_notify_task 的「列清单 ↔ Scan 目标」一致性。
//
// 为什么值得单独测：notifyTaskColumns 是**字符串清单**，scanNotifyTask 是**位置参数**。
// 两者一旦漂移（加了列没加 Scan、或调换了两列顺序），Go 不会报错、MySQL 也不会报错——
// 只会把值扫串位（把 channel 扫进 state、把 attempts 扫进 last_error），
// 而这类错误在业务上表现为「状态莫名不对」，极难定位。故用「按位置回填可识别值再逐字段断言」钉死。
//
// 反向验证：
//   - 从 notifyTaskColumns 删掉一列（如 channel）⇒ Scan 目标数 > 列数 ⇒ 本测试变红；
//   - 调换 scanNotifyTask 里两列的 Scan 顺序（如 Channel ↔ BizID）⇒ 字段值错位 ⇒ 本测试变红。

// columnValue 按列名给出一个**可识别**的值，供 positionRow 按位置回填。
// 新增列后必须在这里补一项，否则 TestNotifyTaskColumnsAlignWithScan 会直接 Fatal。
func columnValue(col string) (any, bool) {
	switch col {
	case "id":
		return int64(7), true
	case "repair_id":
		return int64(1409), true
	case "emp_id":
		return int64(88), true
	case "open_id":
		return "open-88", true
	case "source_id":
		return "repair-1409", true
	case "purpose":
		return "dispatch", true
	case "channel":
		return model.NotifyChannelFlowCenter, true
	case "biz_id":
		return "repair-1409-88-dispatch", true
	case "state":
		return "active", true
	case "attempts":
		return 3, true
	case "last_error":
		return "boom", true
	case "created_at":
		return time.Unix(1000, 0).UTC(), true
	case "updated_at":
		return time.Unix(2000, 0).UTC(), true
	}
	return nil, false
}

// positionRow 是 notifyRow 的测试实现：把第 i 个 Scan 目标填成「第 i 个列名」对应的值。
// 于是「列顺序」与「Scan 顺序」只要有一处漂移，回填到结构体字段的值就会错位。
type positionRow struct {
	cols []string
}

func (r *positionRow) Scan(dest ...any) error {
	if len(dest) != len(r.cols) {
		return fmt.Errorf("Scan 目标数 %d 与 notifyTaskColumns 列数 %d 不一致", len(dest), len(r.cols))
	}
	for i, d := range dest {
		v, ok := columnValue(r.cols[i])
		if !ok {
			return fmt.Errorf("列 %q 无对应测试值（新增列后请同步 columnValue）", r.cols[i])
		}
		switch p := d.(type) {
		case *int64:
			*p = v.(int64)
		case *int:
			*p = v.(int)
		case *string:
			*p = v.(string)
		case *time.Time:
			*p = v.(time.Time)
		default:
			return fmt.Errorf("第 %d 列 %q 的目标类型 %T 不受支持", i, r.cols[i], dest)
		}
	}
	return nil
}

func TestNotifyTaskColumnsAlignWithScan(t *testing.T) {
	cols := strings.Split(notifyTaskColumns, ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}

	// 每个列名都必须有可识别值（防止「加了列却忘了更新本测试」）。
	for _, c := range cols {
		if _, ok := columnValue(c); !ok {
			t.Fatalf("列 %q 没有对应的测试值——新增列后请同步更新 columnValue 与本测试断言", c)
		}
	}

	got, err := scanNotifyTask(&positionRow{cols: cols})
	if err != nil {
		t.Fatalf("scanNotifyTask 失败（列数或顺序不一致）: %v", err)
	}

	// 逐字段断言：任一列顺序漂移都会让这里的某个断言变红。
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ID", got.ID, int64(7)},
		{"RepairID", got.RepairID, int64(1409)},
		{"EmpID", got.EmpID, int64(88)},
		{"OpenID", got.OpenID, "open-88"},
		{"SourceID", got.SourceID, "repair-1409"},
		{"Purpose", got.Purpose, "dispatch"},
		{"State", got.State, "active"},
		{"Attempts", got.Attempts, 3},
		{"LastError", got.LastError, "boom"},
		{"CreatedAt", got.CreatedAt, time.Unix(1000, 0).UTC()},
		{"UpdatedAt", got.UpdatedAt, time.Unix(2000, 0).UTC()},
		{"Channel", got.Channel, model.NotifyChannelFlowCenter},
		{"BizID", got.BizID, "repair-1409-88-dispatch"},
	}
	// 断言条数必须与列数一致——否则「加了列但没加断言」会静默漏检。
	if len(checks) != len(cols) {
		t.Fatalf("断言条数 %d 与列数 %d 不一致（列: %v）", len(checks), len(cols), cols)
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, 期望 %v —— notifyTaskColumns 与 scanNotifyTask 顺序漂移", c.name, c.got, c.want)
		}
	}
}
