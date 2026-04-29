import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],

  // ── Production Build Optimizations ──────────────────────────────
  build: {
    // Target modern browsers for smaller bundles
    target: "es2020",

    // Enable CSS code splitting
    cssCodeSplit: true,

    // Generate sourcemaps for production debugging (disabled for smaller output)
    sourcemap: false,

    // Chunk size warning threshold (KB)
    chunkSizeWarningLimit: 500,

    // Minification options
    minify: "esbuild",

    // Terser/esbuild options for aggressive minification
    // esbuild is used by default and is already fast/compact

    rollupOptions: {
      output: {
        // Manual chunk splitting: separate vendor code from app code
        manualChunks: {
          // React and ReactDOM in their own vendor chunk (cached across deploys)
          "vendor-react": ["react", "react-dom"],
        },

        // Naming patterns for better caching
        chunkFileNames: "assets/js/[name]-[hash].js",
        entryFileNames: "assets/js/[name]-[hash].js",
        assetFileNames: "assets/[ext]/[name]-[hash].[ext]",
      },
    },

    // Enable brotli/gzip size reporting
    reportCompressedSize: true,

    // Inline small assets as base64 (default 4096 bytes)
    assetsInlineLimit: 4096,
  },

  // ── CSS Processing ──────────────────────────────────────────────
  css: {
    // CSS module behavior
    modules: {
      localsConvention: "camelCaseOnly",
    },

    // PostCSS for autoprefixer and minification
    postcss: {},
    devSourcemap: false,
  },

  // ── Preview Server ──────────────────────────────────────────────
  preview: {
    port: 4173,
    strictPort: true,
  },

  // ── Dev Server ──────────────────────────────────────────────────
  server: {
    port: 5173,
    strictPort: true,
  },
});
