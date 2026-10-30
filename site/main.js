"use strict";

const REPO = "bjarneo/st8ks";
const DL = `https://github.com/${REPO}/releases/latest/download/`;

// Download buttons point to the asset for the platform of the visitor.
// Without a match they keep their link to the file table.
function platform() {
  const ua = navigator.userAgent;
  const p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "";
  if (/Android|iPhone|iPad|iPod/i.test(ua)) return null;
  if (/Mac/i.test(p) || /Macintosh/i.test(ua)) return "mac";
  if (/Win/i.test(p) || /Windows/i.test(ua)) return "win";
  if (/Linux|X11/i.test(p) || /Linux/i.test(ua)) return "linux";
  return null;
}

function asset(os, arm) {
  if (os === "mac") return { file: "st8ks-darwin-universal.dmg", label: "Download for macOS" };
  if (os === "win") return { file: `st8ks-windows-${arm ? "arm64" : "amd64"}-setup.exe`, label: "Download for Windows" };
  if (os === "linux") return { file: `st8ks-linux-${arm ? "arm64" : "amd64"}.tar.gz`, label: "Download for Linux" };
  return null;
}

function setDownloads(os, arm) {
  const a = asset(os, arm);
  if (!a) return;
  for (const el of document.querySelectorAll("[data-dl]")) {
    el.href = DL + a.file;
    el.title = a.file;
    const label = el.querySelector("[data-dl-label]");
    if (label) label.textContent = a.label;
  }
  if (os === "linux" && arm) {
    const pre = document.querySelector("#linux-cmd code");
    if (pre) pre.textContent = pre.textContent.replaceAll("amd64", "arm64");
  }
}

const os = platform();
setDownloads(os, false);
if (os && navigator.userAgentData && navigator.userAgentData.getHighEntropyValues) {
  navigator.userAgentData.getHighEntropyValues(["architecture"])
    .then((v) => { if (v.architecture === "arm") setDownloads(os, true); })
    .catch(() => {});
}

// The version of the latest release. The page reads "Latest release" when the API does not answer.
fetch(`https://api.github.com/repos/${REPO}/releases/latest`, { headers: { Accept: "application/vnd.github+json" } })
  .then((r) => (r.ok ? r.json() : null))
  .then((rel) => {
    if (!rel || !rel.tag_name) return;
    for (const el of document.querySelectorAll("[data-version]")) el.textContent = `Release ${rel.tag_name}`;
  })
  .catch(() => {});

// The hero window: Live, Edit and Diff, and the server dry run.
const code = document.getElementById("code");
const dry = document.getElementById("dry");
const dryBtn = document.getElementById("dryrun");
const tabs = document.querySelectorAll('.seg [role="tab"]');

function setView(view) {
  code.dataset.mode = view;
  for (const t of tabs) t.setAttribute("aria-selected", String(t.dataset.view === view));
  dry.classList.toggle("hide", view === "live");
}

for (const t of tabs) t.addEventListener("click", () => setView(t.dataset.view));

let busy = false;
dryBtn.addEventListener("click", () => {
  if (busy) return;
  busy = true;
  if (code.dataset.mode === "live") setView("diff");
  dry.classList.remove("reveal");
  dry.classList.add("hide");
  dryBtn.classList.add("busy");
  dryBtn.disabled = true;
  setTimeout(() => {
    dryBtn.classList.remove("busy");
    dryBtn.disabled = false;
    dry.classList.remove("hide");
    void dry.offsetWidth;
    dry.classList.add("reveal");
    busy = false;
  }, 750);
});

// The tool list works as tabs. Arrow keys move between them.
const tree = document.querySelector(".tree");
const treeTabs = [...tree.querySelectorAll('[role="tab"]')];

function selectTool(tab, focus) {
  for (const t of treeTabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  }
  if (focus) tab.focus();
  // The screenshot of the new panel measures its scroll width only when it shows.
  dispatchEvent(new Event("resize"));
}

for (const t of treeTabs) t.addEventListener("click", () => selectTool(t, false));
tree.addEventListener("keydown", (e) => {
  const i = treeTabs.indexOf(document.activeElement);
  if (i < 0) return;
  const step = { ArrowDown: 1, ArrowRight: 1, ArrowUp: -1, ArrowLeft: -1 }[e.key];
  if (e.key === "Home") { e.preventDefault(); selectTool(treeTabs[0], true); }
  else if (e.key === "End") { e.preventDefault(); selectTool(treeTabs[treeTabs.length - 1], true); }
  else if (step) { e.preventDefault(); selectTool(treeTabs[(i + step + treeTabs.length) % treeTabs.length], true); }
});
selectTool(treeTabs[0], false);

// The scenes of the video: a click plays from the scene, and the scene that plays shows as current.
const film = document.getElementById("film");
const scenes = [...document.querySelectorAll(".chapters button")];
// The video loads only on play, and a seek before its metadata loads has no effect.
function seek(t) {
  if (film.readyState >= 1) film.currentTime = t;
  else film.addEventListener("loadedmetadata", () => { film.currentTime = t; }, { once: true });
}
for (const b of scenes) {
  b.addEventListener("click", () => {
    seek(Number(b.dataset.t));
    film.play().catch(() => {});
  });
}
film.addEventListener("timeupdate", () => {
  let cur = null;
  for (const b of scenes) if (Number(b.dataset.t) <= film.currentTime + 0.25) cur = b;
  for (const b of scenes) {
    if (b === cur) b.setAttribute("aria-current", "true");
    else b.removeAttribute("aria-current");
  }
});

// Copy buttons
for (const b of document.querySelectorAll("[data-copy]")) {
  b.addEventListener("click", async () => {
    const text = document.getElementById(b.dataset.copy).textContent;
    const label = b.querySelector("span");
    try {
      await navigator.clipboard.writeText(text);
      label.textContent = "Copied";
      b.classList.add("done");
    } catch {
      label.textContent = "Select the text to copy";
    }
    setTimeout(() => { label.textContent = "Copy"; b.classList.remove("done"); }, 1800);
  });
}

// On narrow screens a screenshot scrolls sideways. The frame fades at the right edge until the end shows.
for (const fig of document.querySelectorAll(".shot")) {
  const update = () => {
    const scrolls = fig.scrollWidth > fig.clientWidth + 2;
    fig.classList.toggle("more", scrolls && fig.scrollLeft + fig.clientWidth < fig.scrollWidth - 2);
    if (scrolls) fig.tabIndex = 0;
    else fig.removeAttribute("tabindex");
  };
  fig.addEventListener("scroll", update, { passive: true });
  fig.querySelector("img").addEventListener("load", update);
  addEventListener("resize", update);
  update();
}
