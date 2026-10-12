<template>
  <el-menu :default-active="active" router class="menu" @select="emit('select')">
    <!-- 组内可见项为 0 的整组已在 visibleNavGroups 里剔除，这里不再需要额外判断 -->
    <el-menu-item-group v-for="g in visibleNavGroups" :key="g.key" :title="g.title">
      <template v-for="it in g.items" :key="it.key">
        <el-sub-menu v-if="it.children" :index="it.key">
          <template #title>
            <el-icon><component :is="it.icon" /></el-icon>
            <span>{{ it.label }}</span>
          </template>
          <el-menu-item v-for="c in it.children" :key="c.key" :index="c.path">
            {{ c.label }}
          </el-menu-item>
        </el-sub-menu>

        <el-menu-item v-else :index="it.path!">
          <el-icon><component :is="it.icon" /></el-icon>
          <span>{{ it.label }}</span>
        </el-menu-item>
      </template>
    </el-menu-item-group>
  </el-menu>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";
import { visibleNavGroups } from "../composables/useNav";

const emit = defineEmits(["select"]);
const route = useRoute();

// el-menu 的 index 是「导航目标」，可能是 "/repairs" 也可能是 "/repairs?mine=1"
// （取决于 repairEntry 按权限算出的去向），而 route.path 永远只有 "/repairs"。
// 所以高亮判定必须分两级，不能直接把 query 拼回去就完事：
//   1) 完整 index 精确命中 —— 覆盖「维护人员只有 /repairs?mine=1 这一个入口」的情况；
//   2) 退化到只比路径（忽略 query）—— 覆盖「管理员在 /repairs 页切到『待我处理』分段」
//      的情况：此时菜单 index 是 /repairs，若拿 "/repairs?mine=1" 去比就一个都不匹配，
//      整条侧栏会失去高亮（实测踩到）。
const active = computed(() => {
  const p = route.path;
  const paths: string[] = [];
  for (const g of visibleNavGroups.value) {
    for (const it of g.items) {
      if (it.children) for (const c of it.children) if (c.path) paths.push(c.path);
      else if (it.path) paths.push(it.path);
    }
  }
  const full = route.query.mine === "1" ? `${p}?mine=1` : p;
  if (paths.includes(full)) return full;
  return paths.find((x) => x.split("?")[0] === p) ?? p;
});
</script>

<style scoped>
/* 深色主题靠挂在 .menu 上的 CSS 变量向下继承，抽屉里复用同一份。
   底色与登录页同为石墨黑（原为 #2b3245 蓝灰），次级文字 #c8cddb 在 #14171b 上
   实测 11.3:1，远高于 AA 的 4.5:1，无对比度回退。 */
.menu {
  border-right: none;
  background: var(--ink);
  --el-menu-text-color: #c8cddb;
  --el-menu-bg-color: var(--ink);
  --el-menu-hover-bg-color: var(--ink-2);
  --el-menu-active-color: #ffffff;
  --el-menu-item-height: 40px;
  --el-menu-sub-item-height: 36px;
}

/* —— 分组标题：小号、加字距、钢灰，只做层级提示不抢视线 —— */
.menu :deep(.el-menu-item-group__title) {
  padding: 14px 18px 4px;
  font-size: var(--t-11);
  line-height: 1.4;
  letter-spacing: .1em;
  color: var(--steel-400);
}

.menu :deep(.el-menu-item-group:first-child .el-menu-item-group__title) {
  padding-top: 10px;
}

/* —— 选中指示条：信号黄 2px 左侧竖条，唯一强调色只用于「你在哪」 —— */
.menu :deep(.el-menu-item) {
  position: relative;
}

.menu :deep(.el-menu-item.is-active)::before {
  content: "";
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 2px;
  background: var(--signal);
}

.menu :deep(.el-menu-item.is-active) {
  background: var(--ink-2);
}

/* 子菜单展开后的子项缩进比 EP 默认略收，与 40px 行高更协调 */
.menu :deep(.el-sub-menu .el-menu-item) {
  padding-left: 46px !important;
  min-width: 0;
}
</style>
