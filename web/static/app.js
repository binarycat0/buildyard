import { api } from './api.js';

const statusText = {
  queued: 'queued', running: 'running', succeeded: 'succeeded', failed: 'failed',
  cancelled: 'cancelled', interrupted: 'interrupted',
};
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
        if ((m = h.match(/^\/projects\/(\d+)$/))) {
          this.view = 'project';
          await this.loadProject(Number(m[1]));
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
      let s = 'Last run: ' + (statusText[j.status] ?? j.status);
      if (j.startedAt && j.finishedAt) {
        const sec = Math.round((new Date(j.finishedAt) - new Date(j.startedAt)) / 1000);
        s += `, ${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`;
      }
      return s;
    },

    // ---------- project settings ----------
    async loadProject(id) {
      const [p, runtime, jdks, settings, tools] = await Promise.all([
        api('GET', `/projects/${id}`), api('GET', `/projects/${id}/runtime`),
        api('GET', '/jdks'), api('GET', '/settings'), api('GET', '/tools'),
      ]);
      Object.assign(this, { proj: p, runtime, jdks, settings, tools });
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
      } catch (e) {
        this.pformError = e.message;
      } finally {
        this.busy = false;
      }
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
