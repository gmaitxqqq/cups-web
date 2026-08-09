import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import ui from '@nuxt/ui/vite'

export default defineConfig({
  plugins: [
    vue(),
    ui({
      components: {
        prefix: 'U'
      }
    })
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules/vue') || id.includes('node_modules/vue-router')) {
            return 'vue-vendor'
          }
          if (id.includes('node_modules/@nuxt/ui') || id.includes('node_modules/reka-ui') || id.includes('node_modules/@vueuse')) {
            return 'ui-vendor'
          }
          // lucide 图标全集离线内联（见 main.js），单独成块便于长期缓存——
          // 图标数据几乎不变，与频繁更新的业务代码分离可减少重复下载
          if (id.includes('node_modules/@iconify-json/lucide')) {
            return 'icons-vendor'
          }
          if (id.includes('node_modules/pdfjs-dist')) {
            return 'pdf-vendor'
          }
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8090',
        changeOrigin: true
      }
    }
  }
})
