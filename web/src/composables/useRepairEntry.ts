import { computed, type ComputedRef } from "vue";
import { auth } from "../stores/auth";

export interface RepairEntry {
  /** 菜单/入口标题，按权限优先级决定（方案 §3.2） */
  label: string;
  /** 去向，按权限优先级决定（方案 §3.2） */
  path: string;
}

/**
 * 「查看维修单」入口的标题与去向，按权限优先级决定：
 *
 *   1. `repair.dispatch || repair.manage` → 维修工单 / `/repairs`
 *   2. `repair.handle`                    → 待我处理 / `/repairs?mine=1`
 *   3. 兜底（`repair.report`）            → 我的报修单 / `/repairs/mine`
 *
 * 这段判断原先只写在 `Home.vue` 里。侧栏（SideMenu）与移动端标签栏（TabBar）
 * 需要同一份结果，抽到这里共用，避免三处各自漂移。
 *
 * ⚠️ 只影响「入口落在哪张列表」的呈现，不涉及任何权限位本身。
 */
export const repairEntry: ComputedRef<RepairEntry> = computed(() => {
  if (auth.can("repair.dispatch") || auth.can("repair.manage")) {
    return { label: "维修工单", path: "/repairs" };
  }
  if (auth.can("repair.handle")) {
    return { label: "待我处理", path: "/repairs?mine=1" };
  }
  return { label: "我的报修单", path: "/repairs/mine" };
});

/**
 * 是否能看到「维修」这一组入口。
 *
 * ⚠️ 这里**只是呈现层判断**，用的是既有的 `auth.can()`，不新增也不修改任何权限位。
 * 与 SideMenu 原先逐个 `v-if="auth.can('repair.*')"` 的判定等价。
 */
export const canSeeRepair: ComputedRef<boolean> = computed(
  () =>
    auth.can("repair.report") ||
    auth.can("repair.handle") ||
    auth.can("repair.dispatch") ||
    auth.can("repair.approve") ||
    auth.can("repair.manage"),
);
