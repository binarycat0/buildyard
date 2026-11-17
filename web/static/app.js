import { api } from './api.js';

const statusText = {
  queued: 'в очереди', running: 'идёт', succeeded: 'успешно', failed: 'ошибка',
  cancelled: 'отменена', interrupted: 'прервана',
};

document.addEventListener('alpine:init', () => {
  window.Alpine.data('projectsPage', () => ({
    projects: [],
    health: null,
    loaded: false,
    error: '',
    adding: false,
    busy: false,
    form: { rootPath: '', name: '' },
    formError: '',
    editing: null,
    editName: '',

    async load() {
      try {
        [this.projects, this.health] = await Promise.all([api('GET', '/projects'), api('GET', '/health')]);
        this.error = '';
      } catch (e) {
        this.error = e.message;
      } finally {
        this.loaded = true;
      }
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
        this.projects = [...this.projects, p].sort((a, b) => a.name.localeCompare(b.name));
        this.form = { rootPath: '', name: '' };
        this.adding = false;
      } catch (e) {
        this.formError = e.message;
      } finally {
        this.busy = false;
      }
    },

    async rename(p) {
      try {
        const updated = await api('PATCH', `/projects/${p.id}`, { name: this.editName });
        Object.assign(p, updated);
        this.editing = null;
      } catch (e) {
        this.error = e.message;
      }
    },

    async remove(p) {
      if (!confirm(`Удалить проект «${p.name}» из Buildyard? Файлы проекта не трогаются.`)) return;
      try {
        await api('DELETE', `/projects/${p.id}`);
        this.projects = this.projects.filter((x) => x.id !== p.id);
      } catch (e) {
        this.error = e.message;
      }
    },

    lastJobText(p) {
      if (!p.lastJob) return 'Запусков ещё не было';
      const j = p.lastJob;
      let s = 'Последний запуск: ' + (statusText[j.status] ?? j.status);
      if (j.startedAt && j.finishedAt) {
        const sec = Math.round((new Date(j.finishedAt) - new Date(j.startedAt)) / 1000);
        s += `, ${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`;
      }
      return s;
    },
  }));
});
