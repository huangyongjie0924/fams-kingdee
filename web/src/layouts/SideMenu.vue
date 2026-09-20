<template>
  <el-menu :default-active="active" router class="menu" @select="emit('select')">
    <el-menu-item index="/assets">
      <el-icon><Tickets /></el-icon><span>资产列表</span>
    </el-menu-item>
    <!-- 扫码只在窄屏出现：桌面浏览器没有摄像头取景的实用场景，列表页也有更快的检索 -->
    <el-menu-item v-if="isMobile" index="/scan">
      <el-icon><Camera /></el-icon><span>扫码</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('asset.manage')" index="/import">
      <el-icon><Upload /></el-icon><span>批量导入</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('sync.manage')" index="/sync">
      <el-icon><Refresh /></el-icon><span>金蝶同步</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('count.manage')" index="/count">
      <el-icon><Files /></el-icon><span>盘点管理</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('count.enter')" index="/count/mine">
      <el-icon><Checked /></el-icon><span>我的盘点</span>
    </el-menu-item>
    <!-- 维修流程：入口按权限出现，风格与既有菜单一致（全部 v-if="auth.can(...)"） -->
    <el-menu-item v-if="auth.can('repair.report')" index="/repairs/mine">
      <el-icon><Bell /></el-icon><span>我的报修</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('repair.handle')" index="/repairs?mine=1">
      <el-icon><Tools /></el-icon><span>我的维修</span>
    </el-menu-item>
    <el-menu-item v-if="auth.can('repair.dispatch')" index="/repairs">
      <el-icon><List /></el-icon><span>维修管理</span>
    </el-menu-item>
    <el-sub-menu v-if="auth.can('master.manage')" index="master">
      <template #title>
        <el-icon><Setting /></el-icon><span>基础数据</span>
      </template>
      <el-menu-item index="/master/category">资产分类</el-menu-item>
      <el-menu-item index="/master/area">区域</el-menu-item>
      <el-menu-item index="/master/org">公司与部门员工</el-menu-item>
      <el-menu-item index="/master/vendor">供应商与标签</el-menu-item>
    </el-sub-menu>
    <el-menu-item v-if="auth.can('user.manage')" index="/users">
      <el-icon><User /></el-icon><span>用户管理</span>
    </el-menu-item>
  </el-menu>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";
import { auth } from "../stores/auth";
import { useIsMobile } from "../composables/useIsMobile";

const emit = defineEmits(["select"]);
const route = useRoute();
const active = computed(() => route.path);
const isMobile = useIsMobile();
</script>

<style scoped>
/* 深色主题靠挂在 .menu 上的 CSS 变量向下继承，抽屉里复用同一份 */
.menu {
  border-right: none;
  background: #2b3245;
  --el-menu-text-color: #c8cddb;
  --el-menu-bg-color: #2b3245;
  --el-menu-hover-bg-color: #363e55;
  --el-menu-active-color: #ffffff;
}
</style>
