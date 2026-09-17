import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// 本地联调默认打本机后端；接生产后端时：
//   VITE_API_TARGET=https://<your-host>:8443 npm run dev
const API_TARGET = process.env.VITE_API_TARGET || "http://localhost:8080";
const proxy = {
  target: API_TARGET,
  changeOrigin: true,
  secure: false, // 生产用自签证书，校验会直接断连
};

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    host: true, // 真机通过局域网 IP 访问 dev server
    proxy: {
      "/api": proxy,
      "/uploads": proxy,
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
