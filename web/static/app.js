import { api } from './api.js';

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
  reset(el) {
    this.el = el;
    this.pending = [];
    this.follow = true;
    if (el) el.textContent = '';
  },
  lineNode(text) {
    const d = document.createElement('div');
    const head = text.slice(0, 80);
    if (/\[ERROR\]|BUILD FAILURE/.test(head)) d.className = 'l-error';
    else if (/\[WARNING\]|\[WARN\]/.test(head)) d.className = 'l-warn';
    else if (/BUILD SUCCESS/.test(head)) d.className = 'l-ok';
    d.textContent = text;
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
    for (const t of this.pending) frag.appendChild(this.lineNode(t));
    this.pending = [];
    this.el.appendChild(frag);
    if (this.follow) this.el.scrollTop = this.el.scrollHeight;
  },
  prepend(lines) {
    if (!this.el) return;
    const frag = document.createDocumentFragment();
    for (const t of lines) frag.appendChild(this.lineNode(t));
    const before = this.el.scrollHeight;
    this.el.insertBefore(frag, this.el.firstChild);
    this.el.scrollTop += this.el.scrollHeight - before;
  },
};

let stream = null;
let ticker = 0;

function closeStream() {
  if (stream) stream.close();
  stream = null;
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
    rform: { goals: 'clean install', args: '' },
    rpreview: null,
    runError: '',

    job: null,
    jobProject: null,
    firstLine: 0,
    logCount: 0,
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
      const [p, runtime, jdks, settings, tools, pjobs] = await Promise.all([
        api('GET', `/projects/${id}`), api('GET', `/projects/${id}/runtime`),
        api('GET', '/jdks'), api('GET', '/settings'), api('GET', '/tools'), api('GET', `/projects/${id}/jobs`),
      ]);
      Object.assign(this, { proj: p, runtime, jdks, settings, tools, pjobs });
      // Start from the last run's spec.
      const last = pjobs.find((j) => j.spec?.goals?.length);
      this.rform = last
        ? { goals: last.spec.goals.join(' '), args: (last.spec.extraArgs || []).map(shellQuote).join(' ') }
        : { goals: 'clean install', args: '' };
      this.runError = '';
      this.refreshPreview();
      const exe = p.executable ?? '';
      const known = ['', 'auto', 'wrapper', 'mvnd', 'mvn'].includes(exe);
      this.pform = {
        name: p.name,
        execChoice: known ? exe : 'custom',
        customPath: known ? '' : exe,
        jdkId: p.jdkId ?? 0,
        toolJdkId: p.toolJdkId ?? 0,
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
          env: textToEnv(f.envText),
        });
        this.proj = p;
        this.runtime = await api('GET', `/projects/${p.id}/runtime`);
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
      return { kind: 'maven', goals: splitArgs(this.rform.goals), extraArgs: splitArgs(this.rform.args) };
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

    async cancelJob() {
      try {
        await api('POST', `/jobs/${this.job.id}/cancel`);
      } catch (e) {
        this.error = e.message;
      }
    },

    async openJob(id) {
      closeStream();
      const job = await api('GET', `/jobs/${id}`);
      this.job = job;
      this.firstLine = 0;
      this.logCount = job.liveLines;
      if (this.jobProject?.id !== job.projectId) this.jobProject = await api('GET', `/projects/${job.projectId}`);
      await this.$nextTick();
      logView.reset(this.$refs.log);
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
