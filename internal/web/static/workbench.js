// verix-dbm — workbench shell: DataGrip-style tabs, connection modal, and a
// right-click context menu. State lives in Alpine; content loads via HTMX.
document.addEventListener('alpine:init', () => {
  Alpine.data('workbench', () => ({
    tabs: [],
    active: null,
    menuOpen: false,
    modal: { open: false, kind: 'postgres', port: '5432' },
    // right-click context menu
    ctx: { open: false, x: 0, y: 0, type: null, id: null, connId: null, name: '', schema: '', table: '', kind: '' },

    // open or focus a tab. tab = {key, title, url, icon}
    openTab(tab) {
      if (!this.tabs.some((t) => t.key === tab.key)) this.tabs.push(tab);
      this.select(tab.key);
    },

    select(key) {
      this.active = key;
      const t = this.tabs.find((x) => x.key === key);
      if (!t) return;
      this.$nextTick(() => {
        window.htmx && htmx.ajax('GET', t.url, { target: '#tab-content', swap: 'innerHTML' });
      });
    },

    closeTab(key, ev) {
      if (ev) ev.stopPropagation();
      const i = this.tabs.findIndex((t) => t.key === key);
      if (i < 0) return;
      this.tabs.splice(i, 1);
      if (this.active !== key) return;
      const next = this.tabs[i] || this.tabs[i - 1];
      if (next) this.select(next.key);
      else {
        this.active = null;
        const c = document.getElementById('tab-content');
        if (c) c.innerHTML = '';
      }
    },

    openModal(kind) {
      this.menuOpen = false;
      this.modal.kind = kind || 'postgres';
      this.modal.port = this.modal.kind === 'redis' ? '6379' : '5432';
      this.modal.open = true;
    },

    // ── context menu ──
    openCtx(ev, d) {
      ev.preventDefault();
      ev.stopPropagation();
      Object.assign(this.ctx, { schema: '', table: '', kind: '' }, d, { open: true, x: ev.clientX, y: ev.clientY });
    },
    closeCtx() { this.ctx.open = false; },

    ctxBrowse() {
      const c = this.ctx;
      this.openTab({
        key: `grid:${c.connId}:${c.schema}.${c.table}`,
        title: `${c.schema}.${c.table}`,
        url: `/c/${c.connId}/grid?schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}`,
        icon: 'grid',
      });
      this.closeCtx();
    },
    ctxConsole() {
      const c = this.ctx;
      this.openTab({ key: `console:${c.id}`, title: `console [${c.name}]`, url: `/c/${c.id}/console`, icon: 'console' });
      this.closeCtx();
    },
    ctxRefresh() {
      const t = document.getElementById('cc-' + this.ctx.id);
      if (t && window.htmx) {
        const d = t.closest('details');
        if (d) d.open = true;
        htmx.ajax('GET', `/c/${this.ctx.id}/explorer`, { target: '#cc-' + this.ctx.id, swap: 'innerHTML' });
      }
      this.closeCtx();
    },
    copy(text) {
      if (navigator.clipboard) navigator.clipboard.writeText(text);
      this.closeCtx();
    },
    ctxCopySelect() {
      const c = this.ctx;
      this.copy(`SELECT * FROM "${c.schema}"."${c.table}" LIMIT 100;`);
    },
    ctxProps() {
      const id = this.ctx.id;
      this.closeCtx();
      if (window.htmx) htmx.ajax('GET', `/c/${id}/edit`, { target: '#editmodal-host', swap: 'innerHTML' });
    },
  }));
});
