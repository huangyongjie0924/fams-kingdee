import { createApp } from "vue";
import ElementPlus, { ElMessage } from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import * as Icons from "@element-plus/icons-vue";
import "element-plus/dist/index.css";

import App from "./App.vue";
import router from "./router";
import { auth } from "./stores/auth";
import { stashAssetCode } from "./utils/deeplink";
import "./style.css";

const app = createApp(App);
for (const [name, comp] of Object.entries(Icons)) {
  app.component(name, comp as any);
}
app.use(ElementPlus, { locale: zhCn });

// 云之家单点登录：云之家 APP 打开轻应用时会把 ticket 追加到首页 URL（查询串或 hash）。
// app.use(router) 会立刻发起首次导航，而 beforeEach 在无 token 时跳登录页会重写 hash ——
// 所以必须先把 ticket 换掉 token 再装 router，否则首次免登会落到登录页，
// 深链里的 asset_code 也会一起被冲掉。
function readTicket(): string {
  const q = new URLSearchParams(window.location.search);
  const t = q.get("ticket");
  if (t) return t;
  const h = window.location.hash;
  const i = h.indexOf("?");
  if (i >= 0) return new URLSearchParams(h.slice(i + 1)).get("ticket") || "";
  return "";
}

function cleanTicket() {
  const u = new URL(window.location.href);
  u.searchParams.delete("ticket");
  const h = u.hash;
  const i = h.indexOf("?");
  if (i >= 0) {
    const p = new URLSearchParams(h.slice(i + 1));
    p.delete("ticket");
    u.hash = p.toString() ? h.slice(0, i) + "?" + p.toString() : h.slice(0, i);
  }
  history.replaceState({}, "", u.toString());
}

async function trySSO() {
  const ticket = readTicket();
  if (!ticket) return;
  try {
    // 用 fetch 直接调，绕开 axios 拦截器：SSO 失败的 401 不是「会话过期」，需要展示真实原因
    const res = await fetch("/api/sso/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticket }),
    });
    const data = await res.json();
    if (!res.ok) {
      ElMessage.error(data?.error || "云之家登录失败");
      return;
    }
    auth.login(data.token, data.user);
  } catch {
    ElMessage.error("云之家登录失败");
  } finally {
    cleanTicket();
  }
}

(async () => {
  stashAssetCode();
  await trySSO();
  app.use(router);
  app.mount("#app");
})();
