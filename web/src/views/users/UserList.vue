<template>
  <div class="page">
    <!-- 这个页面没做窄屏适配：改角色和重置密码都在电脑上做，给个占位比给个残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="用户管理需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-else>
      <div class="toolbar">
        <span class="hint">
          员工 / 盘点员 / 维护人员绑定员工：员工看自己名下资产及报修中的资产，盘点员据此被指派资产，
          维护人员据此被指派维修单；部门负责人选管辖部门，看本部门及全部下级部门。
        </span>
        <span class="spacer" />
        <el-button v-if="canManage" type="primary" :icon="Plus" @click="open()">新增账号</el-button>
      </div>

      <el-table :data="users" border v-loading="loading">
        <el-table-column prop="username" label="用户名" width="160" />
        <el-table-column prop="real_name" label="姓名" width="140" />
        <el-table-column label="角色" width="200">
          <template #default="{ row }">
            <el-tag size="small">{{ roleLabelOf(row) }}</el-tag>
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
          <el-select v-model="form.roles" multiple style="width: 100%"
            placeholder="可多选">
            <el-option v-for="r in roles" :key="r.value" :label="r.label" :value="r.value" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="needDept" label="管辖部门">
          <el-select v-model="form.dept_id" filterable clearable style="width: 100%"
            placeholder="本部门及全部下级部门">
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="needEmployee" label="绑定员工">
          <el-select v-model="form.employee_id" filterable clearable style="width: 100%"
            placeholder="员工 / 盘点员 / 维护人员需绑定">
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

// 顺序与后端 model.Roles 一致（同时是展示顺序 / NormalizeRoles 的归一化顺序）。
// ⚠️ 新增角色必须**追加在末尾**（与后端 Roles 同一条纪律，见 model/model.go 的注释）。
const roles = [
  { value: "admin", label: "管理员" },
  { value: "asset_manager", label: "资产管理员" },
  { value: "counter", label: "盘点员" },
  { value: "dept_head", label: "部门负责人" },
  { value: "maintainer", label: "维护人员" },
  { value: "employee", label: "员工" },
  // it_admin（IT 管理员）：维护 IT 信息设备台账。**不需要绑定员工**——
  // 它在 assetScope / itScope 里都是「不限」，不靠 employee_id 收窄范围
  // （对照 employee / counter / maintainer 三者的范围都依赖 employee_id）。
  // 故**刻意不加进** EMP_BOUND_ROLES，后端 roleBindingErr 同样不要求它。
  { value: "it_admin", label: "IT 管理员" },
];

// 需要绑定员工的角色：缺 employee_id 时后端会拒绝，前端只做友好提示。
const EMP_BOUND_ROLES = ["employee", "counter", "maintainer"];

// form.roles 可能是旧数据里的单值 role，统一取成数组，模板与提交都用它。
const formRoles = computed<string[]>(() => {
  const r = form.value.roles;
  if (Array.isArray(r)) return r;
  return r ? [r] : [];
});

// 含 dept_head 才显示「管辖部门」。
const needDept = computed(() => formRoles.value.includes("dept_head"));
// 含任一需绑定员工的角色就显示「绑定员工」；没有 dept_head 时也显示（沿用旧行为：非部门负责人显示绑定员工）。
const needEmployee = computed(
  () => formRoles.value.some((r) => EMP_BOUND_ROLES.includes(r)) || !formRoles.value.includes("dept_head"),
);

// 单行角色展示：优先用多角色 roles，回落到单值 role（旧数据）。
function roleLabelOf(row: any): string {
  const rs: string[] = row.roles?.length ? row.roles : row.role ? [row.role] : [];
  return rs.map((r) => roles.find((x) => x.value === r)?.label ?? r).join("、");
}

function empLabel(e: any) {
  return e.dept_name ? `${e.name}（${e.dept_name}）` : e.name;
}

// 前端友好校验：与后端 roleBindingErr 同口径（权威校验在后端）。
// 返回 null 表示通过，否则返回提示文案。
function bindingError(rs: string[], employeeId: any, deptId: any): string | null {
  if (rs.includes("dept_head") && !deptId) return "部门负责人必须选择管辖的部门";
  if (rs.includes("employee") && !employeeId) return "员工需绑定员工";
  if (rs.includes("counter") && !employeeId) return "盘点员需绑定员工";
  if (rs.includes("maintainer") && !employeeId) return "维护人员需绑定员工";
  return null;
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
    ? {
        ...row,
        password: "",
        // 旧账号只有 role、没有 roles，回落到单元素数组（与后端 EffectiveRoles 同规则）。
        roles: row.roles?.length ? row.roles : row.role ? [row.role] : [],
      }
    : {
        id: 0, username: "", password: "", real_name: "",
        roles: ["employee"],
        employee_id: null, dept_id: null, enabled: true,
      };
  dialog.value = true;
}

async function save() {
  const { id, username, password, real_name, employee_id, dept_id, enabled } = form.value;
  const roleList: string[] = Array.isArray(form.value.roles)
    ? form.value.roles
    : form.value.roles
      ? [form.value.roles]
      : [];
  const err = bindingError(roleList, employee_id, dept_id);
  if (err) {
    ElMessage.warning(err);
    return;
  }
  const payload = {
    username, password, real_name,
    roles: roleList,
    // role 保留为主角色（= roles[0]），旧后端 / 回滚时仍可用。
    role: roleList[0],
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
