import { createRouter, createWebHashHistory } from "vue-router";
import { auth } from "../stores/auth";

const routes = [
  { path: "/login", name: "login", component: () => import("../views/Login.vue") },
  {
    path: "/",
    component: () => import("../layouts/Main.vue"),
    children: [
      { path: "", name: "home", component: () => import("../views/Home.vue") },
      { path: "assets", name: "assets", component: () => import("../views/asset/List.vue") },
      { path: "assets/new", name: "asset-new", component: () => import("../views/asset/CardForm.vue") },
      { path: "assets/labels", name: "asset-labels", component: () => import("../views/asset/Labels.vue") },
      { path: "assets/:id/edit", name: "asset-edit", component: () => import("../views/asset/CardForm.vue") },
      // IT 信息设备台账（2026-10-10 需求）。**新增路由，既有 23 条一条未动** ——
      // 云之家待办深链 `#/repairs/{id}` 已推到员工手机上，动既有路由的风险不可接受。
      { path: "it-equipment", name: "it-equipment", component: () => import("../views/it/List.vue") },
      { path: "it-equipment/import", name: "it-import", component: () => import("../views/it/Import.vue") },
      { path: "scan", name: "scan", component: () => import("../views/scan/Scan.vue") },
      { path: "import", name: "import", component: () => import("../views/asset/Import.vue") },
      { path: "sync", name: "sync", component: () => import("../views/sync/Sync.vue") },
      { path: "count", name: "count", component: () => import("../views/count/PlanList.vue") },
      { path: "count/mine", name: "count-mine", component: () => import("../views/count/MyCount.vue") },
      { path: "count/:id", name: "count-detail", component: () => import("../views/count/PlanDetail.vue") },
      { path: "repairs/new", name: "repair-new", component: () => import("../views/repair/New.vue") },
      { path: "repairs/mine", name: "repair-mine", component: () => import("../views/repair/Mine.vue") },
      { path: "repairs", name: "repairs", component: () => import("../views/repair/List.vue") },
      { path: "repairs/:id", name: "repair-detail", component: () => import("../views/repair/Detail.vue") },
      { path: "users", name: "users", component: () => import("../views/users/UserList.vue") },
      { path: "master/category", name: "master-category", component: () => import("../views/master/CategoryTree.vue") },
      { path: "master/area", name: "master-area", component: () => import("../views/master/AreaTree.vue") },
      { path: "master/org", name: "master-org", component: () => import("../views/master/Org.vue") },
      { path: "master/vendor", name: "master-vendor", component: () => import("../views/master/Vendor.vue") },
    ],
  },
];

const router = createRouter({ history: createWebHashHistory(), routes });

router.beforeEach((to) => {
  if (to.name === "login" && auth.token) {
    return { name: "home" };
  }
  if (to.name !== "login" && !auth.token) {
    // 带上目标路径，登录后回跳到同一张单（如经待办深链进入的维修单详情）。
    return { name: "login", query: { redirect: to.fullPath } };
  }
  return true;
});

export default router;
