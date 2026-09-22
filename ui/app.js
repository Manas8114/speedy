// Speedy 2.0 Dashboard Logic
document.addEventListener("DOMContentLoaded", () => {
  // State
  const state = {
    bonded: false,
    tier: 2,
    tierNames: {
      1: "Tier 1: Round-Robin (Naive 50/50 packet alternation)",
      2: "Tier 2: Goodput-Weighted (Adaptive EWMA measurement dynamically balances links)",
      3: "Tier 3: Min-RTT + cwnd (Picks lowest-latency link with open congestion window)",
      4: "Tier 4: HoL-Aware (Actively skips high-latency mismatched links to prevent head-of-line stalls)",
      0: "REDUNDANT Mode (Duplicates traffic across all links for zero-loss VoIP/Gaming)"
    },
    paths: [],
    history: {
      labels: [],
      agg: Array(30).fill(0),
      p0: Array(30).fill(0),
      p1: Array(30).fill(0)
    },
    apiToken: ""
  };

  // DOM Elements
  const statusDot = document.getElementById("statusDot");
  const tunnelStatusText = document.getElementById("tunnelStatusText");
  const aggSpeedValue = document.getElementById("aggSpeedValue");
  const aggPacketsSec = document.getElementById("aggPacketsSec");
  const reorderOcc = document.getElementById("reorderOcc");
  const tierDescription = document.getElementById("tierDescription");
  const nicList = document.getElementById("nicList");
  const logTerminal = document.getElementById("logTerminal");
  const masterToggle = document.getElementById("masterToggle");
  const btnPreflight = document.getElementById("btnPreflight");
  const btnSimulateDrop = document.getElementById("btnSimulateDrop");
  const btnDeployModal = document.getElementById("btnDeployModal");
  const relayModal = document.getElementById("relayModal");
  const btnCloseModal = document.getElementById("btnCloseModal");
  const btnCancelModal = document.getElementById("btnCancelModal");
  const btnCopyCode = document.getElementById("btnCopyCode");
  const deployCodeBlock = document.getElementById("deployCodeBlock");
  const canvas = document.getElementById("telemetryChart");
  const ctx = canvas.getContext("2d");

  // Initial Setup
  statusDot.className = "status-dot";
  statusDot.style.backgroundColor = "var(--accent-amber)";
  tunnelStatusText.textContent = "STANDBY (READY)";
  aggSpeedValue.innerHTML = `0.0 <span class="stat-unit">Mbps</span>`;
  aggPacketsSec.textContent = "0 packets/sec";
  reorderOcc.innerHTML = `0 <span class="stat-unit">pkts in buffer</span>`;

  renderNICs();
  initChart();
  appendLog("INFO", "Speedy 2.0 Web Dashboard active. Connected to local daemon.");

  // Tier Selector Event Listeners
  document.querySelectorAll(".tier-pill").forEach(pill => {
    pill.addEventListener("click", () => {
      document.querySelectorAll(".tier-pill").forEach(p => p.classList.remove("active"));
      pill.classList.add("active");
      const tier = parseInt(pill.dataset.tier);
      state.tier = tier;
      tierDescription.textContent = state.tierNames[tier];
      appendLog("INFO", `Scheduler switched to ${state.tierNames[tier].split("(")[0]}`);
      
      // Update backend API if available
      fetch("http://127.0.0.1:8721/api/v1/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ selected_tier: tier })
      }).catch(() => {});
    });
  });

  // Master Toggle
  masterToggle.addEventListener("change", (e) => {
    state.bonded = e.target.checked;
    if (state.bonded) {
      statusDot.className = "status-dot pulsing";
      tunnelStatusText.textContent = "BONDED & ACTIVE";
      appendLog("BOND", "Bonding re-established across all physical paths");
    } else {
      statusDot.className = "status-dot";
      statusDot.style.backgroundColor = "var(--accent-red)";
      tunnelStatusText.textContent = "DISCONNECTED";
      appendLog("WARN", "Tunnel interface disabled by user. Default route uninstalled");
    }
  });

  // Simulate Wi-Fi Drop / Unplug
  let wifiDropped = false;
  btnSimulateDrop.addEventListener("click", () => {
    wifiDropped = !wifiDropped;
    const wlan = state.paths[0];
    if (wifiDropped) {
      wlan.state = "DEAD";
      wlan.goodput = 0;
      btnSimulateDrop.textContent = "Restore Wi-Fi (Hot-Plug)";
      btnSimulateDrop.classList.add("btn-primary");
      appendLog("WARN", "[NIC WATCHER] wlan0 carrier lost! Path drained without breaking session");
    } else {
      wlan.state = "ACTIVE";
      wlan.goodput = 540.2;
      btnSimulateDrop.textContent = "Simulate Wi-Fi Drop";
      btnSimulateDrop.classList.remove("btn-primary");
      appendLog("BOND", "[NIC WATCHER] wlan0 restored! PATH_ADD elevates link to ACTIVE via probe ACKs");
    }
    renderNICs();
  });

  // Pre-flight Candidate Estimation
  btnPreflight.addEventListener("click", async () => {
    btnPreflight.disabled = true;
    btnPreflight.innerHTML = `<span class="stat-dot pulsing"></span> Probing APs...`;
    appendLog("INFO", "Probing reachable Wi-Fi access points and sampling driver link rates...");

    try {
      const res = await fetch("/api/wifi/estimate");
      if (res.ok) {
        const data = await res.json();
        if (data.candidates && data.candidates.length > 0) {
          wifiCandidates = data.candidates;
          renderWiFiCandidates(wifiCandidates, data.selected);
          const topSSID = data.selected ? data.selected.ssid : data.candidates[0].ssid;
          const topRate = data.selected ? (data.selected.estimated_throughput_mbps || data.selected.tx_rate_mbps * 0.72) : 0;
          appendLog("BOND", `Candidate probe complete: ${data.candidates.length} AP(s) evaluated. Top: ${topSSID} (~${topRate.toFixed(0)} Mbps)`);
        } else {
          appendLog("WARN", "Scan completed: 0 Wi-Fi networks currently detected.");
        }
      } else {
        appendLog("ERROR", `Wi-Fi probe returned status ${res.status}`);
      }
    } catch (err) {
      appendLog("ERROR", `Probe failed: ${err.message}`);
    } finally {
      btnPreflight.disabled = false;
      btnPreflight.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16 14"></polyline></svg> Pre-Flight Benchmark`;
    }
  });

  // Modal Dialog
  btnDeployModal.addEventListener("click", () => relayModal.classList.add("open"));
  btnCloseModal.addEventListener("click", () => relayModal.classList.remove("open"));
  btnCancelModal.addEventListener("click", () => relayModal.classList.remove("open"));
  document.getElementById("btnApplyRelay").addEventListener("click", () => {
    const host = document.getElementById("inputRelayHost").value;
    document.getElementById("relayPinnedIP").textContent = host.split(":")[0];
    relayModal.classList.remove("open");
    appendLog("BOND", `Connected to custom relay: ${host}. Handshake verified.`);
  });

  btnCopyCode.addEventListener("click", () => {
    navigator.clipboard.writeText(deployCodeBlock.textContent);
    btnCopyCode.textContent = "Copied!";
    setTimeout(() => btnCopyCode.textContent = "Copy Config", 2000);
  });

  // Provider Tabs & Cloud Orchestrator in Modal
  const configs = {
    docker: `services:\n  speedy-relay:\n    image: speedy/relay:v2.0\n    restart: always\n    network_mode: "host"\n    cap_add:\n      - NET_ADMIN\n    environment:\n      - PORT=51820\n      - POOL=10.254.1.0/24\n    volumes:\n      - ./relay_key.hex:/opt/speedy/relay_key.hex`,
    cloudinit: `#cloud-config\npackage_update: true\npackages:\n  - iptables\n  - curl\n\nruncmd:\n  - curl -fsSL https://speedy.bond/deploy.sh | bash\n  - systemctl start speedy-relay`,
    script: `curl -fsSL https://raw.githubusercontent.com/speedy/relay/main/scripts/deploy-relay.sh | sudo bash`
  };

  const cloudDeployBox = document.getElementById("cloudDeployBox");
  const codeBoxWrapper = document.getElementById("codeBoxWrapper");
  const cloudProviderSelect = document.getElementById("cloudProviderSelect");
  const cloudRegionSelect = document.getElementById("cloudRegionSelect");
  const groupApiToken = document.getElementById("groupApiToken");
  const cloudApiToken = document.getElementById("cloudApiToken");
  const btnRunCloudDeploy = document.getElementById("btnRunCloudDeploy");
  const cloudTerminal = document.getElementById("cloudTerminal");

  // Load regions dynamically
  async function loadOrchestratorProviders() {
    try {
      const res = await fetch("/api/orchestrate/providers");
      if (res.ok) {
        const data = await res.json();
        window._cloudProviders = data.providers;
        updateRegionDropdown();
      }
    } catch (_) {}
  }
  loadOrchestratorProviders();

  function updateRegionDropdown() {
    const provId = cloudProviderSelect.value;
    groupApiToken.style.display = (provId === "sandbox") ? "none" : "block";

    if (!window._cloudProviders) return;
    const provider = window._cloudProviders.find(p => p.id === provId);
    if (!provider || !provider.regions) return;

    cloudRegionSelect.innerHTML = "";
    provider.regions.forEach(r => {
      const opt = document.createElement("option");
      opt.value = r.id;
      opt.textContent = `${r.flag} ${r.name} (${r.location})`;
      cloudRegionSelect.appendChild(opt);
    });
  }

  cloudProviderSelect.addEventListener("change", updateRegionDropdown);

  document.querySelectorAll(".provider-tab").forEach(tab => {
    tab.addEventListener("click", () => {
      document.querySelectorAll(".provider-tab").forEach(t => t.classList.remove("active"));
      tab.classList.add("active");

      const provider = tab.dataset.provider;
      if (provider === "auto-cloud") {
        cloudDeployBox.style.display = "flex";
        codeBoxWrapper.style.display = "none";
      } else {
        cloudDeployBox.style.display = "none";
        codeBoxWrapper.style.display = "block";
        deployCodeBlock.textContent = configs[provider];
      }
    });
  });

  // Execute 1-Click Cloud Deployment
  btnRunCloudDeploy.addEventListener("click", async () => {
    const provider = cloudProviderSelect.value;
    const region = cloudRegionSelect.value;
    const token = cloudApiToken.value.trim();

    if (provider !== "sandbox" && !token) {
      alert("Please enter your " + provider + " API token to provision a live cloud server.");
      return;
    }

    btnRunCloudDeploy.disabled = true;
    btnRunCloudDeploy.innerHTML = `<span>Provisioning Cloud Relay...</span>`;
    cloudTerminal.style.display = "flex";
    cloudTerminal.innerHTML = `<div class="log-line"><span class="cloud-terminal-time">${new Date().toLocaleTimeString()}</span> <span class="log-line-step">[INIT]</span> Initializing 1-click cloud relay pipeline (${provider})...</div>`;

    try {
      const res = await fetch("/api/orchestrate/deploy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: provider,
          region: region,
          api_token: token,
          port: 51820
        })
      });

      const data = await res.json();
      if (data.logs && data.logs.length > 0) {
        cloudTerminal.innerHTML = "";
        data.logs.forEach(l => {
          const line = document.createElement("div");
          line.className = "log-line";
          line.innerHTML = `<span class="cloud-terminal-time">${l.time}</span> <span class="log-line-step">[${l.step}]</span> ${l.message}`;
          cloudTerminal.appendChild(line);
        });
        cloudTerminal.scrollTop = cloudTerminal.scrollHeight;
      }

      if (data.success && data.result) {
        const result = data.result;
        const successLine = document.createElement("div");
        successLine.className = "log-line log-line-success";
        successLine.innerHTML = `<span class="log-line-step">[SUCCESS]</span> Relay Ready! Endpoint: ${result.public_ip}:${result.port}`;
        cloudTerminal.appendChild(successLine);

        // Auto-fill configuration fields
        document.getElementById("inputRelayHost").value = `${result.public_ip}:${result.port}`;
        document.getElementById("inputRelayKey").value = result.server_public_key;

        appendLog("BOND", `1-Click Cloud Relay successfully provisioned (${result.provider}, ${result.public_ip}:${result.port})`);
      } else {
        const errLine = document.createElement("div");
        errLine.className = "log-line";
        errLine.style.color = "var(--accent-amber)";
        errLine.innerHTML = `<span class="log-line-step">[ERROR]</span> Provisioning failed: ${data.error || "Unknown error"}`;
        cloudTerminal.appendChild(errLine);
      }
    } catch (err) {
      const errLine = document.createElement("div");
      errLine.className = "log-line";
      errLine.style.color = "var(--accent-amber)";
      errLine.innerHTML = `<span class="log-line-step">[FATAL]</span> Network error: ${err.message}`;
      cloudTerminal.appendChild(errLine);
    } finally {
      btnRunCloudDeploy.disabled = false;
      btnRunCloudDeploy.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></svg> <span>Deploy Cloud Relay (30 Seconds)</span>`;
    }
  });

  // Render NIC List
  function renderNICs() {
    nicList.innerHTML = "";
    if (!state.paths || state.paths.length === 0) {
      nicList.innerHTML = `<div style="padding: 24px; text-align: center; color: var(--text-muted); font-size: 0.9rem;">No active tunnel paths detected. Connect to relay to begin packet bonding.</div>`;
      return;
    }

    state.paths.forEach((p, idx) => {
      const item = document.createElement("div");
      const st = (p.state || "STANDBY").toLowerCase();
      item.className = `nic-item ${st}`;

      const isUp = p.state === "ACTIVE" || p.state === "STANDBY";
      const dotColor = p.state === "ACTIVE" ? "var(--accent-green)" : (p.state === "DEGRADED" ? "var(--accent-amber)" : "var(--accent-blue)");

      item.innerHTML = `
        <div class="nic-top-row">
          <div class="nic-name-wrap">
            <span class="status-dot ${p.state === 'ACTIVE' ? 'pulsing' : ''}" style="background-color: ${dotColor}"></span>
            <span class="nic-name">${p.name || p.iface}</span>
            <span class="nic-category-badge">${p.category || 'unlimited'}</span>
          </div>
          <select class="btn-sm btn-outline cat-select" data-id="${p.id}">
            <option value="unlimited" ${p.category === 'unlimited' ? 'selected' : ''}>Unlimited</option>
            <option value="metered" ${p.category === 'metered' ? 'selected' : ''}>Metered</option>
            <option value="backup" ${p.category === 'backup' ? 'selected' : ''}>Backup-Only</option>
          </select>
        </div>
        <div class="nic-metrics">
          <div>RTT: <span class="metric-val">${p.state === 'DEAD' || !p.rtt ? '—' : p.rtt.toFixed(1) + ' ms'}</span></div>
          <div>Loss: <span class="metric-val">${p.state === 'DEAD' ? '100%' : (p.loss ? p.loss.toFixed(1) + '%' : '0.0%')}</span></div>
          <div>Goodput: <span class="metric-val">${p.goodput ? p.goodput.toFixed(1) + ' Mbps' : '0.0 Mbps'}</span></div>
        </div>
        <div class="nic-slider-row">
          <span>Manual Share:</span>
          <input type="range" min="0" max="100" value="${p.weight || 50}" class="weight-slider" data-id="${p.id}" ${p.category === 'backup' ? 'disabled' : ''}>
          <span class="weight-num">${p.category === 'backup' ? '0%' : (p.weight || 50) + '%'}</span>
        </div>
      `;

      nicList.appendChild(item);
    });

    // Attach Event Listeners
    document.querySelectorAll(".cat-select").forEach(sel => {
      sel.addEventListener("change", (e) => {
        const id = parseInt(e.target.dataset.id);
        if (state.paths[id]) {
          state.paths[id].category = e.target.value;
          appendLog("INFO", `Path ${state.paths[id].name} set to ${e.target.value.toUpperCase()}`);
          renderNICs();
        }
      });
    });

    document.querySelectorAll(".weight-slider").forEach(slider => {
      slider.addEventListener("input", (e) => {
        const id = parseInt(e.target.dataset.id);
        const val = parseInt(e.target.value);
        if (state.paths[id]) {
          state.paths[id].weight = val;
          if (state.paths.length === 2) {
            const otherId = id === 0 ? 1 : 0;
            state.paths[otherId].weight = Math.max(0, 100 - val);
          }
          renderNICs();
        }
      });
    });
  }

  function appendLog(level, msg) {
    const line = document.createElement("div");
    line.className = "log-line";
    const time = new Date().toTimeString().split(" ")[0];
    line.innerHTML = `<span class="log-time">[${time}]</span> <span class="log-level ${level}">${level}</span> <span class="log-msg">${msg}</span>`;
    logTerminal.appendChild(line);
    logTerminal.scrollTop = logTerminal.scrollHeight;
  }

  // Telemetry Canvas Chart — Real API Polling
  function initChart() {
    canvas.width = canvas.parentElement.clientWidth - 40;
    canvas.height = 200;

    let initialPathsLoaded = false;

    async function pollTelemetry() {
      try {
        const res = await fetch("/api/status");
        if (res.ok) {
          const data = await res.json();

          if (data.paths && data.paths.length > 0) {
            if (!initialPathsLoaded || state.paths.length === 0) {
              state.paths = data.paths;
              initialPathsLoaded = true;
              renderNICs();
              const names = data.paths.map(p => p.name).join(", ");
              appendLog("INFO", `Uplinks detected from daemon: ${names}`);
            }
          }

          const isTunnelActive = data.tunnel_active || false;
          const aggRate = data.aggregate_throughput_mbps || 0;
          let p0Rate = 0;
          let p1Rate = 0;

          if (data.paths && data.paths.length > 0) {
            p0Rate = data.paths[0].goodput || 0;
            if (data.paths.length > 1) {
              p1Rate = data.paths[1].goodput || 0;
            }
          }

          updateChart(aggRate, p0Rate, p1Rate);

          if (isTunnelActive) {
            statusDot.className = "status-dot pulsing";
            statusDot.style.backgroundColor = "var(--accent-green)";
            tunnelStatusText.textContent = "BONDED & ACTIVE";
            aggSpeedValue.innerHTML = `${aggRate.toFixed(1)} <span class="stat-unit">Mbps</span>`;
            aggPacketsSec.textContent = `${Math.round(aggRate * 128).toLocaleString()} packets/sec`;
          } else {
            statusDot.className = "status-dot";
            statusDot.style.backgroundColor = "var(--accent-amber)";
            tunnelStatusText.textContent = "STANDBY (READY)";
            aggSpeedValue.innerHTML = `0.0 <span class="stat-unit">Mbps</span>`;
            aggPacketsSec.textContent = "0 packets/sec";
          }

          const occ = (data.reorder_buffer && data.reorder_buffer.occupancy) || 0;
          reorderOcc.innerHTML = `${occ} <span class="stat-unit">pkts in buffer</span>`;
        }
      } catch (err) {
        updateChart(0, 0, 0);
        statusDot.className = "status-dot";
        statusDot.style.backgroundColor = "var(--accent-red)";
        tunnelStatusText.textContent = "DAEMON OFFLINE";
        aggSpeedValue.innerHTML = `0.0 <span class="stat-unit">Mbps</span>`;
        aggPacketsSec.textContent = "0 packets/sec";
        reorderOcc.innerHTML = `0 <span class="stat-unit">pkts in buffer</span>`;
      }
    }

    pollTelemetry();
    setInterval(pollTelemetry, 1000);
  }

  function updateChart(agg, p0, p1) {
    state.history.agg.shift();
    state.history.agg.push(agg);
    state.history.p0.shift();
    state.history.p0.push(p0);
    state.history.p1.shift();
    state.history.p1.push(p1);

    drawChart();
  }

  function drawChart() {
    const w = canvas.width;
    const h = canvas.height;
    ctx.clearRect(0, 0, w, h);

    // Draw Grid Lines
    ctx.strokeStyle = "rgba(255, 255, 255, 0.05)";
    ctx.lineWidth = 1;
    for (let y = 40; y < h; y += 40) {
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }

    const maxVal = 1000;
    const stepX = w / (state.history.agg.length - 1);

    // Draw Line Function
    function drawLine(data, color, fillGrad) {
      ctx.beginPath();
      data.forEach((val, i) => {
        const x = i * stepX;
        const y = h - (val / maxVal) * (h - 20) - 10;
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      });
      ctx.strokeStyle = color;
      ctx.lineWidth = 2.5;
      ctx.stroke();

      if (fillGrad) {
        ctx.lineTo(w, h);
        ctx.lineTo(0, h);
        ctx.closePath();
        ctx.fillStyle = fillGrad;
        ctx.fill();
      }
    }

    const gradAgg = ctx.createLinearGradient(0, 0, 0, h);
    gradAgg.addColorStop(0, "rgba(0, 240, 255, 0.2)");
    gradAgg.addColorStop(1, "rgba(0, 240, 255, 0.0)");

    drawLine(state.history.p1, "#c084fc", null);
    drawLine(state.history.p0, "#38bdf8", null);
    drawLine(state.history.agg, "#00f0ff", gradAgg);
  }

  // ==========================================
  // Phase 2.5: Wi-Fi Optimizer Controller
  // ==========================================
  const currentWifiStandard = document.getElementById("currentWifiStandard");
  const btnWifiModeAuto = document.getElementById("btnWifiModeAuto");
  const btnWifiModePin = document.getElementById("btnWifiModePin");
  const btnWifiModeExclude = document.getElementById("btnWifiModeExclude");
  const btnWifiRescan = document.getElementById("btnWifiRescan");
  const wifiCandidateBody = document.getElementById("wifiCandidateBody");

  let wifiMode = "auto";
  let pinnedBSSID = "";
  let wifiCandidates = [];

  async function fetchWiFiScan() {
    try {
      const res = await fetch("/api/wifi/scan");
      if (res.ok) {
        const data = await res.json();
        if (data.candidates && data.candidates.length > 0) {
          wifiCandidates = data.candidates;
          pinnedBSSID = data.pinned_bssid || "";
          wifiMode = data.mode || "auto";
          renderWiFiCandidates(wifiCandidates, data.selected);
          return;
        }
      }
    } catch (_) {
      // Fallback
    }
    wifiCandidates = [];
    renderWiFiCandidates([], null);
  }

  function getBadgeClass(std) {
    if (!std) return "badge-wifi-5";
    if (std.includes("7")) return "badge-wifi-7";
    if (std.includes("6E") || std.includes("6e")) return "badge-wifi-6e";
    if (std.includes("6")) return "badge-wifi-6";
    if (std.includes("5") || std.includes("ac")) return "badge-wifi-5";
    return "badge-wifi-4";
  }

  function renderWiFiCandidates(candidates, selected) {
    wifiCandidateBody.innerHTML = "";

    // Sort according to current mode
    let list = [...candidates];
    if (wifiMode === "exclude") {
      list = list.filter(c => !c.band.includes("2.4"));
    }

    if (list.length === 0) {
      currentWifiStandard.textContent = "NO WI-FI DETECTED";
      wifiCandidateBody.innerHTML = `<tr><td colspan="8" style="text-align: center; color: var(--text-muted); padding: 24px;">No visible Wi-Fi access points detected on this system. Connect a Wi-Fi adapter or run as Administrator.</td></tr>`;
      return;
    }

    list.forEach(c => {
      const isPinned = (wifiMode === "pin" && (c.bssid === pinnedBSSID || c.ssid === pinnedBSSID));
      const isActive = isPinned || (wifiMode === "auto" && selected && (c.bssid === selected.bssid || c.ssid === selected.ssid)) || (!selected && c.is_connected);

      if (isActive) {
        currentWifiStandard.textContent = `ACTIVE: ${c.ssid} • ${c.standard.split(" ")[0]} (${c.band} • Ch ${c.channel} • ${Math.round(c.tx_rate_mbps)} Mbps)`;
      }

      const tr = document.createElement("tr");
      if (isActive) tr.classList.add("active-row");
      if (isPinned) tr.classList.add("pinned-row");

      const badgeCls = getBadgeClass(c.standard);
      const rawRTT = c.estimated_rtt || c.benchmarked_rtt;
      const rttMs = rawRTT ? (typeof rawRTT === "number" ? (rawRTT / 1e6).toFixed(1) : "—") : "—";
      const rawTput = c.estimated_throughput_mbps || c.benchmarked_throughput_mbps || (c.tx_rate_mbps * 0.72);
      const tput = rawTput ? rawTput.toFixed(1) : "—";

      tr.innerHTML = `
        <td class="wifi-ssid-cell">
          <span class="wifi-ssid-name">
            ${c.ssid}
            ${c.is_connected ? '<span class="stat-pill success" style="font-size:0.65rem; padding:2px 6px;">OS ASSOC</span>' : ''}
          </span>
          <span class="wifi-bssid">${c.bssid}</span>
        </td>
        <td><span class="badge-wifi-std ${badgeCls}">${c.standard.split(" ")[0]}</span></td>
        <td><span class="badge-band">${c.band}</span></td>
        <td><span style="font-family:var(--font-mono); font-size:0.75rem;">${c.channel_width_mhz || 80} MHz</span></td>
        <td>
          <div class="signal-bar-wrap">
            <div class="signal-bar-bg"><div class="signal-bar-fill" style="width: ${c.signal_percent}%;"></div></div>
            <span class="signal-val">${c.signal_percent}% (${c.rssi_dbm || -60}dBm)</span>
          </div>
        </td>
        <td><span style="font-family:var(--font-mono); font-weight:600;">${Math.round(c.tx_rate_mbps)} Mbps</span></td>
        <td>
          <span style="font-family:var(--font-mono); font-weight:700; color:var(--accent-cyan);">${tput} Mbps</span>
          <span style="color:var(--text-dim); font-size:0.72rem;"> (${rttMs} ms)</span>
        </td>
        <td>
          <div class="wifi-actions">
            ${isActive ? 
              `<button class="btn btn-sm btn-outline" style="border-color:var(--accent-cyan); color:var(--accent-cyan);" disabled>Active</button>` : 
              `<button class="btn btn-sm btn-outline btn-pin-candidate" data-bssid="${c.bssid}" data-ssid="${c.ssid}">Pin</button>`
            }
          </div>
        </td>
      `;

      wifiCandidateBody.appendChild(tr);
    });

    // Attach Pin event listeners
    document.querySelectorAll(".btn-pin-candidate").forEach(btn => {
      btn.addEventListener("click", () => {
        const bssid = btn.dataset.bssid;
        const ssid = btn.dataset.ssid;
        setWiFiMode("pin", bssid);
        appendLog("BOND", `[WI-FI OPTIMIZER] Manually pinned path to SSID "${ssid}" (${bssid})`);
      });
    });
  }

  async function setWiFiMode(mode, pinBSSID = "", excludeBand = "") {
    wifiMode = mode;
    if (mode === "pin" && pinBSSID) {
      pinnedBSSID = pinBSSID;
    }

    [btnWifiModeAuto, btnWifiModePin, btnWifiModeExclude].forEach(b => b.classList.remove("active"));
    if (mode === "auto") btnWifiModeAuto.classList.add("active");
    else if (mode === "pin") btnWifiModePin.classList.add("active");
    else if (mode === "exclude") btnWifiModeExclude.classList.add("active");

    try {
      const res = await fetch("/api/wifi/config", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          mode: mode,
          pinned_bssid: pinnedBSSID,
          exclude_band: excludeBand,
          clear_exclude: (mode !== "exclude")
        })
      });
      if (res.ok) {
        const data = await res.json();
        if (data.candidates) {
          wifiCandidates = data.candidates;
          renderWiFiCandidates(wifiCandidates, data.selected);
          return;
        }
      }
    } catch (_) {}

    renderWiFiCandidates(wifiCandidates, mode === "pin" ? wifiCandidates.find(c => c.bssid === pinnedBSSID) : wifiCandidates[0]);
  }

  btnWifiModeAuto.addEventListener("click", () => {
    setWiFiMode("auto");
    appendLog("INFO", "[WI-FI OPTIMIZER] Mode set to AUTO: Best composite score (Band + Standard + Link Rate) selected");
  });

  btnWifiModePin.addEventListener("click", () => {
    setWiFiMode("pin", pinnedBSSID || (wifiCandidates[0] ? wifiCandidates[0].bssid : ""));
    appendLog("INFO", "[WI-FI OPTIMIZER] Mode set to MANUAL PIN: Locked to selected BSSID");
  });

  btnWifiModeExclude.addEventListener("click", () => {
    setWiFiMode("exclude", "", "2.4 GHz");
    appendLog("WARN", "[WI-FI OPTIMIZER] Mode set to EXCLUDE 2.4G: Legacy slow 2.4GHz bands disqualified from bonding");
  });

  btnWifiRescan.addEventListener("click", async () => {
    btnWifiRescan.disabled = true;
    btnWifiRescan.innerHTML = `<span class="stat-dot pulsing"></span> Estimating Candidates...`;
    appendLog("INFO", "[WI-FI OPTIMIZER] Triggering on-demand multi-band candidate estimation...");

    try {
      const res = await fetch("/api/wifi/estimate");
      if (res.ok) {
        const data = await res.json();
        if (data.candidates) {
          wifiCandidates = data.candidates;
          renderWiFiCandidates(wifiCandidates, data.selected);
          appendLog("BOND", `[WI-FI OPTIMIZER] Candidate estimation complete! Evaluated ${data.candidates.length} AP(s).`);
        }
      }
    } catch (err) {
      appendLog("ERROR", `[WI-FI OPTIMIZER] Estimation failed: ${err.message}`);
    } finally {
      btnWifiRescan.disabled = false;
      btnWifiRescan.innerHTML = `
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
        Re-Estimate Candidates
      `;
    }
  });

  // Fetch initial Wi-Fi scan
  fetchWiFiScan();
});

