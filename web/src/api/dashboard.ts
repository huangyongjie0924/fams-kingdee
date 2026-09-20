import http from "./client";

// —— GET /api/dashboard 的响应结构（契约见 docs/增量架构-可维修标签与首页.md §2.2）——
//
// 「待我处理」的一项。哪些项出现、count 多少**全部由服务端按角色算好**，
// 前端只渲染与跳转，不做任何「角色 → 待办项」的业务判断（否则会形成第二套规则）。
export interface DashboardTodo {
  key: string;
  label: string;
  count: number;
  link: string;
  // 紧急度，服务端下发：'danger' | 'warning' | 'info'。
  // 缺省按 'info' 处理：后端该字段上线前 / 老数据都不会带 level，前端必须兜得住。
  level?: string;
}

// 首页指标卡。与 todo 同形，语义区别是「与你相关的量」而非「要你去做的事」：
// 管理员拿到「资产总数/待受理/待派工/待确认」，员工拿到「我的资产/我的报修/我的维修/我的待盘点」。
// 给哪几项同样由服务端按角色算好，前端只渲染，不在前端按角色再挑一遍。
export interface DashboardStatCard {
  key: string;
  label: string;
  count: number;
  link: string;
}

// 「状态 + 数量」通用分组计数：
// - 资产状态分布（asset_status）里 status 直接是中文有效状态，label 缺省；
// - 维修单状态分布（repair_status）里 status 是英文状态码、label 是中文白话。
export interface DashboardStatusCount {
  status: string;
  label?: string;
  count: number;
}

// 资产按分类分布的「分类名 + 数量」。
export interface DashboardCategoryCount {
  name: string;
  count: number;
}

export interface DashboardOverview {
  asset_total: number;
  asset_status: DashboardStatusCount[];
  asset_by_category: DashboardCategoryCount[];
  repair_status: DashboardStatusCount[];
}

export interface Dashboard {
  todo: DashboardTodo[];
  stats: DashboardStatCard[];
  overview: DashboardOverview;
}

// 首页是只读聚合接口，不需要额外权限声明；可见范围由服务端 assetScope / repairScope 收窄。
// 请求失败时交由 client.ts 的响应拦截器统一弹错，调用方只需处理自身状态。
export async function getDashboard(): Promise<Dashboard> {
  const { data } = await http.get<Dashboard>("/dashboard");
  return data;
}
