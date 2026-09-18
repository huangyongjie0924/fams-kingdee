package config

import "asset-mgr/integration/kingdee"

// KingdeeClientConfig 把应用配置转成金蝶客户端配置。
//
// 集中在一处，避免每个命令各拼一份字面量——之前 main.go / cmd/dryrun /
// cmd/kdverify 三处各写了一遍，加 personnel_query_path 时就会漏改其中一两处，
// 而漏改的症状是「同步时报接口路径为空」，跟配置本身看不出关系。
func (c *Config) KingdeeClientConfig() kingdee.Config {
	return kingdee.Config{
		BaseURL:             c.Kingdee.BaseURL,
		ClientID:            c.Kingdee.ClientID,
		ClientSecret:        c.Kingdee.ClientSecret,
		Username:            c.Kingdee.Username,
		AccountID:           c.Kingdee.AccountID,
		RequestTimeout:      c.Kingdee.RequestTimeout,
		PageSize:            c.Kingdee.PageSize,
		MaxRetries:          c.Kingdee.MaxRetries,
		QueryPath:           c.Kingdee.QueryPath,
		PersonnelQueryPath:  c.Kingdee.PersonnelQueryPath,
		DepartmentQueryPath: c.Kingdee.DepartmentQueryPath,
	}
}
