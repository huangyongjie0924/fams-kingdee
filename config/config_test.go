package config

import "testing"

func TestParseDailyAt(t *testing.T) {
	ok := []struct {
		in        string
		hour, min int
	}{
		{"00:00", 0, 0},
		{"23:59", 23, 59},
		{"01:30", 1, 30},
		{" 08:05 ", 8, 5}, // 容忍首尾空白（YAML 里手写容易带上）
	}
	for _, c := range ok {
		hour, min, err := ParseDailyAt(c.in)
		if err != nil {
			t.Errorf("ParseDailyAt(%q) 报错: %v", c.in, err)
			continue
		}
		if hour != c.hour || min != c.min {
			t.Errorf("ParseDailyAt(%q) = %02d:%02d，期望 %02d:%02d", c.in, hour, min, c.hour, c.min)
		}
	}

	// 这些都必须在启动时就报错。放到调度器里宽松解析的话，
	// 一个写错的时刻会表现为「定时同步静默不跑」，通常要等第二天早上才发现。
	bad := []string{
		"",         // 空
		"0:0",      // 没补零，歧义
		"24:00",    // 小时越界
		"00:60",    // 分钟越界
		"-1:00",    // 负数
		"00",       // 缺分钟
		"00:00:00", // 多了秒
		"aa:bb",    // 非数字
		"零点",       // 中文
	}
	for _, in := range bad {
		if _, _, err := ParseDailyAt(in); err == nil {
			t.Errorf("ParseDailyAt(%q) 应当报错，却通过了", in)
		}
	}
}
