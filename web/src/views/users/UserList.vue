<template>
  <div class="page">
    <!-- 这个页面没做窄屏适配：改角色和重置密码都在电脑上做，给个占位比给个残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="用户管理需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-else>
      <div class="toolbar">
        <span class="hint">
          只读、盘点员、维修工绑定员工：只读看自己名下资产与自己的报修，盘点员据此被指派资产，
          维修工据此被指派维修单；部门负责人选管辖部门，看本部门及全部下级部门。
        </span>
        <span class="spacer" />
        <el-button v-if="canManage" type="primary" :icon="Plus" @click="open()">新增账号</el-button>
      </div>

      <el-table :data="users" border v-loading="loading">
        <el-table-column prop="username" label="用户名" width="160" />
        <el-table-column prop="real_name" label="姓名" width="140" />
        <el-table-column label="角色" width="140">
          <template #default="{ row }">
            <el-tag size="small">{{ roleLabel(row.role) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="绑定员工 / 管辖部门" min-width="180">
          <template #default="{ row }">
            <span v-if="row.dept_name">{{ row.dept_name }}</span>
            <span v-else-if="row.employee_name">{{ row.employee_name }}</span>
            <span v-else class="muted">未绑定</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? "启用" : "停用" }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="canManage" label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="open(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialog" :title="form.id ? '编辑账号' : '新增账号'" width="460px">
      <el-form :model="form" label-width="90px">
        <el-form-item label="用户名">
          <el-input v-model="form.username" :disabled="!!form.id" />
        </el-form-item>
        <el-form-item :label="form.id ? '重置密码' : '密码'">
          <el-input v-model="form.password" type="password" show-password
            :placeholder="form.id ? '留空表示不修改' : '至少 6 位'" />
        </el-form-item>
        <el-form-item label="姓名">
          <el-input v-model="form.real_name" />
        </el-form-item>
        <el-form-item label="角色">
          <el-select v-model="form.role" style="width: 100%">
            <el-option v-for="r in roles" :key="r.value" :label="r.label" :value="r.value" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.role === 'dept_head'" label="管辖部门">
          <el-select v-model="form.dept_id" filterable clearable style="width: 100%"
            placeholder="本部门及全部下级部门">
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-else label="绑定员工">
          <el-select v-model="form.employee_id" filterable clearable style="width: 100%"
            placeholder="盘点员 / 只读需绑定">
            <el-option v-for="e in employees" :key="e.id" :label="empLabel(e)" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const users = ref<any[]>([]);
const employees = ref<any[]>([]);
const departments = ref<any[]>([]);
const loading = ref(false);
const dialog = ref(false);
const form = ref<any>({});

const canManage = computed(() => auth.can("user.manage"));

const roles = [
  { value: "admin", label: "管理员" },
  { value: "asset_manager", label: "资产管理员" },
  { value: "counter", label: "盘点员" },
  { value: "dept_head", label: "部门负责人" },
  { value: "repair_tech", label: "维修工" },
  { value: "viewer", label: "只读" },
];

function roleLabel(v: string) {
  return roles.find((r) => r.value === v)?.label ?? v;
}

function empLabel(e: any) {
  return e.dept_name ? `${e.name}（${e.dept_name}）` : e.name;
}

async function load() {
  loading.value = true;
  try {
    const { data } = await http.get("/users");
    users.value = data || [];
  } finally {
    loading.value = false;
  }
}

async function loadEmployees() {
  const { data } = await http.get("/employees");
  employees.value = data || [];
}

async function loadDepartments() {
  const { data } = await http.get("/departments");
  departments.value = data || [];
}

function open(row?: any) {
  form.value = row
    ? { ...row, password: "" }
    : {
        id: 0, username: "", password: "", real_name: "", role: "viewer",
        employee_id: null, dept_id: null, enabled: true,
      };
  dialog.value = true;
}

async function save() {
  const { id, username, password, real_name, role, employee_id, dept_id, enabled } = form.value;
  const payload = {
    username, password, real_name, role,
    employee_id: employee_id || 0,
    dept_id: dept_id || 0,
    enabled,
  };
  if (id) {
    await http.put(`/users/${id}`, { ...payload, username: undefined });
  } else {
    await http.post("/users", payload);
  }
  dialog.value = false;
  ElMessage.success("已保存");
  await load();
}

async function remove(row: any) {
  await ElMessageBox.confirm(`确认删除账号「${row.username}」？`, "提示", { type: "warning" });
  await http.delete(`/users/${row.id}`);
  ElMessage.success("已删除");
  await load();
}

onMounted(async () => {
  await Promise.all([load(), loadEmployees(), loadDepartments()]);
});
</script>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.hint {
  color: #909399;
  font-size: 13px;
}

.spacer {
  flex: 1;
}

.muted {
  color: #c0c4cc;
}
</style>
