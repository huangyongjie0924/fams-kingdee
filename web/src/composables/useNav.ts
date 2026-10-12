import { computed, type Component, type ComputedRef } from "vue";
import {
  Camera,
  Checked,
  EditPen,
  Files,
  HomeFilled,
  Monitor,
  Refresh,
  Setting,
  Tickets,
  Tools,
  Upload,
  User,
} from "@element-plus/icons-vue";
import { auth } from "../stores/auth";
import { useIsMobile } from "./useIsMobile";
import { canSeeRepair, repairEntry } from "./useRepairEntry";

/** 子菜单里的叶子项（如「基础数据」下的四张主数据表）。 */
export interface NavLeaf {
  key: string;
  label: string;
  path: string;
}

export interface NavItem {
  key: string;
  label: string;
  icon: Component;
  /** 是否可见。⚠️ 只读 `auth.can()`，不新增/不修改任何权限位。 */
  show: boolean;
  /** 叶子项的去向；有 children 时为 undefined */
  path?: string;
  children?: NavLeaf[];
}

export interface NavGroup {
  key: string;
  title: string;
  items: NavItem[];
}

/**
 * 导航从「模块清单」改成「任务地图」（方案 §3.1）：12 项平铺 → 5 组。
 *
 * ⚠️ **分组可见性规则**：组内可见项为 0 时整组不渲染，否则低权限账号会看到一堆空组。
 * 见下方 `visibleNavGroups`。
 *
 * ⚠️ **零路由改动**：这里所有 path 与 `router/index.ts` 逐条对应，route name 一个没动
 * （云之家待办深链 `#/repairs/{id}` 已推到员工手机上）。
 */
export const navGroups: ComputedRef<NavGroup[]> = computed(() => {
  const isMobile = useIsMobile();
  return [
    {
      key: "daily",
      title: "日常",
      items: [
        // 工作台与资产台账对所有角色可见，不加权限门（与改造前一致）
        { key: "home", label: "工作台", icon: HomeFilled, path: "/", show: true },
        { key: "assets", label: "资产台账", icon: Tickets, path: "/assets", show: true },
        // IT 信息设备台账（2026-10-10 需求）。
        //
        // ⚠️ **不加权限门，与资产台账一致**：用户明确要求「IT 台账的可见范围复用资产台账的范围」
        // —— 员工只看自己名下的 IT 设备、部门负责人看本部门。若在这里门控 it.manage，
        // 员工就完全进不去这个页面，「复用资产台账范围」这条需求等于没落地。
        // 写操作（新增/编辑/删除/导入）在页面内按 auth.can('it.manage') 隐藏。
        { key: "it-equipment", label: "IT 设备台账", icon: Monitor, path: "/it-equipment", show: true },
        // 扫码只在窄屏出现：桌面浏览器没有摄像头取景的实用场景，列表页也有更快的检索
        { key: "scan", label: "扫码", icon: Camera, path: "/scan", show: isMobile.value },
      ],
    },
    {
      key: "repair",
      title: "维修",
      items: [
        // 创建动作：从页内按钮提升为显式入口（方案 §3.4）
        {
          key: "repair-new",
          label: "报修申请",
          icon: EditPen,
          path: "/repairs/new",
          show: auth.can("repair.report"),
        },
        // 查看动作：标题与去向由权限优先级决定，与工作台共用同一份判断
        {
          key: "repair-view",
          label: repairEntry.value.label,
          icon: Tools,
          path: repairEntry.value.path,
          show: canSeeRepair.value,
        },
      ],
    },
    {
      key: "count",
      title: "盘点",
      items: [
        { key: "count", label: "盘点计划", icon: Files, path: "/count", show: auth.can("count.manage") },
        { key: "count-mine", label: "我的盘点", icon: Checked, path: "/count/mine", show: auth.can("count.enter") },
      ],
    },
    {
      key: "data",
      title: "数据",
      items: [
        { key: "import", label: "批量导入", icon: Upload, path: "/import", show: auth.can("asset.manage") },
        { key: "sync", label: "金蝶同步", icon: Refresh, path: "/sync", show: auth.can("sync.manage") },
      ],
    },
    {
      key: "system",
      title: "系统",
      items: [
        {
          key: "master",
          label: "基础数据",
          icon: Setting,
          show: auth.can("master.manage"),
          children: [
            { key: "master-category", label: "资产分类", path: "/master/category" },
            { key: "master-area", label: "区域", path: "/master/area" },
            { key: "master-org", label: "公司与部门员工", path: "/master/org" },
            { key: "master-vendor", label: "供应商与标签", path: "/master/vendor" },
          ],
        },
        { key: "users", label: "用户管理", icon: User, path: "/users", show: auth.can("user.manage") },
      ],
    },
  ];
});

/**
 * 组内可见项为 0 的整组被剔除 —— 低权限账号（如维护人员）只会看到「日常 · 维修」两组，
 * 不会看到 3 个空组标题。
 */
export const visibleNavGroups: ComputedRef<NavGroup[]> = computed(() =>
  navGroups.value
    .map((g) => ({ ...g, items: g.items.filter((i) => i.show) }))
    .filter((g) => g.items.length > 0),
);

/** 面包屑路径表：`[组, 项]` 或 `[组, 子项, 项]`。顺序敏感，具体规则必须排在通用规则之前。 */
const CRUMB_RULES: { test: (p: string) => boolean; trail: string[] }[] = [
  { test: (p) => p === "/", trail: ["日常", "工作台"] },
  { test: (p) => p === "/assets/new", trail: ["日常", "新建资产"] },
  { test: (p) => p === "/assets/labels", trail: ["日常", "打印标签"] },
  { test: (p) => /^\/assets\/[^/]+\/edit$/.test(p), trail: ["日常", "编辑资产"] },
  // ⚠️ 这里**故意没有** `/assets/:id` 的规则：资产详情不是路由页，而是 asset/List.vue
  // 里的对话框（`<CardDetail v-model="detailVisible" :card-id="detailId" />`），
  // router/index.ts 里根本没有 `assets/:id`。曾按「有详情页」的假设写过一条规则，
  // 结果是死代码，还会让人误以为深链 `#/assets/{id}` 可用（实际是空白页）。
  { test: (p) => p.startsWith("/assets"), trail: ["日常", "资产台账"] },
  { test: (p) => p.startsWith("/scan"), trail: ["日常", "扫码"] },

  // IT 信息设备台账（2026-10-10 需求）。具体规则必须排在通用规则之前。
  { test: (p) => p.startsWith("/it-equipment/import"), trail: ["日常", "IT 设备导入"] },
  { test: (p) => p.startsWith("/it-equipment"), trail: ["日常", "IT 设备台账"] },

  { test: (p) => p === "/repairs/new", trail: ["维修", "报修申请"] },
  { test: (p) => p === "/repairs/mine", trail: ["维修", "我的报修单"] },
  { test: (p) => /^\/repairs\/[^/]+$/.test(p), trail: ["维修", "维修单详情"] },
  { test: (p) => p.startsWith("/repairs"), trail: ["维修", "维修工单"] },

  { test: (p) => p === "/count/mine", trail: ["盘点", "我的盘点"] },
  { test: (p) => /^\/count\/[^/]+$/.test(p), trail: ["盘点", "盘点详情"] },
  { test: (p) => p.startsWith("/count"), trail: ["盘点", "盘点计划"] },

  { test: (p) => p.startsWith("/import"), trail: ["数据", "批量导入"] },
  { test: (p) => p.startsWith("/sync"), trail: ["数据", "金蝶同步"] },

  { test: (p) => p.startsWith("/master/category"), trail: ["系统", "基础数据", "资产分类"] },
  { test: (p) => p.startsWith("/master/area"), trail: ["系统", "基础数据", "区域"] },
  { test: (p) => p.startsWith("/master/org"), trail: ["系统", "基础数据", "公司与部门员工"] },
  { test: (p) => p.startsWith("/master/vendor"), trail: ["系统", "基础数据", "供应商与标签"] },
  { test: (p) => p.startsWith("/users"), trail: ["系统", "用户管理"] },
];

/**
 * 面包屑：解决「深层页面不知道自己在哪」（方案 §5.1）。
 *
 * 不改路由 —— 全部由 `route.path` 推导，因此 `router/index.ts` 一行未动，
 * 也就不存在「改名导致历史深链失效」的风险。
 */
export function crumbTrail(path: string): string[] {
  for (const r of CRUMB_RULES) {
    if (r.test(path)) return r.trail;
  }
  return [];
}
