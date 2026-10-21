// The preference belongs to this GUI, across profiles and restarts. CSS resolves
// "system" so changes to the OS appearance take effect without a reload.
export function createThemePreference(doc = document) {
  const key = 'ouroboros.gui.theme';
  const valid = value => ['system', 'light', 'dark'].includes(value);
  let preference = 'system';
  try {
    const saved = doc.defaultView?.localStorage?.getItem(key);
    if (valid(saved)) preference = saved;
  } catch { /* Storage can be unavailable; appearance still works in this window. */ }
  const apply = () => doc.documentElement.setAttribute('data-theme', preference);
  apply();
  return {
    get value() { return preference; },
    set(value) {
      if (!valid(value)) return;
      preference = value;
      apply();
      try { doc.defaultView?.localStorage?.setItem(key, preference); }
      catch { /* Keep the in-memory preference when storage is unavailable. */ }
    },
  };
}
