<template>
  <el-container class="layout">
    <el-aside v-if="!isMobile" width="200px" class="aside">
      <div class="brand">固定资产台账</div>
      <SideMenu />
    </el-aside>

    <el-drawer
      v-if="isMobile"
      v-model="menuOpen"
      class="menu-drawer"
      direction="ltr"
      size="70%"
      :with-header="false"
      :append-to-body="true"
    >
      <div class="brand">固定资产台账</div>
      <SideMenu @select="menuOpen = false" />
    </el-drawer>

    <el-container>
      <el-header class="header">
        <el-button v-if="isMobile" text :icon="Menu" @click="menuOpen = true" />
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
      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import { Menu } from "@element-plus/icons-vue";
import { auth } from "../stores/auth";
import { useIsMobile } from "../composables/useIsMobile";
import SideMenu from "./SideMenu.vue";

const router = useRouter();
const isMobile = useIsMobile();
const menuOpen = ref(false);

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
  background: #2b3245;
  color: #dfe3ec;
}

.brand {
  height: 56px;
  line-height: 56px;
  text-align: center;
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  background: #232a3b;
}

.header {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #fff;
  border-bottom: 1px solid #e4e7ed;
  height: 56px;
}

.header .spacer {
  flex: 1;
}

.user {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 14px;
}

.main {
  padding: 0;
  overflow: auto;
}
</style>

<style>
/* el-drawer 开了 append-to-body，会 teleport 到 body，scoped 样式够不到 */
.menu-drawer .el-drawer__body {
  padding: 0;
  background: #2b3245;
}
</style>
