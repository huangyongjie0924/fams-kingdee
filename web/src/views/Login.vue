<template>
  <div class="login-wrap">
    <el-card class="login-card">
      <h2 class="title">固定资产台账系统</h2>
      <el-form :model="form" @submit.prevent>
        <el-form-item>
          <el-input v-model="form.username" placeholder="用户名" size="large" @keyup.enter="submit">
            <template #prefix><el-icon><User /></el-icon></template>
          </el-input>
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="form.password"
            type="password"
            placeholder="密码"
            size="large"
            show-password
            @keyup.enter="submit"
          >
            <template #prefix><el-icon><Lock /></el-icon></template>
          </el-input>
        </el-form-item>
        <el-button type="primary" size="large" :loading="loading" style="width: 100%" @click="submit">
          登录
        </el-button>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import http from "../api/client";
import { auth } from "../stores/auth";

const router = useRouter();
const loading = ref(false);
const form = ref({ username: "", password: "" });

async function submit() {
  if (!form.value.username || !form.value.password) return;
  loading.value = true;
  try {
    const { data } = await http.post("/login", form.value);
    auth.login(data.token, data.user);
    router.push({ name: "assets" });
  } finally {
    loading.value = false;
  }
}
</script>

<style scoped>
.login-wrap {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #5b6bc0 0%, #3a4a9f 100%);
}

.login-card {
  width: 380px;
  padding: 12px 8px;
}

.title {
  margin: 0 0 24px;
  text-align: center;
  font-size: 20px;
  font-weight: 600;
}
</style>
