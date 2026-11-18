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

// Module graphs per module list, kept outside Alpine so building them does not
// write reactive state during rendering.
const graphCache = new WeakMap();

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

function emptyForm() {
  return {
    goals: 'clean install', args: '', modulesMode: 'all', selected: [], alsoMake: true, alsoMakeDependents: false,
    skips: [], offline: false, nsu: false, ntp: true, errors: false, threads: '', failMode: '', logLevel: 'default',
    profiles: '', props: '',
  };
}

// RunSpec <-> run form.
function specToForm(spec) {
  const f = emptyForm();
  if (!spec) return f;
  const m = spec.modules || {};
  const fl = spec.flags || {};
  return {
    goals: (spec.goals || []).join(' '),
    args: (spec.extraArgs || []).map(shellQuote).join(' '),
    modulesMode: m.mode === 'selected' ? 'selected' : 'all',
    selected: [...(m.list || [])],
    alsoMake: m.mode === 'selected' ? !!m.alsoMake : true,
    alsoMakeDependents: !!m.alsoMakeDependents,
    skips: [...(spec.skips || [])],
    offline: !!fl.offline, nsu: !!fl.noSnapshotUpdates, ntp: spec.flags ? !!fl.noTransferProgress : true,
    errors: !!fl.errors, threads: fl.threads || '', failMode: fl.failMode || '', logLevel: spec.logLevel || 'default',
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
    flags: { offline: f.offline, noSnapshotUpdates: f.nsu, threads: f.threads.trim(), noTransferProgress: f.ntp, failMode: f.failMode, errors: f.errors },
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
    discoverTask: null,
    discoverError: '',
    profiles: [],
    editingTask: null,
    saveAsNew: false,
    taskName: '',
    copied: false,
    rpreview: null,
    runError: '',

    job: null,
    jobProject: null,
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

    async init() {
      window.addEventListener('hashchange', () => this.route());
      try {
        this.health = await api('GET', '/health');
      } catch (e) {
        this.error = e.message;
      }
      await this.route();
      // The daemon scans JDKs at startup; pick up a scan that is still running.
      const running = (await api('GET', '/tasks')).find((t) => t.kind === 'jdk.detect' && (t.status === 'queued' || t.status === 'running'));
      if (running) this.followDetect(running);
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
        } else if ((m = h.match(/^\/jobs\/(\d+)$/))) {
          this.view = 'job';
          await this.openJob(Number(m[1]));
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
      const sel = new Set(this.selectedKeys());
      return this.modules.every((m) => sel.has(m.key));
    },

    // Notes of the ticked skip presets, e.g. why skipping test compilation can break a build.
    skipNotes() {
      return this.skipPresets.filter((sp) => sp.note && this.rform.skips.includes(sp.id) && sp.id !== 'tests')
        .map((sp) => `${sp.label}: ${sp.note}`);
    },

    specSummary(spec) {
      const s = spec || {};
      const parts = [...(s.goals || [])];
      if (s.modules?.mode === 'selected') parts.push(`-pl ${(s.modules.list || []).join(',')}${s.modules.alsoMake ? ' -am' : ''}${s.modules.alsoMakeDependents ? ' -amd' : ''}`);
      if (s.skips?.length) parts.push('skip ' + s.skips.join(','));
      if (s.profiles?.length) parts.push('-P ' + s.profiles.join(','));
      if (s.flags?.threads) parts.push('-T ' + s.flags.threads);
      return parts.join(' · ');
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
      const dup = this.modules.filter((x) => x.artifactId === m.artifactId).length > 1;
      return dup ? m.key : ':' + m.artifactId;
    },

    hasTree() {
      return this.modules.some((m) => m.aggregator);
    },

    // Rebuilt when modules change: lookups and the aggregator / dependency graphs.
    graph() {
      const cached = graphCache.get(this.modules);
      if (cached) return cached;
      const byKey = new Map(this.modules.map((m) => [m.key, m]));
      const kids = new Map();
      const users = new Map();
      for (const m of this.modules) {
        if (m.aggregator && byKey.has(m.aggregator)) {
          if (!kids.has(m.aggregator)) kids.set(m.aggregator, []);
          kids.get(m.aggregator).push(m);
        }
        for (const d of m.deps || []) {
          if (!users.has(d)) users.set(d, []);
          users.get(d).push(m.key);
        }
      }
      const roots = this.modules.filter((m) => !m.aggregator || !byKey.has(m.aggregator));
      const g = { byKey, kids, users, roots };
      graphCache.set(this.modules, g);
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

    subtree(m) {
      const g = this.graph();
      const out = [];
      const walk = (x) => {
        out.push(x);
        for (const c of g.kids.get(x.key) || []) walk(c);
      };
      walk(m);
      return out;
    },

    refToKey(ref) {
      if (!ref.startsWith(':')) return ref;
      return this.modules.find((m) => m.artifactId === ref.slice(1))?.key ?? ref;
    },

    selectedKeys() {
      return this.rform.selected.map((r) => this.refToKey(r));
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
    // when filtering or when discovery had no folder information.
    shownRows() {
      const q = this.moduleFilter.trim().toLowerCase();
      if (q || !this.hasTree()) {
        return this.filteredModules().slice(0, 300).map((m) => ({ m, depth: 0, kids: 0, open: false, flat: true }));
      }
      const g = this.graph();
      const rows = [];
      const walk = (m, depth) => {
        const kids = g.kids.get(m.key) || [];
        const open = this.isOpen(m.key, depth);
        rows.push({ m, depth, kids: kids.length, open });
        if (open) for (const c of kids) walk(c, depth + 1);
      };
      for (const r of g.roots) walk(r, 0);
      return rows;
    },

    isOpen(key, depth) {
      return key in this.openState ? this.openState[key] : depth === 0;
    },

    toggleOpen(key) {
      const depth = this.shownRows().find((r) => r.m.key === key)?.depth ?? 1;
      this.openState = { ...this.openState, [key]: !this.isOpen(key, depth) };
    },

    setAllOpen(open) {
      const st = {};
      for (const k of this.graph().kids.keys()) st[k] = open;
      this.openState = st;
    },

    filteredModules() {
      const q = this.moduleFilter.trim().toLowerCase();
      if (!q) return this.modules;
      return this.modules.filter((m) => m.name.toLowerCase().includes(q) || m.key.toLowerCase().includes(q) || (m.relPath || '').toLowerCase().includes(q));
    },

    moreModules() {
      const q = this.moduleFilter.trim();
      if (!q && this.hasTree()) return 0;
      return Math.max(0, this.filteredModules().length - 300);
    },

    // all | some | none for a row: a parent counts its whole subtree.
    checkState(r) {
      const sel = new Set(this.selectedKeys());
      const keys = r.kids && !r.flat ? this.subtree(r.m).map((m) => m.key) : [r.m.key];
      const n = keys.filter((k) => sel.has(k)).length;
      return n === 0 ? 'none' : n === keys.length ? 'all' : 'some';
    },

    // Ticking a tree parent ticks everything under it.
    toggleModule(r) {
      const group = r.kids && !r.flat ? this.subtree(r.m) : [r.m];
      const refs = group.map((m) => this.moduleRef(m));
      const on = this.checkState(r) !== 'all';
      const sel = this.rform.selected.filter((x) => !refs.includes(x));
      this.rform.selected = on ? [...sel, ...refs] : sel;
      if (this.rform.selected.length) this.rform.modulesMode = 'selected';
      this.refreshPreview();
    },

    selectShown() {
      const add = this.shownRows().map((r) => this.moduleRef(r.m)).filter((x) => !this.rform.selected.includes(x));
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

    async copyCommand() {
      try {
        await navigator.clipboard.writeText(this.shellJoin(this.rpreview.argv));
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
