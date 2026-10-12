import { createApp } from "vue";
import ElementPlus from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import * as Icons from "@element-plus/icons-vue";
import "element-plus/dist/index.css";

import App from "./App.vue";
import router from "./router";
import { stashAssetCode } from "./utils/deeplink";
import { tryYunzhijiaLogin, type YzjCredentials } from "./utils/yzj-login";
import "./style.css";
// 设计令牌层必须排在 style.css 之后：:root 上同特异性时后引入者胜出，
// 只有排在最后才压得住 element-plus/dist/index.css 的默认变量。回滚 = 删这一行。
import "./styles/tokens.css";

const app = createApp(App);
for (const [name, comp] of Object.entries(Icons)) {
  app.component(name, comp as any);
}
app.use(ElementPlus, { locale: zhCn });

// 云之家免登：云之家 APP / 云之家客户端打开待办卡片时会把 ticket 或 yzj_token 追加到首页 URL
// （查询串或 hash）。app.use(router) 会立刻发起首次导航，而 beforeEach 在无 token 时跳登录页
// 会重写 hash —— 所以必须先把凭据换掉 token 再装 router，否则首次免登会落到登录页，
// 深链里的 asset_code 也会一起被冲掉。
//
// 时序（docs/云之家待办通知集成方案.md §5.6）：
//   先读取并暂存凭据 → 立即从 URL 剥掉凭据（早于任何外部脚本加载，避免凭据随 Referer 外泄）
//   → 走三层免登链 → app.use(router)。

/** 从 query 或 hash query 读取指定参数（两处都要读，参照 utils/deeplink.ts）。 */
function readParam(name: string): string {
  const q = new URLSearchParams(window.location.search);
  const v = q.get(name);
  if (v) return v;
  const h = window.location.hash;
  const i = h.indexOf("?");
  if (i >= 0) return new URLSearchParams(h.slice(i + 1)).get(name) || "";
  return "";
}

/** 读取当前 URL 上的免登凭据（ticket / yzj_token）。 */
function readCredentials(): YzjCredentials {
  return {
    ticket: readParam("ticket"),
    yzjToken: readParam("yzj_token"),
  };
}

/**
 * 从 URL（query 与 hash query）剥掉所有免登凭据。
 *
 * ticket 与 yzj_token 都是等价于密码的 bearer 凭据，必须早于任何外部资源加载清除，
 * 否则会随 Referer 外泄（§5.6）。
 *
 * 注：原先这里还有一句「这也是 qing.js 用动态注入而非静态 <script> 的原因」——qing.js 已随
 * 免登第 1 层（JS 桥）作废而删除（§5.3），但**这条纪律保留**：将来一旦引入任何第三方脚本，
 * 「先清 URL 再加载外部资源」会立刻重新变成硬约束。
 */
function cleanCredentials() {
  const u = new URL(window.location.href);
  u.searchParams.delete("ticket");
  u.searchParams.delete("yzj_token");
  const h = u.hash;
  const i = h.indexOf("?");
  if (i >= 0) {
    const p = new URLSearchParams(h.slice(i + 1));
    p.delete("ticket");
    p.delete("yzj_token");
    u.hash = p.toString() ? h.slice(0, i) + "?" + p.toString() : h.slice(0, i);
  }
  history.replaceState({}, "", u.toString());
}

(async () => {
  stashAssetCode();
  // 先读凭据再清 URL：清 URL 必须早于任何外部资源加载（当前页面已无外部脚本，见 cleanCredentials 注释）。
  const creds = readCredentials();
  cleanCredentials();
  // 免登必须先于 app.use(router)：否则首次导航会因无 token 跳登录页并重写 hash。
  await tryYunzhijiaLogin(creds);
  app.use(router);
  app.mount("#app");
})();
