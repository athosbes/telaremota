'use strict';
// Visualizador TelaRemota. Protocolo descrito em cmd/agent/stream.go e internal/auth.
(() => {
  const $ = (s) => document.querySelector(s);
  const te = new TextEncoder();
  const MIN_ITER = 150000;

  const canvas = $('#screen');
  const ctx = canvas.getContext('2d', { alpha: false });

  let ws = null, pc = null, ctl = null, video = null, sid = null;
  let pendingCands = [], remoteSet = false;
  let password = '', nonce = '', hmacKey = null, peerId = '';
  let active = false, finished = true;
  let fit = true, statsTimer = null, lastClip = '';
  const stat = { bytes: 0, frames: 0 };

  // ---------- utilidades ----------
  const b64enc = (u8) => { let s = ''; for (const b of u8) s += String.fromCharCode(b); return btoa(s); };
  const b64dec = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
  const normalize = (p) => p.toLowerCase().replace(/[\s-]/g, '');
  const fingerprint = (sdp) => {
    const m = /^a=fingerprint:sha-256 ([0-9a-f:]+)\s*$/im.exec(sdp || '');
    return m ? m[1].toUpperCase() : '';
  };
  const eqBytes = (a, b) => {
    if (a.length !== b.length) return false;
    let d = 0;
    for (let i = 0; i < a.length; i++) d |= a[i] ^ b[i];
    return d === 0;
  };
  const fmtId = (id) => id.replace(/(\d{3})(\d{3})(\d{3})/, '$1 $2 $3');

  function setStatus(msg, error) {
    const el = $('#status');
    el.textContent = msg || '';
    el.classList.toggle('error', !!error);
  }

  let toastTimer;
  function toast(msg) {
    const el = $('#toast');
    el.textContent = msg;
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 2500);
  }

  // ---------- tela inicial ----------
  const initialId = location.hash.replace(/\D/g, '');
  if (initialId) {
    $('#id').value = fmtId(initialId);
    $('#pw').focus();
  } else {
    $('#id').focus();
  }

  $('#form').addEventListener('submit', (e) => {
    e.preventDefault();
    const id = $('#id').value.replace(/\D/g, '');
    const pw = normalize($('#pw').value);
    if (id.length !== 9) return setStatus('O ID tem 9 dígitos.', true);
    if (!pw) return setStatus('Informe a senha.', true);
    if (!window.isSecureContext || !crypto.subtle) {
      return setStatus('Abra esta página via HTTPS (necessário para a criptografia do navegador).', true);
    }
    start(id, pw);
  });

  function start(id, pw) {
    finished = false;
    peerId = id;
    password = pw;
    $('#go').disabled = true;
    setStatus('Conectando ao servidor…');
    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    ws = new WebSocket(proto + location.host + '/ws');
    ws.onopen = () => ws.send(JSON.stringify({ t: 'connect', id }));
    ws.onmessage = (ev) => onSignal(JSON.parse(ev.data)).catch((err) => fail(err.message || String(err)));
    ws.onerror = () => {};
    ws.onclose = () => { if (!active) fail('Conexão com o servidor perdida.'); };
  }

  function sig(data) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ t: 'signal', sid, data }));
  }

  async function onSignal(m) {
    switch (m.t) {
      case 'session':
        sid = m.sid;
        await startPeer(m.ice || []);
        break;
      case 'signal': {
        const d = m.data || {};
        if (d.sdp) {
          await pc.setRemoteDescription(d.sdp);
          remoteSet = true;
          for (const c of pendingCands) await pc.addIceCandidate(c).catch(() => {});
          pendingCands = [];
        } else if (d.candidate) {
          if (remoteSet) await pc.addIceCandidate(d.candidate).catch(() => {});
          else pendingCands.push(d.candidate);
        }
        break;
      }
      case 'bye':
        if (active) end('O computador remoto encerrou a sessão.');
        else fail('O computador remoto recusou a conexão (ocupado ou indisponível).');
        break;
      case 'error':
        fail(m.msg || 'Erro no servidor.');
        break;
    }
  }

  async function startPeer(ice) {
    pc = new RTCPeerConnection({ iceServers: ice });
    ctl = pc.createDataChannel('ctl', { ordered: true });
    video = pc.createDataChannel('video', { ordered: true });
    video.binaryType = 'arraybuffer';

    pc.onicecandidate = (e) => { if (e.candidate) sig({ candidate: e.candidate.toJSON() }); };
    pc.onconnectionstatechange = () => {
      const s = pc && pc.connectionState;
      if (s === 'connected' && !active) setStatus('Canal seguro aberto. Verificando senha…');
      if (s === 'failed') {
        fail(active ? 'A conexão caiu.' : 'Não foi possível conectar diretamente. Peça ao administrador para configurar um servidor TURN.');
      }
    };
    ctl.onmessage = (e) => onCtl(JSON.parse(e.data)).catch((err) => fail(err.message || String(err)));
    ctl.onclose = () => { if (active) end('Conexão encerrada.'); };
    video.onmessage = onVideo;

    await pc.setLocalDescription(await pc.createOffer());
    sig({ sdp: pc.localDescription });
    setStatus('Estabelecendo conexão direta…');
  }

  function ctlSend(obj) {
    if (ctl && ctl.readyState === 'open') ctl.send(JSON.stringify(obj));
  }

  // ---------- autenticação ----------
  function proofMessage(role) {
    const fpV = fingerprint(pc.localDescription && pc.localDescription.sdp);
    const fpA = fingerprint(pc.remoteDescription && pc.remoteDescription.sdp);
    if (!fpV || !fpA) throw new Error('Não foi possível ler as impressões digitais DTLS.');
    return `telaremota/v1/${role}|${nonce}|${fpV}|${fpA}`;
  }

  async function hmac(msg) {
    return new Uint8Array(await crypto.subtle.sign('HMAC', hmacKey, te.encode(msg)));
  }

  async function onCtl(m) {
    switch (m.t) {
      case 'hello': {
        if (!(m.iter >= MIN_ITER)) throw new Error('Parâmetros de segurança inválidos do computador remoto.');
        nonce = m.nonce;
        const base = await crypto.subtle.importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveBits']);
        const bits = await crypto.subtle.deriveBits(
          { name: 'PBKDF2', salt: b64dec(m.salt), iterations: m.iter, hash: 'SHA-256' }, base, 256);
        hmacKey = await crypto.subtle.importKey('raw', bits, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
        password = '';
        ctlSend({ t: 'auth', proof: b64enc(await hmac(proofMessage('viewer'))) });
        break;
      }
      case 'auth_ok': {
        const expected = await hmac(proofMessage('agent'));
        if (!m.proof || !eqBytes(expected, b64dec(m.proof))) {
          fail('O computador remoto não comprovou conhecer a senha. Conexão abortada por segurança.');
          return;
        }
        showSession(m.w, m.h);
        break;
      }
      case 'auth_fail':
        fail(m.msg || 'Senha incorreta.');
        break;
      case 'denied':
        fail('O usuário do computador remoto recusou o acesso.');
        break;
      case 'clip':
        if (typeof m.text === 'string' && m.text !== lastClip) {
          lastClip = m.text;
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(m.text).then(() => toast('Texto copiado do computador remoto'), () => {});
          }
        }
        break;
    }
  }

  // ---------- sessão ----------
  function showSession(w, h) {
    active = true;
    $('#home').hidden = true;
    $('#session').hidden = false;
    $('#peer').textContent = fmtId(peerId);
    if (w && h) { canvas.width = w; canvas.height = h; }
    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    layout();
    canvas.focus();
    ctlSend({ t: 'q', v: Number($('#quality').value) });
    stat.bytes = 0; stat.frames = 0;
    statsTimer = setInterval(updateStats, 1000);
    updateRoute();
    document.title = `${fmtId(peerId)} — TelaRemota`;
  }

  function teardown() {
    active = false;
    clearInterval(statsTimer);
    releaseAll();
    try { if (ws && ws.readyState === WebSocket.OPEN && sid) ws.send(JSON.stringify({ t: 'bye', sid })); } catch (_) {}
    try { if (pc) pc.close(); } catch (_) {}
    try { if (ws) ws.close(); } catch (_) {}
    ws = pc = ctl = video = sid = hmacKey = null;
    pendingCands = []; remoteSet = false; password = ''; nonce = '';
    chunks = []; drawQ = Promise.resolve();
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    $('#session').hidden = true;
    $('#home').hidden = false;
    $('#go').disabled = false;
    $('#pw').value = '';
    document.title = 'TelaRemota';
  }

  function fail(msg) {
    if (finished) return;
    finished = true;
    teardown();
    setStatus(msg, true);
  }

  function end(msg) {
    if (finished) return;
    finished = true;
    teardown();
    setStatus(msg, false);
  }

  $('#end').addEventListener('click', () => end('Sessão encerrada.'));
  window.addEventListener('beforeunload', () => { if (active) teardown(); });

  // ---------- vídeo ----------
  let chunks = [];
  let drawQ = Promise.resolve();
  const queue = (fn) => { drawQ = drawQ.then(fn).catch((e) => console.error(e)); };

  function onVideo(e) {
    const u8 = new Uint8Array(e.data);
    chunks.push(u8.subarray(1));
    if (u8[0] !== 1) return;
    let msg;
    if (chunks.length === 1) {
      msg = chunks[0];
    } else {
      let n = 0;
      for (const c of chunks) n += c.length;
      msg = new Uint8Array(n);
      let o = 0;
      for (const c of chunks) { msg.set(c, o); o += c.length; }
    }
    chunks = [];
    stat.bytes += msg.length;
    handleVideo(msg);
  }

  function handleVideo(m) {
    const dv = new DataView(m.buffer, m.byteOffset, m.byteLength);
    switch (m[0]) {
      case 1: { // retângulo JPEG: decodifica em paralelo, desenha em ordem
        const x = dv.getUint16(1), y = dv.getUint16(3);
        const decoded = createImageBitmap(new Blob([m.subarray(9)], { type: 'image/jpeg' }));
        queue(async () => {
          const bmp = await decoded;
          ctx.drawImage(bmp, x, y);
          bmp.close();
        });
        break;
      }
      case 2: { // fim de quadro
        const f = dv.getUint32(1);
        queue(() => { ctlSend({ t: 'ack', f }); stat.frames++; });
        break;
      }
      case 3: { // nova resolução
        const w = dv.getUint16(1), h = dv.getUint16(3);
        queue(() => {
          if (canvas.width !== w || canvas.height !== h) { canvas.width = w; canvas.height = h; }
          layout();
        });
        break;
      }
    }
  }

  function updateStats() {
    const mbps = (stat.bytes * 8 / 1e6).toFixed(1);
    $('#stats').textContent = `${stat.frames} qps · ${mbps} Mb/s · ${canvas.width}×${canvas.height}`;
    stat.bytes = 0; stat.frames = 0;
  }

  async function updateRoute() {
    if (!pc) return;
    try {
      const stats = await pc.getStats();
      let pair = null;
      stats.forEach((r) => {
        if (r.type === 'transport' && r.selectedCandidatePairId) pair = stats.get(r.selectedCandidatePairId);
      });
      if (!pair) stats.forEach((r) => { if (r.type === 'candidate-pair' && r.nominated && r.state === 'succeeded') pair = r; });
      if (!pair) return;
      const l = stats.get(pair.localCandidateId), r = stats.get(pair.remoteCandidateId);
      const relay = (l && l.candidateType === 'relay') || (r && r.candidateType === 'relay');
      $('#route').textContent = relay ? 'via TURN · criptografado' : 'P2P direto · criptografado';
    } catch (_) { /* ignora */ }
  }

  // ---------- layout ----------
  function layout() {
    const st = $('#stage');
    if (fit) {
      const s = Math.min(st.clientWidth / canvas.width, st.clientHeight / canvas.height);
      canvas.style.width = Math.floor(canvas.width * s) + 'px';
      canvas.style.height = Math.floor(canvas.height * s) + 'px';
      st.classList.remove('scroll');
    } else {
      const dpr = window.devicePixelRatio || 1;
      canvas.style.width = canvas.width / dpr + 'px';
      canvas.style.height = canvas.height / dpr + 'px';
      st.classList.add('scroll');
    }
    $('#fit').textContent = fit ? 'Tamanho real' : 'Ajustar';
  }
  window.addEventListener('resize', () => { if (active) layout(); });
  $('#fit').addEventListener('click', () => { fit = !fit; layout(); canvas.focus(); });

  $('#quality').addEventListener('change', (e) => { ctlSend({ t: 'q', v: Number(e.target.value) }); canvas.focus(); });

  $('#fs').addEventListener('click', async () => {
    try {
      if (document.fullscreenElement) {
        await document.exitFullscreen();
      } else {
        await $('#session').requestFullscreen();
        // captura Win, Alt+Tab, Esc etc. (Chrome/Edge)
        if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
      }
    } catch (_) {}
    canvas.focus();
  });
  document.addEventListener('fullscreenchange', () => {
    $('#fs').textContent = document.fullscreenElement ? 'Sair da tela cheia' : 'Tela cheia';
    if (!document.fullscreenElement && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
    setTimeout(layout, 50);
  });

  // ---------- mouse ----------
  function remotePos(e) {
    const r = canvas.getBoundingClientRect();
    const x = Math.round((e.clientX - r.left) * canvas.width / r.width);
    const y = Math.round((e.clientY - r.top) * canvas.height / r.height);
    return { x: Math.max(0, Math.min(canvas.width - 1, x)), y: Math.max(0, Math.min(canvas.height - 1, y)) };
  }

  let pendingMove = null, moveScheduled = false;
  const pressedButtons = new Set();

  canvas.addEventListener('mousemove', (e) => {
    if (!active) return;
    pendingMove = remotePos(e);
    if (moveScheduled) return;
    moveScheduled = true;
    requestAnimationFrame(() => {
      moveScheduled = false;
      if (pendingMove) ctlSend({ t: 'm', x: pendingMove.x, y: pendingMove.y });
      pendingMove = null;
    });
  });
  canvas.addEventListener('mousedown', (e) => {
    if (!active) return;
    e.preventDefault();
    canvas.focus();
    const p = remotePos(e);
    ctlSend({ t: 'm', x: p.x, y: p.y });
    pressedButtons.add(e.button);
    ctlSend({ t: 'mb', b: e.button, d: true });
  });
  window.addEventListener('mouseup', (e) => {
    if (!active || !pressedButtons.has(e.button)) return;
    pressedButtons.delete(e.button);
    ctlSend({ t: 'mb', b: e.button, d: false });
  });
  canvas.addEventListener('contextmenu', (e) => e.preventDefault());
  canvas.addEventListener('auxclick', (e) => e.preventDefault());
  canvas.addEventListener('wheel', (e) => {
    if (!active) return;
    e.preventDefault();
    const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
    const dy = Math.round(-e.deltaY * k * 1.2);
    const dx = Math.round(e.deltaX * k * 1.2);
    if (dy || dx) ctlSend({ t: 'w', dy, dx });
  }, { passive: false });

  // ---------- teclado ----------
  const pressedKeys = new Set();
  let keyQ = Promise.resolve();
  const keyQueue = (fn) => { keyQ = keyQ.then(fn).catch(() => {}); };

  window.addEventListener('keydown', (e) => {
    if (!active || !e.code) return;
    e.preventDefault();
    // Ctrl+V: envia antes a área de transferência local para o computador remoto
    if ((e.ctrlKey || e.metaKey) && e.code === 'KeyV' && !e.repeat && navigator.clipboard && navigator.clipboard.readText) {
      keyQueue(async () => {
        try {
          const t = await navigator.clipboard.readText();
          if (t && t !== lastClip) { lastClip = t; ctlSend({ t: 'clip', text: t }); }
        } catch (_) {}
      });
    }
    keyQueue(() => { pressedKeys.add(e.code); ctlSend({ t: 'k', c: e.code, d: true }); });
  }, true);
  window.addEventListener('keyup', (e) => {
    if (!active || !e.code) return;
    e.preventDefault();
    keyQueue(() => { pressedKeys.delete(e.code); ctlSend({ t: 'k', c: e.code, d: false }); });
  }, true);

  function releaseAll() {
    for (const c of pressedKeys) ctlSend({ t: 'k', c, d: false });
    for (const b of pressedButtons) ctlSend({ t: 'mb', b, d: false });
    pressedKeys.clear();
    pressedButtons.clear();
  }
  window.addEventListener('blur', () => { if (active) releaseAll(); });
})();
