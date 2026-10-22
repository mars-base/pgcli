package tune

import (
	"encoding/json"
	"fmt"
	"strings"
)

// webTemplate is the self-contained --web-tune site: a form over the tune
// inputs, a JS re-implementation of Recommend() that recomputes live, and a
// load-time self-check against Go-computed golden fixtures (a mismatch shows a
// red banner instead of silently drifting). Everything is inline — no external
// requests. Placeholders __FIXTURES__ / __PREFILL__ are substituted by WebHTML.
const webTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>pg tune — interactive PostgreSQL tuning</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 2rem 1.5rem; background: #0d1117; color: #e6edf3;
         font: 15px/1.5 -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
  .wrap { max-width: 960px; margin: 0 auto; }
  h1 { font-size: 1.4rem; margin: 0 0 .3rem; }
  .sub { color: #8b949e; margin: 0 0 1.5rem; font-size: .9rem; }
  .cols { display: flex; gap: 2rem; flex-wrap: wrap; }
  .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 1.2rem; }
  form { min-width: 280px; }
  label { display: block; margin: .8rem 0 .2rem; color: #8b949e; font-size: .82rem;
          text-transform: uppercase; letter-spacing: .03em; }
  select, input { width: 100%; padding: .5rem .6rem; background: #0d1117; color: #e6edf3;
                  border: 1px solid #30363d; border-radius: 6px; font: inherit; }
  .out { flex: 1; min-width: 320px; }
  table { width: 100%; border-collapse: collapse; font-size: .9rem; }
  th, td { text-align: left; padding: .4rem .5rem; border-bottom: 1px solid #21262d; vertical-align: top; }
  th { color: #8b949e; font-weight: 600; font-size: .78rem; text-transform: uppercase; }
  td.v { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; color: #56d364; white-space: nowrap; }
  td.n { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  td.r { color: #8b949e; }
  .warn { color: #e3b341; }
  pre { background: #0d1117; border: 1px solid #30363d; border-radius: 6px; padding: .8rem;
        overflow-x: auto; font-size: .82rem; }
  .banner { display: none; background: #4d1f1f; border: 1px solid #f85149; color: #ffbbaa;
            padding: .7rem .9rem; border-radius: 6px; margin-bottom: 1rem; }
  .tabs { display: flex; gap: .4rem; margin: 0 0 .8rem; }
  .tab { padding: .35rem .8rem; border: 1px solid #30363d; border-bottom: none; border-radius: 6px 6px 0 0;
         background: #0d1117; color: #8b949e; cursor: pointer; font: inherit; font-size: .85rem; }
  .tab.active { background: #161b22; color: #e6edf3; }
  button.copy { margin-top: .6rem; padding: .4rem .9rem; background: #238636; color: #fff;
                border: none; border-radius: 6px; cursor: pointer; font: inherit; font-size: .85rem; }
  h2 { font-size: .95rem; margin: 1.6rem 0 .4rem; }
</style>
</head>
<body>
<div class="wrap">
  <h1>pg tune</h1>
  <p class="sub">Generate optimized <code>postgresql.conf</code> settings based on your hardware and workload.
    Adjust the parameters on the left — the recommended configuration updates instantly.
    Copy the output as a parameter table, <code>postgresql.conf</code> snippet, or <code>ALTER SYSTEM</code> statements.
    No data leaves your browser; everything runs client-side.</p>
  <div class="banner" id="banner"></div>
  <div class="cols">
    <div class="card">
      <form id="f">
        <label>PostgreSQL version</label>
        <select id="version"></select>
        <label>OS type</label>
        <select id="os">
          <option value="linux">linux</option>
          <option value="windows">windows</option>
          <option value="mac">mac</option>
        </select>
        <label>Workload type</label>
        <select id="type">
          <option value="web">web</option>
          <option value="oltp">oltp</option>
          <option value="dw">dw (data warehouse)</option>
          <option value="desktop">desktop</option>
          <option value="mixed">mixed</option>
        </select>
        <label>Total memory</label>
        <div style="display:flex;gap:.5rem">
          <input id="ram" type="number" min="1" step="1">
          <select id="ramunit" style="width:5.5rem">
            <option value="MB">MB</option>
            <option value="GB">GB</option>
            <option value="TB">TB</option>
          </select>
        </div>
        <label>CPUs</label>
        <input id="cpus" type="number" min="1" step="1">
        <label>Connections (0 = workload preset)</label>
        <input id="conns" type="number" min="0" step="1">
        <label>Data storage</label>
        <select id="hd">
          <option value="ssd">ssd</option>
          <option value="hdd">hdd</option>
          <option value="san">san</option>
          <option value="nvme">nvme</option>
        </select>
        <label>Database size vs RAM</label>
        <select id="size">
          <option value="less_ram">fits in RAM</option>
          <option value="mid_ram">a few × RAM</option>
          <option value="greater_ram">far larger than RAM</option>
        </select>
      </form>
    </div>
    <div class="out">
      <div class="tabs">
        <button class="tab active" data-tab="table" type="button">parameters</button>
        <button class="tab" data-tab="conf" type="button">postgresql.conf</button>
        <button class="tab" data-tab="alter" type="button">ALTER SYSTEM</button>
      </div>
      <div class="card">
        <div id="panel-table">
          <table><thead><tr><th>Parameter</th><th>Value</th><th>Why</th></tr></thead>
          <tbody id="rows"></tbody></table>
        </div>
        <div id="panel-conf" style="display:none"><pre id="conf"></pre></div>
        <div id="panel-alter" style="display:none"><pre id="alter"></pre></div>
        <button class="copy" id="copy" type="button">Copy current output</button>
      </div>
    </div>
  </div>
</div>
<script>
"use strict";
// --- algorithm (mirror of internal/tune/algo.go; keep byte-for-byte in sync) ---
var KB=1024, MB=1024*KB, GB=1024*MB;         // bytes
var MBKB=1024, GBKB=1024*MBKB;               // kB per MB/GB
function floorKb(v){ return Math.floor(v); }
function formatValue(kb){
  if (kb % GBKB === 0) return (kb/GBKB)+"GB";
  if (kb % MBKB === 0) return (kb/MBKB)+"MB";
  return kb+"kB";
}
function normalize(i){
  if (!i.ramBytes || i.ramBytes <= 0) return null;
  if (!i.cpus || i.cpus < 1) i.cpus = 1;
  if (!i.version || i.version <= 0) i.version = 180000;
  i.os = i.os || "linux"; i.type = i.type || "mixed"; i.hd = i.hd || "ssd"; i.size = i.size || "mid_ram";
  return i;
}
function maxConnPreset(t){
  return ({web:200, oltp:300, dw:40, desktop:20}[t]) || 100;
}
function parallel(i, major){
  if (i.cpus < 4) return [];
  var perGather = Math.ceil(i.cpus/2);
  if (i.type !== "dw" && perGather > 4) perGather = 4;
  var out = [["max_worker_processes", String(i.cpus)], ["max_parallel_workers_per_gather", String(perGather)]];
  if (major >= 10) out.push(["max_parallel_workers", String(i.cpus)]);
  if (major >= 11) out.push(["max_parallel_maintenance_workers", String(Math.min(4, Math.ceil(i.cpus/2)))]);
  return out;
}
function recommend(raw){
  var i = normalize(Object.assign({}, raw));
  if (!i) return {error: "RAM must be positive"};
  var ramKB = i.ramBytes / KB;
  var major = Math.floor(i.version / 10000);
  var maxConns = i.conns > 0 ? i.conns : maxConnPreset(i.type);

  var sbDiv = i.type === "desktop" ? 16 : 4;
  var sb = Math.floor(ramKB / sbDiv);
  if (major < 10 && i.os === "windows") { var cap = Math.floor(512*MBKB); if (sb > cap) sb = cap; }
  sb = floorKb(sb);

  var ecs = i.type === "desktop" ? floorKb(ramKB/4) : floorKb(ramKB*3/4);

  var mwm = Math.floor(ramKB/16); if (i.type === "dw") mwm = Math.floor(ramKB/8);
  var limit = 8*GBKB;
  if (i.os === "windows" && major <= 17) limit = 2*GBKB - MBKB;
  if (mwm >= limit) { mwm = limit; if (i.os === "windows" && major <= 17) mwm = 2*GBKB - MBKB; }
  mwm = floorKb(mwm);

  var par = parallel(i, major);
  var wb = Math.floor(sb*3/100); var maxBuf = 16*MBKB, near = 14*MBKB;
  if (wb > maxBuf) wb = maxBuf;
  if (wb > near && wb < maxBuf) wb = maxBuf;
  if (wb < 32) wb = 32;
  wb = floorKb(wb);

  var workers = 1;
  par.forEach(function(p){ if (p[0] === "max_worker_processes") workers = Math.max(workers, parseInt(p[1],10)); });
  if (par.length === 0) workers = 8;
  var base = (ramKB - sb) / ((maxConns + workers) * 3);
  if (i.type === "dw" || i.type === "mixed") base = Math.floor(base/2);
  else if (i.type === "desktop") base = Math.floor(base/6);
  if (i.size === "less_ram") base = Math.floor(base*1.3);
  else if (i.size === "greater_ram") base = Math.floor(base*0.9);
  if (base < 4*MBKB) base = 4*MBKB;
  if (i.os === "windows" && major <= 17) { var wl = 2*GBKB - MBKB; if (base > wl) base = wl; }
  var wm = floorKb(base);

  var rpc = (i.size === "less_ram") ? "1.1" : (i.hd === "hdd" || i.type === "dw" ? "4" : "1.1");
  var io = i.os === "linux" ? ({hdd:"2", ssd:"200", san:"300", nvme:"1000"}[i.hd] || "") : "";
  var huge = i.os === "mac" ? "off" : (sb >= 2*GBKB ? "try" : "off");
  var jit = (major >= 12 && (i.type === "web" || i.type === "oltp" || i.type === "mixed")) ? "off" : "";
  var walComp = major >= 15 ? "lz4" : (major >= 10 ? "on" : "");
  var avw = i.cpus >= 32 ? "5" : (i.cpus >= 16 ? "4" : "");
  var avwm = ""; { var th = 2*GBKB; var v = mwm >= th ? th : 0;
    if (i.os === "windows" && major <= 17 && v > 0) v = Math.min(v, 2*GBKB - MBKB);
    if (v > 0) avwm = formatValue(floorKb(v)); }
  // io_uring deliberately not recommended (needs --with-liburing build + kernel
  // support; mismatch is FATAL at startup) — worker is always available on PG18.
  // var ioMethod = major < 18 ? "" : (i.os === "linux" ? "io_uring" : "worker");
  var ioMethod = major < 18 ? "" : "worker";
  var ioWorkers = "";
  // if (major >= 18 && ioMethod !== "io_uring") { var iv = Math.min(32, Math.max(3, Math.floor(i.cpus/4))); if (iv > 3) ioWorkers = String(iv); }
  if (major >= 18) { var iv = Math.min(32, Math.max(3, Math.floor(i.cpus/4))); if (iv > 3) ioWorkers = String(iv); }
  var minw = ({web:1024, oltp:2048, dw:4096, desktop:100, mixed:1024}[i.type])*MBKB;
  var maxw = ({web:4096, oltp:8192, dw:16384, desktop:2048, mixed:4096}[i.type])*MBKB;

  var rows = [];
  rows.push(["max_connections", String(maxConns), "connection budget for the workload class"]);
  rows.push(["shared_buffers", formatValue(sb), "buffer cache; 1/4 RAM (1/16 on desktop)"]);
  rows.push(["effective_cache_size", formatValue(ecs), "planner hint for OS + buffer cache"]);
  rows.push(["maintenance_work_mem", formatValue(mwm), "vacuum/index-build memory"]);
  rows.push(["checkpoint_completion_target", "0.9", "spread checkpoint writes over the interval"]);
  rows.push(["wal_buffers", formatValue(wb), "3% of shared_buffers, capped at 16MB"]);
  rows.push(["default_statistics_target", i.type === "dw" ? "500" : "100", "ANALYZE sample multiplier (500 for DW)"]);
  var rpcW = (i.type === "dw" && i.hd !== "hdd" && i.size !== "less_ram")
    ? "Cost parameters for Data Warehouses on SSDs/NVMe drives/SAN storage are left at defaults to avoid catastrophic index scan selections" : "";
  rows.push(["random_page_cost", rpc, rpcW || "seek vs sequential cost ratio", !!rpcW]);
  if (io) rows.push(["effective_io_concurrency", io, "prefetch depth for the storage class"]);
  rows.push(["work_mem", formatValue(wm), "per-node sort/hash memory"]);
  rows.push(["huge_pages", huge, "reduce TLB pressure for large shared_buffers"]);
  if (jit) rows.push(["jit", jit, "disable JIT for short OLTP/web queries"]);
  if (walComp) rows.push(["wal_compression", walComp, walComp === "lz4" ? "compress WAL (needs --with-lz4 build)" : "compress WAL to cut I/O"]);
  if (avw) rows.push(["autovacuum_max_workers", avw, "more vacuum workers on high-core hosts"]);
  if (avwm) rows.push(["autovacuum_work_mem", avwm, "cap per-worker vacuum memory below maintenance_work_mem"]);
  if (ioMethod) rows.push(["io_method", ioMethod, ioMethod === "io_uring" ? "asynchronous I/O (needs --with-liburing build)" : "asynchronous I/O method (PG18+)"]);
  if (ioWorkers) rows.push(["io_workers", ioWorkers, "AIO worker processes (PG18+)"]);
  rows.push(["min_wal_size", formatValue(minw), "WAL checkpoint sizing for the workload class"]);
  rows.push(["max_wal_size", formatValue(maxw), "WAL checkpoint sizing for the workload class"]);
  par.forEach(function(p){ rows.push([p[0], p[1], "parallelism (from CPU count)"]); });
  if (i.type === "desktop") {
    rows.push(["wal_level", "minimal", "desktop: no WAL archiving/replication needed"]);
    rows.push(["max_wal_senders", "0", "required 0 when wal_level=minimal"]);
  }
  return {rows: rows, inputs: i};
}
// --- end algorithm ---

var FIXTURES = __FIXTURES__;
var PREFILL = __PREFILL__;

function toInputs(i){ return {version: i.version, os: i.os, type: i.type,
  ramBytes: i.ram_bytes, cpus: i.cpus, hd: i.hd, size: i.size, conns: i.max_conns}; }

function selfCheck(){
  var bad = [];
  FIXTURES.forEach(function(f, fi){
    var got = recommend(toInputs(f.inputs)).rows.map(function(r){ return r[0] + "=" + r[1]; });
    var want = f.params.map(function(p){ return p[0] + "=" + p[1]; });
    if (got.join(",") !== want.join(",")) {
      bad.push("fixture " + fi + ": JS and pg tune disagree\n  js:   " + got.join(" ") + "\n  want: " + want.join(" "));
    }
  });
  if (bad.length) {
    var b = document.getElementById("banner");
    b.style.display = "block";
    b.textContent = "⚠ Page self-check failed — the embedded calculator differs from the pgcli binary. " +
      "Values may be wrong.\\n\\n" + bad.join("\\n\\n");
  }
}

function fmtGB(i){ return Math.round(i / GB); }
function fillForm(){
  var sel = document.getElementById("version");
  [18,17,16,15,14,13,12,11,10].forEach(function(v){
    var o = document.createElement("option"); o.value = v*10000; o.textContent = "PostgreSQL " + v;
    sel.appendChild(o);
  });
  sel.value = PREFILL.version;
  document.getElementById("os").value = PREFILL.os;
  document.getElementById("type").value = PREFILL.type;
  document.getElementById("hd").value = PREFILL.hd;
  document.getElementById("size").value = PREFILL.size;
  document.getElementById("cpus").value = PREFILL.cpus || 1;
  document.getElementById("conns").value = PREFILL.conns || 0;
  var unit = "GB";
  document.getElementById("ramunit").value = unit;
  // Probed RAM is rarely a whole GB count (kernels reserve some); round the
  // prefill so the number input shows a tidy integer instead of a long tail.
  document.getElementById("ram").value = PREFILL.ram_bytes ? Math.round(PREFILL.ram_bytes / GB) : "";
}
function readForm(){
  var unit = document.getElementById("ramunit").value;
  var mult = unit === "TB" ? TB() : unit === "GB" ? GB : MB;
  return {
    version: +document.getElementById("version").value,
    os: document.getElementById("os").value,
    type: document.getElementById("type").value,
    ramBytes: (+document.getElementById("ram").value) * mult,
    cpus: +document.getElementById("cpus").value,
    hd: document.getElementById("hd").value,
    size: document.getElementById("size").value,
    conns: +document.getElementById("conns").value
  };
}
function TB(){ return 1024*GB; }

var currentText = "";
function render(){
  var r = recommend(readForm());
  var tbody = document.getElementById("rows");
  tbody.innerHTML = "";
  currentText = "";
  if (r.error) { tbody.innerHTML = "<tr><td colspan=3 class=warn>" + r.error + "</td></tr>"; return; }
  var confLines = [], alterLines = [];
  r.rows.forEach(function(row){
    var tr = document.createElement("tr");
    tr.innerHTML = "<td class=n>" + esc(row[0]) + "</td><td class=v>" + esc(row[1]) +
      "</td><td class=" + (row[3] ? "warn" : "r") + ">" + esc(row[2]) + "</td>";
    tbody.appendChild(tr);
    confLines.push(row[0] + " = " + row[1]);
    alterLines.push("ALTER SYSTEM SET " + row[0] + " = '" + row[1] + "';");
  });
  document.getElementById("conf").textContent = confLines.join("\n");
  document.getElementById("alter").textContent = alterLines.join("\n");
  var active = document.querySelector(".tab.active").dataset.tab;
  currentText = active === "alter" ? alterLines.join("\n")
              : active === "conf" ? confLines.join("\n")
              : r.rows.map(function(x){ return x[0] + " = " + x[1]; }).join("\n");
}
function esc(s){ s = String(s);
  var pairs = ["&","&amp;","<","&lt;",">","&gt;","\"","&quot;"];
  for (var k = 0; k < pairs.length; k += 2) { s = s.split(pairs[k]).join(pairs[k+1]); }
  return s; }

function showTab(name){
  ["table","conf","alter"].forEach(function(t){
    document.getElementById("panel-" + t).style.display = t === name ? "" : "none";
  });
  document.querySelectorAll(".tab").forEach(function(b){ b.classList.toggle("active", b.dataset.tab === name); });
  render();
}

document.getElementById("f").addEventListener("input", render);
document.querySelectorAll(".tab").forEach(function(b){
  b.addEventListener("click", function(){ showTab(b.dataset.tab); });
});
document.getElementById("copy").addEventListener("click", function(){
  navigator.clipboard && navigator.clipboard.writeText(currentText);
});

selfCheck();
fillForm();
render();
</script>
</body>
</html>
`

// webFixture is one golden input/output pair the browser checks its JS port
// against — the same shape Recommend() produces, so any drift lights up.
type webFixture struct {
	Inputs webInputs   `json:"inputs"`
	Params [][2]string `json:"params"`
}

type webInputs struct {
	Version  int    `json:"version"`
	OS       string `json:"os"`
	Type     string `json:"type"`
	RAMBytes int64  `json:"ram_bytes"`
	CPUs     int    `json:"cpus"`
	HD       string `json:"hd"`
	Size     string `json:"size"`
	MaxConns int    `json:"max_conns"`
}

// WebHTML renders the interactive tuning site. prefill carries the host probe
// (RAM/CPUs/OS) and an optional target version so the form opens on sensible
// values; blank enums fall back to the algorithm defaults.
func WebHTML(prefill Inputs) (string, error) {
	fixtures, err := goldenFixtures()
	if err != nil {
		return "", err
	}
	fj, err := json.Marshal(fixtures)
	if err != nil {
		return "", err
	}
	prefill = normalizePrefill(prefill)
	pj, err := json.Marshal(toWebInputs(prefill))
	if err != nil {
		return "", err
	}
	// Minify the script before substituting the data: the embedded JSON stays
	// byte-exact and the (comment-heavy) code becomes one compact line.
	html := webTemplate
	if open := strings.Index(html, "<script>"); open >= 0 {
		if close := strings.Index(html[open:], "</script>"); close >= 0 {
			body := html[open+len("<script>") : open+close]
			html = html[:open] + "<script>" + minifyJS(body) + html[open+close:]
		}
	}
	html = strings.ReplaceAll(html, "__FIXTURES__", string(fj))
	html = strings.ReplaceAll(html, "__PREFILL__", string(pj))
	return html, nil
}

func goldenFixtures() ([]webFixture, error) {
	cases := []Inputs{
		{DBVersion: 180000, OS: OSLinux, Type: TypeMixed, RAMBytes: 16 * 1024 * 1024 * 1024, CPUs: 8, Disk: HDSSD, Size: SizeMid},
		{DBVersion: 150000, OS: OSLinux, Type: TypeDW, RAMBytes: 8 * 1024 * 1024 * 1024, CPUs: 2, Disk: HDNVME, Size: SizeMid},
		{DBVersion: 130004, OS: OSLinux, Type: TypeDesktop, RAMBytes: 4 * 1024 * 1024 * 1024, CPUs: 8, Disk: HDSSD, Size: SizeLess},
	}
	var out []webFixture
	for _, in := range cases {
		params, err := Recommend(in)
		if err != nil {
			return nil, fmt.Errorf("tune: golden fixture: %w", err)
		}
		pairs := make([][2]string, len(params))
		for i, p := range params {
			pairs[i] = [2]string{p.Name, p.Value}
		}
		out = append(out, webFixture{Inputs: toWebInputs(in), Params: pairs})
	}
	return out, nil
}

func toWebInputs(in Inputs) webInputs {
	return webInputs{
		Version: in.DBVersion, OS: string(in.OS), Type: string(in.Type),
		RAMBytes: in.RAMBytes, CPUs: in.CPUs, HD: string(in.Disk),
		Size: string(in.Size), MaxConns: in.MaxConns,
	}
}

func normalizePrefill(in Inputs) Inputs {
	// Same defaults as normalize(), but tolerant of RAM=0 (the standalone
	// --web-tune case starts the form blank) and never errors.
	if in.DBVersion <= 0 {
		in.DBVersion = 180000
	}
	if in.CPUs < 1 {
		in.CPUs = 1
	}
	if in.Type == "" {
		in.Type = TypeMixed
	}
	if in.Disk == "" {
		in.Disk = HDSSD
	}
	if in.Size == "" {
		in.Size = SizeMid
	}
	if in.OS == "" {
		in.OS = OSLinux
	}
	return in
}
