import { reactive } from "vue";

export interface AuthUser {
  id: number;
  username: string;
  real_name: string;
  /** 主角色（= roles[0]）。旧 token / 旧数据只有它，作为回落依据保留。 */
  role: string;
  /** 账号的全部角色（一人可多角色）。可能缺失——旧 localStorage / 旧接口不带它。 */
  roles?: string[];
  employee_id: number;
  employee_name?: string;
}

const TOKEN_KEY = "asset_token";
const USER_KEY = "asset_user";
// 只读会话标记：经云之家待办深链免登进来的会话被后端收窄（rsc>0），只能读、不能写。
// 用 "1"/"0" 持久化，刷新后仍能隐藏写入口，避免又变成「点得下去但 403」。
const READONLY_KEY = "asset_readonly";

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
// repair.report 授给 employee 是**有意为之**（后端同理）：报修是「服务到每一位员工」的核心动作，
// 打破「employee 纯只读」的既有约定，但报修天然自收窄（只看得到自己提交的单）。
const rolePermissions: Record<string, string[]> = {
  asset_manager: [
    "asset.manage", "master.manage", "count.manage", "count.enter",
    "repair.report", "repair.dispatch", "repair.approve", "repair.manage",
  ],
  counter: ["count.enter", "repair.report"],
  // dept_head 对台账纯只读；维修侧可报修、可审批（P1）
  dept_head: ["repair.report", "repair.approve"],
  maintainer: ["repair.handle"],
  employee: ["repair.report"],
  // IT 管理员：只有 it.manage（2026-10-10 需求）。**刻意不给 asset.manage** ——
  // 他需要「看」全量资产台账来找设备做关联（那是可见范围，不是权限位），但不该能改资产卡。
  // ⚠️ 这一行必须与后端 model.rolePermissions 的 RoleITAdmin 条目逐项一致。
  it_admin: ["it.manage"],
};

const roleLabels: Record<string, string> = {
  admin: "管理员",
  asset_manager: "资产管理员",
  counter: "盘点员",
  dept_head: "部门负责人",
  maintainer: "维护人员",
  employee: "员工",
  it_admin: "IT 管理员",
};

// 后端按角色收窄了台账的可见范围，页面得说清楚「共 N 条」是这个范围内的 N 条，
// 否则受限账号会以为数据丢了。留空表示不限制。
const scopeHints: Record<string, string> = {
  dept_head: "仅显示本部门及下级部门的资产",
  employee: "仅显示你名下的资产及报修中的资产",
};

// effectiveRoles 返回账号实际生效的角色集合，与后端 model.User.EffectiveRoles() 同一回落规则：
// roles 非空则用它，否则回落到 [role]。auth.user 是从 localStorage 反序列化来的，
// 旧数据可能没有 roles 字段，这里的回落必须健壮（user 可能为 null / role 可能为空）。
export function effectiveRoles(user: AuthUser | null | undefined): string[] {
  if (!user) return [];
  if (user.roles && user.roles.length) return user.roles;
  return user.role ? [user.role] : [];
}

export const auth = reactive({
  token: localStorage.getItem(TOKEN_KEY) || "",
  user: readUser() as AuthUser | null,
  readonly: localStorage.getItem(READONLY_KEY) === "1",

  get isAdmin() {
    return effectiveRoles(this.user).includes("admin");
  },

  get roleLabel() {
    return effectiveRoles(this.user)
      .map((r) => roleLabels[r] ?? r)
      .join("、");
  },

  get scopeHint() {
    // 多角色时把各角色各自的可见范围提示用「、」连接；无提示的角色不贡献文案。
    return effectiveRoles(this.user)
      .map((r) => scopeHints[r] ?? "")
      .filter((s) => s)
      .join("、");
  },

  can(perm: string): boolean {
    // 权限 = 并集：任一角色持有该权限即放行；admin 恒真（与后端 CanAny 一致）。
    return effectiveRoles(this.user).some(
      (r) => r === "admin" || (rolePermissions[r] ?? []).includes(perm),
    );
  },

  login(token: string, user: AuthUser, readonly = false) {
    this.token = token;
    this.user = user;
    this.readonly = readonly;
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(USER_KEY, JSON.stringify(user));
    localStorage.setItem(READONLY_KEY, readonly ? "1" : "0");
  },

  logout() {
    this.token = "";
    this.user = null;
    this.readonly = false;
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(READONLY_KEY);
  },
});
