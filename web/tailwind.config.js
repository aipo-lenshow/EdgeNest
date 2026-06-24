/** @type {import('tailwindcss').Config} */
export default {
  // `class` strategy: a `<html class="dark">` toggle (set by theme.ts based on
  // user preference: light / dark / auto). Default skin is dark to preserve
  // v0.01 look — `light` is a new opt-in.
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: { extend: {} },
  plugins: [],
};
