// verix-dbm — workbench shell: DataGrip-style tabs, connection modal, and a
// right-click context menu with nested fly-out submenus. State lives in Alpine;
// content loads via HTMX. The menu is data-driven: openCtx() builds a model with
// buildMenu() and a small recursive template renders it.
document.addEventListener('alpine:init', () => {
  Alpine.data('workbench', () => ({
    tabs: [],
    active: null,
    menuOpen: false,
    drawer: false, // mobile: explorer slides in as an off-canvas drawer
    modal: { open: false, kind: 'postgres', port: '5432' },
    caps: { admin: false, write: false, csrf: '' },
    touch: false,
    // right-click context menu
    ctx: { open: false, x: 0, y: 0, sheet: false, subFlip: false, openSub: null,
           type: null, id: '', connId: '', name: '', schema: '', table: '', kind: '', def: '', items: [] },

    init() {
      const el = this.$el; // .ide
      this.caps = {
        admin: el.dataset.admin === '1',
        write: el.dataset.write === '1',
        csrf: el.dataset.csrf || '',
      };
      this.touch = ('ontouchstart' in window) || navigator.maxTouchPoints > 0;
      // DDL forms ask the server (via HX-Trigger) to refresh the affected tree.
      document.body.addEventListener('verix:refreshConn', (ev) => {
        this.refreshConn(ev.detail);
        this.closeDDL();
      });
    },

    sheetMode() { return window.matchMedia('(max-width: 900px)').matches; },

    // ── tabs ──
    openTab(tab) {
      if (!this.tabs.some((t) => t.key === tab.key)) this.tabs.push(tab);
      this.select(tab.key);
    },
    select(key) {
      this.active = key;
      this.drawer = false; // opening a tab on mobile closes the explorer drawer
      const t = this.tabs.find((x) => x.key === key);
      if (!t) return;
      this.$nextTick(() => {
        window.htmx && htmx.ajax('GET', t.url, { target: '#tab-content', swap: 'innerHTML' });
        // keep the active tab visible when the bar overflows on narrow screens
        const tabEl = this.$root.querySelector('.tab.on');
        if (tabEl && tabEl.scrollIntoView) tabEl.scrollIntoView({ inline: 'nearest', block: 'nearest' });
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

    // Parse a connection URL like postgresql://user:pass@host:5432/db?sslmode=require
    // into the individual fields. Returns null if it isn't a usable URL.
    parseConnUrl(url) {
      url = (url || '').trim();
      if (!url) return null;
      let u;
      try { u = new URL(url); } catch (e) { return null; }
      if (!u.hostname) return null;
      let kind = u.protocol.replace(':', '').toLowerCase();
      if (kind === 'postgresql' || kind === 'postgres' || kind === 'pg') kind = 'postgres';
      else if (kind === 'redis' || kind === 'rediss' || kind === 'valkey') kind = 'redis';
      return {
        kind,
        host: u.hostname,
        port: u.port || (kind === 'redis' ? '6379' : '5432'),
        username: decodeURIComponent(u.username || ''),
        password: decodeURIComponent(u.password || ''),
        dbname: decodeURIComponent(u.pathname.replace(/^\//, '')),
        options: u.search ? u.search.slice(1) : '',
      };
    },
    // Fired when the URL field changes: fill the rest of the form from the URL.
    applyUrl(ev) {
      const p = this.parseConnUrl(ev.target.value);
      if (!p) return;
      const form = ev.target.closest('form');
      if (!form) return;
      const set = (name, val) => {
        const el = form.querySelector(`[name="${name}"]`);
        if (el && val !== undefined && val !== '') el.value = val;
      };
      set('host', p.host);
      set('username', p.username);
      set('password', p.password);
      set('dbname', p.dbname);
      set('options', p.options);
      // kind + port may be x-model-bound (create modal) → sync Alpine state too.
      if (this.modal && this.modal.open) { this.modal.kind = p.kind; this.modal.port = p.port; }
      else { set('kind', p.kind); set('port', p.port); }
      const nameEl = form.querySelector('[name="name"]');
      if (nameEl && !nameEl.value) nameEl.value = (p.username ? p.username + '@' : '') + p.host;
    },

    // ── context menu plumbing ──

    // dataFor builds the ctx payload from a tree row's data-* attributes, so the
    // desktop right-click, the ⋯ kebab, and touch long-press all share one source.
    dataFor(el, type) {
      const d = el ? el.dataset : {};
      return ({
        conn:   { type: 'conn', id: d.id, connId: d.id, name: d.name, kind: d.kind },
        schema: { type: 'schema', connId: d.conn, schema: d.schema, name: d.schema },
        table:  { type: 'table', connId: d.conn, schema: d.schema, table: d.table, name: d.table },
        col:    { type: 'col', connId: d.conn, schema: d.schema, table: d.table, name: d.name },
        key:    { type: 'key', connId: d.conn, schema: d.schema, table: d.table, name: d.name, def: d.def },
        index:  { type: 'index', connId: d.conn, schema: d.schema, table: d.table, name: d.name, def: d.def },
      })[type] || {};
    },
    cid() { return this.ctx.connId || this.ctx.id; },

    openCtx(ev, d) {
      ev.preventDefault();
      ev.stopPropagation && ev.stopPropagation();
      const sheet = this.sheetMode();
      // desktop: clamp to viewport. sheet mode: ignored (CSS pins it to the bottom)
      const mw = 240, mh = 320;
      const x = Math.max(8, Math.min(ev.clientX, window.innerWidth - mw - 8));
      const y = Math.max(8, Math.min(ev.clientY, window.innerHeight - mh - 8));
      Object.assign(this.ctx,
        { schema: '', table: '', kind: '', def: '', id: '', connId: '' },
        d,
        { open: true, x, y, sheet, openSub: null, subFlip: x + mw + 220 > window.innerWidth });
      this.ctx.items = this.buildMenu(this.ctx);
    },
    // ⋯ kebab — same menu, sourced from the row's data-* attributes.
    kebab(ev, type) {
      ev.stopPropagation();
      this.openCtx(ev, this.dataFor(ev.currentTarget.closest('.tree-row'), type));
    },
    closeCtx() { this.ctx.open = false; this.ctx.openSub = null; },

    // ── touch long-press (opens the same menu as the bottom sheet) ──
    pressStart(ev, type) {
      if (!ev.touches || ev.touches.length !== 1) return;
      const t = ev.touches[0];
      const data = this.dataFor(ev.currentTarget.closest('.tree-row'), type);
      this._press = { x: t.clientX, y: t.clientY };
      this._pressTimer = setTimeout(() => {
        this.longPressed = true;
        this.openCtx({ clientX: this._press.x, clientY: this._press.y, preventDefault() {}, stopPropagation() {} }, data);
        if (navigator.vibrate) { try { navigator.vibrate(8); } catch (e) {} }
      }, 450);
    },
    pressMove(ev) {
      if (!this._press || !ev.touches || !ev.touches.length) return;
      const t = ev.touches[0];
      if (Math.abs(t.clientX - this._press.x) > 10 || Math.abs(t.clientY - this._press.y) > 10) this.pressCancel();
    },
    pressEnd(ev) {
      this.pressCancel();
      if (this.longPressed) { if (ev && ev.cancelable) ev.preventDefault(); this.longPressed = false; }
    },
    pressCancel() { clearTimeout(this._pressTimer); this._press = null; },

    // ── submenu open/close ──
    subEnter(i) {
      if (this.ctx.sheet) return; // sheet uses tap-to-expand, not hover
      clearTimeout(this._subTimer);
      this._subTimer = setTimeout(() => { this.ctx.openSub = i; }, 120);
    },
    subLeave() {
      if (this.ctx.sheet) return;
      clearTimeout(this._subTimer);
      this._subTimer = setTimeout(() => { this.ctx.openSub = null; }, 200);
    },
    itemClick(it, i) {
      if (it.children) { this.ctx.openSub = this.ctx.openSub === i ? null : i; return; }
      if (it.run) it.run();
    },

    // ── menu model ──
    buildMenu(c) {
      const w = this.caps.write, a = this.caps.admin;
      const SEP = { sep: true };
      const m = [];
      if (c.type === 'conn') {
        m.push({ head: c.name });
        m.push({ label: 'Query console', icon: 'ico ico-console', key: 'Ctrl+⏎', run: () => this.ctxConsole() });
        if (w) m.push({ label: 'New…', glyph: '＋', children: [
          { label: 'Query console', icon: 'ico ico-console', run: () => this.ctxConsole() },
          { label: 'Schema…', glyph: '▤', run: () => this.openForm('new-schema') },
        ] });
        m.push({ label: 'Refresh', glyph: '↻', key: 'Ctrl+F5', run: () => { this.closeCtx(); this.refreshConn(this.cid()); } });
        m.push(SEP);
        m.push({ label: 'Copy name', glyph: '⎘', run: () => this.copy(c.name) });
        if (a) {
          m.push({ label: 'Properties', glyph: '⚙', key: 'F4', run: () => this.ctxProps() });
          m.push({ label: 'Duplicate…', glyph: '⎘', run: () => this.ctxProps() });
          m.push(SEP);
          m.push({ label: 'Remove data source', glyph: '✕', key: 'Del', danger: true, run: () => this.removeDataSource() });
        }
      } else if (c.type === 'schema') {
        m.push({ head: c.schema });
        m.push({ label: 'Query console', icon: 'ico ico-console', run: () => this.ctxConsole() });
        if (w) m.push({ label: 'New…', glyph: '＋', children: [
          { label: 'Table…', glyph: '▦', run: () => this.openForm('new-table') },
        ] });
        m.push({ label: 'Refresh', glyph: '↻', key: 'Ctrl+F5', run: () => { this.closeCtx(); this.refreshConn(this.cid()); } });
        m.push(SEP);
        m.push({ label: 'Copy name', glyph: '⎘', run: () => this.copy(c.schema) });
      } else if (c.type === 'table') {
        m.push({ head: c.schema + '.' + c.table });
        m.push({ label: 'Open data', icon: 'ico ico-table', key: 'F4', run: () => this.ctxBrowse() });
        m.push({ label: 'Generate', glyph: '▷', children: [
          { label: 'SELECT', glyph: '▷', run: () => this.generate('select') },
          { label: 'INSERT', glyph: '▷', run: () => this.generate('insert') },
          { label: 'UPDATE', glyph: '▷', run: () => this.generate('update') },
          { label: 'CREATE (DDL)', glyph: '▷', run: () => this.generate('create') },
        ] });
        m.push({ label: 'Export', glyph: '⭳', children: [
          { label: 'CSV', glyph: '⭳', run: () => this.exportAs('csv') },
          { label: 'JSON', glyph: '⭳', run: () => this.exportAs('json') },
        ] });
        m.push(SEP);
        m.push({ label: 'Copy name', glyph: '⎘', run: () => this.copy(c.table) });
        m.push({ label: 'Copy qualified name', glyph: '⎘', run: () => this.copy(c.schema + '.' + c.table) });
        m.push({ label: 'Copy DDL', glyph: '❑', run: () => this.generate('create') });
        m.push(SEP);
        m.push({ label: 'Quick documentation', glyph: 'ⓘ', key: 'Ctrl+Q', run: () => this.quickDoc() });
        m.push({ label: 'Find usages', glyph: '⌕', key: 'Alt+F7', run: () => this.findUsages() });
        if (w) {
          m.push(SEP);
          m.push({ label: 'Modify table', glyph: '✎', children: [
            { label: 'Add column…', glyph: '＋', run: () => this.openForm('add-column') },
            { label: 'Create index…', glyph: '⊞', run: () => this.openForm('new-index') },
          ] });
          m.push({ label: 'Rename…', glyph: '✎', run: () => this.openForm('rename-table') });
          m.push({ label: 'Truncate…', glyph: '∅', danger: true, run: () => this.truncate() });
        }
        if (a) m.push({ label: 'Drop table…', glyph: '✕', danger: true, run: () => this.dropTable() });
        m.push(SEP);
        m.push({ label: 'Refresh', glyph: '↻', run: () => { this.closeCtx(); this.refreshConn(this.cid()); } });
      } else if (c.type === 'col') {
        m.push({ head: c.name });
        m.push({ label: 'Copy name', glyph: '⎘', run: () => this.copy(c.name) });
        m.push({ label: 'Copy qualified name', glyph: '⎘', run: () => this.copy(c.schema + '.' + c.table + '.' + c.name) });
        if (w) {
          m.push(SEP);
          m.push({ label: 'Modify column…', glyph: '✎', run: () => this.openForm('modify-column', { column: c.name }) });
        }
        if (a) m.push({ label: 'Drop column…', glyph: '✕', danger: true, run: () => this.dropColumn() });
      } else if (c.type === 'key' || c.type === 'index') {
        m.push({ head: c.name });
        m.push({ label: 'Copy name', glyph: '⎘', run: () => this.copy(c.name) });
        if (c.def) m.push({ label: 'Copy definition', glyph: '⎘', run: () => this.copy(c.def) });
        if (c.type === 'index' && a) {
          m.push(SEP);
          m.push({ label: 'Drop index…', glyph: '✕', danger: true, run: () => this.dropIndex() });
        }
      }
      return m;
    },

    // ── actions ──
    ctxBrowse() {
      const c = this.ctx, id = this.cid();
      this.openTab({
        key: `grid:${id}:${c.schema}.${c.table}`,
        title: `${c.schema}.${c.table}`,
        url: `/c/${id}/grid?schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}`,
        icon: 'grid',
      });
      this.closeCtx();
    },
    ctxConsole() {
      const c = this.ctx, id = this.cid();
      this.openTab({ key: `console:${id}`, title: `console [${c.name || id}]`, url: `/c/${id}/console`, icon: 'console' });
      this.closeCtx();
    },
    refreshConn(id) {
      const host = document.getElementById('cc-' + id);
      if (host && window.htmx) {
        const d = host.closest('details');
        if (d) d.open = true;
        htmx.ajax('GET', `/c/${id}/explorer`, { target: '#cc-' + id, swap: 'innerHTML' });
      }
    },
    doCopy(text) { if (navigator.clipboard) navigator.clipboard.writeText(text); },
    copy(text) { this.doCopy(text); this.closeCtx(); },
    ctxProps() {
      const id = this.cid();
      this.closeCtx();
      if (window.htmx) htmx.ajax('GET', `/c/${id}/edit`, { target: '#editmodal-host', swap: 'innerHTML' });
    },
    removeDataSource() {
      const c = this.ctx;
      this.closeCtx();
      if (confirm('Remove data source “' + c.name + '”?')) {
        const f = this.$refs['del' + c.id];
        if (f) f.submit();
      }
    },

    // generate/copy SQL text (read-only endpoints → clipboard)
    generate(kind) {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      const u = `/c/${id}/pg/generate?kind=${kind}&schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}`;
      fetch(u).then(async (r) => {
        if (!r.ok) { alert(await r.text()); return; }
        this.doCopy(await r.text());
      });
    },
    quickDoc() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      this.openTab({ key: `doc:${id}:${c.schema}.${c.table}`, title: `doc [${c.table}]`,
        url: `/c/${id}/pg/doc?schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}`, icon: 'grid' });
    },
    findUsages() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      this.openTab({ key: `usages:${id}:${c.schema}.${c.table}`, title: `usages [${c.table}]`,
        url: `/c/${id}/pg/usages?schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}`, icon: 'grid' });
    },
    exportAs(format) {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      window.open(`/c/${id}/export?schema=${encodeURIComponent(c.schema)}&table=${encodeURIComponent(c.table)}&format=${format}`, '_blank');
    },

    // ── DDL: confirm-then-POST (fetch) and form modals ──
    post(url, data) {
      const body = new URLSearchParams(Object.assign({ csrf: this.caps.csrf }, data));
      return fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body })
        .then(async (r) => { if (!r.ok) { const t = await r.text(); alert(t); throw new Error(t); } return r; });
    },
    dropTable() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      if (!confirm(`Drop table ${c.schema}.${c.table}? This cannot be undone.`)) return;
      this.post(`/c/${id}/pg/table/drop`, { schema: c.schema, table: c.table }).then(() => this.refreshConn(id)).catch(() => {});
    },
    truncate() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      if (!confirm(`Truncate ${c.schema}.${c.table}? This deletes ALL rows.`)) return;
      this.post(`/c/${id}/pg/table/truncate`, { schema: c.schema, table: c.table }).catch(() => {});
    },
    dropColumn() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      if (!confirm(`Drop column ${c.name} from ${c.schema}.${c.table}?`)) return;
      this.post(`/c/${id}/pg/column/drop`, { schema: c.schema, table: c.table, column: c.name }).then(() => this.refreshConn(id)).catch(() => {});
    },
    dropIndex() {
      const c = this.ctx, id = this.cid();
      this.closeCtx();
      if (!confirm(`Drop index ${c.name}?`)) return;
      this.post(`/c/${id}/pg/index/drop`, { schema: c.schema, name: c.name }).then(() => this.refreshConn(id)).catch(() => {});
    },
    openForm(kind, extra) {
      const c = this.ctx, id = this.cid();
      const params = new URLSearchParams(Object.assign({ kind, schema: c.schema || '', table: c.table || '' }, extra || {}));
      this.closeCtx();
      if (window.htmx) htmx.ajax('GET', `/c/${id}/pg/form?` + params.toString(), { target: '#ddl-host', swap: 'innerHTML' });
    },
    closeDDL() { const h = document.getElementById('ddl-host'); if (h) h.innerHTML = ''; },
  }));
});
