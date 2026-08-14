(() => {
  'use strict';

  const status = document.querySelector('#tradingViewStatus');
  if (!status) return;
  const label = status.querySelector('span');
  const nativeFetch = window.fetch.bind(window);
  let lastSymbol = '';

  function showToast(message) {
    const toast = document.querySelector('#toast');
    if (!toast || !message) return;
    toast.textContent = message;
    toast.classList.add('show');
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => toast.classList.remove('show'), 2200);
  }

  function render(payload) {
    status.className = 'status';
    status.style.marginLeft = '0';
    if (!payload || !payload.enabled) {
      lastSymbol = '';
      label.textContent = 'TV OFF';
      status.title = 'TradingView Desktop integration is disabled';
      return;
    }
    if (payload.connected) {
      if (payload.symbol) lastSymbol = String(payload.symbol).toUpperCase();
      status.classList.add('live');
      label.textContent = lastSymbol ? `TV ${lastSymbol}` : 'TV READY';
      status.title = 'TradingView Desktop chart is connected';
      return;
    }
    lastSymbol = '';
    status.classList.add('degraded');
    label.textContent = payload.error ? 'TV ERROR' : 'TV OFFLINE';
    status.title = payload.error || 'TradingView Desktop chart is unavailable';
  }

  async function refresh() {
    try {
      const response = await nativeFetch('/api/integrations/tradingview/status', { cache: 'no-store' });
      if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
      render(await response.json());
    } catch (error) {
      render({ enabled: true, connected: false, error: error.message });
    }
  }

  // app.js already owns the row-click behavior. Observe its /api/select result
  // without consuming the response it expects, so TradingView gets a visible
  // success or failure state while the original Tape handling remains intact.
  window.fetch = async (...args) => {
    const response = await nativeFetch(...args);
    const target = typeof args[0] === 'string' ? args[0] : args[0] && args[0].url;
    if (target === '/api/select' && response.ok) {
      response.clone().json().then((payload) => {
        if (!payload.tradingview_enabled) {
          render({ enabled: false, connected: false });
          return;
        }
        if (payload.tradingview_ok) {
          render({ enabled: true, connected: true, symbol: payload.symbol });
          return;
        }
        const error = payload.tradingview_error || 'TradingView Desktop is unavailable';
        render({ enabled: true, connected: false, error });
        showToast(error);
      }).catch(() => {});
    }
    return response;
  };

  void refresh();
  setInterval(refresh, 5000);
})();
