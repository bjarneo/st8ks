import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { flushSync } from "react-dom";
import { call, errText } from "../lib/bridge";
import { ask, fail, protectedWord, ui } from "../lib/app";
import { plural, rel } from "../lib/format";
import { shallowEqual, useStore } from "../lib/store";
import { ensureLoaded, useTable } from "../lib/tables";
import { CodeView } from "./CodeView";
import { DiffView, diffRows, diffStat } from "./DiffView";
import { useNow, useScale } from "./VTable";

// Mirrors of the Go types in internal/kube/helm_install.go.
interface ChartRepo { name: string; url: string; type: string; charts: number; updated: number; err?: string }
interface ChartHit { ref: string; name: string; repo: string; version: string; app: string; desc: string; icon: string; deprecated?: boolean }
interface ChartSearch { hits: ChartHit[]; total: number; repos: number; warnings?: string[] }
interface ChartVersion { version: string; app?: string; created?: number; pre?: boolean }
interface ChartDep { name: string; version: string; repository: string; condition?: string; alias?: string; bundled: boolean }
interface ChartDetail {
  ref: string; name: string; version: string; app: string; desc: string; type: string; apiVersion: string; kubeVersion: string;
  home: string; icon: string; sources: string[]; keywords: string[]; maintainers: string[]; deprecated: boolean;
  deps: ChartDep[]; values: string; readme: string;
}
export interface InstallResult {
  name: string; ns: string; chart: string; version: string; status: string; rev: number; notes: string;
  manifest?: string; resources: number; hooks: number; checks?: string[]; note?: string;
}

type Tab = "values" | "diff" | "readme" | "chart" | "manifest";

const RELEASE_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const NS_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const EXAMPLE_OCI = "oci://ghcr.io/stefanprodan/charts/podinfo";

/** splitRef moves the tag of an OCI reference into the version. */
function splitRef(s: string): { ref: string; version: string; name: string } {
  const t = s.trim();
  const m = /^(oci:\/\/.+\/([^/:@]+)):([^/:@]+)$/.exec(t);
  if (m) return { ref: m[1], version: m[3], name: m[2] };
  return { ref: t, version: "", name: t.split("/").pop() ?? t };
}

function openURL(u: string) {
  void call("OpenURL", u);
}

function Icon({ src, name }: { src: string; name: string }) {
  const [bad, setBad] = useState(false);
  if (!src.startsWith("https://") || bad) return <span className="hi-icon">{name.charAt(0).toUpperCase()}</span>;
  return <img className="hi-icon" src={src} alt="" loading="lazy" referrerPolicy="no-referrer" onError={() => setBad(true)} />;
}

function Check({ on, set, children, title }: { on: boolean; set: (v: boolean) => void; children: ReactNode; title?: string }) {
  return (
    <span className="flex noselect" style={{ cursor: "pointer", gap: 6 }} title={title} onClick={() => set(!on)}>
      <span className={"chk" + (on ? " on" : "")}>{on ? "✓" : ""}</span>{children}
    </span>
  );
}

/** VList renders only the visible rows of a list with a fixed row height. */
function VList<T>({ items, rowH, cursor, render }: { items: T[]; rowH: number; cursor: number; render: (it: T, i: number) => ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [view, setView] = useState({ top: 0, h: 600 });
  const raf = useRef(0);
  const onScroll = useCallback(() => {
    if (raf.current) return;
    raf.current = requestAnimationFrame(() => {
      raf.current = 0;
      const el = ref.current;
      if (el) flushSync(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    });
  }, []);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    const el = ref.current;
    if (!el || cursor < 0) return;
    const y = cursor * rowH;
    if (y < el.scrollTop) el.scrollTop = y;
    else if (y + rowH > el.scrollTop + el.clientHeight) el.scrollTop = y + rowH - el.clientHeight;
  }, [cursor, rowH]);
  const over = Math.max(6, Math.ceil(view.h / rowH));
  const start = Math.max(0, Math.floor(view.top / rowH) - over);
  const end = Math.min(items.length, Math.ceil((view.top + view.h) / rowH) + over);
  return (
    <div className="hi-list" ref={ref} onScroll={onScroll}>
      <div style={{ position: "relative", height: items.length * rowH }}>
        {items.slice(start, end).map((it, k) => (
          <div key={start + k} style={{ position: "absolute", left: 0, right: 0, top: (start + k) * rowH, height: rowH }}>{render(it, start + k)}</div>
        ))}
      </div>
    </div>
  );
}

// ---- Simple Markdown ----

const INLINE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let k = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (m[1]) out.push(<code key={k++}>{t.slice(1, -1)}</code>);
    else if (m[2]) out.push(<b key={k++}>{t.slice(2, -2)}</b>);
    else {
      const [, label, href] = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(t) ?? [];
      if (/^https?:\/\//.test(href ?? "")) {
        out.push(<a key={k++} href="#" title={href} onClick={(e) => { e.preventDefault(); openURL(href!); }}>{label}</a>);
      } else out.push(label);
    }
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Markdown renders headings, paragraphs, lists, quotes and code. It drops
 * HTML tags and images, and it shows tables as plain text. */
export function Markdown({ text }: { text: string }) {
  const blocks = useMemo(() => {
    const src = text.replace(/\r\n/g, "\n").replace(/<!--[\s\S]*?-->/g, "");
    const lines = src.split("\n");
    const out: ReactNode[] = [];
    let para: string[] = [];
    let list: string[] = [];
    let k = 0;
    const flush = () => {
      if (para.length) out.push(<p key={k++}>{inline(para.join(" "))}</p>);
      if (list.length) out.push(<ul key={k++}>{list.map((l, i) => <li key={i}>{inline(l)}</li>)}</ul>);
      para = [];
      list = [];
    };
    for (let i = 0; i < lines.length; i++) {
      const raw = lines[i];
      const fence = /^\s*(```|~~~)/.exec(raw);
      if (fence) {
        flush();
        const code: string[] = [];
        for (i++; i < lines.length && !lines[i].trim().startsWith(fence[1]); i++) code.push(lines[i]);
        out.push(<pre key={k++} className="pre">{code.join("\n")}</pre>);
        continue;
      }
      if (/^\s*\|/.test(raw)) {
        flush();
        const rows: string[] = [];
        for (; i < lines.length && /^\s*\|/.test(lines[i]); i++) rows.push(lines[i].trim());
        i--;
        out.push(<pre key={k++} className="pre">{rows.filter((r) => !/^\|[\s:|-]+\|?$/.test(r)).join("\n")}</pre>);
        continue;
      }
      const line = raw.replace(/!\[[^\]]*\]\([^)]*\)/g, "").replace(/<[^>]+>/g, "").replace(/\[\]\([^)]*\)/g, "");
      if (!line.trim()) {
        flush();
        continue;
      }
      const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
      if (h) {
        flush();
        const n = Math.min(h[1].length, 4);
        out.push(<div key={k++} className={"md-h md-h" + n}>{inline(h[2])}</div>);
        continue;
      }
      const li = /^\s*(?:[-*+]|\d+[.)])\s+(.*)$/.exec(line);
      if (li) {
        if (para.length) {
          const p = para;
          para = [];
          out.push(<p key={k++}>{inline(p.join(" "))}</p>);
        }
        list.push(li[1]);
        continue;
      }
      const q = /^\s*>\s?(.*)$/.exec(line);
      if (q) {
        flush();
        out.push(<blockquote key={k++}>{inline(q[1])}</blockquote>);
        continue;
      }
      if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
        flush();
        out.push(<hr key={k++} />);
        continue;
      }
      if (list.length && /^\s{2,}\S/.test(raw)) {
        list[list.length - 1] += " " + line.trim();
        continue;
      }
      if (list.length) flush();
      para.push(line.trim());
    }
    flush();
    return out;
  }, [text]);
  return <div className="md">{blocks}</div>;
}

// ---- Repositories ----

function Repos({ repos, reload }: { repos: ChartRepo[] | null; reload: () => void }) {
  const now = useNow();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [auth, setAuth] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null);
  const run = (id: string, p: Promise<string>, after?: () => void) => {
    setBusy(id);
    setRes(null);
    p.then((msg) => {
      setRes({ ok: true, text: msg });
      after?.();
    }, (e) => setRes({ ok: false, text: errText(e) })).finally(() => {
      setBusy(null);
      reload();
    });
  };
  const add = () => run("add", call<string>("HelmRepoAdd", name, url, auth ? user : "", auth ? pass : ""), () => {
    setName("");
    setUrl("");
    setUser("");
    setPass("");
  });
  return (
    <div className="col grow" style={{ minHeight: 0 }}>
      <div className="flex" style={{ padding: "0 14px 10px" }}>
        <span className="small fa grow">The same list as helm repo list.</span>
        <button className="btn xs" disabled={!!busy || !repos?.length} onClick={() => run("all", call<string>("HelmRepoUpdate", ""))}>
          {busy === "all" && <span className="spinner" />}Update all
        </button>
      </div>
      {res && <div className={res.ok ? "okbox" : "errbox"} style={{ margin: "0 14px 10px" }}>{res.text}</div>}
      <div className="grow" style={{ overflowY: "auto", borderTop: "1px solid var(--ln)", minHeight: 0 }}>
        {repos === null && <div className="empty flex"><span className="spinner" />Loading repositories…</div>}
        {repos?.length === 0 && <div className="empty">There are no chart repositories. Add one below.</div>}
        {repos?.map((r) => (
          <div key={r.name} className="repo-row">
            <div className="flex">
              <span className="ell grow" style={{ fontWeight: 500 }}>{r.name}</span>
              <button className="btn xs" disabled={!!busy} onClick={() => run(r.name, call<string>("HelmRepoUpdate", r.name))}>
                {busy === r.name && <span className="spinner" />}Update
              </button>
              <button className="btn xs danger-text" disabled={!!busy} onClick={() => ask({
                title: `Remove repository ${r.name}`, danger: true, cta: "Remove", noDry: true, items: [`${r.name} · ${r.url}`],
                note: "Removes the repository from the Helm repository list and deletes its cached index. Installed releases do not change.",
                run: () => call<string>("HelmRepoRemove", r.name), after: reload,
              })}>Remove</button>
            </div>
            <span className="mono small mu ell" title={r.url}>{r.url}</span>
            {r.err ? <span className="small er">{r.err}</span> : (
              <span className="small fa">{plural(r.charts, "chart")} · {r.type} · updated {rel(Math.min(r.updated, now), now)} ago</span>
            )}
          </div>
        ))}
      </div>
      <div className="col" style={{ gap: 8, padding: "12px 14px", borderTop: "1px solid var(--ln)" }}>
        <span className="sec-label">Add a repository</span>
        <input className="input mono" placeholder="Name, for example podinfo" value={name} spellCheck={false} onChange={(e) => setName(e.target.value)} />
        <input className="input mono" placeholder="https://stefanprodan.github.io/podinfo" value={url} spellCheck={false} onChange={(e) => setUrl(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && name && url && !busy && add()} />
        <Check on={auth} set={setAuth}>The repository needs a login</Check>
        {auth && (
          <div className="flex">
            <input className="input mono grow" placeholder="User name" value={user} spellCheck={false} onChange={(e) => setUser(e.target.value)} />
            <input className="input mono grow" placeholder="Password" type="password" value={pass} onChange={(e) => setPass(e.target.value)} />
          </div>
        )}
        <div className="flex">
          <span className="small fa grow">st8ks downloads the index and writes repositories.yaml, like helm repo add.</span>
          <button className="btn sm primary" disabled={!!busy || !name.trim() || !url.trim()} onClick={add}>{busy === "add" && <span className="spinner" />}Add</button>
        </div>
      </div>
    </div>
  );
}

// ---- Install ----

export interface Props {
  start: "charts" | "repos";
  onClose: () => void;
  onInstalled: (r: InstallResult) => void;
}

export default function HelmInstall({ start, onClose, onInstalled }: Props) {
  const z = useScale();
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const ctxNs = useStore(ui, (s) => s.contexts.find((c) => c.name === s.cluster?.context)?.namespace ?? "");
  const nsTable = useTable("Namespaces");
  useEffect(() => void ensureLoaded("Namespaces"), []);
  const namespaces = useMemo(() => [...nsTable.rows.values()].map((r) => r.m).sort(), [nsTable.version]); // eslint-disable-line react-hooks/exhaustive-deps

  const [side, setSide] = useState(start);
  const [repos, setRepos] = useState<ChartRepo[] | null>(null);
  const loadRepos = useCallback(() => call<ChartRepo[]>("HelmRepoList").then(setRepos, fail), []);
  useEffect(() => void loadRepos(), [loadRepos]);

  // Search
  const [q, setQ] = useState("");
  const [devel, setDevel] = useState(false);
  const [found, setFound] = useState<ChartSearch | null>(null);
  const [searchErr, setSearchErr] = useState("");
  const [cursor, setCursor] = useState(-1);
  const seq = useRef(0);
  const isOci = q.trim().startsWith("oci://");
  useEffect(() => {
    if (isOci) return;
    const my = ++seq.current;
    const t = setTimeout(() => {
      call<ChartSearch>("HelmSearch", q, devel).then((r) => {
        if (my !== seq.current) return;
        setFound(r);
        setSearchErr("");
        setCursor(-1);
      }, (e) => my === seq.current && setSearchErr(errText(e)));
    }, found ? 120 : 0);
    return () => clearTimeout(t);
  }, [q, devel, isOci, repos]); // eslint-disable-line react-hooks/exhaustive-deps
  const hits = isOci ? [] : found?.hits ?? [];

  // Selected chart
  const [chart, setChart] = useState<{ ref: string; name: string } | null>(null);
  const [versions, setVersions] = useState<ChartVersion[] | null>(null);
  const [version, setVersion] = useState("");
  const [detail, setDetail] = useState<ChartDetail | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [values, setValues] = useState("");
  const [tab, setTab] = useState<Tab>("values");
  const [plain, setPlain] = useState(false);
  const [dry, setDry] = useState<InstallResult | null>(null);
  const [runErr, setRunErr] = useState("");
  const [busy, setBusy] = useState(false);
  const want = useRef("");

  // Form
  const [name, setName] = useState("");
  const [ns, setNsName] = useState(nsSel[0] || ctxNs || "default");
  const [createNs, setCreateNs] = useState(false);
  const [wait, setWait] = useState(false);
  const [atomic, setAtomic] = useState(false);
  const [timeout, setTimeoutSec] = useState("300");
  const nsExists = namespaces.includes(ns.trim());
  useEffect(() => {
    if (namespaces.length) setCreateNs(!namespaces.includes(ns.trim()));
  }, [ns, namespaces.length > 0]); // eslint-disable-line react-hooks/exhaustive-deps

  const pick = (ref: string) => {
    const s = splitRef(ref);
    setChart({ ref: s.ref, name: s.name });
    setName(s.name.toLowerCase());
    setVersions(null);
    setVersion(s.version);
    setDetail(null);
    setValues("");
    setDry(null);
    setRunErr("");
    setLoadErr("");
    setTab("values");
  };

  // Load the versions when the chart or the prerelease switch changes.
  useEffect(() => {
    if (!chart) return;
    const key = chart.ref + "|" + devel;
    want.current = key;
    call<ChartVersion[]>("HelmChartVersions", chart.ref, devel).then((vs) => {
      if (want.current !== key) return;
      setVersions(vs);
      setVersion((cur) => (cur && (vs.some((v) => v.version === cur) || !vs.length) ? cur : vs[0]?.version ?? ""));
    }, (e) => {
      if (want.current !== key) return;
      setVersions([]);
      setLoadErr(errText(e));
    });
  }, [chart, devel]);

  // Load the chart detail when the version changes. Edited values stay.
  // Values that nobody changed take the defaults of the new version.
  const detailKey = useRef("");
  const lastDefaults = useRef("");
  useEffect(() => {
    if (!chart || !version) return;
    const key = chart.ref + "@" + version;
    detailKey.current = key;
    setDetail(null);
    setDry(null);
    setLoadErr("");
    call<ChartDetail>("HelmChartDetail", chart.ref, version).then((d) => {
      if (detailKey.current !== key) return;
      const prev = lastDefaults.current;
      lastDefaults.current = d.values;
      setValues((v) => (v === "" || v === prev ? d.values : v));
      setDetail(d);
    }, (e) => detailKey.current === key && setLoadErr(errText(e)));
  }, [chart, version]);

  const defaults = detail?.values ?? "";
  const changed = detail !== null && values !== defaults;
  const diff = useMemo(() => (tab === "diff" && detail ? diffRows(defaults, values) : []), [tab, detail, defaults, values]);
  const stat = useMemo(() => (changed ? diffStat(diffRows(defaults, values)) : ""), [changed, defaults, values]);

  const nameOk = RELEASE_RE.test(name) && name.length <= 53;
  const nsOk = NS_RE.test(ns.trim()) && ns.trim().length <= 63;
  const secs = Math.max(10, Math.min(3600, parseInt(timeout, 10) || 300));
  const ready = !!detail && nameOk && nsOk && !busy;
  const spec = (dryRun: boolean) => ({
    ref: chart?.ref ?? "", version: detail?.version ?? version, name, ns: ns.trim(), createNs, values,
    wait: wait || atomic, timeout: secs, atomic, dry: dryRun,
  });

  const dryRun = async () => {
    setBusy(true);
    setRunErr("");
    setDry(null);
    try {
      const r = await call<InstallResult>("HelmInstall", spec(true));
      setDry(r);
      setTab("manifest");
    } catch (e) {
      setRunErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const install = () => {
    if (!detail) return;
    const s = spec(false);
    let out: InstallResult | null = null;
    const notes = [
      `Installs ${detail.name} ${detail.version} as the release ${s.name} in the namespace ${s.ns}.`,
      createNs && !nsExists ? "Helm creates the namespace." : "",
      s.wait ? `Helm waits up to ${secs} seconds for the resources to become ready.` : "",
      atomic ? "If the install fails, Helm uninstalls the release." : "",
    ];
    ask({
      title: `Install ${s.name}`, cta: "Install", noDry: true,
      items: [`helm release ${s.ns}/${s.name}`, `chart ${detail.ref} ${detail.version}`],
      note: notes.filter(Boolean).join(" "),
      word: protectedWord([s.ns], s.name),
      run: async () => {
        out = await call<InstallResult>("HelmInstall", s);
        return `Installed ${out.name} · ${out.status}`;
      },
      after: () => out && onInstalled(out),
    });
  };

  const rowH = Math.round(50 * z);
  const onKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setCursor((c) => Math.min(hits.length - 1, c + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setCursor((c) => Math.max(0, c - 1));
    } else if (e.key === "Enter") {
      if (isOci) pick(q);
      else if (hits[cursor] ?? hits[0]) pick((hits[cursor] ?? hits[0]).ref);
    } else if (e.key === "Escape" && q) {
      e.stopPropagation();
      setQ("");
    }
  };

  const tabs: [Tab, string][] = [["values", "Values"], ["diff", changed ? `Changes ${stat}` : "Changes"], ["readme", "README"], ["chart", "Chart"]];
  if (dry) tabs.push(["manifest", `Manifest · ${dry.resources}`]);

  return (
    <div className="hi">
      <div className="hi-left">
        <div className="flex" style={{ padding: "14px 14px 10px" }}>
          <div className="seg">
            <button className={side === "charts" ? "on" : ""} onClick={() => setSide("charts")}>Charts</button>
            <button className={side === "repos" ? "on" : ""} onClick={() => setSide("repos")}>Repositories{repos ? ` · ${repos.length}` : ""}</button>
          </div>
          <span style={{ flex: 1 }} />
          <button className="btn sm ghost fa" onClick={onClose}>Close</button>
        </div>
        {side === "repos" && <Repos repos={repos} reload={() => void loadRepos()} />}
        {side === "charts" && (
          <>
            <div className="col" style={{ gap: 8, padding: "0 14px 10px" }}>
              <div className="filterbox" style={{ width: "100%" }}>
                <span className="fa">⌕</span>
                <input autoFocus value={q} placeholder="Search charts, or paste oci://…" spellCheck={false}
                  onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} />
              </div>
              <div className="flex small mu">
                <Check on={devel} set={setDevel} title="Show alpha, beta and release candidate versions">Prereleases</Check>
                <span style={{ flex: 1 }} />
                {!isOci && found && <span className="fa">{found.total > found.hits.length ? `${found.hits.length} of ${found.total}` : plural(found.total, "chart")}</span>}
              </div>
              {found?.warnings?.map((w) => <div key={w} className="warnbox small">{w}</div>)}
              {searchErr && <div className="errbox">{searchErr}</div>}
            </div>
            {isOci ? (
              <div style={{ borderTop: "1px solid var(--ln)" }}>
                <div className={"hi-hit" + (chart?.ref === splitRef(q).ref ? " on" : "")} style={{ position: "static", height: rowH }} onClick={() => pick(q)}>
                  <span className="hi-icon">⎈</span>
                  <div className="col grow" style={{ gap: 2 }}>
                    <span className="ell mono small" style={{ color: "var(--tx)" }}>{q.trim()}</span>
                    <span className="small fa">OCI registry · press Enter to load</span>
                  </div>
                </div>
              </div>
            ) : found && found.repos === 0 ? (
              <div className="empty" style={{ borderTop: "1px solid var(--ln)", lineHeight: 1.5 }}>
                There are no chart repositories. <a href="#" onClick={(e) => { e.preventDefault(); setSide("repos"); }}>Add a repository</a>, or paste an OCI reference such as <span className="mono small">{EXAMPLE_OCI}</span>.
              </div>
            ) : found && !hits.length ? (
              <div className="empty" style={{ borderTop: "1px solid var(--ln)" }}>No charts match “{q.trim()}”.</div>
            ) : (
              <div className="col grow" style={{ minHeight: 0, borderTop: "1px solid var(--ln)" }}>
                {!found && !searchErr && <div className="empty flex"><span className="spinner" />Loading the indexes…</div>}
                <VList items={hits} rowH={rowH} cursor={cursor} render={(h, i) => (
                  <div className={"hi-hit" + (chart?.ref === h.ref ? " on" : "") + (i === cursor ? " cur" : "")} style={{ height: rowH }} onClick={() => pick(h.ref)}>
                    <Icon src={h.icon} name={h.name} />
                    <div className="col grow" style={{ gap: 2 }}>
                      <div className="flex" style={{ gap: 6 }}>
                        <span className="ell" style={{ fontWeight: 500 }}>{h.name}</span>
                        <span className="mono small fa ell">{h.repo}</span>
                        <span style={{ flex: 1 }} />
                        <span className="mono small mu">{h.version}</span>
                      </div>
                      <span className="small fa ell" title={h.desc}>{h.deprecated ? "Deprecated · " : ""}{h.desc || "No description"}</span>
                    </div>
                  </div>
                )} />
              </div>
            )}
          </>
        )}
      </div>

      <div className="hi-main">
        {!chart && (
          <div className="center">
            <div className="col mu" style={{ gap: 8, maxWidth: 440, lineHeight: 1.5 }}>
              <span style={{ fontSize: "calc(15px * var(--zf))", fontWeight: 600, color: "var(--tx)" }}>Install a Helm chart</span>
              <span>Search the charts of your repositories and select one. To install from an OCI registry, paste its reference, for example <span className="mono small">{EXAMPLE_OCI}</span>.</span>
              <span>Then set the release name, the namespace and the values. Click Dry run to see the manifest before you install.</span>
            </div>
          </div>
        )}
        {chart && (
          <>
            <div className="view-head" style={{ alignItems: "flex-start" }}>
              <Icon src={detail?.icon ?? ""} name={chart.name} />
              <div className="col grow" style={{ gap: 4 }}>
                <div className="flex">
                  <h1 className="ell">{detail?.name ?? chart.name}</h1>
                  {detail && <span className="meta-mono">app {detail.app || "—"} · {detail.type}</span>}
                </div>
                <span className="mono small fa ell">{chart.ref}</span>
                {detail?.desc && <span className="small mu" style={{ lineHeight: 1.45 }}>{detail.desc}</span>}
              </div>
              <label className="col" style={{ gap: 4 }}>
                <span className="small fa">Version</span>
                <select className="input mono" style={{ minWidth: 150 }} value={version} disabled={!versions?.length} onChange={(e) => setVersion(e.target.value)}>
                  {!versions?.length && <option value={version}>{version || "…"}</option>}
                  {versions?.map((v) => <option key={v.version} value={v.version}>{v.version}{v.app ? ` · app ${v.app}` : ""}{v.pre ? " · prerelease" : ""}</option>)}
                </select>
              </label>
            </div>
            {detail?.deprecated && <div className="warnbox" style={{ margin: "0 18px 10px" }}>The maintainers marked this chart as deprecated.</div>}
            {loadErr && <div className="errbox" style={{ margin: "0 18px 10px" }}>{loadErr}</div>}
            <div className="hi-form">
              <label>Release name
                <input className="input mono" style={{ width: 200, borderColor: nameOk ? undefined : "var(--er)" }} value={name} spellCheck={false} onChange={(e) => setName(e.target.value)} />
              </label>
              <label>Namespace
                <input className="input mono" style={{ width: 200, borderColor: nsOk ? undefined : "var(--er)" }} list="hi-ns" value={ns} spellCheck={false} onChange={(e) => setNsName(e.target.value)} />
                <datalist id="hi-ns">{namespaces.map((n) => <option key={n} value={n} />)}</datalist>
              </label>
              <label>Timeout in seconds
                <input className="input mono" style={{ width: 110 }} type="number" min={10} max={3600} value={timeout} disabled={!wait && !atomic} onChange={(e) => setTimeoutSec(e.target.value)} />
              </label>
              <div className="col" style={{ gap: 6, fontSize: "calc(12px * var(--zf))" }}>
                <Check on={createNs} set={setCreateNs} title="helm install --create-namespace">Create namespace{namespaces.length && !nsExists ? " · new" : ""}</Check>
                <div className="flex" style={{ gap: 14 }}>
                  <Check on={wait || atomic} set={(v) => { setWait(v); if (!v) setAtomic(false); }} title="helm install --wait">Wait for ready</Check>
                  <Check on={atomic} set={setAtomic} title="helm install --atomic. If the install fails, Helm uninstalls the release.">Atomic</Check>
                </div>
              </div>
            </div>
            {!nameOk && name && <div className="small er" style={{ padding: "0 18px 8px" }}>Use 53 or fewer lowercase letters, digits and dashes in the release name.</div>}
            <div className="tabs" style={{ padding: "0 18px", borderBottom: "1px solid var(--ln)" }}>
              {tabs.map(([t, l]) => <button key={t} className={"tab" + (tab === t ? " on" : "")} onClick={() => setTab(t)}>{l}</button>)}
              {tab === "values" && changed && (
                <button className="btn xs ghost fa" style={{ marginLeft: "auto", alignSelf: "center" }} onClick={() => setValues(defaults)}>Reset to the defaults</button>
              )}
              {tab === "readme" && detail?.readme && (
                <button className="btn xs ghost fa" style={{ marginLeft: "auto", alignSelf: "center" }} onClick={() => setPlain(!plain)}>{plain ? "Formatted" : "Plain text"}</button>
              )}
            </div>
            <div className="hi-body">
              {!detail && !loadErr && <div className="empty flex"><span className="spinner" />Loading {chart.name} {version}…</div>}
              {detail && (
                <>
                  <div style={{ display: tab === "values" ? "flex" : "none", flex: 1, minHeight: 0 }}>
                    <CodeView value={values} readOnly={false} onChange={setValues} />
                  </div>
                  {tab === "diff" && (
                    <div className="grow" style={{ overflow: "auto" }}>
                      {changed ? <DiffView rows={diff} /> : <div className="empty">The values are the same as the chart defaults.</div>}
                    </div>
                  )}
                  {tab === "readme" && (
                    <div className="grow" style={{ overflow: "auto" }}>
                      {!detail.readme ? <div className="empty">The chart has no README.</div>
                        : plain ? <pre className="pre" style={{ margin: 16 }}>{detail.readme}</pre> : <Markdown text={detail.readme} />}
                    </div>
                  )}
                  {tab === "chart" && <ChartFacts d={detail} />}
                  {tab === "manifest" && dry && (
                    <div className="col grow" style={{ minHeight: 0 }}>
                      <div className="col" style={{ gap: 8, padding: "10px 18px", borderBottom: "1px solid var(--ln)" }}>
                        {dry.checks?.length ? (
                          <div className="errbox">The API server refused {plural(dry.checks.length, "object")}:{"\n"}{dry.checks.join("\n")}</div>
                        ) : (
                          <div className="okbox">✓ The dry run rendered {plural(dry.resources, "resource")} and {plural(dry.hooks, "hook")}.{dry.note ? "" : " The API server accepted every object."}</div>
                        )}
                        {dry.note && <div className="warnbox small">{dry.note}</div>}
                      </div>
                      <div style={{ display: "flex", flex: 1, minHeight: 0 }}>
                        <CodeView value={dry.manifest ?? ""} readOnly />
                      </div>
                    </div>
                  )}
                </>
              )}
            </div>
            <div className="hi-foot">
              {runErr ? <div className="errbox grow" style={{ maxHeight: 120, overflowY: "auto" }}>{runErr}</div>
                : <span className="small fa grow">{changed ? "st8ks sends only the values that differ from the chart defaults. To remove a default, set it to null." : "The chart defaults apply."}</span>}
              <button className="btn" disabled={!ready} onClick={() => void dryRun()}>{busy && <span className="spinner" />}Dry run</button>
              <button className="btn primary" disabled={!ready} onClick={install}>Install</button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function ChartFacts({ d }: { d: ChartDetail }) {
  const link = (u: string) => (/^https?:\/\//.test(u) ? <a href="#" onClick={(e) => { e.preventDefault(); openURL(u); }}>{u}</a> : <span>{u}</span>);
  return (
    <div className="grow" style={{ overflow: "auto" }}>
      <div className="dpad">
        <div className="kv wide">
          <span>Chart</span><span>{d.name} {d.version}</span>
          <span>App version</span><span>{d.app || "—"}</span>
          <span>Type</span><span>{d.type}</span>
          <span>API version</span><span>{d.apiVersion || "—"}</span>
          <span>Kubernetes</span><span>{d.kubeVersion || "any version"}</span>
          <span>Home</span><span>{d.home ? link(d.home) : "—"}</span>
          <span>Sources</span><span className="col" style={{ gap: 2 }}>{d.sources.length ? d.sources.map((s) => <span key={s}>{link(s)}</span>) : "—"}</span>
          <span>Keywords</span><span>{d.keywords.join(", ") || "—"}</span>
          <span>Maintainers</span><span className="col" style={{ gap: 2 }}>{d.maintainers.length ? d.maintainers.map((m) => <span key={m}>{m}</span>) : "—"}</span>
        </div>
        <div className="col" style={{ gap: 8 }}>
          <span className="sec-label">Dependencies</span>
          {!d.deps.length && <span className="mu">The chart has no dependencies.</span>}
          {d.deps.length > 0 && (
            <div className="nodes-t">
              <div className="hd" style={{ gridTemplateColumns: "minmax(120px,1fr) 100px minmax(160px,2fr) minmax(100px,1fr) 80px", minWidth: 600 }}>
                <span>Name</span><span>Version</span><span>Repository</span><span>Condition</span><span>Bundled</span>
              </div>
              {d.deps.map((x) => (
                <div key={x.alias || x.name} className="rw" style={{ gridTemplateColumns: "minmax(120px,1fr) 100px minmax(160px,2fr) minmax(100px,1fr) 80px", minWidth: 600, cursor: "default" }}>
                  <span className="ell">{x.name}{x.alias ? ` as ${x.alias}` : ""}</span>
                  <span className="mono small mu ell">{x.version}</span>
                  <span className="mono small mu ell" title={x.repository}>{x.repository || "—"}</span>
                  <span className="mono small mu ell">{x.condition || "—"}</span>
                  <span className={x.bundled ? "ok" : "er"}>{x.bundled ? "✓ yes" : "missing"}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
