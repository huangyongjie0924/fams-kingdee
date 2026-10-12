<template>
  <nav class="tabbar" aria-label="主导航">
    <router-link
      v-for="t in tabs"
      :key="t.key"
      :to="t.path"
      class="tab"
      :class="{ 'is-active': isActive(t.key) }"
    >
      <el-icon class="tab-icon"><component :is="t.icon" /></el-icon>
      <span class="tab-label">{{ t.label }}</span>
    </router-link>
  </nav>
</template>

<script setup lang="ts">
import { computed, type Component } from "vue";
import { useRoute } from "vue-router";
import { Camera, HomeFilled, Tickets, Tools } from "@element-plus/icons-vue";
import { canSeeRepair, repairEntry } from "../composables/useRepairEntry";

interface Tab {
  key: string;
  label: string;
  path: string;
  icon: Component;
}

const route = useRoute();

/**
 * 底部标签栏只放 4 项高频：工作台 / 资产 / 扫码 / 维修。
 *
 * 扫码从「抽屉第 3 项」提到标签栏第 3 格 —— 从两步变一步，且落在拇指最舒适的位置。
 * 抽屉**保留**：标签栏是补充不是替代，完整菜单仍在汉堡抽屉里（方案 §3.3 约束 3）。
 *
 * ⚠️ 本组件的渲染由 Main.vue 的 `showTabBar` 门控，条件是 `isMobile && !auth.readonly`，
 * 与侧栏、抽屉用**同一个**判断 —— 只读会话（云之家深链免登）下三处一起消失，
 * 不会出现「点得下去但 403」。
 */
const tabs = computed<Tab[]>(() => {
  const list: Tab[] = [
    { key: "home", label: "工作台", path: "/", icon: HomeFilled },
    { key: "assets", label: "资产", path: "/assets", icon: Tickets },
    { key: "scan", label: "扫码", path: "/scan", icon: Camera },
  ];
  // 第 4 格只在有维修权限时出现，去向沿用 §3.2 的角色规则（与侧栏、工作台同一份）
  if (canSeeRepair.value) {
    list.push({ key: "repair", label: "维修", path: repairEntry.value.path, icon: Tools });
  }
  return list;
});

function isActive(key: string): boolean {
  const p = route.path;
  switch (key) {
    case "home":
      // 首页是 "/"，必须精确匹配，否则 startsWith("/") 会把所有页面都点亮
      return p === "/";
    case "assets":
      return p.startsWith("/assets");
    case "scan":
      return p.startsWith("/scan");
    case "repair":
      return p.startsWith("/repairs") || p.startsWith("/repair");
    default:
      return false;
  }
}
</script>

<style scoped>
/* 52px + 安全区；fixed 定位，不参与 .layout 的 flex 流，因此不会引起布局跳动 */
.tabbar {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 20;
  display: flex;
  height: calc(52px + env(safe-area-inset-bottom));
  padding-bottom: env(safe-area-inset-bottom);
  background: var(--surface);
  border-top: var(--hairline);
}

.tab {
  position: relative;
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  min-width: 0;
  text-decoration: none;
  color: var(--steel-500);
  transition: color 0.15s;
}

/* ⚠️ 当前项用「信号黄顶部 2px 指示条 + 石墨黑图标文字」，而不是把图标染成黄色。
   原因：信号黄 #f5b301 在白底上只有 1.85:1、压暗版 #cf9700 也只有 2.60:1，
   都低于图形元素 3:1 的门槛。黄色只承担指示条，图标与文字用石墨黑保证可读。 */
.tab.is-active {
  color: var(--ink);
}

.tab.is-active::before {
  content: "";
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: var(--signal);
}

.tab-icon {
  font-size: 19px;
}

.tab-label {
  font-size: var(--t-11);
  line-height: 1.1;
}
</style>
