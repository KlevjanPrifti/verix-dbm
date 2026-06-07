// verix-dbm — workbench shell: DataGrip-style tabs + connection modal.
// Driven by Alpine (state) and HTMX (content loading into #tab-content).
document.addEventListener('alpine:init', () => {
  Alpine.data('workbench', () => ({
    tabs: [],
    active: null,
    menuOpen: false,
    modal: { open: false, kind: 'postgres', port: '5432' },

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
  }));
});
