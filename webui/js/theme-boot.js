// Apply the stored theme before first paint. theme.js owns every later change.
// This lives in its own file because the admin CSP is `script-src 'self'` and
// an inline copy is blocked.
try {
  var theme = localStorage.getItem("opencode2api.ui.theme") || "auto";
  var dark =
    theme === "dark" ||
    (theme === "auto" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
} catch (_) {}
