// Theme bootstrap — runs before any styles are applied so the page paints
// in the chosen theme on first frame (no flash of light content). The
// preference lives in localStorage under 'theme' with values
// "system" | "light" | "dark"; "system" (or missing) follows the OS via
// prefers-color-scheme. A change in either source flips html.is-dark.
(function(){
  var mq = window.matchMedia('(prefers-color-scheme: dark)');
  function apply(){
    var pref;
    try { pref = localStorage.getItem('theme') || 'system'; } catch(e) { pref = 'system'; }
    var dark = pref === 'dark' || (pref === 'system' && mq.matches);
    document.documentElement.classList.toggle('is-dark', dark);
  }
  apply();
  mq.addEventListener('change', apply);
  window.addEventListener('storage', function(e){ if (e.key === 'theme') apply(); });
})();
// Metadata-sidebar visibility bootstrap — same FOUC rationale as the
// theme bootstrap above. Without this, body paints with the 280px
// sidebar visible, then the inline IIFE further down runs and snaps
// it away on reloads where the user previously hid it. Setting the
// class on <html> before any styles apply paints the right layout
// on the first frame.
//
// On phone-sized viewports the panel becomes a fixed overlay (see the
// @media block in the stylesheet) — defaulting to hidden there keeps
// first-time mobile visitors from landing on a screen the slide-in
// panel has fully covered.
(function(){
  try {
    var pref = localStorage.getItem('hetchy.metaHidden');
    var mobile = window.matchMedia('(max-width: 768px)').matches;
    if (pref === '1' || (pref === null && mobile)) {
      document.documentElement.classList.add('meta-hidden');
    }
  } catch (e) {}
})();
