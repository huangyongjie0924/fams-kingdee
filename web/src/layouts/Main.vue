<template>
  <el-container class="layout">
    <el-aside v-if="!isMobile && !auth.readonly" width="200px" class="aside">
      <div class="brand">
        <span class="brand-plate">
          <img :src="gcgsLogo" alt="示例集团" />
        </span>
        <span class="brand-lines">
          <b>固定资产台账</b>
          <i class="u-mono">FAMS</i>
        </span>
      </div>
      <SideMenu />
    </el-aside>

    <el-drawer
      v-if="isMobile && !auth.readonly"
      v-model="menuOpen"
      class="menu-drawer"
      direction="ltr"
      size="70%"
      :with-header="false"
      :append-to-body="true"
    >
      <div class="brand">
        <span class="brand-plate">
          <img :src="gcgsLogo" alt="示例集团" />
        </span>
        <span class="brand-lines">
          <b>固定资产台账</b>
          <i class="u-mono">FAMS</i>
        </span>
      </div>
      <SideMenu @select="menuOpen = false" />
    </el-drawer>

    <el-container>
      <el-header class="header">
        <el-button v-if="isMobile && !auth.readonly" text :icon="Menu" @click="menuOpen = true" />
        <!-- 面包屑：解决「深层页面不知道自己在哪」。全部由 route.path 推导，路由零改动。 -->
        <nav v-if="crumbs.length" class="crumbs" aria-label="面包屑">
          <template v-for="(c, i) in crumbs" :key="i">
            <span v-if="i" class="crumb-sep" aria-hidden="true">/</span>
            <span class="crumb" :class="{ 'is-last': i === crumbs.length - 1 }">{{ c }}</span>
          </template>
        </nav>
        <div class="spacer" />
        <el-tag v-if="!auth.isAdmin" type="info" size="small">{{ auth.roleLabel }}</el-tag>
        <el-dropdown>
          <span class="user">
            {{ auth.user?.real_name || auth.user?.username }}
            <el-icon><ArrowDown /></el-icon>
          </span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="logout">退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </el-header>
      <el-main class="main" :class="{ 'has-tabbar': showTabBar }">
        <router-view />
      </el-main>
    </el-container>

    <!-- 底部标签栏：与侧栏/抽屉同一个门控条件（isMobile && !auth.readonly） -->
    <TabBar v-if="showTabBar" />
  </el-container>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Menu } from "@element-plus/icons-vue";
import { auth } from "../stores/auth";
import { useIsMobile } from "../composables/useIsMobile";
import { crumbTrail } from "../composables/useNav";
import gcgsLogo from "../assets/logos/gcgs.svg";
import SideMenu from "./SideMenu.vue";
import TabBar from "./TabBar.vue";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();
const menuOpen = ref(false);

const crumbs = computed(() => crumbTrail(route.path));

// 侧栏、抽屉、标签栏三处共用这一个判断：只读会话（云之家深链免登）下一起消失，
// 避免出现「能点但点了 403」。
const showTabBar = computed(() => isMobile.value && !auth.readonly);

function logout() {
  auth.logout();
  router.push({ name: "login" });
}
</script>

<style scoped>
.layout {
  height: 100%;
}

.aside {
  background: var(--ink);
  color: #dfe3ec;
}

/* 品牌区：集团 logo 必须用原色（品牌规范），而它的灰色字在石墨黑上只有 1.9:1，
   所以承载在浅色小铭牌上 —— 与登录页那块大铭牌同一语汇，只是缩到侧栏尺寸。 */
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 64px;
  padding: 0 14px;
  background: #0d0f12;
  border-bottom: 1px solid #23282f;
}

.brand-plate {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  height: 30px;
  padding: 0 7px;
  background: var(--paper);
  border-left: 2px solid var(--signal);
  border-radius: var(--r);
}

.brand-plate img {
  display: block;
  height: 20px;
  width: auto;
}

.brand-lines {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.25;
}

.brand-lines b {
  font-size: var(--t-13);
  font-weight: 600;
  color: #fff;
  white-space: nowrap;
}

.brand-lines i {
  font-style: normal;
  font-size: 10px;
  letter-spacing: .12em;
  color: var(--steel-400);
}

.header {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--surface);
  border-bottom: var(--hairline);
  height: 56px;
}

.header .spacer {
  flex: 1;
}

/* —— 面包屑 —— */
.crumbs {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: var(--t-13);
  color: var(--steel-500);
}

.crumb {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.crumb.is-last {
  color: var(--ink);
  font-weight: 600;
}

.crumb-sep {
  /* 原来是 --steel-300，在白底上只有 1.67:1（低于正文 4.5:1 门槛）。
     分隔符虽是 aria-hidden 的装饰，但既然成本为零就一起达标，避免体检留红。 */
  color: var(--steel-500);
}

.user {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: var(--t-14);
}

.main {
  padding: 0;
  overflow: auto;
}

/* 标签栏是 fixed 定位的，必须给主内容区留出等高底部空间，
   否则列表最后一行会被永久遮住（方案 §3.3 约束 2）。 */
.main.has-tabbar {
  padding-bottom: calc(52px + env(safe-area-inset-bottom));
}

/* 窄屏只留最后一级，避免面包屑把顶栏挤爆 */
@media (max-width: 767px), (max-height: 520px) {
  .crumbs .crumb:not(.is-last),
  .crumbs .crumb-sep {
    display: none;
  }
}
</style>

<style>
/* el-drawer 开了 append-to-body，会 teleport 到 body，scoped 样式够不到 */
.menu-drawer .el-drawer__body {
  padding: 0;
  background: var(--ink);
}
</style>
