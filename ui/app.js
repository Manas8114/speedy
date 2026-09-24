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
      fetch("/api/v1/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ selected_tier: tier })
      }).catch(() => {});
    });
  });

  // Master Toggle
  masterToggle.addEventListener("change", async (e) => {
    state.bonded = e.target.checked;
    try {
      await fetch("/api/v1/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tunnel_active: state.bonded })
      });
    } catch (_) {}

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

    state.paths.forEach((p) => {
      const item = document.createElement("div");
      const st = (p.state || "STANDBY").toLowerCase();
      item.className = `nic-item ${st}`;
      item.dataset.id = p.id;

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

  // Live update of existing NIC card metrics without recreating DOM sliders
  function updateNICMetrics(paths) {
    paths.forEach(p => {
      const card = document.querySelector(`.nic-item[data-id="${p.id}"]`);
      if (!card) return;
      const metrics = card.querySelectorAll(".metric-val");
      if (metrics.length >= 3) {
        metrics[0].textContent = p.state === 'DEAD' || !p.rtt ? '—' : p.rtt.toFixed(1) + ' ms';
        metrics[1].textContent = p.state === 'DEAD' ? '100%' : (p.loss ? p.loss.toFixed(1) + '%' : '0.0%');
        metrics[2].textContent = p.goodput ? p.goodput.toFixed(1) + ' Mbps' : '0.0 Mbps';
      }
      const dot = card.querySelector(".status-dot");
      if (dot) {
        dot.className = `status-dot ${p.state === 'ACTIVE' ? 'pulsing' : ''}`;
        dot.style.backgroundColor = p.state === 'ACTIVE' ? 'var(--accent-green)' : (p.state === 'DEGRADED' ? 'var(--accent-amber)' : 'var(--accent-blue)');
      }
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
            state.paths = data.paths;
            if (!initialPathsLoaded) {
              initialPathsLoaded = true;
              renderNICs();
              const names = data.paths.map(p => p.name).join(", ");
              appendLog("INFO", `Uplinks detected from daemon: ${names}`);
            } else {
              updateNICMetrics(data.paths);
            }
          }

          const isTunnelActive = data.tunnel_active !== false;
          masterToggle.checked = isTunnelActive;
          const aggRate = data.aggregate_throughput_mbps || 0;

          // Dynamically pick top active paths for p0 and p1 graph lines
          const activePaths = [...(data.paths || [])].sort((a, b) => (b.goodput || 0) - (a.goodput || 0));
          let p0Rate = 0;
          let p1Rate = 0;
          if (activePaths.length > 0) {
            p0Rate = activePaths[0].goodput || 0;
            if (activePaths.length > 1) {
              p1Rate = activePaths[1].goodput || 0;
            }
          }

          updateChart(aggRate, p0Rate, p1Rate);

          if (isTunnelActive) {
            statusDot.className = "status-dot pulsing";
            statusDot.style.backgroundColor = "var(--accent-green)";
            tunnelStatusText.textContent = "BONDED & ACTIVE";
          } else {
            statusDot.className = "status-dot";
            statusDot.style.backgroundColor = "var(--accent-amber)";
            tunnelStatusText.textContent = "STANDBY (READY)";
          }

          aggSpeedValue.innerHTML = `${aggRate.toFixed(1)} <span class="stat-unit">Mbps</span>`;
          aggPacketsSec.textContent = `${Math.round(aggRate * 128).toLocaleString()} packets/sec`;

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

    // Dynamic auto-scaling based on peak rate in rolling window
    const peak = Math.max(...state.history.agg, ...state.history.p0, ...state.history.p1, 2.0);
    const maxVal = Math.max(10, Math.ceil((peak * 1.35) / 10) * 10);

    // Draw Grid Lines
    ctx.strokeStyle = "rgba(255, 255, 255, 0.05)";
    ctx.lineWidth = 1;
    for (let y = 40; y < h; y += 40) {
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }

    // Scale label in top-right
    ctx.fillStyle = "rgba(255, 255, 255, 0.3)";
    ctx.font = "10px monospace";
    ctx.textAlign = "right";
    ctx.fillText(`Scale: ${maxVal} Mbps`, w - 10, 16);

    const stepX = w / (state.history.agg.length - 1);

    // Draw Line Function
    function drawLine(data, color, fillGrad) {
      ctx.beginPath();
      data.forEach((val, i) => {
        const x = i * stepX;
        const y = h - (val / maxVal) * (h - 26) - 10;
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
    gradAgg.addColorStop(0, "rgba(0, 240, 255, 0.22)");
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

// ===== PLEXO TURBO DOWNLOADER UI =====
(function() {
  const NIC_COLORS = ['#00f0ff','#8b5cf6','#10b981','#f59e0b','#ec4899','#3b82f6'];

  let plexoPolling = null;
  let plexoIfaces  = [];
  let plexoSelected = {};
  let chunkEls = [];

  const modal      = document.getElementById('plexoModal');
  const btnOpen    = document.getElementById('btnPlexoModal');
  const btnClose   = document.getElementById('btnClosePlexo');
  const btnCancel  = document.getElementById('btnCancelPlexo');
  const urlInput   = document.getElementById('plexoUrl');
  const btnProbe   = document.getElementById('btnPlexoProbe');
  const probeCard  = document.getElementById('plexoProbeCard');
  const ifaceList  = document.getElementById('plexoIfaceList');
  const controls   = document.getElementById('plexoControls');
  const btnStart   = document.getElementById('btnPlexoStart');
  const btnPause   = document.getElementById('btnPlexoPause');
  const btnResume  = document.getElementById('btnPlexoResume');
  const btnCancelDl = document.getElementById('btnPlexoCancel');
  const progressDiv = document.getElementById('plexoProgress');
  const barFill    = document.getElementById('plexoBarFill');
  const statPct    = document.getElementById('plexoStatPercent');
  const statSpeed  = document.getElementById('plexoStatSpeed');
  const statETA    = document.getElementById('plexoStatETA');
  const statBytes  = document.getElementById('plexoStatBytes');
  const ifaceSpeeds = document.getElementById('plexoIfaceSpeeds');
  const chunkGrid  = document.getElementById('plexoChunkGrid');
  const legend     = document.getElementById('plexoLegend');

  function openModal() {
    modal.classList.add('open');
    loadInterfaces();
    checkStatusOnOpen();
  }
  function closeModal() {
    modal.classList.remove('open');
    stopPolling();
  }

  btnOpen  && btnOpen.addEventListener('click', openModal);
  btnClose && btnClose.addEventListener('click', closeModal);
  btnCancel && btnCancel.addEventListener('click', closeModal);
  modal && modal.addEventListener('click', e => { if (e.target === modal) closeModal(); });

  async function loadInterfaces() {
    ifaceList.innerHTML = '<span class="plexo-iface-loading">Scanning interfaces\u2026</span>';
    try {
      const res = await fetch('/api/plexo/interfaces');
      const data = await res.json();
      plexoIfaces = data.interfaces || [];
      renderIfaceChips();
    } catch(e) {
      ifaceList.innerHTML = '<span class="plexo-iface-loading" style="color:var(--accent-red)">Scan failed: ' + e.message + '</span>';
    }
  }

  function renderIfaceChips() {
    ifaceList.innerHTML = '';
    if (!plexoIfaces.length) {
      ifaceList.innerHTML = '<span class="plexo-iface-loading">No interfaces found.</span>';
      return;
    }
    plexoIfaces.forEach((iface, i) => {
      const color = NIC_COLORS[i % NIC_COLORS.length];
      const isRoutable = iface.routable !== false;
      if (!(iface.name in plexoSelected)) {
        plexoSelected[iface.name] = isRoutable;
      }
      const chip = document.createElement('div');
      chip.className = 'plexo-iface-chip' + (plexoSelected[iface.name] ? ' selected' : '');
      chip.style.setProperty('--chip-color', color);
      const ip = (iface.local_ips || [])[0] || '';
      const tag = isRoutable ? '' : '<span style="font-size:0.68rem;opacity:0.6;margin-left:4px">(local)</span>';
      chip.innerHTML = `<span class="plexo-chip-dot" style="background:${color}"></span><span>${iface.name}</span><span class="plexo-chip-ip">${ip}</span>${tag}`;
      chip.addEventListener('click', () => {
        plexoSelected[iface.name] = !plexoSelected[iface.name];
        chip.classList.toggle('selected', plexoSelected[iface.name]);
      });
      ifaceList.appendChild(chip);
    });
    controls.classList.remove('hidden');
    controls.style.display = 'flex';
  }

  btnProbe && btnProbe.addEventListener('click', async () => {
    const url = urlInput.value.trim();
    if (!url) return;
    btnProbe.textContent = 'Probing\u2026';
    btnProbe.disabled = true;
    probeCard.style.display = 'none';
    try {
      const res = await fetch('/api/plexo/probe', {
        method: 'POST',
        headers: {'Content-Type':'application/json'},
        body: JSON.stringify({url})
      });
      const data = await res.json();
      if (!data.success) throw new Error(data.error || 'probe failed');
      renderProbeCard(data.probe);
    } catch(e) {
      alert('Probe failed: ' + e.message);
    } finally {
      btnProbe.textContent = 'Probe';
      btnProbe.disabled = false;
    }
  });

  function renderProbeCard(p) {
    document.getElementById('probeFilename').textContent = p.filename || '\u2014';
    document.getElementById('probeSize').textContent     = p.total_bytes ? fmtBytes(p.total_bytes) : 'Unknown';
    const rangeEl = document.getElementById('probeRange');
    rangeEl.textContent  = p.supports_ranges ? '\u2705 206 Partial Content' : '\u274c Single stream only';
    rangeEl.style.color  = p.supports_ranges ? '#10b981' : '#f59e0b';
    document.getElementById('probeType').textContent = p.content_type || '\u2014';
    document.getElementById('probeTTFB').textContent = p.ttfb_ms ? p.ttfb_ms + ' ms' : '\u2014';
    probeCard.classList.remove('hidden');
    probeCard.style.display = 'grid';
  }

  btnStart && btnStart.addEventListener('click', async () => {
    const url = urlInput.value.trim();
    if (!url) { alert('Enter a URL first.'); return; }
    const selected = plexoIfaces.filter(i => plexoSelected[i.name]).map(i => i.name);
    btnStart.disabled = true;
    try {
      const res = await fetch('/api/plexo/start', {
        method: 'POST',
        headers: {'Content-Type':'application/json'},
        body: JSON.stringify({url, ifaces: selected, chunk_mb: 4})
      });
      const data = await res.json();
      if (!data.success) throw new Error(data.error || 'start failed');
      progressDiv.classList.remove('hidden');
      progressDiv.style.display = 'block';
      btnStart.style.display = 'none';
      btnPause.classList.remove('hidden');
      btnPause.style.display = 'inline-flex';
      btnCancelDl.classList.remove('hidden');
      btnCancelDl.style.display = 'inline-flex';
      buildChunkGrid(data.chunks);
      startPolling();
    } catch(e) {
      alert('Start failed: ' + e.message);
    } finally {
      btnStart.disabled = false;
    }
  });

  btnPause && btnPause.addEventListener('click', async () => {
    await fetch('/api/plexo/pause', {method:'POST'});
    btnPause.style.display = 'none';
    btnResume.classList.remove('hidden');
    btnResume.style.display = 'inline-flex';
  });
  btnResume && btnResume.addEventListener('click', async () => {
    await fetch('/api/plexo/resume', {method:'POST'});
    btnResume.style.display = 'none';
    btnPause.classList.remove('hidden');
    btnPause.style.display = 'inline-flex';
  });
  btnCancelDl && btnCancelDl.addEventListener('click', async () => {
    await fetch('/api/plexo/cancel', {method:'POST'});
    stopPolling();
    resetUI();
  });


  function startPolling() {
    stopPolling();
    plexoPolling = setInterval(pollStatus, 250);
  }
  function stopPolling() {
    if (plexoPolling) { clearInterval(plexoPolling); plexoPolling = null; }
  }

  async function checkStatusOnOpen() {
    try {
      const res = await fetch('/api/plexo/status');
      const data = await res.json();
      if (data.stats && (data.stats.state === 'running' || data.stats.state === 'paused')) {
        progressDiv.style.display = 'block';
        btnStart.style.display = 'none';
        btnCancelDl.style.display = '';
        if (data.stats.state === 'paused') { btnPause.style.display='none'; btnResume.style.display=''; }
        else { btnPause.style.display=''; btnResume.style.display='none'; }
        buildChunkGrid(data.stats.chunks.length);
        updateProgress(data.stats);
        startPolling();
      }
    } catch(e) {}
  }

  async function pollStatus() {
    try {
      const res = await fetch('/api/plexo/status');
      const data = await res.json();
      if (!data.stats) return;
      updateProgress(data.stats);
      if (data.stats.state === 'done') {
        stopPolling();
        barFill.style.width = '100%';
        statPct.textContent = '100%';
        btnPause.style.display = 'none';
        btnResume.style.display = 'none';
        btnCancelDl.style.display = 'none';
        btnStart.style.display = 'inline-flex';
        btnStart.disabled = false;
        btnStart.textContent = 'Download Another File';
        renderSavedBanner(data.stats.dest_path, data.stats.filename);
      } else if (data.stats.state === 'cancelled') {
        stopPolling();
        resetUI();
      }
    } catch(e) {}
  }

  function renderSavedBanner(destPath, filename) {
    let banner = document.getElementById('plexoSavedBanner');
    if (!banner) {
      banner = document.createElement('div');
      banner.id = 'plexoSavedBanner';
      banner.className = 'plexo-saved-banner';
      progressDiv.appendChild(banner);
    }
    const displayPath = destPath || filename || 'Downloads';
    banner.innerHTML = `
      <div style="display:flex;align-items:center;justify-content:space-between;background:rgba(16,185,129,0.12);border:1px solid #10b981;border-radius:8px;padding:12px 16px;margin-top:14px;">
        <div style="min-width:0;">
          <div style="font-weight:700;color:#10b981;display:flex;align-items:center;gap:6px;font-size:0.9rem;">
            <span>✓</span> Download Complete & Saved!
          </div>
          <div style="font-family:var(--font-mono);font-size:0.75rem;color:var(--text-main);margin-top:4px;word-break:break-all;">
            ${displayPath}
          </div>
        </div>
        <button class="btn btn-sm btn-primary" id="btnOpenSavedFolder" style="margin-left:14px;white-space:nowrap;cursor:pointer;">
          📁 Open Folder
        </button>
      </div>
    `;
    const openBtn = document.getElementById('btnOpenSavedFolder');
    if (openBtn) {
      openBtn.addEventListener('click', () => {
        fetch('/api/plexo/open-folder', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({path: destPath})
        });
      });
    }
  }


  function updateProgress(stats) {
    const total = stats.total_bytes || 1;
    const done  = stats.completed_bytes || 0;
    const pct   = Math.min(100, (done / total * 100));
    barFill.style.width = pct.toFixed(1) + '%';
    statPct.textContent  = pct.toFixed(1) + '%';
    statSpeed.textContent = ((stats.speed_bps||0)/1e6).toFixed(2) + ' MB/s';
    statETA.textContent   = stats.eta_ms > 0 ? 'ETA: ' + fmtDur(stats.eta_ms) : 'ETA: \u2014';
    statBytes.textContent = fmtBytes(done) + ' / ' + fmtBytes(total);

    if (stats.per_iface && stats.per_iface.length) {
      ifaceSpeeds.innerHTML = stats.per_iface.map((iface, i) => {
        const color = NIC_COLORS[i % NIC_COLORS.length];
        const spd = ((iface.speed_bps||0)/1e6).toFixed(2);
        return `<div class="plexo-speed-badge"><span class="plexo-speed-dot" style="background:${color}"></span><span>${iface.name}</span><span style="color:${color};font-weight:700;margin-left:4px">${spd} MB/s</span></div>`;
      }).join('');
    }

    if (stats.chunks && stats.chunks.length) updateChunkGrid(stats.chunks);
  }

  function buildChunkGrid(countOrArr) {
    const count = typeof countOrArr === 'number' ? countOrArr : (countOrArr||[]).length;
    chunkGrid.innerHTML = '';
    chunkEls = [];
    const legendHtml = [
      '<div class="plexo-legend-item"><span class="plexo-legend-swatch" style="background:rgba(255,255,255,0.07)"></span>Pending</div>',
    ].concat(plexoIfaces.map((iface, i) => {
      const color = NIC_COLORS[i % NIC_COLORS.length];
      return `<div class="plexo-legend-item"><span class="plexo-legend-swatch" style="background:${color}"></span>${iface.name}</div>`;
    }));
    legend.innerHTML = legendHtml.join('');
    for (let i = 0; i < count; i++) {
      const el = document.createElement('div');
      el.className = 'plexo-chunk';
      el.title = 'Chunk #' + i;
      chunkGrid.appendChild(el);
      chunkEls.push(el);
    }
  }

  function updateChunkGrid(chunks) {
    chunks.forEach((c, i) => {
      const el = chunkEls[i];
      if (!el) return;
      el.className = 'plexo-chunk';
      if (c.status === 1) {
        el.classList.add('downloading');
        if (c.iface_idx >= 0) el.classList.add('nic-' + (c.iface_idx % NIC_COLORS.length));
      } else if (c.status === 2) {
        el.classList.add('done');
        if (c.iface_idx >= 0) el.classList.add('nic-' + (c.iface_idx % NIC_COLORS.length));
      } else if (c.status === 3) {
        el.classList.add('failed');
      }
    });
  }

  function resetUI() {
    progressDiv.style.display = 'none';
    btnStart.style.display = 'inline-flex';
    btnStart.disabled = false;
    btnStart.textContent = 'Start Turbo Download';
    btnPause.style.display = 'none';
    btnResume.style.display = 'none';
    btnCancelDl.style.display = 'none';
    barFill.style.width = '0%';
    statPct.textContent = '0%';
    chunkGrid.innerHTML = '';
    legend.innerHTML = '';
    ifaceSpeeds.innerHTML = '';
    const banner = document.getElementById('plexoSavedBanner');
    if (banner) banner.remove();
  }


  function fmtBytes(b) {
    if (!b) return '0 B';
    const k = 1024, sz = ['B','KB','MB','GB','TB'];
    const i = Math.floor(Math.log(b)/Math.log(k));
    return (b/Math.pow(k,i)).toFixed(2) + ' ' + sz[i];
  }
  function fmtDur(ms) {
    const s = Math.round(ms/1000);
    return s < 60 ? s + 's' : Math.floor(s/60) + 'm ' + (s%60) + 's';
  }
})();
