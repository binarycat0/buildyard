import { api } from './api.js';
import { LineClassifier } from './logclass.js';

const execNames = { auto: 'auto', wrapper: './mvnw', mvnd: 'mvnd', mvn: 'mvn' };

// Polls a background task until it finishes.
async function waitTask(id, { interval = 300, timeout = 120000 } = {}) {
  const until = Date.now() + timeout;
  for (;;) {
    const t = await api('GET', `/tasks/${id}`);
    if (t.status === 'succeeded' || t.status === 'failed') return t;
    if (Date.now() > until) throw new Error('the task did not finish in time');
    await new Promise((r) => setTimeout(r, interval));
  }
}

// Splits an argument string like a shell would: whitespace separates, quotes group.
function splitArgs(text) {
  const out = [];
  let cur = '';
  let quote = null;
  let any = false;
  for (const ch of text) {
    if (quote) {
      if (ch === quote) quote = null;
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      any = true;
    } else if (/\s/.test(ch)) {
      if (cur || any) out.push(cur);
      cur = '';
      any = false;
    } else {
      cur += ch;
    }
  }
  if (cur || any) out.push(cur);
  return out;
}

function shellQuote(a) {
  return /^[\w@%+=:,./-]+$/.test(a) ? a : "'" + a.replace(/'/g, "'\\''") + "'";
}

// Live log rendering lives outside Alpine: thousands of lines must not become reactive.
const logView = {
  el: null,
  pending: [],
  frame: 0,
  follow: true,
  cls: new LineClassifier(),
  counts: { all: 0, important: 0, warn: 0, error: 0 },
  query: '',
  onCounts: null,
  reset(el) {
    this.el = el;
    this.pending = [];
    this.follow = true;
    this.cls = new LineClassifier();
    this.counts = { all: 0, important: 0, warn: 0, error: 0 };
    if (el) el.textContent = '';
    this.onCounts?.(this.counts);
  },
  lineNode(text, cls) {
    const c = cls.classify(text);
    const d = document.createElement('div');
    d.className = 'lv-' + c.level + (c.important ? ' imp' : '') + (c.ok ? ' l-ok' : '');
    if (this.query && !text.toLowerCase().includes(this.query)) d.classList.add('nomatch');
    d.textContent = text;
    this.counts.all++;
    if (c.important) this.counts.important++;
    if (c.level === 'error') this.counts.error++;
    if (c.level === 'error' || c.level === 'warn') this.counts.warn++;
    return d;
  },
  push(text) {
    this.pending.push(text);
    if (!this.frame) this.frame = requestAnimationFrame(() => this.flush());
  },
  flush() {
    this.frame = 0;
    if (!this.el) return;
    const frag = document.createDocumentFragment();
    for (const t of this.pending) frag.appendChild(this.lineNode(t, this.cls));
    this.pending = [];
    this.el.appendChild(frag);
    if (this.follow) this.el.scrollTop = this.el.scrollHeight;
    this.onCounts?.(this.counts);
  },
  prepend(lines) {
    if (!this.el) return;
    const frag = document.createDocumentFragment();
    const cls = new LineClassifier();
    for (const t of lines) frag.appendChild(this.lineNode(t, cls));
    const before = this.el.scrollHeight;
    this.el.insertBefore(frag, this.el.firstChild);
    this.el.scrollTop += this.el.scrollHeight - before;
    this.onCounts?.(this.counts);
  },
  search(q) {
    this.query = q.trim().toLowerCase();
    if (!this.el) return;
    this.el.classList.toggle('searching', !!this.query);
    for (const d of this.el.children) d.classList.toggle('nomatch', !!this.query && !d.textContent.toLowerCase().includes(this.query));
  },
};

// Module indexes, kept outside Alpine: they are plain objects built once per module
// list (or selection) and never become reactive proxies. Projects can have 1000+
// modules, so every per-row question must be answered in O(1).
const graphCache = new WeakMap(); // modules array -> graph
const selCache = new WeakMap(); // selected refs array -> selection index
let rowsCache = null; // the last tree rows, keyed by what they were built from

const ROW_H = 26; // px, the fixed height of a module row (virtual scrolling)

function buildGraph(modules) {
  const byKey = new Map(modules.map((m) => [m.key, m]));
  const kids = new Map();
  const users = new Map();
  const perArtifact = new Map();
  for (const m of modules) perArtifact.set(m.artifactId, (perArtifact.get(m.artifactId) || 0) + 1);
  const ref = new Map(); // key -> ":artifactId" when unique, else "groupId:artifactId"
  const keyOf = new Map(); // ref -> key
  for (const m of modules) {
    const r = perArtifact.get(m.artifactId) > 1 ? m.key : ':' + m.artifactId;
    ref.set(m.key, r);
    keyOf.set(r, m.key);
    keyOf.set(m.key, m.key);
    if (m.aggregator && byKey.has(m.aggregator)) {
      if (!kids.has(m.aggregator)) kids.set(m.aggregator, []);
      kids.get(m.aggregator).push(m);
    }
    for (const d of m.deps || []) {
      if (!users.has(d)) users.set(d, []);
      users.get(d).push(m.key);
    }
  }
  const roots = modules.filter((m) => !m.aggregator || !byKey.has(m.aggregator));
  const parent = (k) => {
    const a = byKey.get(k)?.aggregator;
    return a && byKey.has(a) ? a : null;
  };
  // Subtree key lists, filled bottom-up once (iteratively: trees can be deep).
  const sub = new Map();
  const order = [];
  const stack = [...roots];
  while (stack.length) {
    const m = stack.pop();
    order.push(m);
    for (const c of kids.get(m.key) || []) stack.push(c);
  }
  for (let i = order.length - 1; i >= 0; i--) {
    const m = order[i];
    const keys = [m.key];
    for (const c of kids.get(m.key) || []) for (const k of sub.get(c.key)) keys.push(k);
    sub.set(m.key, keys);
  }
  const hasTree = modules.some((m) => m.aggregator);
  return { byKey, kids, users, roots, ref, keyOf, parent, sub, hasTree };
}

// For a selection: the selected keys, and how many are selected under each aggregator.
function buildSelection(g, refs) {
  const keys = new Set();
  const under = new Map();
  for (const r of refs) {
    const k = g.keyOf.get(r) ?? r;
    if (keys.has(k)) continue;
    keys.add(k);
    if (!g.byKey.has(k)) continue;
    for (let a = k; a; a = g.parent(a)) under.set(a, (under.get(a) || 0) + 1);
  }
  return { keys, under };
}

let stream = null;
let ticker = 0;

function closeStream() {
  if (stream) stream.close();
  stream = null;
}

function loadPref(key, def) {
  try {
    return localStorage.getItem('buildyard.' + key) ?? def;
  } catch {
    return def;
  }
}

function savePref(key, value) {
  try {
    localStorage.setItem('buildyard.' + key, value);
  } catch {
    // private mode or blocked storage: the choice lasts until reload
  }
}

// Resource presets: [value, label]. Anything else is a custom value.
const RES = {
  cpu: [['1', '1 thread (sequential build)'], ['0.5C', 'Half of the CPU cores (0.5C)'], ['1C', '1 thread per CPU core (1C)'],
    ['1.5C', '1.5 threads per CPU core (1.5C)'], ['2C', '2 threads per CPU core (2C)']],
  heap: [['2048', '2 GB'], ['4096', '4 GB'], ['8192', '8 GB'], ['12288', '12 GB'], ['16384', '16 GB'], ['24576', '24 GB'], ['32768', '32 GB']],
  forks: [['1', '1 JVM'], ['2', '2 JVMs'], ['0.5C', 'Half of the CPU cores (0.5C)'], ['1C', '1 JVM per CPU core (1C)']],
};
const RES_SPECIAL = ['', '-1', 'none'];

// A stored value as [choice, custom text]; heap custom values are shown in GB.
function splitChoice(kind, v) {
  v = v === undefined || v === null || v === 0 ? '' : String(v);
  if (RES_SPECIAL.includes(v) || RES[kind].some(([x]) => x === v)) return [v, ''];
  return ['custom', kind === 'heap' ? String(Math.round((Number(v) / 1024) * 100) / 100) : v];
}

function joinChoice(kind, choice, custom) {
  const v = choice === 'custom' ? String(custom || '').trim() : choice;
  if (kind !== 'heap') return v;
  if (v === '') return 0;
  return choice === 'custom' ? Math.round(Number(v) * 1024) || 0 : Number(v);
}

function resLabel(kind, v) {
  v = v === undefined || v === null || v === 0 ? '' : String(v);
  const hit = RES[kind].find(([x]) => x === v);
  if (hit) return hit[1];
  if (kind === 'heap') return Number(v) % 1024 ? `${v} MB` : `${Number(v) / 1024} GB`;
  return v;
}

function emptyForm() {
  return {
    goals: 'clean install', args: '', modulesMode: 'all', selected: [], alsoMake: true, alsoMakeDependents: false,
    skips: [], offline: false, nsu: false, ntp: true, errors: false, failMode: '', logLevel: 'default',
    profiles: '', props: '',
    cpu: '', cpuCustom: '', heap: '', heapCustom: '', forks: '', forksCustom: '',
  };
}

// RunSpec <-> run form.
function specToForm(spec) {
  const f = emptyForm();
  if (!spec) return f;
  const m = spec.modules || {};
  const fl = spec.flags || {};
  const r = spec.resources || {};
  const [cpu, cpuCustom] = splitChoice('cpu', fl.threads);
  const [heap, heapCustom] = splitChoice('heap', r.heapMb);
  const [forks, forksCustom] = splitChoice('forks', r.testForks);
  return {
    cpu, cpuCustom, heap, heapCustom, forks, forksCustom,
    goals: (spec.goals || []).join(' '),
    args: (spec.extraArgs || []).map(shellQuote).join(' '),
    modulesMode: m.mode === 'selected' ? 'selected' : 'all',
    selected: [...(m.list || [])],
    alsoMake: m.mode === 'selected' ? !!m.alsoMake : true,
    alsoMakeDependents: !!m.alsoMakeDependents,
    skips: [...(spec.skips || [])],
    offline: !!fl.offline, nsu: !!fl.noSnapshotUpdates, ntp: spec.flags ? !!fl.noTransferProgress : true,
    errors: !!fl.errors, failMode: fl.failMode || '', logLevel: spec.logLevel || 'default',
    profiles: (spec.profiles || []).join(','),
    props: Object.entries(spec.properties || {}).map(([k, v]) => (v === '' ? k : `${k}=${v}`)).join('\n'),
  };
}

function formToSpec(f) {
  const properties = {};
  for (const raw of f.props.split('\n')) {
    const line = raw.trim().replace(/^-D/, '');
    if (!line) continue;
    const i = line.indexOf('=');
    if (i < 0) properties[line] = '';
    else properties[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  return {
    kind: 'maven',
    goals: splitArgs(f.goals),
    modules: { mode: f.modulesMode, list: f.modulesMode === 'selected' ? f.selected : [], alsoMake: f.alsoMake, alsoMakeDependents: f.alsoMakeDependents },
    profiles: f.profiles.split(/[\s,]+/).filter(Boolean),
    skips: f.skips,
    properties,
    flags: { offline: f.offline, noSnapshotUpdates: f.nsu, threads: joinChoice('cpu', f.cpu, f.cpuCustom), noTransferProgress: f.ntp, failMode: f.failMode, errors: f.errors },
    resources: { heapMb: joinChoice('heap', f.heap, f.heapCustom), testForks: joinChoice('forks', f.forks, f.forksCustom) },
    logLevel: f.logLevel,
    extraArgs: splitArgs(f.args),
  };
}

function envToText(env) {
  return Object.entries(env || {}).map(([k, v]) => `${k}=${v}`).join('\n');
}

function textToEnv(text) {
  const env = {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const i = line.indexOf('=');
    if (i <= 0) throw new Error(`Not a KEY=VALUE line: ${line}`);
    env[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  return env;
}

document.addEventListener('alpine:init', () => {
  window.Alpine.data('app', () => ({
    view: 'projects',
    health: null,
    error: '',
    busy: false,
    saved: false,

    projects: [],
    loaded: false,
    adding: false,
    form: { rootPath: '', name: '' },
    formError: '',

    proj: null,
    runtime: null,
    pform: {},
    pformError: '',

    ptab: 'general',
    pview: 'run',
    pjobs: [],
    commonGoals: ['clean', 'compile', 'test', 'package', 'verify', 'install'],
    rform: emptyForm(),
    modules: [],
    modulesAt: null,
    skipPresets: [],
    defaultPresets: [],
    moduleFilter: '',
    moduleInfo: null,
    openState: {},
    modScroll: 0,
    modHeight: 360,
    rtab: loadPref('runTab', 'modules'),
    goalDraft: '',
    discoverTask: null,
    discoverError: '',
    profiles: [],
    editingTask: null,
    saveAsNew: false,
    taskName: '',
    copied: false,
    cmdShown: null,
    rpreview: null,
    runError: '',

    job: null,
    jobProject: null,
    cmp: null,
    cmpSort: 'delta',
    pick: [],
    jobMods: [],
    jobProblems: [],
    probOpen: {},
    modIndex: {},
    modsOpen: false,
    modsSort: 'reactor',
    firstLine: 0,
    logCount: 0,
    logFilter: loadPref('logFilter', 'important'),
    logQuery: '',
    logCounts: { all: 0, important: 0, warn: 0, error: 0 },
    now: Date.now(),
    settings: { tools: { maven: { executable: 'auto', jdkId: null } } },
    sform: { maven: { executable: 'auto', jdkId: 0 } },
    tools: {},
    jdks: [],
    jdkPath: '',
    editingJdk: null,
    managers: [],
    toolsTask: null,
    jdkError: '',
    detectTask: null,
    detectResult: '',
    btasks: [],
    cform: null,
    cfgSaved: false,
    seenTasksAt: loadPref('seenTasksAt', ''), // queuedAt of the newest task shown on the Activity page

    async init() {
      window.addEventListener('hashchange', () => this.route());
      try {
        this.health = await api('GET', '/health');
      } catch (e) {
        this.error = e.message;
      }
      await this.route();
      // The daemon scans JDKs at startup; pick up a scan that is still running.
      await this.loadActivity();
      const running = this.btasks.find((t) => t.kind === 'jdk.detect' && (t.status === 'queued' || t.status === 'running'));
      if (running) this.followDetect(running);
      // Background tasks are cheap to list (in memory); poll faster while the page is open.
      const poll = async () => {
        await this.loadActivity().catch(() => {});
        setTimeout(poll, this.view === 'activity' ? 2000 : 10000);
      };
      setTimeout(poll, 2000);
    },

    async route() {
      const h = location.hash.replace(/^#/, '') || '/';
      this.error = '';
      this.saved = false;
      let m;
      try {
        if (!h.startsWith('/jobs/')) closeStream();
        if ((m = h.match(/^\/projects\/(\d+)(?:\/(history|settings))?$/))) {
          const id = Number(m[1]);
          const same = this.view === 'project' && this.proj?.id === id;
          this.pview = m[2] || 'run';
          this.view = 'project';
          if (same) {
            if (this.pview === 'history') this.pjobs = await api('GET', `/projects/${id}/jobs`);
          } else {
            await this.loadProject(id);
          }
        } else if ((m = h.match(/^\/compare\/(\d+)\/(\d+)$/))) {
          this.view = 'compare';
          await this.openCompare(Number(m[1]), Number(m[2]));
        } else if ((m = h.match(/^\/jobs\/(\d+)$/))) {
          this.view = 'job';
          await this.openJob(Number(m[1]));
        } else if (h === '/activity') {
          this.view = 'activity';
          await this.loadActivity();
        } else if (h === '/settings') {
          this.view = 'settings';
          await this.loadSettings();
        } else {
          this.view = 'projects';
          await this.loadProjects();
        }
      } catch (e) {
        this.error = e.message;
      }
    },

    // ---------- projects ----------
    async loadProjects() {
      this.projects = await api('GET', '/projects');
      this.loaded = true;
    },

    openAdd() {
      this.adding = true;
      this.formError = '';
      this.$nextTick(() => this.$refs.rootPath.focus());
    },

    async add() {
      this.busy = true;
      this.formError = '';
      try {
        const p = await api('POST', '/projects', { rootPath: this.form.rootPath, name: this.form.name });
        this.form = { rootPath: '', name: '' };
        this.adding = false;
        location.hash = `#/projects/${p.id}`;
      } catch (e) {
        this.formError = e.message;
      } finally {
        this.busy = false;
      }
    },

    async remove(p) {
      if (!confirm(`Remove project "${p.name}" from Buildyard? Project files are not touched.`)) return;
      try {
        await api('DELETE', `/projects/${p.id}`);
        this.projects = this.projects.filter((x) => x.id !== p.id);
      } catch (e) {
        this.error = e.message;
      }
    },

    lastJobText(p) {
      if (!p.lastJob) return 'No runs yet';
      const j = p.lastJob;
      const d = this.duration(j);
      return this.when(j.finishedAt || j.startedAt || j.queuedAt) + (d && j.finishedAt ? ' · ' + d : '');
    },

    // ---------- project settings ----------
    async loadProject(id) {
      const [p, runtime, jdks, settings, tools, pjobs, mods, profiles] = await Promise.all([
        api('GET', `/projects/${id}`), api('GET', `/projects/${id}/runtime`),
        api('GET', '/jdks'), api('GET', '/settings'), api('GET', '/tools'), api('GET', `/projects/${id}/jobs`),
        api('GET', `/projects/${id}/modules`), api('GET', `/projects/${id}/profiles`),
      ]);
      Object.assign(this, { proj: p, runtime, jdks, settings, tools, pjobs, profiles });
      this.setModules(mods);
      // Start from the last run's spec.
      const last = pjobs.find((j) => j.spec?.goals?.length);
      this.rform = specToForm(last?.spec);
      this.editingTask = null;
      this.saveAsNew = false;
      this.runError = '';
      this.discoverError = '';
      this.moduleFilter = '';
      this.moduleInfo = null;
      this.openState = {};
      this.modScroll = 0;
      this.refreshPreview();
      // First visit: list the modules in the background.
      if (!this.modules.length && !this.discoverTask) this.discover();
      const exe = p.executable ?? '';
      const known = ['', 'auto', 'wrapper', 'mvnd', 'mvn'].includes(exe);
      this.pform = {
        name: p.name,
        execChoice: known ? exe : 'custom',
        customPath: known ? '' : exe,
        jdkId: p.jdkId ?? 0,
        toolJdkId: p.toolJdkId ?? 0,
        presets: this.skipPresets.map((sp) => ({
          id: sp.id, label: sp.label, argsText: sp.args.join(' '),
          builtin: this.defaultPresets.some((d) => d.id === sp.id),
        })),
        envText: envToText(p.env),
        ...this.resourcesToForm(p.resources),
      };
      this.pformError = '';
    },

    async saveProject() {
      this.busy = true;
      this.pformError = '';
      this.saved = false;
      try {
        const f = this.pform;
        const executable = f.execChoice === 'custom' ? f.customPath.trim() : f.execChoice;
        if (f.execChoice === 'custom' && !executable.startsWith('/')) throw new Error('Enter an absolute path to the executable');
        const p = await api('PATCH', `/projects/${this.proj.id}`, {
          name: f.name, executable, jdkId: Number(f.jdkId) || 0, toolJdkId: Number(f.toolJdkId) || 0,
          env: textToEnv(f.envText), skipPresets: this.presetDiff(f.presets),
          resources: {
            threads: joinChoice('cpu', f.cpu, f.cpuCustom),
            heapMb: joinChoice('heap', f.heap, f.heapCustom),
            testForks: joinChoice('forks', f.forks, f.forksCustom),
          },
        });
        this.proj = p;
        this.runtime = await api('GET', `/projects/${p.id}/runtime`);
        this.setModules(await api('GET', `/projects/${p.id}/modules`));
        this.rform.skips = this.rform.skips.filter((id) => this.skipPresets.some((sp) => sp.id === id));
        this.saved = true;
        this.refreshPreview();
      } catch (e) {
        this.pformError = e.message;
      } finally {
        this.busy = false;
      }
    },

    // ---------- resources ----------
    resChoices(kind) {
      return RES[kind];
    },

    resourcesToForm(r) {
      r = r || {};
      const [cpu, cpuCustom] = splitChoice('cpu', r.threads);
      const [heap, heapCustom] = splitChoice('heap', r.heapMb);
      const [forks, forksCustom] = splitChoice('forks', r.testForks);
      return { cpu, cpuCustom, heap, heapCustom, forks, forksCustom };
    },

    isMvnd() {
      const e = this.runtime?.executable;
      return !!e && (e.kind === 'mvnd' || /(^|\/)mvnd$/.test(e.path || ''));
    },

    // "Project default (…)" options in the run form.
    projectDefault(kind) {
      const r = this.proj?.resources || {};
      const v = { cpu: r.threads, heap: r.heapMb, forks: r.testForks }[kind];
      const none = { cpu: this.isMvnd() ? 'mvnd: cores − 1, parallel' : 'Maven: 1 thread, sequential', heap: "the JVM's", forks: "the test plugin's" }[kind];
      return `Project default (${v ? resLabel(kind, v) : none})`;
    },

    // ---------- runs ----------
    spec() {
      const spec = formToSpec(this.rform);
      // Every module ticked (e.g. the root of the tree) is the same as building all of them,
      // without a -pl list of hundreds of modules.
      if (spec.modules.mode === 'selected' && this.modules.length && this.allSelected()) {
        spec.modules = { mode: 'all', list: [], alsoMake: false, alsoMakeDependents: false };
      }
      return spec;
    },

    allSelected() {
      const sel = this.selection().keys;
      return this.modules.length > 0 && this.modules.every((m) => sel.has(m.key));
    },

    selection() {
      const refs = Alpine.raw(this.rform.selected);
      let s = selCache.get(refs);
      if (!s) {
        s = buildSelection(this.graph(), refs);
        selCache.set(refs, s);
      }
      return s;
    },

    // Notes of the ticked skip presets, e.g. why skipping test compilation can break a build.
    skipNotes() {
      return this.skipPresets.filter((sp) => sp.note && this.rform.skips.includes(sp.id) && sp.id !== 'tests')
        .map((sp) => `${sp.label}: ${sp.note}`);
    },

    // A saved task in a few tags: goals, which modules, profiles, skips and notable flags.
    taskTags(spec) {
      const s = spec || {};
      const tags = (s.goals || []).map((g) => ({ text: g, cls: 'goal' }));
      const m = s.modules || {};
      if (m.mode === 'selected' && m.list?.length) {
        const list = m.list;
        const text = list.length <= 2 ? list.map((r) => r.replace(/^:/, '')).join(', ') : `${list.length} modules`;
        const extra = [m.alsoMake && '-am', m.alsoMakeDependents && '-amd'].filter(Boolean).join(' ');
        tags.push({ text: extra ? `${text} ${extra}` : text, title: list.join('\n') });
      } else {
        tags.push({ text: 'all modules', cls: 'dim' });
      }
      if (s.profiles?.length) tags.push({ text: '-P ' + s.profiles.join(','), cls: 'prof' });
      if (s.skips?.length) tags.push({ text: 'skip ' + s.skips.join(', '), cls: 'dim' });
      const f = s.flags || {};
      if (f.threads && f.threads !== '1') tags.push({ text: '-T ' + f.threads });
      const r = s.resources || {};
      if (r.heapMb > 0) tags.push({ text: resLabel('heap', r.heapMb) + ' heap', cls: 'dim' });
      if (r.testForks && r.testForks !== 'none') tags.push({ text: 'test JVMs ' + r.testForks, cls: 'dim' });
      if (f.offline) tags.push({ text: 'offline', cls: 'dim' });
      if (s.logLevel && s.logLevel !== 'default') tags.push({ text: 'log ' + s.logLevel, cls: 'dim' });
      return tags;
    },

    // ---------- modules ----------
    setModules(resp) {
      this.modules = resp.modules;
      this.modulesAt = resp.discoveredAt;
      this.skipPresets = resp.skipPresets;
      this.defaultPresets = resp.defaultPresets;
    },

    moduleStatus() {
      if (this.discoverTask) return 'running mvn help:active-profiles…';
      if (!this.modules.length) return 'modules not discovered yet';
      return `${this.modules.length} modules · discovered ${this.when(this.modulesAt)}`;
    },

    // ":artifactId" when unique, else "groupId:artifactId".
    moduleRef(m) {
      return this.graph().ref.get(m.key) ?? m.key;
    },

    hasTree() {
      return this.graph().hasTree;
    },

    // Rebuilt when modules change: lookups and the aggregator / dependency graphs.
    graph() {
      const mods = Alpine.raw(this.modules);
      let g = graphCache.get(mods);
      if (!g) {
        g = buildGraph(mods);
        graphCache.set(mods, g);
      }
      return g;
    },

    // Keys reachable through deps (up) or users (down), not including the start keys.
    closure(keys, dir) {
      const g = this.graph();
      const seen = new Set();
      const stack = [...keys];
      while (stack.length) {
        const k = stack.pop();
        const next = dir === 'up' ? (g.byKey.get(k)?.deps || []) : (g.users.get(k) || []);
        for (const n of next) {
          if (!seen.has(n)) {
            seen.add(n);
            stack.push(n);
          }
        }
      }
      for (const k of keys) seen.delete(k);
      return seen;
    },

    subtreeKeys(key) {
      return this.graph().sub.get(key) || [key];
    },

    selectedKeys() {
      return [...this.selection().keys];
    },

    impactText() {
      const keys = this.selectedKeys();
      const parts = [];
      if (this.rform.alsoMake) parts.push(`-am adds ${this.plural(this.closure(keys, 'up').size, 'module')}`);
      if (this.rform.alsoMakeDependents) parts.push(`-amd adds ${this.plural(this.closure(keys, 'down').size, 'module')}`);
      return parts.length ? `${parts.join(', ')} to the build.` : '';
    },

    plural(n, word) {
      return `${n} ${word}${n === 1 ? '' : 's'}`;
    },

    infoFor(key) {
      const g = this.graph();
      const m = g.byKey.get(key);
      if (!m) return null;
      const named = (keys) => keys.map((k) => g.byKey.get(k) || { key: k, name: k });
      return {
        m,
        deps: named(m.deps || []),
        users: named(g.users.get(key) || []),
        upAll: this.closure([key], 'up').size,
        downAll: this.closure([key], 'down').size,
      };
    },

    // Rows to render: the aggregator tree (top level open by default), or a flat list
    // when filtering or when discovery had no folder information. Cached: the list is
    // asked for by every visible row.
    shownRows() {
      const mods = Alpine.raw(this.modules);
      const open = Alpine.raw(this.openState);
      const q = this.moduleFilter.trim().toLowerCase();
      const c = rowsCache;
      if (c && c.mods === mods && c.open === open && c.q === q) return c.rows;
      const g = this.graph();
      let rows;
      if (q || !g.hasTree) {
        rows = this.filteredModules().map((m) => ({ m, depth: 0, kids: 0, open: false, flat: true }));
      } else {
        rows = [];
        const stack = g.roots.map((m) => [m, 0]).reverse();
        while (stack.length) {
          const [m, depth] = stack.pop();
          const kids = g.kids.get(m.key) || [];
          const isOpen = m.key in open ? open[m.key] : depth === 0;
          rows.push({ m, depth, kids: kids.length, open: isOpen, size: g.sub.get(m.key)?.length || 1 });
          if (isOpen) for (let i = kids.length - 1; i >= 0; i--) stack.push([kids[i], depth + 1]);
        }
      }
      rowsCache = { mods, open, q, rows };
      return rows;
    },

    // The rows in view, plus a few around them; the rest is empty space of the same height.
    visibleRows() {
      const rows = this.shownRows();
      const from = Math.max(0, Math.floor(this.modScroll / ROW_H) - 10);
      const to = Math.min(rows.length, from + Math.ceil(this.modHeight / ROW_H) + 20);
      return { rows: rows.slice(from, to), top: from * ROW_H, bottom: (rows.length - to) * ROW_H };
    },

    // The module list reaches down to the bottom of the window (with the page scrolled to
    // the top), leaving room for the dependency panel under it when that is open.
    fitModules() {
      const el = this.$refs.modlist;
      if (!el || !el.offsetParent) return;
      const top = el.getBoundingClientRect().top + window.scrollY;
      const info = el.parentElement.querySelector('.modinfo');
      const below = (info ? info.offsetHeight + 12 : 0) + 36; // card padding and page margin
      this.modHeight = Math.max(260, Math.round(window.innerHeight - top - below));
    },

    onModScroll(e) {
      this.modScroll = e.target.scrollTop;
    },

    toggleOpen(r) {
      this.openState = { ...Alpine.raw(this.openState), [r.m.key]: !r.open };
    },

    setAllOpen(open) {
      const st = {};
      for (const k of this.graph().kids.keys()) st[k] = open;
      this.openState = st;
    },

    filteredModules() {
      const q = this.moduleFilter.trim().toLowerCase();
      const mods = Alpine.raw(this.modules);
      if (!q) return mods;
      return mods.filter((m) => m.name.toLowerCase().includes(q) || m.key.toLowerCase().includes(q) || (m.relPath || '').toLowerCase().includes(q));
    },

    // all | some | none for a row: a parent counts its whole subtree.
    checkState(r) {
      const sel = this.selection();
      if (!r.kids || r.flat) return sel.keys.has(r.m.key) ? 'all' : 'none';
      const n = sel.under.get(r.m.key) || 0;
      return n === 0 ? 'none' : n === r.size ? 'all' : 'some';
    },

    // Ticking a tree parent ticks everything under it.
    toggleModule(r) {
      const g = this.graph();
      const keys = r.kids && !r.flat ? this.subtreeKeys(r.m.key) : [r.m.key];
      const on = this.checkState(r) !== 'all';
      const drop = new Set(keys.map((k) => g.ref.get(k) ?? k));
      const sel = Alpine.raw(this.rform.selected).filter((x) => !drop.has(x) && !drop.has(g.ref.get(g.keyOf.get(x))));
      this.rform.selected = on ? [...sel, ...drop] : sel;
      if (this.rform.selected.length) this.rform.modulesMode = 'selected';
      this.refreshPreview();
    },

    selectShown() {
      const have = new Set(Alpine.raw(this.rform.selected));
      const add = this.shownRows().map((r) => this.moduleRef(r.m)).filter((x) => !have.has(x));
      this.rform.selected = [...this.rform.selected, ...add];
      this.refreshPreview();
    },

    toggleSkip(id) {
      const s = this.rform.skips;
      this.rform.skips = s.includes(id) ? s.filter((x) => x !== id) : [...s, id];
      this.refreshPreview();
    },

    knownProfiles() {
      return [...new Set(this.modules.flatMap((m) => m.profiles))].sort();
    },

    async discover() {
      const id = this.proj.id;
      this.discoverError = '';
      try {
        this.discoverTask = await api('POST', `/projects/${id}/modules/refresh`, { spec: this.spec() });
        const t = await waitTask(this.discoverTask.id, { timeout: 15 * 60 * 1000 });
        if (this.proj?.id !== id) return;
        if (t.status === 'failed') this.discoverError = 'Module discovery failed: ' + t.error;
        this.setModules(await api('GET', `/projects/${id}/modules`));
      } catch (e) {
        this.discoverError = e.message;
      } finally {
        this.discoverTask = null;
      }
    },

    // ---------- saved tasks ----------
    async reloadTasks() {
      this.profiles = await api('GET', `/projects/${this.proj.id}/profiles`);
    },

    editTask(t) {
      this.editingTask = t;
      this.saveAsNew = false;
      this.rform = specToForm(t.spec);
      this.refreshPreview();
    },

    stopEditing() {
      this.editingTask = null;
      this.saveAsNew = false;
    },

    async createTask() {
      this.runError = '';
      try {
        const t = await api('POST', `/projects/${this.proj.id}/profiles`, { name: this.taskName, spec: this.spec() });
        this.taskName = '';
        this.saveAsNew = false;
        await this.reloadTasks();
        this.editingTask = this.profiles.find((x) => x.id === t.id) || t;
      } catch (e) {
        this.runError = e.message;
      }
    },

    async updateTask() {
      this.runError = '';
      try {
        const t = this.editingTask;
        await api('PUT', `/profiles/${t.id}`, { name: t.name, pinned: t.pinned, spec: this.spec() });
        await this.reloadTasks();
        this.editingTask = this.profiles.find((x) => x.id === t.id) || null;
      } catch (e) {
        this.runError = e.message;
      }
    },

    async togglePin(t) {
      try {
        await api('PUT', `/profiles/${t.id}`, { name: t.name, pinned: !t.pinned, spec: t.spec });
        await this.reloadTasks();
      } catch (e) {
        this.runError = e.message;
      }
    },

    resetRunForm() {
      this.rform = emptyForm();
      this.refreshPreview();
    },

    async deleteTask(t) {
      if (!confirm(`Delete task "${t.name}"? Its past runs stay in the history.`)) return;
      try {
        await api('DELETE', `/profiles/${t.id}`);
        this.stopEditing();
        await this.reloadTasks();
      } catch (e) {
        this.runError = e.message;
      }
    },

    async runTask(t) {
      this.busy = true;
      try {
        const j = await api('POST', `/projects/${this.proj.id}/jobs`, { profileId: t.id });
        location.hash = `#/jobs/${j.id}`;
      } catch (e) {
        this.runError = `${t.name}: ${e.message}`;
      } finally {
        this.busy = false;
      }
    },

    // The command as short tokens: the executable's name, then the flags; a long -pl list
    // becomes a module count (the full command is one click away).
    shortArgs(argv) {
      if (!argv?.length) return [];
      const out = [{ text: argv[0].split('/').pop(), cls: 'exe', title: argv[0] }];
      for (let i = 1; i < argv.length; i++) {
        const a = argv[i];
        if (a === '-B') continue;
        if (a === '-pl' && i + 1 < argv.length) {
          const list = argv[++i].split(',');
          out.push(list.length > 3
            ? { text: `-pl ${list.length} modules`, title: list.join('\n') }
            : { text: '-pl ' + list.join(',') });
          continue;
        }
        if ((a === '-P' || a === '-T') && i + 1 < argv.length) {
          out.push({ text: a + ' ' + argv[++i] });
          continue;
        }
        const goal = !a.startsWith('-');
        out.push({ text: shellQuote(a), cls: goal ? 'goal' : '' });
      }
      return out;
    },

    showCommand(c) {
      this.cmdShown = { argv: c.argv, javaHome: c.javaHome, dir: c.dir, mavenOpts: c.mavenOpts };
      this.copied = false;
      this.$refs.cmdDialog.showModal();
    },

    async copyCommand(argv) {
      try {
        await navigator.clipboard.writeText(this.shellJoin(argv));
        this.copied = true;
        setTimeout(() => { this.copied = false; }, 1500);
      } catch (e) {
        this.runError = 'Copy failed: ' + e.message;
      }
    },

    // Project presets are stored as changes to the built-in ones.
    presetDiff(rows) {
      const out = [];
      const ids = new Set();
      for (const r of rows) {
        const id = r.id.trim();
        if (!id) continue;
        ids.add(id);
        const args = splitArgs(r.argsText);
        if (!args.length) throw new Error(`Skip preset "${id}" needs arguments`);
        const d = this.defaultPresets.find((x) => x.id === id);
        if (d && d.label === r.label && d.args.join(' ') === args.join(' ')) continue;
        out.push({ id, label: r.label.trim() || id, args });
      }
      for (const d of this.defaultPresets) if (!ids.has(d.id)) out.push({ id: d.id, label: d.label, args: [] });
      return out;
    },

    resetPresets() {
      this.pform.presets = this.defaultPresets.map((sp) => ({ id: sp.id, label: sp.label, argsText: sp.args.join(' '), builtin: true }));
    },

    customGoals() {
      return splitArgs(this.rform.goals).filter((g) => !this.commonGoals.includes(g));
    },

    addGoals() {
      const add = splitArgs(this.goalDraft).filter((g) => !this.hasGoal(g));
      this.goalDraft = '';
      if (!add.length) return;
      this.rform.goals = [...splitArgs(this.rform.goals), ...add].join(' ');
      this.refreshPreview();
    },

    // Run form tabs, each with a count of what is set in it.
    runTabs() {
      const f = this.rform;
      const n = (x) => (x ? String(x) : '');
      const props = f.props.split('\n').filter((l) => l.trim()).length;
      const profiles = f.profiles.split(/[\s,]+/).filter(Boolean).length;
      return [
        { id: 'modules', label: 'Modules', count: f.modulesMode === 'selected' ? n(f.selected.length) : '' },
        { id: 'skip', label: 'Skip', count: n(f.skips.length) },
        { id: 'options', label: 'Options', count: n([f.offline, f.nsu, f.errors, f.failMode, f.logLevel !== 'default'].filter(Boolean).length) },
        { id: 'resources', label: 'Resources', count: n([f.cpu, f.heap, f.forks].filter(Boolean).length) },
        { id: 'properties', label: 'Profiles & properties', count: n(props + profiles) },
        { id: 'extra', label: 'Extra args', count: n(splitArgs(f.args).length) },
      ];
    },

    setRunTab(id) {
      this.rtab = id;
      savePref('runTab', id);
    },

    hasGoal(g) {
      return splitArgs(this.rform.goals).includes(g);
    },

    toggleGoal(g) {
      const goals = splitArgs(this.rform.goals);
      const order = this.commonGoals;
      let next = goals.includes(g) ? goals.filter((x) => x !== g) : [...goals, g];
      // Keep the lifecycle order for common goals; custom goals stay at the end.
      next = [...order.filter((x) => next.includes(x)), ...next.filter((x) => !order.includes(x))];
      this.rform.goals = next.join(' ');
      this.refreshPreview();
    },

    async refreshPreview() {
      if (!this.proj) return;
      this.runError = '';
      try {
        this.rpreview = await api('POST', `/projects/${this.proj.id}/preview`, { spec: this.spec() });
      } catch (e) {
        this.runError = e.message;
      }
    },

    async runJob() {
      this.busy = true;
      this.runError = '';
      try {
        const j = await api('POST', `/projects/${this.proj.id}/jobs`, { spec: this.spec() });
        location.hash = `#/jobs/${j.id}`;
      } catch (e) {
        this.runError = e.message;
      } finally {
        this.busy = false;
      }
    },

    async rerun(j) {
      try {
        const n = await api('POST', `/jobs/${j.id}/rerun`);
        location.hash = `#/jobs/${n.id}`;
      } catch (e) {
        this.error = e.message;
      }
    },

    // stop: graceful (SIGINT, like Ctrl+C); kill: SIGKILL to the whole process group.
    async controlJob(action, j = this.job) {
      if (action === 'kill' && !confirm(`Kill job ${j.id} now? The build and all its processes end at once.`)) return;
      try {
        const v = await api('POST', `/jobs/${j.id}/${action}`);
        if (this.job?.id === v.id) this.job = v;
        const i = this.pjobs.findIndex((x) => x.id === v.id);
        if (i >= 0) this.pjobs.splice(i, 1, v);
      } catch (e) {
        this.error = e.message;
      }
    },

    since(t) {
      const sec = Math.max(0, Math.round((this.now - new Date(t)) / 1000));
      return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`;
    },

    async openJob(id) {
      closeStream();
      const job = await api('GET', `/jobs/${id}`);
      this.job = job;
      this.setMods([]);
      this.jobProblems = [];
      this.probOpen = {};
      this.firstLine = 0;
      this.logCount = job.liveLines;
      if (this.jobProject?.id !== job.projectId) this.jobProject = await api('GET', `/projects/${job.projectId}`);
      await this.$nextTick();
      logView.onCounts = (c) => { this.logCounts = { ...c }; };
      logView.reset(this.$refs.log);
      logView.search(this.logQuery);
      clearInterval(ticker);
      ticker = setInterval(() => { this.now = Date.now(); }, 1000);

      const es = new EventSource(`/api/jobs/${id}/stream`);
      stream = es;
      es.addEventListener('log', (e) => {
        const ev = JSON.parse(e.data);
        if (!this.firstLine) this.firstLine = ev.n;
        this.logCount = Math.max(this.logCount, ev.n);
        logView.push(ev.text);
      });
      es.addEventListener('modules', (e) => this.setMods(JSON.parse(e.data) || []));
      es.addEventListener('problems', (e) => { this.jobProblems = JSON.parse(e.data) || []; });
      es.addEventListener('problem', (e) => { this.jobProblems.push(JSON.parse(e.data)); });
      es.addEventListener('module', (e) => {
        const m = JSON.parse(e.data);
        const i = this.modIndex[m.key];
        if (i !== undefined) this.jobMods[i] = m;
      });
      es.addEventListener('status', async (e) => {
        const st = JSON.parse(e.data).status;
        if (st !== this.job.status) this.job = await api('GET', `/jobs/${id}`);
      });
      es.addEventListener('end', (e) => {
        this.job = JSON.parse(e.data);
        this.logCount = this.job.logLines;
        if (!this.firstLine && this.job.logLines) this.firstLine = this.job.logLines + 1;
        es.close();
        if (stream === es) stream = null;
        clearInterval(ticker);
      });
    },

    setMods(mods) {
      this.jobMods = mods;
      this.modIndex = Object.fromEntries(mods.map((m, i) => [m.key, i]));
    },

    modsWith(...st) {
      return this.jobMods.filter((m) => st.includes(m.status));
    },

    modsSummary() {
      const total = this.jobMods.length;
      const done = this.modsWith('succeeded', 'failed', 'skipped', 'cancelled').length;
      const parts = [`${done} of ${total} modules`];
      const failed = this.modsWith('failed').length;
      if (failed) parts.push(`${failed} failed`);
      const skipped = this.modsWith('skipped').length;
      if (skipped) parts.push(`${skipped} skipped`);
      if (this.job && !['queued', 'running'].includes(this.job.status)) {
        const notBuilt = this.modsWith('pending').length;
        if (notBuilt) parts.push(`${notBuilt} not built`);
      }
      return parts.join(' · ');
    },

    modsPercent() {
      const total = this.jobMods.length;
      return total ? (100 * this.modsWith('succeeded', 'failed', 'skipped', 'cancelled').length) / total : 0;
    },

    modsTable() {
      const rows = [...this.jobMods];
      if (this.modsSort === 'slowest') rows.sort((a, b) => this.modMs(b) - this.modMs(a));
      return rows;
    },

    modMs(m) {
      if (m.status === 'running' && m.startedAt) return this.now - new Date(m.startedAt);
      return m.ms || 0;
    },

    modTime(m) {
      if (m.status === 'pending') return '';
      const ms = this.modMs(m);
      if (ms < 1000) return ms + ' ms';
      const sec = Math.round(ms / 1000);
      return sec < 60 ? sec + ' s' : `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`;
    },

    modStatus(m) {
      if (m.status === 'pending') return this.job && ['queued', 'running'].includes(this.job.status) ? 'waiting' : 'not built';
      return m.status;
    },

    findModule(m) {
      // The artifactId is in Maven's own messages at every log level ("on project x").
      this.logQuery = m.key.split(':')[1] || m.name;
      if (this.logFilter !== 'all') this.setLogFilter('all');
      logView.search(this.logQuery);
    },

    // ---------- compare two runs ----------
    async openCompare(a, b) {
      const load = async (id) => {
        const [job, mods, probs] = await Promise.all([api('GET', `/jobs/${id}`), api('GET', `/jobs/${id}/modules`), api('GET', `/jobs/${id}/problems`)]);
        return { job, mods, probs };
      };
      const [A, B] = await Promise.all([load(a), load(b)]);
      if (this.jobProject?.id !== B.job.projectId) this.jobProject = await api('GET', `/projects/${B.job.projectId}`);
      this.cmp = { A, B };
    },

    togglePick(id) {
      this.pick = this.pick.includes(id) ? this.pick.filter((x) => x !== id) : [...this.pick.slice(-1), id];
    },

    comparePicked() {
      const [x, y] = [...this.pick].sort((p, q) => p - q);
      this.pick = [];
      location.hash = `#/compare/${x}/${y}`;
    },

    async compareWithPrevious(j) {
      const jobs = await api('GET', `/projects/${j.projectId}/jobs`);
      const prev = jobs.find((x) => x.id < j.id && x.status !== 'queued' && x.status !== 'running');
      if (!prev) {
        this.error = 'There is no earlier finished run of this project to compare with.';
        return;
      }
      location.hash = `#/compare/${prev.id}/${j.id}`;
    },

    jobMs(j) {
      if (!j?.startedAt) return 0;
      return new Date(j.finishedAt || Date.now()) - new Date(j.startedAt);
    },

    fmtMs(ms) {
      if (ms === null || ms === undefined) return '';
      const neg = ms < 0;
      ms = Math.abs(ms);
      let t;
      if (ms < 1000) t = ms + ' ms';
      else if (ms < 60000) t = (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + ' s';
      else t = `${Math.floor(ms / 60000)}:${String(Math.round((ms % 60000) / 1000)).padStart(2, '0')} min`;
      return (neg ? '−' : '') + t;
    },

    // Argument differences, with a -pl list compared as a set of modules.
    cmpArgs() {
      const { A, B } = this.cmp;
      const split = (argv) => {
        const args = [];
        let pl = [];
        for (let i = 1; i < argv.length; i++) {
          if (argv[i] === '-pl' && i + 1 < argv.length) pl = argv[++i].split(',');
          else if ((argv[i] === '-P' || argv[i] === '-T') && i + 1 < argv.length) args.push(argv[i] + ' ' + argv[++i]);
          else args.push(argv[i]);
        }
        return { args, pl, exe: argv[0] };
      };
      const a = split(A.job.argv || []);
      const b = split(B.job.argv || []);
      const only = (x, y) => x.filter((v) => !y.includes(v));
      return {
        removed: only(a.args, b.args), added: only(b.args, a.args),
        plRemoved: only(a.pl, b.pl), plAdded: only(b.pl, a.pl),
        exe: a.exe === b.exe ? '' : `${a.exe} → ${b.exe}`,
        same: !only(a.args, b.args).length && !only(b.args, a.args).length && !only(a.pl, b.pl).length && !only(b.pl, a.pl).length && a.exe === b.exe,
      };
    },

    cmpRows() {
      const { A, B } = this.cmp;
      const byKey = new Map();
      const add = (m, side) => {
        const k = m.key;
        if (!byKey.has(k)) byKey.set(k, { key: k, name: m.name || k, ord: m.ord, a: null, b: null });
        byKey.get(k)[side] = m;
      };
      A.mods.forEach((m) => add(m, 'a'));
      B.mods.forEach((m) => add(m, 'b'));
      const rows = [...byKey.values()].map((r) => {
        const built = (m) => m && m.status !== 'pending' && m.status !== 'skipped';
        r.delta = built(r.a) && built(r.b) ? (r.b.ms || 0) - (r.a.ms || 0) : null;
        r.changed = r.a && r.b && r.a.status !== r.b.status;
        if (r.b) r.ord = r.b.ord;
        return r;
      });
      if (this.cmpSort === 'delta') rows.sort((x, y) => Math.abs(y.delta ?? -1) - Math.abs(x.delta ?? -1) || x.ord - y.ord);
      else rows.sort((x, y) => x.ord - y.ord);
      return rows;
    },

    cmpSummary() {
      const { A, B } = this.cmp;
      const ta = this.jobMs(A.job);
      const tb = this.jobMs(B.job);
      const rows = this.cmpRows();
      const parts = [];
      if (ta && tb) {
        const pct = Math.round(((tb - ta) / ta) * 100);
        parts.push(pct === 0 ? 'Same total time' : `Run ${B.job.id} took ${this.fmtMs(Math.abs(tb - ta))} ${tb > ta ? 'longer' : 'less'} (${pct > 0 ? '+' : ''}${pct}%)`);
      }
      const changed = rows.filter((r) => r.changed).length;
      if (changed) parts.push(this.plural(changed, 'module') + ' changed status');
      const onlyA = rows.filter((r) => r.a && !r.b).length;
      const onlyB = rows.filter((r) => r.b && !r.a).length;
      if (onlyA) parts.push(`${onlyA} only in run ${A.job.id}`);
      if (onlyB) parts.push(`${onlyB} only in run ${B.job.id}`);
      return parts.join(' · ');
    },

    cmpProblems() {
      const { A, B } = this.cmp;
      const key = (p) => [p.kind, p.module, p.file, p.line, p.message].join('|');
      const ka = new Set(A.probs.map(key));
      const kb = new Set(B.probs.map(key));
      return { added: B.probs.filter((p) => !ka.has(key(p))), fixed: A.probs.filter((p) => !kb.has(key(p))) };
    },

    // ---------- problems ----------
    problemGroups() {
      const titles = { compile: 'Compile errors', test: 'Failed tests', goal: 'Failed goals', dependency: 'Dependency problems' };
      return Object.keys(titles)
        .map((kind) => ({ kind, title: titles[kind], items: this.jobProblems.filter((p) => p.kind === kind) }))
        .filter((g) => g.items.length);
    },

    shownProblems(g) {
      return this.probOpen[g.kind] ? g.items : g.items.slice(0, 8);
    },

    problemWhere(p) {
      if (p.kind === 'compile') return `${p.file.split(/[\\/]/).pop()}:${p.line}`;
      return '';
    },

    // Shows log line n: loads earlier lines if needed, switches to All, highlights it.
    async goToLine(n) {
      if (!n || !logView.el) return;
      logView.flush();
      if (this.firstLine && n < this.firstLine) {
        const to = this.firstLine - 1;
        const from = Math.max(1, n - 200);
        const page = await api('GET', `/jobs/${this.job.id}/log?from=${from}&limit=${to - from + 1}`);
        logView.prepend(page.lines);
        this.firstLine = from;
      }
      if (this.logFilter !== 'all') this.setLogFilter('all');
      if (this.logQuery) {
        this.logQuery = '';
        logView.search('');
      }
      await this.$nextTick();
      const node = logView.el.children[n - (this.firstLine || 1)];
      if (!node) return;
      logView.follow = false;
      logView.el.querySelectorAll('.hl').forEach((x) => x.classList.remove('hl'));
      node.classList.add('hl');
      logView.el.scrollTop = node.offsetTop - logView.el.offsetTop - logView.el.clientHeight / 3;
    },

    async loadEarlier() {
      const to = this.firstLine - 1;
      const from = Math.max(1, to - 4999);
      const page = await api('GET', `/jobs/${this.job.id}/log?from=${from}&limit=${to - from + 1}`);
      logView.prepend(page.lines);
      this.firstLine = from;
    },

    searchLog() {
      logView.search(this.logQuery);
    },

    setLogFilter(f) {
      this.logFilter = f;
      savePref('logFilter', f);
      this.$nextTick(() => { if (logView.follow && logView.el) logView.el.scrollTop = logView.el.scrollHeight; });
    },

    onLogScroll(e) {
      const el = e.target;
      logView.follow = el.scrollTop + el.clientHeight >= el.scrollHeight - 24;
    },

    shellJoin(argv) {
      return (argv || []).map(shellQuote).join(' ');
    },

    async loadActivity() {
      this.btasks = await api('GET', '/tasks');
      this.now = Date.now();
      // Times, not ids: task ids start over when the daemon restarts.
      const newest = this.btasks[0]?.queuedAt ?? '';
      if (this.view === 'activity' && newest && new Date(newest) > new Date(this.seenTasksAt || 0)) {
        this.seenTasksAt = newest;
        savePref('seenTasksAt', newest);
      }
    },

    // The nav dot: red for a failure not seen on the Activity page yet, blue while something runs.
    activityDot() {
      if (this.btasks.some((t) => t.status === 'failed' && new Date(t.queuedAt) > new Date(this.seenTasksAt || 0))) return 'failed';
      if (this.btasks.some((t) => t.status === 'running' || t.status === 'queued')) return 'running';
      return '';
    },

    taskLabel(t) {
      const names = { 'jdk.detect': 'JDK detection', 'tools.detect': 'Build tool detection', 'modules.discover': 'Module discovery' };
      let label = names[t.kind] ?? t.kind;
      const pid = Number(t.key.split(':')[1]);
      if (pid) label += ' · ' + (this.projects.find((p) => p.id === pid)?.name ?? 'project ' + pid);
      return label;
    },

    taskResult(t) {
      const r = t.result;
      if (r == null) return '';
      if (t.kind === 'tools.detect' && Array.isArray(r)) {
        return r.map((x) => `${x.id} ${x.path ? x.version || 'found' : 'not found'}`).join(', ');
      }
      if (t.kind === 'jdk.detect') return `${r.found} found, ${r.added} new, ${r.missing} missing`;
      if (t.kind === 'modules.discover') return `${r.modules} modules` + (r.offline ? ', offline' : '');
      return '';
    },

    // The first line that names the problem; the whole output is one click away.
    errorSummary(err) {
      const lines = (err || '').split('\n').map((l) => l.trim()).filter(Boolean);
      return lines.find((l) => /^\[(FATAL|ERROR)\]\s*\S/.test(l) && !/^\[ERROR\]\s*(\[ERROR\])?\s*Some problems/.test(l)) ?? lines[0] ?? '';
    },

    specText(j) {
      const s = j.spec || {};
      return [...(s.goals || []), ...(s.extraArgs || []).map(shellQuote)].join(' ') || this.shellJoin(j.argv);
    },

    when(t) {
      if (!t) return '';
      const d = new Date(t);
      const today = new Date().toDateString() === d.toDateString();
      return today ? d.toLocaleTimeString() : d.toLocaleString();
    },

    duration(j) {
      if (!j?.startedAt) return '';
      const end = j.finishedAt ? new Date(j.finishedAt) : new Date(this.now);
      const sec = Math.max(0, Math.round((end - new Date(j.startedAt)) / 1000));
      const h = Math.floor(sec / 3600);
      const m = Math.floor((sec % 3600) / 60);
      const ss = String(sec % 60).padStart(2, '0');
      return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
    },

    execLabel(mode) {
      return execNames[mode] ?? mode;
    },

    maven() {
      return this.settings.tools?.maven ?? { executable: 'auto', jdkId: null };
    },

    jdkText(id, fallback) {
      const j = this.jdks.find((x) => x.id === id);
      return j ? this.jdkLabel(j) : fallback;
    },

    // What "JDK for Maven: use the project JDK" resolves to in the project form.
    toolJdkFallbackText() {
      const pid = Number(this.pform.jdkId) || 0;
      if (pid) return 'Project JDK: ' + this.jdkText(pid, '?');
      return 'Global Maven default: ' + this.jdkText(this.maven().jdkId, 'daemon JAVA_HOME');
    },

    jdkFromText(from) {
      return { tool: 'Maven tab', project: 'project JDK', default: 'global Maven default' }[from] ?? '';
    },

    jdkLabel(j) {
      const name = j.name || j.label || j.vendor || '';
      return `${j.version || '?'}${name ? ' · ' + name : ''}${j.arch ? ' · ' + j.arch : ''}${j.missing ? ' (not found)' : ''}`;
    },

    // ---------- global settings ----------
    async loadSettings() {
      const [settings, tools, jdks, managers] = await Promise.all([
        api('GET', '/settings'), api('GET', '/tools'), api('GET', '/jdks'), api('GET', '/jdk-managers'),
      ]);
      Object.assign(this, { settings, tools, jdks, managers });
      this.cform = await api('GET', '/config');
      this.cfgSaved = false;
      const mv = settings.tools?.maven ?? {};
      this.sform = { maven: { executable: mv.executable || 'auto', jdkId: mv.jdkId ?? 0 } };
    },

    async saveSettings() {
      this.busy = true;
      this.saved = false;
      try {
        const mv = this.sform.maven;
        this.settings = await api('PUT', '/settings', {
          tools: { maven: { executable: mv.executable, jdkId: Number(mv.jdkId) || null } },
        });
        this.saved = true;
      } catch (e) {
        this.error = e.message;
      } finally {
        this.busy = false;
      }
    },

    async saveConfig() {
      this.busy = true;
      this.cfgSaved = false;
      try {
        const { maxConcurrentJobs, jobsPerProject } = this.cform;
        this.cform = await api('PUT', '/config', { maxConcurrentJobs, jobsPerProject });
        this.cfgSaved = true;
      } catch (e) {
        this.error = e.message;
      } finally {
        this.busy = false;
      }
    },

    async detect() {
      try {
        this.followDetect(await api('POST', '/jdks/detect'));
      } catch (e) {
        this.jdkError = e.message;
      }
    },

    async followDetect(task) {
      this.detectTask = task;
      this.detectResult = '';
      try {
        const t = await waitTask(task.id);
        if (t.status === 'failed') {
          this.jdkError = 'JDK detection failed: ' + t.error;
        } else {
          const r = t.result || {};
          this.detectResult = `Found ${r.found ?? 0}, new ${r.added ?? 0}, missing ${r.missing ?? 0}.`;
        }
        [this.jdks, this.managers] = await Promise.all([api('GET', '/jdks'), api('GET', '/jdk-managers')]);
      } catch (e) {
        this.jdkError = e.message;
      } finally {
        this.detectTask = null;
      }
    },

    async detectTools() {
      try {
        this.toolsTask = await api('POST', '/tools/detect');
        const t = await waitTask(this.toolsTask.id);
        if (t.status === 'failed') this.error = 'Build tool detection failed: ' + t.error;
        this.tools = await api('GET', '/tools');
      } catch (e) {
        this.error = e.message;
      } finally {
        this.toolsTask = null;
      }
    },

    // JDKs grouped by the manager they came from: installed managers first (in their order),
    // then sources that are not managers (manual entries, JAVA_HOME, ...).
    jdkGroups() {
      const extra = {
        manual: { title: 'Added manually' },
        JAVA_HOME: { title: 'JAVA_HOME', description: 'the JAVA_HOME the daemon was started with' },
      };
      const groups = [];
      const seen = new Set();
      for (const m of this.managers) {
        const jdks = this.jdks.filter((j) => j.source === m.id);
        if (!m.installed && !jdks.length) continue;
        seen.add(m.id);
        groups.push({ key: m.id, title: m.name, kind: m.kind, location: m.location, description: m.description, jdks });
      }
      for (const j of this.jdks) {
        if (seen.has(j.source)) continue;
        seen.add(j.source);
        const e = extra[j.source] ?? { title: j.source };
        groups.push({ key: j.source, ...e, jdks: this.jdks.filter((x) => x.source === j.source) });
      }
      return groups;
    },

    missingManagers() {
      return this.managers.filter((m) => !m.installed && !this.jdks.some((j) => j.source === m.id));
    },

    async addJdk() {
      this.jdkError = '';
      try {
        await api('POST', '/jdks', { home: this.jdkPath });
        this.jdkPath = '';
        this.jdks = await api('GET', '/jdks');
      } catch (e) {
        this.jdkError = e.message;
      }
    },

    async renameJdk(j, name) {
      if ((j.name || '') === name.trim()) return;
      try {
        Object.assign(j, await api('PATCH', `/jdks/${j.id}`, { name }));
      } catch (e) {
        this.jdkError = e.message;
      }
    },

    async deleteJdk(j) {
      if (!confirm(`Remove JDK ${j.version} from the list? The JDK itself is not deleted; the next detection may find it again.`)) return;
      try {
        await api('DELETE', `/jdks/${j.id}`);
        this.jdks = this.jdks.filter((x) => x.id !== j.id);
      } catch (e) {
        this.jdkError = e.message;
      }
    },
  }));
});
