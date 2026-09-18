package syncer

import (
	"testing"
	"time"
)

// 调度时刻的计算是整条定时链路里唯一"纯逻辑"的部分，也是最容易在
// 边界上出错的部分（正好卡在定时点、跨天、跨月）。落库和接口调用都有别的测试兜着，
// 这两个函数没有，所以单独测。

func day(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.Local)
}

// cst 是固定 +08:00 偏移，用于间隔断言。
// 用 time.Local 的话，在实行夏令时的机器上「加一天」可能是 23 或 25 小时，
// 测试会莫名其妙地飘。部署环境是 +08:00（无夏令时），这里也按它来。
var cst = time.FixedZone("CST", 8*3600)

func TestNextDaily(t *testing.T) {
	cases := []struct {
		name      string
		now       time.Time
		hour, min int
		want      time.Time
	}{
		{
			name: "零点之前 → 今天的零点",
			now:  day(2026, 9, 17, 23, 59), hour: 0, min: 0,
			want: day(2026, 9, 18, 0, 0),
		},
		{
			name: "零点之后 → 明天的零点",
			now:  day(2026, 9, 18, 9, 30), hour: 0, min: 0,
			want: day(2026, 9, 19, 0, 0),
		},
		{
			// 严格晚于：正好卡在定时点时必须推次日，否则 Run 的循环会在同一时刻
			// 反复触发（timer 剩余时间为 0，立刻又到点）
			name: "正好卡在定时点 → 明天的同一时刻",
			now:  day(2026, 9, 18, 0, 0), hour: 0, min: 0,
			want: day(2026, 9, 19, 0, 0),
		},
		{
			name: "非零点的时刻",
			now:  day(2026, 9, 18, 2, 0), hour: 1, min: 30,
			want: day(2026, 9, 19, 1, 30),
		},
		{
			name: "跨月",
			now:  day(2026, 9, 30, 12, 0), hour: 0, min: 0,
			want: day(2026, 10, 1, 0, 0),
		},
		{
			name: "跨年",
			now:  day(2026, 12, 31, 12, 0), hour: 0, min: 0,
			want: day(2027, 1, 1, 0, 0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextDaily(c.now, c.hour, c.min)
			if !got.Equal(c.want) {
				t.Errorf("nextDaily(%s, %02d:%02d) = %s，期望 %s",
					c.now.Format("2006-01-02 15:04"), c.hour, c.min,
					got.Format("2006-01-02 15:04"), c.want.Format("2006-01-02 15:04"))
			}
			if !got.After(c.now) {
				t.Errorf("nextDaily 必须严格晚于 now，得到 %s", got.Format("2006-01-02 15:04"))
			}
		})
	}
}

func TestLastScheduledBefore(t *testing.T) {
	cases := []struct {
		name      string
		now       time.Time
		hour, min int
		want      time.Time
	}{
		{
			name: "零点之后 → 今天的零点",
			now:  day(2026, 9, 18, 9, 30), hour: 0, min: 0,
			want: day(2026, 9, 18, 0, 0),
		},
		{
			name: "零点之前 → 昨天的零点",
			now:  day(2026, 9, 17, 23, 59), hour: 0, min: 0,
			want: day(2026, 9, 17, 0, 0),
		},
		{
			// 「不晚于」而不是「早于」：正好在定时点上，这一批就属于"应该已经跑过"，
			// 补跑判定要拿它当基准去查有没有成功记录
			name: "正好卡在定时点 → 就是此刻",
			now:  day(2026, 9, 18, 0, 0), hour: 0, min: 0,
			want: day(2026, 9, 18, 0, 0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lastScheduledBefore(c.now, c.hour, c.min)
			if !got.Equal(c.want) {
				t.Errorf("lastScheduledBefore(%s, %02d:%02d) = %s，期望 %s",
					c.now.Format("2006-01-02 15:04"), c.hour, c.min,
					got.Format("2006-01-02 15:04"), c.want.Format("2006-01-02 15:04"))
			}
			if got.After(c.now) {
				t.Errorf("lastScheduledBefore 不能晚于 now，得到 %s", got.Format("2006-01-02 15:04"))
			}
		})
	}
}

// TestScheduledWindowIsOneDay 钉住两个函数之间的关系：
// 「上一个定时点」到「下一个定时点」必须正好是一天。
//
// 补跑判定用前者当基准、循环等待用后者当目标，两者算的不是同一个"今天"
// 就会出现「刚补跑完又立刻跑一次」或「补跑判定永远查不到记录」。
func TestScheduledWindowIsOneDay(t *testing.T) {
	// 覆盖定时点前后各一分钟、正好定时点、以及一天里的若干时刻
	base := time.Date(2026, 9, 18, 0, 0, 0, 0, cst)
	offsets := []time.Duration{
		-2 * time.Minute, -time.Minute, 0, time.Minute, 2 * time.Minute,
		9 * time.Hour, 23*time.Hour + 59*time.Minute,
	}
	for _, off := range offsets {
		now := base.Add(off)
		last := lastScheduledBefore(now, 0, 0)
		next := nextDaily(now, 0, 0)
		if d := next.Sub(last); d != 24*time.Hour {
			t.Errorf("now=%s 时 last=%s next=%s，间隔 %v，期望 24h",
				now.Format("2006-01-02 15:04"),
				last.Format("2006-01-02 15:04"),
				next.Format("2006-01-02 15:04"), d)
		}
		if !last.Before(next) {
			t.Errorf("now=%s 时 last 不早于 next", now.Format("2006-01-02 15:04"))
		}
	}
}

// TestTriggerSchedulerIsPersistedValue 钉住触发来源标识的字面值。
//
// "scheduler" 不只是内存里的一个标记：它已经写进了历史 sync_run.triggered_by 行，
// 前端「触发人」一列直接展示它，运维排查时也按它筛。改成别的值不会编译失败，
// 但会让新老记录的来源看起来是两种东西。
func TestTriggerSchedulerIsPersistedValue(t *testing.T) {
	if TriggerScheduler != "scheduler" {
		t.Errorf("TriggerScheduler 是落库值，改动前需同步迁移历史数据与前端展示，实际 %q", TriggerScheduler)
	}
}
