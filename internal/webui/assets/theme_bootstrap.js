// Theme bootstrap. Loaded before CSS so the page paints in the chosen
// theme on first frame.
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
