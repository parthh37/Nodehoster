// Applies the saved theme before first paint to avoid a flash. Kept as an
// external file because the admin console's CSP disallows inline scripts.
(function () {
  try {
    var t = localStorage.getItem('nh-theme');
    var dark = t === 'dark' || ((!t || t === 'system') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (dark) document.documentElement.classList.add('dark');
  } catch (e) {}
})();
