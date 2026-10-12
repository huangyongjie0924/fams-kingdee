package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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

// writeCfg 写一个最小可加载的 config.yaml（只有 jwt_secret 是硬性必填）。
func writeCfg(t *testing.T, yzjYAML string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "auth:\n  jwt_secret: \"test-secret\"\n" + yzjYAML
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

// TestLoadDisablesTodoWhenRequiredFieldMissing 启动防御（§8）：
// todo_enabled=true 但 portal_base_url / head_img / app_id（或 base_url）任一为空时，
// 必须把待办功能当作**关闭**（记错误日志 + TodoEnabled=false），绝不发出缺字段的待办。
func TestLoadDisablesTodoWhenRequiredFieldMissing(t *testing.T) {
	cases := []struct {
		name                                string
		baseURL, appID, headImg, portalBase string
	}{
		{"portal_base_url 为空", "https://yzj.example.com", "500000084", "https://img.example.com/x.png", ""},
		{"head_img 为空", "https://yzj.example.com", "500000084", "", "https://fams.example.com"},
		{"app_id 为空", "https://yzj.example.com", "", "https://img.example.com/x.png", "https://fams.example.com"},
		{"base_url 为空", "", "500000084", "https://img.example.com/x.png", "https://fams.example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeCfg(t, fmt.Sprintf(`
yunzhijia:
  base_url: %q
  app_id: %q
  secret: "s"
  todo_enabled: true
  head_img: %q
  portal_base_url: %q
`, c.baseURL, c.appID, c.headImg, c.portalBase))
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("加载配置失败: %v", err)
			}
			if cfg.Yunzhijia.TodoEnabled {
				t.Errorf("必填项缺失时待办应被自动关闭，实际仍启用")
			}
		})
	}
}

// TestLoadKeepsTodoEnabledWhenComplete 配置齐备时保持启用。
func TestLoadKeepsTodoEnabledWhenComplete(t *testing.T) {
	path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
  todo_enabled: true
  head_img: "https://img.example.com/x.png"
  portal_base_url: "https://fams.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if !cfg.Yunzhijia.TodoEnabled {
		t.Errorf("配置齐备时应保持启用，实际被关闭")
	}
}

// TestTodoAppIDFallsBackToAppID 待办 appId 留空时回退免登 appId（已实测同一「第三方应用ID」）。
func TestTodoAppIDFallsBackToAppID(t *testing.T) {
	path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Yunzhijia.TodoAppID != "500000084" {
		t.Errorf("todo_app_id 应回退 app_id=500000084，实际 %q", cfg.Yunzhijia.TodoAppID)
	}
	// 显式设置时不被回退覆盖。
	path2 := writeCfg(t, `
yunzhijia:
  app_id: "500000084"
  todo_app_id: "999999999"
`)
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg2.Yunzhijia.TodoAppID != "999999999" {
		t.Errorf("显式 todo_app_id 不应被覆盖，实际 %q", cfg2.Yunzhijia.TodoAppID)
	}
}

// TestMissingTodoConfig 直接验证缺项清单（供日志与调用方判断）。
func TestMissingTodoConfig(t *testing.T) {
	var c Config
	c.Yunzhijia.BaseURL = "https://yzj.example.com"
	c.Yunzhijia.TodoAppID = "500000084"
	// head_img / portal_base_url 留空
	got := c.MissingTodoConfig()
	want := map[string]bool{"head_img": true, "portal_base_url": true}
	if len(got) != 2 {
		t.Fatalf("缺项清单应含 2 项，实际 %v", got)
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("缺项清单出现意外项 %q", m)
		}
	}
	// 齐备时为空
	c.Yunzhijia.HeadImg = "https://img/x.png"
	c.Yunzhijia.PortalBaseURL = "https://fams"
	if len(c.MissingTodoConfig()) != 0 {
		t.Errorf("配置齐备时缺项清单应为空，实际 %v", c.MissingTodoConfig())
	}
}

// TestOverrideBoolIfSet 环境变量布尔覆盖（YZJ_TODO_ENABLED）。
func TestOverrideBoolIfSet(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run("enable_"+v, func(t *testing.T) {
			t.Setenv("YZJ_TODO_ENABLED", v)
			var b bool
			overrideBoolIfSet("YZJ_TODO_ENABLED", &b)
			if !b {
				t.Errorf("%q 应解析为 true", v)
			}
		})
	}
	for _, v := range []string{"0", "false", "no", "off"} {
		t.Run("disable_"+v, func(t *testing.T) {
			t.Setenv("YZJ_TODO_ENABLED", v)
			b := true
			overrideBoolIfSet("YZJ_TODO_ENABLED", &b)
			if b {
				t.Errorf("%q 应解析为 false", v)
			}
		})
	}
	// 非法值忽略（保持原值），不静默改写。
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("YZJ_TODO_ENABLED", "maybe")
		b := true
		overrideBoolIfSet("YZJ_TODO_ENABLED", &b)
		if !b {
			t.Errorf("非法值不应改写原值")
		}
	})
}

// flowCenterCfg 生成一份「流程中心配置齐备」的 yaml，供测试按需清空单项。
// 字段名与 config.go 的 yaml tag 一一对应；改 tag 会让本测试立刻失败（有意为之）。
func flowCenterCfg(baseURL, eid, appID, typeID, typeName, appName string) string {
	return fmt.Sprintf(`
yunzhijia:
  base_url: %q
  app_id: %q
  secret: "s"
  eid: %q
  flowcenter_enabled: true
  flowcenter_type_id: %q
  flowcenter_type_name: %q
  flowcenter_app_name: %q
`, baseURL, appID, eid, typeID, typeName, appName)
}

// TestLoadDisablesFlowCenterWhenRequiredFieldMissing 启动防御（§8）：
// flowcenter_enabled=true 但任一必填项为空时，必须把流程中心当作**关闭**，
// 让新待办回退 im-todo，而不是放行后在运行时逐条静默失败。
//
// eid 单独列为必填项的原因：team 级 accessToken 取 token 时 eid 是必填参数，
// 缺失时认证接口直接返回 11000400 参数错误 —— 实测确认过，不是猜测。
func TestLoadDisablesFlowCenterWhenRequiredFieldMissing(t *testing.T) {
	const (
		okBase   = "https://yzj.example.com"
		okEID    = "26527676"
		okAppID  = "500000084"
		okTypeID = "WXSQD"
		okName   = "维修工单"
		okApp    = "固定资产管理"
	)
	cases := []struct {
		name                                           string
		baseURL, eid, appID, typeID, typeName, appName string
	}{
		{"base_url 为空", "", okEID, okAppID, okTypeID, okName, okApp},
		{"eid 为空", okBase, "", okAppID, okTypeID, okName, okApp},
		{"app_id 为空", okBase, okEID, "", okTypeID, okName, okApp},
		{"type_id 为空", okBase, okEID, okAppID, "", okName, okApp},
		{"type_name 为空", okBase, okEID, okAppID, okTypeID, "", okApp},
		{"app_name 为空", okBase, okEID, okAppID, okTypeID, okName, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeCfg(t, flowCenterCfg(c.baseURL, c.eid, c.appID, c.typeID, c.typeName, c.appName))
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("加载配置失败: %v", err)
			}
			if cfg.Yunzhijia.FlowCenterEnabled {
				t.Errorf("必填项 %s 缺失时流程中心应被自动关闭，实际仍启用", c.name)
			}
		})
	}
}

// TestLoadKeepsFlowCenterEnabledWhenComplete 配置齐备时保持启用。
func TestLoadKeepsFlowCenterEnabledWhenComplete(t *testing.T) {
	path := writeCfg(t, flowCenterCfg(
		"https://yzj.example.com", "26527676", "500000084", "WXSQD", "维修工单", "固定资产管理"))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if !cfg.Yunzhijia.FlowCenterEnabled {
		t.Errorf("配置齐备时应保持启用，实际被关闭")
	}
	// 三个模板字段必须原样读入（yaml tag 写错会在这里暴露）。
	if cfg.Yunzhijia.FlowCenterTypeID != "WXSQD" ||
		cfg.Yunzhijia.FlowCenterTypeName != "维修工单" ||
		cfg.Yunzhijia.FlowCenterAppName != "固定资产管理" {
		t.Errorf("模板字段读入错误: typeID=%q typeName=%q appName=%q",
			cfg.Yunzhijia.FlowCenterTypeID, cfg.Yunzhijia.FlowCenterTypeName, cfg.Yunzhijia.FlowCenterAppName)
	}
}

// TestFlowCenterOffByDefault 默认关闭：不配 flowcenter_enabled 时绝不能自己打开。
// 这保证「灰度切换」的默认落点是安全的（存量部署升级后行为不变）。
func TestFlowCenterOffByDefault(t *testing.T) {
	path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
  eid: "26527676"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Yunzhijia.FlowCenterEnabled {
		t.Errorf("未显式开启时流程中心应保持关闭")
	}
}

// TestMissingFlowCenterConfig 直接验证缺项清单。
// 注意 portal_base_url **不在**清单里：流程中心不经过 generatetodo，
// 不需要它的 url / headImg 必填字段，跳转地址由编排层各自生成。
func TestMissingFlowCenterConfig(t *testing.T) {
	var c Config
	c.Yunzhijia.BaseURL = "https://yzj.example.com"
	c.Yunzhijia.EID = "26527676"
	c.Yunzhijia.TodoAppID = "500000084"
	// 三个模板字段留空
	got := c.MissingFlowCenterConfig()
	want := map[string]bool{
		"flowcenter_type_id":   true,
		"flowcenter_type_name": true,
		"flowcenter_app_name":  true,
	}
	if len(got) != len(want) {
		t.Fatalf("缺项清单应含 %d 项，实际 %v", len(want), got)
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("缺项清单出现意外项 %q", m)
		}
	}
	// portal_base_url 为空不应算缺项。
	if c.Yunzhijia.PortalBaseURL != "" {
		t.Fatal("前置条件错误：portal_base_url 应为空")
	}
	for _, m := range got {
		if m == "portal_base_url" {
			t.Errorf("portal_base_url 不应出现在流程中心缺项清单中")
		}
	}
	// 齐备时为空
	c.Yunzhijia.FlowCenterTypeID = "WXSQD"
	c.Yunzhijia.FlowCenterTypeName = "维修工单"
	c.Yunzhijia.FlowCenterAppName = "固定资产管理"
	if len(c.MissingFlowCenterConfig()) != 0 {
		t.Errorf("配置齐备时缺项清单应为空，实际 %v", c.MissingFlowCenterConfig())
	}
}

// TestInformationalEnabledConfig 「信息性告知」开关从编译期常量改为配置项
// （yunzhijia.informational_enabled）：
//
//	① 文件里 true 能正确读入（yaml tag 写错会在这里暴露）；
//	② 环境变量 YZJ_INFORMATIONAL_ENABLED=true 能覆盖文件里的 false；
//	③ 不写该键时默认 false（灰度默认落点安全，存量部署升级后行为不变）。
//
// 三处都显式清掉 YZJ_INFORMATIONAL_ENABLED（t.Setenv 置空 = 不覆盖），使用例不受
// 开发者 shell 里是否导出了该变量影响——否则「缺省为 false」在导出该变量的机器上会假红。
func TestInformationalEnabledConfig(t *testing.T) {
	t.Run("文件 true 读入", func(t *testing.T) {
		t.Setenv("YZJ_INFORMATIONAL_ENABLED", "")
		path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
  informational_enabled: true
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if !cfg.Yunzhijia.InformationalEnabled {
			t.Errorf("informational_enabled: true 应读入为 true（yaml tag 写错？）")
		}
	})

	t.Run("环境变量覆盖文件 false", func(t *testing.T) {
		path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
  informational_enabled: false
`)
		t.Setenv("YZJ_INFORMATIONAL_ENABLED", "true")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if !cfg.Yunzhijia.InformationalEnabled {
			t.Errorf("YZJ_INFORMATIONAL_ENABLED=true 应覆盖文件里的 false，实际仍为 false")
		}
	})

	t.Run("缺省为 false", func(t *testing.T) {
		t.Setenv("YZJ_INFORMATIONAL_ENABLED", "")
		path := writeCfg(t, `
yunzhijia:
  base_url: "https://yzj.example.com"
  app_id: "500000084"
  secret: "s"
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if cfg.Yunzhijia.InformationalEnabled {
			t.Errorf("未配置 informational_enabled 时应默认为 false（灰度默认落点安全）")
		}
	})
}
