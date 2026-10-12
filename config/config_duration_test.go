package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这组测试守的是一类「静默炸启动」的失败：
// Load 一旦返回 error，进程根本起不来，现场表现是「服务没了」而不是「配置错了」。
//
// 起因：deeplink_token_ttl 声明为 time.Duration，示例配置里写的是字符串 "24h"。
// time.Duration 的底层 kind 是 int64，直觉上「字符串塞不进整数」，很容易被误判为
// 必然报错，从而把示例配置改成整数。实测结论（见下）与直觉相反：
//   - 字符串时长 "24h" 是**合法**的（yaml.v3 在 reflect.Int* 分支内对 Duration
//     专门有 `case string: time.ParseDuration`，decode.go:647）；
//   - 整数 24 反而是**硬报错**（同一分支的 `case int` 用 `!isDuration` 刻意跳过，
//     落到 unmarshal 的类型错误）——即「写整数」会直接起不来，不是静默回落。
//
// 两件事都必须有测试钉住，否则后人凭直觉改示例配置就会踩雷。

// TestLoadExampleConfigSucceeds 直接加载仓库里真实发布的 config.example.yaml。
// 这是最强形式：运维就是照抄这个文件，它必须能被 Load 成功解析。
func TestLoadExampleConfigSucceeds(t *testing.T) {
	// 测试工作目录是 config/，示例文件在仓库根目录。
	path := filepath.Join("..", "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("找不到示例配置 %s: %v", path, err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("config.example.yaml 无法被 Load 解析（照抄它的运维会直接起不来）: %v", err)
	}
	if got, want := c.Yunzhijia.DeeplinkTokenTTL, 24*time.Hour; got != want {
		t.Errorf("deeplink_token_ttl 解析结果 = %v, 期望 %v", got, want)
	}
}

// TestDeeplinkTokenTTLParsesDurationString 证明「字符串时长」被正确解析。
func TestDeeplinkTokenTTLParsesDurationString(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{`deeplink_token_ttl: "24h"`, 24 * time.Hour},
		{`deeplink_token_ttl: "90m"`, 90 * time.Minute},
		{`deeplink_token_ttl: "36h"`, 36 * time.Hour},
		{`deeplink_token_ttl: "1h30m"`, 90 * time.Minute},
		// 不带引号也是字符串标量（YAML 里 24h 不是合法整数），同样应被解析。
		{`deeplink_token_ttl: 36h`, 36 * time.Hour},
	} {
		c, err := Load(writeTempConfig(t, tc.raw))
		if err != nil {
			t.Errorf("%s ⇒ Load 报错: %v", tc.raw, err)
			continue
		}
		if c.Yunzhijia.DeeplinkTokenTTL != tc.want {
			t.Errorf("%s ⇒ 解析为 %v, 期望 %v", tc.raw, c.Yunzhijia.DeeplinkTokenTTL, tc.want)
		}
	}
}

// TestDeeplinkTokenTTLIntegerIsHardError 锁定「写整数 ⇒ 启动失败」。
//
// 这是**有意保留**的 fail-fast：运维写 `1` 的本意几乎必然是「1 小时」，
// 而 1 纳秒的 token 会让免登全线失效且极难定位。宁可在启动时用一句
// 明确的类型错误拦下来，也不要静默跑起来。
// 若将来有人想把它改成「整数当小时」，本测试会红——那时必须同步改文档与
// config.example.yaml 的注释，不能只改代码。
func TestDeeplinkTokenTTLIntegerIsHardError(t *testing.T) {
	for _, raw := range []string{
		`deeplink_token_ttl: 24`,
		`deeplink_token_ttl: 1`,
		`deeplink_token_ttl: 0`,
	} {
		_, err := Load(writeTempConfig(t, raw))
		if err == nil {
			t.Errorf("%s ⇒ 期望硬报错（fail-fast），实际 Load 成功", raw)
			continue
		}
		if !strings.Contains(err.Error(), "time.Duration") {
			t.Errorf("%s ⇒ 报错信息应点明类型不符，实际: %v", raw, err)
		}
	}
}

// TestDeeplinkTokenTTLBadStringIsHardError 锁定「非时长字符串 ⇒ 启动失败」。
// 拼错（"24 h" 带空格）在配置里极常见，必须在启动时暴露。
func TestDeeplinkTokenTTLBadStringIsHardError(t *testing.T) {
	for _, raw := range []string{
		`deeplink_token_ttl: "abc"`,
		`deeplink_token_ttl: "24 h"`,
		`deeplink_token_ttl: "24"`, // 带引号的数字也不是时长（缺单位）
		`deeplink_token_ttl: ""`,   // 空串 ≠ 未配置，yaml 仍会尝试解析
	} {
		if _, err := Load(writeTempConfig(t, raw)); err == nil {
			t.Errorf("%s ⇒ 期望硬报错，实际 Load 成功", raw)
		}
	}
}

// TestDeeplinkTokenTTLNonPositiveFallsBack 锁定「显式非正时长 ⇒ 静默回落 24h」。
//
// 语义来源：Load 末尾的 `if TTL <= 0 { TTL = 24h }`。该兜底同时承担「未配置」
// 与「配了负数」两种情况——因为字段是值类型而非指针，无法区分二者。
// 这是**已知且有测试记录**的行为，不是意外。
func TestDeeplinkTokenTTLNonPositiveFallsBack(t *testing.T) {
	for _, raw := range []string{
		`deeplink_token_ttl: "-5h"`,
		`deeplink_token_ttl: "0s"`,
		// 键完全缺失 ⇒ 零值 ⇒ 同样回落。
		`todo_enabled: false`,
	} {
		c, err := Load(writeTempConfig(t, raw))
		if err != nil {
			t.Errorf("%s ⇒ 期望静默回落，但 Load 报错: %v", raw, err)
			continue
		}
		if got, want := c.Yunzhijia.DeeplinkTokenTTL, 24*time.Hour; got != want {
			t.Errorf("%s ⇒ 回落为 %v, 期望 %v", raw, got, want)
		}
	}
}

// TestDeeplinkTokenTTLEnvOverrides 证明两个环境变量入口都能生效，且
// _HOURS 优先于 _TTL（更具体的写法胜出）。
func TestDeeplinkTokenTTLEnvOverrides(t *testing.T) {
	path := writeTempConfig(t, `deeplink_token_ttl: "24h"`)

	t.Setenv("YZJ_DEEPLINK_TOKEN_TTL", "90m")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load 报错: %v", err)
	}
	if got, want := c.Yunzhijia.DeeplinkTokenTTL, 90*time.Minute; got != want {
		t.Errorf("YZJ_DEEPLINK_TOKEN_TTL=90m ⇒ %v, 期望 %v", got, want)
	}

	t.Setenv("YZJ_DEEPLINK_TOKEN_TTL_HOURS", "36")
	c, err = Load(path)
	if err != nil {
		t.Fatalf("Load 报错: %v", err)
	}
	if got, want := c.Yunzhijia.DeeplinkTokenTTL, 36*time.Hour; got != want {
		t.Errorf("两个入口都设时 _HOURS 应优先 ⇒ %v, 期望 %v", got, want)
	}
}

// writeTempConfig 写一个能被 Load 通过的最小配置（含 auth.jwt_secret，
// 它是 Load 的硬性必填项），返回其路径。
func writeTempConfig(t *testing.T, yzjLine string) string {
	t.Helper()
	content := "auth:\n  jwt_secret: \"test-secret-for-config-tests\"\n" +
		"yunzhijia:\n  " + yzjLine + "\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}
