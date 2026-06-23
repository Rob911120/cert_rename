/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ['./index.html'],
  darkMode: ['class', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        sidebar: { DEFAULT: '#1a1d2e', light: '#f1f5f9' },
        main:    { DEFAULT: '#0f1117', light: '#f8fafc' },
        card:    { DEFAULT: '#1e2235', light: '#ffffff' },
        accent:  '#6366f1',
      },
      fontFamily: {
        sans: ['-apple-system', 'BlinkMacSystemFont', 'Segoe UI', 'Inter', 'sans-serif'],
        mono: ['SF Mono', 'ui-monospace', 'Consolas', 'monospace'],
      },
    },
  },
  plugins: [],
};
