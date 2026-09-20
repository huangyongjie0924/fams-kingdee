import { reactive } from "vue";

export interface AuthUser {
  id: number;
  username: string;
  real_name: string;
  role: string;
  employee_id: number;
  employee_name?: string;
}

const TOKEN_KEY = "asset_token";
const USER_KEY = "asset_user";

function readUser(): AuthUser | null {
  try {
    const raw = localStorage.getItem(USER_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

// 与后端 model.rolePermissions 保持一致：admin 恒有权限，未列出的角色按无权限处理。
//
// ⚠️ 这份表必须与后端逐项一致，否则会出现「按钮可见但点击 403」或「后端允许但界面没入口」。
// 已修复的一次真实漂移：asset_manager 曾多出 "sync.manage"，而后端三个同步接口都挂了
// requirePerm(PermSyncManage)（后端不给该角色）→ 按钮可见但点击 403。现以前端向后端对齐。
//
// repair.report 授给 viewer 是**有意为之**（后端同理）：报修是「服务到每一位员工」的核心动作，
// 打破「viewer 纯只读」的既有约定，但报修天然自收窄（只看得到自己提交的单）。
const rolePermissions: Record<string, string[]> = {
  asset_manager: [
    "asset.manage", "master.manage", "count.manage", "count.enter",
    "repair.report", "repair.dispatch", "repair.approve", "repair.manage",
  ],
  counter: ["count.enter", "repair.report"],
  // dept_head 对台账纯只读；维修侧可报修、可审批（P1）
  dept_head: ["repair.report", "repair.approve"],
  repair_tech: ["repair.handle"],
  viewer: ["repair.report"],
};

const roleLabels: Record<string, string> = {
  admin: "管理员",
  asset_manager: "资产管理员",
  counter: "盘点员",
  dept_head: "部门负责人",
  repair_tech: "维修工",
  viewer: "只读",
};

// 后端按角色收窄了台账的可见范围，页面得说清楚「共 N 条」是这个范围内的 N 条，
// 否则受限账号会以为数据丢了。留空表示不限制。
const scopeHints: Record<string, string> = {
  dept_head: "仅显示本部门及下级部门的资产",
  viewer: "仅显示你名下的资产",
};

export const auth = reactive({
  token: localStorage.getItem(TOKEN_KEY) || "",
  user: readUser() as AuthUser | null,

  get isAdmin() {
    return this.user?.role === "admin";
  },

  get roleLabel() {
    return roleLabels[this.user?.role ?? ""] ?? this.user?.role ?? "";
  },

  get scopeHint() {
    return scopeHints[this.user?.role ?? ""] ?? "";
  },

  can(perm: string): boolean {
    const role = this.user?.role;
    if (!role) return false;
    if (role === "admin") return true;
    return (rolePermissions[role] ?? []).includes(perm);
  },

  login(token: string, user: AuthUser) {
    this.token = token;
    this.user = user;
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(USER_KEY, JSON.stringify(user));
  },

  logout() {
    this.token = "";
    this.user = null;
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
  },
});
