package syncer

import (
	"context"
	"log"
	"sync"
	"time"

	"asset-mgr/model"
)

// startupDelay 启动后先等一会再跑第一次，避开启动阶段建表/迁移和外部探测的争抢。
const startupDelay = time.Minute

// Scheduler 定时触发增量同步。
// 不用 time.Ticker：Ticker 必须等满一个间隔才第一次触发，进程重启比间隔频繁就永远不跑；
// 这里改成「启动延迟后先跑一次 → 之后按本地零点对齐的整点跑」，并把下次运行时间暴露出去。
type Scheduler struct {
	svc      *Service
	interval time.Duration

	mu      sync.Mutex
	nextRun time.Time
	lastRun time.Time
	lastErr string
	running bool
}

func NewScheduler(svc *Service, intervalMinutes int) *Scheduler {
	interval := time.Duration(intervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	return &Scheduler{svc: svc, interval: interval}
}

// SchedulerStatus 供 API 只读展示。
type SchedulerStatus struct {
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastError       string     `json:"last_error"`
	Running         bool       `json:"running"`
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := SchedulerStatus{
		Enabled:         true,
		IntervalMinutes: int(s.interval / time.Minute),
		LastError:       s.lastErr,
		Running:         s.running,
	}
	if !s.nextRun.IsZero() {
		t := s.nextRun
		st.NextRunAt = &t
	}
	if !s.lastRun.IsZero() {
		t := s.lastRun
		st.LastRunAt = &t
	}
	return st
}

// Run 调度循环，阻塞，调用方用 go 启动。ctx 取消后退出。
func (s *Scheduler) Run(ctx context.Context) {
	next := time.Now().Add(startupDelay)
	for {
		s.setNext(next)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.tick(ctx)
		next = nextAligned(time.Now(), s.interval)
	}
}

func (s *Scheduler) setNext(t time.Time) {
	s.mu.Lock()
	s.nextRun = t
	s.mu.Unlock()
}

func (s *Scheduler) tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		// 上一轮还没跑完（比如接口变慢）：跳过这一轮，不要排队堆积
		s.mu.Unlock()
		log.Printf("定时同步跳过：上一轮仍在运行")
		return
	}
	s.running = true
	s.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	log.Printf("定时同步开始")
	run, err := s.svc.Run(runCtx, model.SyncModeIncremental, "scheduler")

	s.mu.Lock()
	s.running = false
	s.lastRun = time.Now()
	if err != nil {
		s.lastErr = err.Error()
	} else {
		s.lastErr = ""
	}
	s.mu.Unlock()

	if err != nil {
		log.Printf("定时同步失败: %v", err)
		return
	}
	log.Printf("定时同步完成: id=%d status=%s %s", run.ID, run.Status, run.ErrorSummary)
}

// nextAligned 返回严格晚于 now 的、相对本地零点对齐的时间点。
// 对齐到零点而不是进程启动时刻，重启不会把调度时间整体推移。
func nextAligned(now time.Time, interval time.Duration) time.Time {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	next := dayStart.Add((now.Sub(dayStart)/interval + 1) * interval)
	for !next.After(now) {
		next = next.Add(interval)
	}
	return next
}
