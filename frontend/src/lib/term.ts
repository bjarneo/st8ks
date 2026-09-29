// Colors for xterm.js from the CSS theme tokens.

const probe = document.createElement("canvas").getContext("2d");

/** cssColor resolves a CSS variable to an rgb() color that xterm.js reads. */
export function cssColor(v: string, fallback: string): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(v).trim() || fallback;
  if (!probe) return fallback;
  probe.clearRect(0, 0, 1, 1);
  probe.fillStyle = fallback;
  probe.fillStyle = raw;
  probe.fillRect(0, 0, 1, 1);
  const [r, g, b, a] = probe.getImageData(0, 0, 1, 1).data;
  return a < 255 ? `rgba(${r},${g},${b},${(a / 255).toFixed(2)})` : `rgb(${r},${g},${b})`;
}

export function termTheme() {
  return {
    background: cssColor("--bg", "#111214"),
    foreground: cssColor("--tx", "#e6e7e9"),
    cursor: cssColor("--ac", "#6ea8fe"),
    cursorAccent: cssColor("--bg", "#111214"),
    selectionBackground: cssColor("--acw", "rgba(110,168,254,0.3)"),
  };
}
