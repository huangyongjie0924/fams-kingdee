import { createRouter, createWebHashHistory } from "vue-router";
import { auth } from "../stores/auth";

const routes = [
  { path: "/login", name: "login", component: () => import("../views/Login.vue") },
  {
    path: "/",
    component: () => import("../layouts/Main.vue"),
    children: [
      { path: "", redirect: "/assets" },
      { path: "assets", name: "assets", component: () => import("../views/asset/List.vue") },
      { path: "assets/new", name: "asset-new", component: () => import("../views/asset/CardForm.vue") },
      { path: "assets/labels", name: "asset-labels", component: () => import("../views/asset/Labels.vue") },
      { path: "assets/:id/edit", name: "asset-edit", component: () => import("../views/asset/CardForm.vue") },
      { path: "scan", name: "scan", component: () => import("../views/scan/Scan.vue") },
      { path: "import", name: "import", component: () => import("../views/asset/Import.vue") },
      { path: "sync", name: "sync", component: () => import("../views/sync/Sync.vue") },
      { path: "count", name: "count", component: () => import("../views/count/PlanList.vue") },
      { path: "count/mine", name: "count-mine", component: () => import("../views/count/MyCount.vue") },
      { path: "count/:id", name: "count-detail", component: () => import("../views/count/PlanDetail.vue") },
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
    return { name: "assets" };
  }
  if (to.name !== "login" && !auth.token) {
    return { name: "login" };
  }
  return true;
});

export default router;
