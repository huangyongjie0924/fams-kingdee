package yunzhijia

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// getallpersons 限流（500011）对策：节流 + 有界重试
//
// 复现与对策见 docs/云之家待办通知集成方案.md §6.5「实测阻断项 ①」：
// 该接口有频率限制，连续调用时第 2 次起返回
// {"success":false,"error":"opendata_req_frequen","errorCode":500011,"data":{}}。
// ─────────────────────────────────────────────────────────────────────────────

// writeRateLimited 输出限流响应体（与真网关实测一致）。
func writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":false,"error":"opendata_req_frequen","errorCode":500011,"data":{}}`))
}

// writePersonsOK 输出一页正常数据。
func writePersonsOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true,"error":"","errorCode":100,"data":{"persons":[{"openId":"5de50017e4b0b2767b870423","jobNo":"E001","name":"印"}],"time":"2019-12-19 13:50:57"}}`))
}

// 限流后重试成功：第 1、2 次 500011，第 3 次正常 ⇒ 拿到数据，且 getallpersons 恰好请求 3 次。
func TestFetchAllPersonsRetriesOnRateLimit(t *testing.T) {
	var personsHits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		if n := atomic.AddInt32(&personsHits, 1); n <= 2 {
			writeRateLimited(w)
			return
		}
		writePersonsOK(w)
	})
	c.SetPersonsMinInterval(2 * time.Millisecond) // 测试用小间隔，避免等 2.5s

	res, err := c.FetchAllPersons(context.Background(), "17951708", "2008-08-02 01:40:38", 0, 1000)
	if err != nil {
		t.Fatalf("限流后重试应成功，实际 error=%v", err)
	}
	if len(res.Persons) != 1 || res.Persons[0].JobNo != "E001" {
		t.Fatalf("应拿到第 3 次返回的数据，实际 %+v", res.Persons)
	}
	if got := atomic.LoadInt32(&personsHits); got != 3 {
		t.Fatalf("应恰好请求 3 次 getallpersons（2 次限流 + 1 次成功），实际 %d", got)
	}
}

// 重试耗尽可见失败：一直 500011 ⇒ 返回 error、错误串含 500011 与「限流」，且尝试次数有界。
func TestFetchAllPersonsRateLimitExhausted(t *testing.T) {
	var personsHits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		atomic.AddInt32(&personsHits, 1)
		writeRateLimited(w)
	})
	c.SetPersonsMinInterval(2 * time.Millisecond)

	_, err := c.FetchAllPersons(context.Background(), "17951708", "", 0, 1000)
	if err == nil {
		t.Fatalf("一直限流时应返回错误，实际为 nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500011") || !strings.Contains(msg, "限流") {
		t.Fatalf("错误串应同时含 500011 与「限流」，实际 %q", msg)
	}
	if got := atomic.LoadInt32(&personsHits); got != personsMaxAttempts {
		t.Fatalf("应尝试 %d 次后有界放弃，实际 %d", personsMaxAttempts, got)
	}
}

// 非限流错误不重试：500000 ⇒ 只请求 1 次即返回 error（证明没对不可重试错误做无谓重试）。
func TestFetchAllPersonsNonRateLimitErrorNotRetried(t *testing.T) {
	var personsHits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		atomic.AddInt32(&personsHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error":"param error","errorCode":500000,"data":{}}`))
	})
	c.SetPersonsMinInterval(2 * time.Millisecond)

	_, err := c.FetchAllPersons(context.Background(), "17951708", "", 0, 1000)
	if err == nil {
		t.Fatalf("非限流错误应返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "500000") {
		t.Fatalf("错误串应含 500000，实际 %q", err.Error())
	}
	if got := atomic.LoadInt32(&personsHits); got != 1 {
		t.Fatalf("非限流错误不得重试，应只请求 1 次，实际 %d", got)
	}
}

// 节流确实生效：间隔设 100ms，连续 2 次调用的总耗时 ≥ 100ms（断言机制，不断言 2.5s 常量本身）。
func TestFetchAllPersonsThrottleEnforced(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		writePersonsOK(w)
	})
	const interval = 100 * time.Millisecond
	c.SetPersonsMinInterval(interval)

	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := c.FetchAllPersons(context.Background(), "17951708", "", 0, 1000); err != nil {
			t.Fatalf("第 %d 次调用失败：%v", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed < interval {
		t.Fatalf("两次调用应至少间隔 %v，实际总耗时 %v", interval, elapsed)
	}
}

// ctx 取消能被尊重：间隔设 10s，第 2 次调用带 50ms 超时 ⇒ 立即返回 ctx.Err()，不得干等。
func TestFetchAllPersonsThrottleRespectsContext(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		writePersonsOK(w)
	})
	c.SetPersonsMinInterval(10 * time.Second)

	// 第一次：首次调用无需等待，正常返回，建立节流起点。
	if _, err := c.FetchAllPersons(context.Background(), "17951708", "", 0, 1000); err != nil {
		t.Fatalf("首次调用应成功：%v", err)
	}

	// 第二次：距上次调用不足 10s，但 ctx 50ms 就到期 → 必须返回 ctx.Err()。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.FetchAllPersons(ctx, "17951708", "", 0, 1000)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应返回 ctx.Err()（DeadlineExceeded），实际 %v", err)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("不得等满节流间隔，实际耗时 %v", elapsed)
	}
}
