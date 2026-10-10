<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import {
    SvelteFlow,
    Background,
    Controls,
    MiniMap,
    ConnectionLineType,
    useSvelteFlow,
    getNodesBounds,
    getViewportForBounds,
    type Node as FlowNode,
    type Edge,
    type Connection,
  } from '@xyflow/svelte';
  import {
    Plus,
    Play,
    Square,
    ChevronDown,
    Search,
    Upload,
    Download,
    Undo2,
    Redo2,
    Settings2,
    Check,
    LoaderCircle,
    AlertTriangle,
    X,
    Network,
    Activity,
    PlugZap,
    Cable as CableIcon,
    ArrowUpRight,
    Copy,
    FolderOpen,
    Scissors,
    Maximize,
    Keyboard,
    Layers,
    CircleHelp,
  } from 'lucide-svelte';
  import DeviceNode from './components/DeviceNode.svelte';
  import DeviceGlyph from './components/DeviceGlyph.svelte';
  import Inspector from './components/Inspector.svelte';
  import BottomPanel from './components/BottomPanel.svelte';
  import SettingsDialog from './components/SettingsDialog.svelte';
  import { api, copy, download } from './lib/api';
  import {
    kindNames,
    descriptions,
    ports,
    makeDevice,
    secret,
    type Kind,
    type Lab,
    type Runtime,
    type System,
    type Event as LabEvent,
    type DeviceData,
    type NodeConfig,
    type Cable,
    type Radius,
    type ACS,
    type ImageStatus,
  } from './lib/types';

  const flow = useSvelteFlow();
  const nodeTypes = { device: DeviceNode };
  const palette: { name: string; kinds: Kind[] }[] = [
    { name: 'ACTIVE EQUIPMENT', kinds: ['router', 'olt', 'switch', 'onu'] },
    { name: 'PASSIVE INFRASTRUCTURE', kinds: ['odf', 'splitter', 'odp'] },
  ];
  let lab = $state<Lab | null>(null);
  let labs = $state<Lab[]>([]);
  let runtime = $state<Runtime | null>(null);
  let system = $state<System | null>(null);
  let nodes = $state<FlowNode<DeviceData>[]>([]);
  let edges = $state<Edge[]>([]);
  let events = $state<LabEvent[]>([]);
  let selectedId = $state<string | null>(null);
  let selectedLinkId = $state<string | null>(null);
  let paletteQuery = $state('');
  let tab = $state('subscribers');
  let loading = $state(true);
  let saving = $state(false);
  let dirty = $state(false);
  let busy = $state(false);
  let settings = $state(false);
  let newModal = $state(false);
  let integration = $state(false);
  let scenarios = $state(false);
  let shortcuts = $state(false);
  let newName = $state('Kampung fiber');
  let newCount = $state(32);
  let error = $state('');
  let notice = $state('');
  let connections = $state<{
    devices: {
      id: string;
      name: string;
      kind: string;
      ip: string;
      username: string;
      password: string;
      services: Record<string, { port: number; community?: string }>;
      profile: string;
    }[];
    onus?: {
      id: string;
      name: string;
      oltId: string;
      pon: number;
      serial: string;
      mac: string;
      ftthIdentity: string;
    }[];
    activeLab?: { id: string; name: string; phase: string } | null;
    radius: Radius;
    note: string;
  } | null>(null);
  let reveal = $state(false);
  let history = $state<number[]>([]);
  let undoHistory = $state<Lab[]>([]);
  let redoHistory = $state<Lab[]>([]);
  let canvas = $state<HTMLDivElement>();
  let canvasWidth = $state(0);
  let canvasHeight = $state(0);
  let flowReady = $state(false);
  let fileInput = $state<HTMLInputElement>();
  let librarySearch = $state<HTMLInputElement>();
  let fittedViewport = $state<string | null>(null);
  let lastSaveOK = true;
  let source: EventSource | undefined;
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  let savePromise: Promise<void> | null = null;
  let lastRevision = 0;
  let lastGood: Lab | null = null;
  let lastRefresh = 0;
  let dragging = false;
  let lastHistoryAt = 0;

  const selectedNode = $derived(lab?.nodes.find((n) => n.id === selectedId));
  const selectedLink = $derived(
    lab?.links.find((e) => e.id === selectedLinkId)
  );
  const locked = $derived(
    !!runtime && !['stopped', 'error'].includes(runtime.phase)
  );
  const running = $derived(runtime?.phase === 'running');
  const phase = $derived(runtime?.phase || 'stopped');
  const faultCount = $derived(lab?.faults.length || 0);
  const fleet = $derived(
    lab?.nodes.filter((n) =>
      `${n.label} ${n.id} ${kindNames[n.kind]}`
        .toLowerCase()
        .includes(paletteQuery.toLowerCase())
    ) || []
  );
  const fitKey = $derived(
    lab && canvasWidth > 0 && canvasHeight > 0
      ? lab.id + ':' + canvasWidth + ':' + canvasHeight
      : null
  );
  $effect(() => {
    if (lab && flowReady && fitKey && fittedViewport !== fitKey) {
      const id = lab.id;
      const key = fitKey;
      const width = canvasWidth;
      const height = canvasHeight;
      let cancelled = false;
      untrack(() => {
        void (async () => {
          await tick();
          if (cancelled || lab?.id !== id) return;
          if (nodes.length) {
            // Use the measured canvas and known node dimensions. Queuing an
            // initial fit before the flow has a size can leave it at zoom 1.
            const viewport = getViewportForBounds(
              getNodesBounds(nodes),
              width,
              height,
              0.08,
              1,
              0.16
            );
            if (!(await flow.setViewport(viewport))) return;
          }
          if (!cancelled && lab?.id === id) fittedViewport = key;
        })();
      });
      return () => {
        cancelled = true;
      };
    }
  });
  function clone<T>(value: T): T {
    return JSON.parse(JSON.stringify(value));
  }
  function problem(message: string) {
    error = message;
  }
  function toast(message: string) {
    notice = message;
    setTimeout(() => {
      if (notice === message) notice = '';
    }, 3500);
  }

  function graph() {
    if (!lab) return;
    const sessions = new Map(runtime?.sessions.map((s) => [s.onuId, s]) || []);
    const current = new Map(nodes.map((n) => [n.id, n]));
    nodes = lab.nodes.map((n) => ({
      ...current.get(n.id),
      id: n.id,
      type: 'device',
      initialWidth: n.kind === 'olt' ? 222 : n.kind === 'onu' ? 168 : 190,
      initialHeight: 84,
      position: dragging
        ? current.get(n.id)?.position || n.position
        : n.position,
      selected: n.id === selectedId,
      data: {
        device: n,
        state: runtime?.nodes[n.id],
        session: sessions.get(n.id),
      },
    }));
    edges = lab.links.map((e) => {
      const broken = lab!.faults.some(
        (f) =>
          f.targetId === e.id &&
          (f.kind === 'cable_cut' || f.kind === 'attenuation')
      );
      const selected = e.id === selectedLinkId;
      return {
        id: e.id,
        source: e.source,
        target: e.target,
        sourceHandle: e.sourcePort,
        targetHandle: e.targetPort,
        type: 'smoothstep',
        selected,
        style: `stroke: ${broken ? '#d76a4d' : selected ? '#137f62' : e.medium === 'fiber' ? '#79aaa0' : '#7892ae'}; stroke-width: ${selected ? 2.5 : 1.6};${broken ? 'stroke-dasharray: 5 5;' : ''}`,
        interactionWidth: 22,
        label: broken
          ? lab!.faults.find((f) => f.targetId === e.id)?.kind === 'cable_cut'
            ? 'CUT'
            : 'LOSS'
          : undefined,
        labelStyle: 'font-size: 10px; font-weight: 700; fill: #b3472e;',
        labelBgStyle: 'fill: #fff2ec;',
      };
    });
  }
  function accept(next: Lab) {
    lab = next;
    lastRevision = next.revision;
    lastGood = clone(next);
    dirty = false;
    labs = labs.map((l) => (l.id === next.id ? next : l));
    graph();
  }
  async function load(id: string) {
    if (!(await flush())) return;
    source?.close();
    selectedId = null;
    selectedLinkId = null;
    undoHistory = [];
    redoHistory = [];
    history = [];
    runtime = null;
    events = [];
    const [next, view, log] = await Promise.all([
      api<Lab>(`/labs/${id}`),
      api<Runtime>(`/labs/${id}/runtime`),
      api<LabEvent[]>(`/labs/${id}/events`),
    ]);
    runtime = view;
    events = log;
    fittedViewport = null;
    accept(next);
    try {
      localStorage.setItem('fiberlab.selectedLab', next.id);
    } catch {
      /* The workspace still works when browser storage is disabled. */
    }
    subscribe(id);
  }
  function subscribe(id: string) {
    source?.close();
    source = new EventSource(`/api/v1/labs/${id}/stream`);
    source.addEventListener('state', async (raw) => {
      if (lab?.id !== id) return;
      const message = JSON.parse((raw as MessageEvent).data) as {
        runtime: Runtime;
        revision: number;
        events: LabEvent[];
      };
      runtime = message.runtime;
      if (message.events.length) {
        const unique = new Map(
          [...events, ...message.events].map((e) => [e.seq, e])
        );
        events = [...unique.values()].sort((a, b) => a.seq - b.seq).slice(-250);
      }
      if (Date.now() - lastHistoryAt > 950) {
        history = [...history.slice(-79), runtime.metrics.activeSessions];
        lastHistoryAt = Date.now();
      }
      if (
        message.revision !== lastRevision &&
        !saving &&
        !dirty &&
        Date.now() - lastRefresh > 1000
      ) {
        lastRefresh = Date.now();
        try {
          const next = await api<Lab>(`/labs/${id}`);
          if (lab?.id === id && !saving && !dirty) accept(next);
        } catch {
          /* A later stream update retries synchronization. */
        }
      }
      if (!dragging) graph();
    });
  }
  async function saveNow(): Promise<void> {
    if (!lab || !dirty) return;
    if (savePromise) {
      await savePromise;
      if (dirty) return saveNow();
      return;
    }
    saving = true;
    dirty = false;
    const draft = clone(lab);
    draft.revision = lastRevision;
    savePromise = (async () => {
      try {
        const next = await api<Lab>(`/labs/${draft.id}`, 'PUT', draft);
        lastRevision = next.revision;
        lastGood = clone(next);
        if (lab?.id === next.id) {
          if (dirty) lab.revision = next.revision;
          else accept(next);
        }
      } catch (e) {
        lastSaveOK = false;
        problem((e as Error).message);
        if (lastGood) {
          lab = clone(lastGood);
          dirty = false;
          graph();
        }
      } finally {
        saving = false;
      }
    })();
    await savePromise;
    savePromise = null;
    if (dirty) await saveNow();
  }
  async function flush() {
    if (saveTimer) clearTimeout(saveTimer);
    if (savePromise) await savePromise;
    if (dirty) await saveNow();
    const result = lastSaveOK;
    lastSaveOK = true;
    return result;
  }
  function mutate(change: (l: Lab) => void, record = true) {
    if (!lab) return;
    if (record && !locked) {
      undoHistory = [...undoHistory.slice(-19), clone(lab)];
      redoHistory = [];
    }
    const next = clone(lab);
    change(next);
    lab = next;
    dirty = true;
    graph();
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(saveNow, 450);
  }
  async function undo(redo = false) {
    if (!lab || locked) return;
    if (!(await flush())) return;
    const stack = redo ? redoHistory : undoHistory;
    const next = stack.at(-1);
    if (!next) return;
    if (redo) {
      redoHistory = redoHistory.slice(0, -1);
      undoHistory = [...undoHistory, clone(lab)];
    } else {
      undoHistory = undoHistory.slice(0, -1);
      redoHistory = [...redoHistory, clone(lab)];
    }
    lab = clone(next);
    lab.revision = lastRevision;
    dirty = true;
    graph();
    await saveNow();
  }
  function add(kind: Kind, position?: { x: number; y: number }) {
    if (!lab || locked) return;
    if (
      kind === 'onu' &&
      lab.nodes.filter((n) => n.kind === 'onu').length >= 500
    )
      return problem('A lab supports up to 500 ONUs.');
    const bounds = canvas!.getBoundingClientRect();
    const p =
      position ||
      flow.screenToFlowPosition({
        x: bounds.x + bounds.width / 2 - 90,
        y: bounds.y + bounds.height / 2 - 40,
      });
    const node = makeDevice(
      kind,
      lab.nodes.filter((n) => n.kind === kind).length + 1,
      p
    );
    selectedId = node.id;
    selectedLinkId = null;
    mutate((l) => {
      l.nodes.push(node);
      if (kind === 'onu') {
        const used = new Set(l.subscribers.map((s) => s.address));
        let i = 10;
        let address = '';
        do {
          address = `172.30.${Math.floor(i / 256)}.${i % 256}`;
          i++;
        } while (used.has(address));
        let j = 1;
        while (
          l.subscribers.some(
            (s) => s.username === `pelanggan${String(j).padStart(4, '0')}`
          )
        )
          j++;
        l.subscribers.push({
          id: `subscriber-${secret().slice(0, 8)}`,
          onuId: node.id,
          username: `pelanggan${String(j).padStart(4, '0')}`,
          password: secret().slice(0, 12),
          enabled: true,
          rateLimit: '1M/1M',
          address,
        });
      }
    });
  }
  function drop(event: DragEvent) {
    event.preventDefault();
    const kind = event.dataTransfer?.getData(
      'application/fiberlab-device'
    ) as Kind;
    if (kind && kindNames[kind])
      add(
        kind,
        flow.screenToFlowPosition({
          x: event.clientX - 85,
          y: event.clientY - 35,
        })
      );
  }
  function validConnection(c: Connection | Edge): boolean {
    if (
      !lab ||
      locked ||
      c.source === c.target ||
      !c.sourceHandle ||
      !c.targetHandle
    )
      return false;
    const sourceNode = lab.nodes.find((n) => n.id === c.source);
    const targetNode = lab.nodes.find((n) => n.id === c.target);
    if (!sourceNode || !targetNode) return false;
    const s = ports(sourceNode).find((p) => p.id === c.sourceHandle);
    const t = ports(targetNode).find((p) => p.id === c.targetHandle);
    if (
      !s ||
      !t ||
      s.direction !== 'out' ||
      t.direction !== 'in' ||
      s.medium !== t.medium
    )
      return false;
    if (
      lab.links.some(
        (e) =>
          (e.source === c.source && e.sourcePort === c.sourceHandle) ||
          e.target === c.target
      )
    )
      return false;
    let id: string | undefined = c.source;
    const seen = new Set<string>();
    while (id) {
      if (id === c.target || seen.has(id)) return false;
      seen.add(id);
      id = lab.links.find((e) => e.target === id)?.source;
    }
    return true;
  }
  function connect(c: Connection) {
    if (!validConnection(c)) return;
    const n = lab!.nodes.find((n) => n.id === c.source)!;
    const medium = ports(n).find((p) => p.id === c.sourceHandle)!.medium as
      'fiber' | 'ethernet';
    mutate((l) =>
      l.links.push({
        id: `cable-${secret().slice(0, 8)}`,
        source: c.source,
        sourcePort: c.sourceHandle!,
        target: c.target,
        targetPort: c.targetHandle!,
        medium,
        lengthM: medium === 'fiber' ? 100 : 3,
        lossDb: medium === 'fiber' ? 0.5 : 0,
      })
    );
  }
  function selectNode(id: string, focus = false) {
    selectedId = id;
    selectedLinkId = null;
    graph();
    if (focus) {
      const n = lab?.nodes.find((n) => n.id === id);
      if (n)
        flow.setCenter(n.position.x + 90, n.position.y + 45, {
          zoom: 1,
          duration: 300,
        });
    }
  }
  function remove(type: 'node' | 'link', id: string) {
    if (locked) return;
    mutate((l) => {
      if (type === 'node') {
        l.nodes = l.nodes.filter((n) => n.id !== id);
        const removed = new Set(
          l.links
            .filter((e) => e.source === id || e.target === id)
            .map((e) => e.id)
        );
        l.links = l.links.filter((e) => !removed.has(e.id));
        l.subscribers = l.subscribers.filter((s) => s.onuId !== id);
        l.faults = l.faults.filter(
          (f) =>
            f.targetId !== id &&
            !f.targetId.startsWith(`${id}:`) &&
            !removed.has(f.targetId)
        );
      } else {
        l.links = l.links.filter((e) => e.id !== id);
        l.faults = l.faults.filter((f) => f.targetId !== id);
      }
    });
    if (selectedId === id) selectedId = null;
    if (selectedLinkId === id) selectedLinkId = null;
  }
  function moved(movedNodes: FlowNode[]) {
    dragging = false;
    mutate((l) => {
      for (const n of movedNodes) {
        const d = l.nodes.find((v) => v.id === n.id);
        if (d) d.position = n.position;
      }
    });
  }
  async function action(kind: string, targetId: string, value = '') {
    if (!lab) return;
    if (!(await flush())) return;
    try {
      const next = await api<Lab>(`/labs/${lab.id}/actions`, 'POST', {
        kind,
        targetId,
        value,
      });
      accept(next);
      toast(`${kind.replaceAll('_', ' ')} · ${targetId}`);
    } catch (e) {
      problem((e as Error).message);
    }
  }
  async function fault(
    kind: string,
    targetType: string,
    targetId: string,
    value = 0
  ) {
    if (!lab) return;
    if (!(await flush())) return;
    try {
      accept(
        await api<Lab>(`/labs/${lab.id}/faults`, 'POST', {
          id: '',
          kind,
          targetType,
          targetId,
          value,
          createdAt: new Date().toISOString(),
        })
      );
      toast('Fault injected. Follow its effects in Activity.');
      tab = 'activity';
    } catch (e) {
      problem((e as Error).message);
    }
  }
  async function repair(id: string) {
    if (!lab) return;
    if (!(await flush())) return;
    try {
      accept(await api<Lab>(`/labs/${lab.id}/faults/${id}`, 'DELETE'));
      toast('Fault repaired');
    } catch (e) {
      problem((e as Error).message);
    }
  }
  async function run() {
    if (!lab || busy) return;
    if (!(await flush())) return;
    busy = true;
    try {
      if (locked) {
        runtime = await api<Runtime>(`/labs/${lab.id}/stop`, 'POST', {});
      } else {
        system = await api<System>('/system');
        if (!system.helperOnline || !lab.imageId) {
          settings = true;
          return;
        }
        runtime = await api<Runtime>(`/labs/${lab.id}/start`, 'POST', {});
      }
      graph();
    } catch (e) {
      problem((e as Error).message);
    } finally {
      busy = false;
    }
  }
  async function create() {
    busy = true;
    try {
      if (!(await flush())) return;
      const next = await api<Lab>('/labs', 'POST', {
        name: newName,
        count: Math.max(1, newCount),
        empty: newCount === 0,
      });
      labs = [next, ...labs];
      newModal = false;
      await load(next.id);
    } catch (e) {
      problem((e as Error).message);
    } finally {
      busy = false;
    }
  }
  async function imported(event: globalThis.Event) {
    const file = (event.target as HTMLInputElement).files?.[0];
    if (!file) return;
    try {
      if (file.size > 4 * 1024 * 1024)
        throw new Error('Topology import is limited to 4 MiB.');
      const next = await api<Lab>(
        '/labs/import',
        'POST',
        JSON.parse(await file.text())
      );
      labs = [next, ...labs];
      await load(next.id);
      toast('Topology imported');
    } catch (e) {
      problem((e as Error).message);
    } finally {
      fileInput!.value = '';
    }
  }
  async function exported() {
    if (!lab) return;
    if (!(await flush())) return;
    download(
      `${lab.name.replace(/[^a-z0-9_-]/gi, '-')}.fiberlab.json`,
      JSON.stringify(lab, null, 2)
    );
  }
  async function integrate() {
    if (!lab) return;
    if (!(await flush())) return;
    try {
      connections = await api(`/labs/${lab.id}/connections`);
      integration = true;
    } catch (e) {
      problem((e as Error).message);
    }
  }
  async function capture(id: string) {
    if (!lab) return;
    toast('Capturing actual packets for 10 seconds…');
    try {
      const response = await fetch(`/api/v1/labs/${lab.id}/capture`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ linkId: id, seconds: 10 }),
      });
      if (!response.ok) throw new Error((await response.json()).error);
      download(
        `${id}.pcap`,
        await response.arrayBuffer(),
        'application/vnd.tcpdump.pcap'
      );
      toast('Packet capture downloaded');
    } catch (e) {
      problem((e as Error).message);
    }
  }
  function terminal(id: string) {
    selectNode(id);
    tab = 'terminal';
  }
  async function saveRadius(radius: Radius, trapAddress: string) {
    mutate((l) => {
      l.radius = radius;
      l.trapAddress = trapAddress;
    });
    if (!(await flush()))
      throw new Error(
        'Settings could not be saved. Check the fields and try again.'
      );
  }
  async function saveImage(id: string) {
    mutate((l) => (l.imageId = id));
    if (!(await flush()))
      throw new Error('The CHR image selection could not be saved.');
  }
  async function saveACS(acs: ACS) {
    mutate((l) => (l.acs = structuredClone($state.snapshot(acs))));
    if (!(await flush()))
      throw new Error(
        'ACS settings could not be saved. Check the URL, listener and credentials.'
      );
  }
  function key(event: KeyboardEvent) {
    const target = event.target as HTMLElement;
    if (
      event.key === 'Escape' &&
      (settings || newModal || integration || scenarios || shortcuts)
    ) {
      settings = newModal = integration = scenarios = shortcuts = false;
      return;
    }
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement ||
      target.isContentEditable ||
      target.closest('.xterm')
    )
      return;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') {
      event.preventDefault();
      undo(event.shiftKey);
    }
    if (event.key === 'Escape') {
      settings = false;
      newModal = false;
      integration = false;
      scenarios = false;
      shortcuts = false;
      selectedId = null;
      selectedLinkId = null;
      graph();
    }
    if (event.key === 'f') flow.fitView({ duration: 300, padding: 0.16 });
    if (event.key === '/') {
      event.preventDefault();
      librarySearch?.focus();
    }
  }

  onMount(() => {
    let alive = true;
    (async () => {
      try {
        const result = await api<Lab[]>('/labs');
        if (!alive) return;
        labs = result;
        system = await api<System>('/system');
        if (labs[0]) {
          let remembered = '';
          try {
            remembered = localStorage.getItem('fiberlab.selectedLab') || '';
          } catch {
            /* Use the newest lab when browser storage is disabled. */
          }
          await load(
            labs.find((entry) => entry.id === remembered)?.id || labs[0].id
          );
          const available = await api<ImageStatus>('/images');
          if (lab && !lab.imageId && available.images[0])
            await saveImage(available.images[0].id);
        } else newModal = true;
      } catch (e) {
        problem((e as Error).message);
      } finally {
        loading = false;
      }
    })();
    const systemTimer = setInterval(async () => {
      try {
        system = await api<System>('/system');
        if (integration && lab)
          connections = await api(`/labs/${lab.id}/connections`);
      } catch {
        /* Connection indicator is refreshed on the next successful request. */
      }
    }, 10000);
    window.addEventListener('keydown', key);
    const beforeUnload = (e: BeforeUnloadEvent) => {
      if (dirty || saving) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => {
      alive = false;
      source?.close();
      if (saveTimer) clearTimeout(saveTimer);
      clearInterval(systemTimer);
      window.removeEventListener('keydown', key);
      window.removeEventListener('beforeunload', beforeUnload);
    };
  });
</script>

<svelte:head
  ><title
    >{lab ? `${lab.name} · Fiberlab` : 'Fiberlab · FTTH network lab'}</title
  ></svelte:head
>
<div class="app-shell">
  <header class="app-header">
    <a class="brand" href="/" aria-label="Fiberlab home"
      ><span class="brand-mark"
        ><svg viewBox="0 0 30 30" fill="none"
          ><path
            d="M8 22V8h14M8 15h10"
            stroke="currentColor"
            stroke-width="2.5"
            stroke-linecap="round"
          /><circle cx="22" cy="8" r="2.5" fill="currentColor" /><circle
            cx="18"
            cy="15"
            r="2.5"
            fill="currentColor"
          /></svg
        ></span
      ><strong>fiberlab<span>.</span></strong><span class="brand-tag"
        >NETWORK SANDBOX</span
      ></a
    >
    <nav class="main-nav">
      <button
        class="current"
        onclick={() => {
          scenarios = false;
          integration = false;
        }}><Network size={15} />Topology</button
      ><button
        class:current={scenarios}
        onclick={() => (scenarios = !scenarios)}
        ><Activity size={15} />Scenarios{#if faultCount}<span>{faultCount}</span
          >{/if}</button
      ><button class:current={integration} onclick={integrate}
        ><PlugZap size={15} />Integration</button
      >
    </nav>
    <div class="header-right">
      <span class="local-indicator"><i></i>LOCAL WORKSPACE</span><button
        class="icon-button header-icon"
        onclick={() => (shortcuts = true)}
        title="Keyboard shortcuts"><CircleHelp size={17} /></button
      ><button
        class="icon-button header-icon"
        onclick={() => (settings = true)}
        title="Runtime settings"><Settings2 size={17} /></button
      ><span class="user-avatar">FL</span>
    </div>
  </header>
  <div class="workspace-toolbar">
    <div class="lab-switcher">
      <FolderOpen size={17} /><select
        aria-label="Select lab"
        value={lab?.id || ''}
        onchange={(e) =>
          load(e.currentTarget.value).catch((e) => problem(e.message))}
        >{#each labs as item}<option value={item.id}>{item.name}</option
          >{/each}</select
      ><ChevronDown size={12} /><span class="toolbar-divider"></span><span
        class="save-status"
        >{#if saving}<LoaderCircle
            size={12}
            class="spin"
          />Saving{:else if dirty}<span class="pending-dot"
          ></span>Unsaved{:else}<Check size={13} />Saved locally{/if}</span
      >
    </div>
    <div class="workspace-actions">
      <button
        class="icon-button"
        disabled={!undoHistory.length || locked}
        onclick={() => undo()}
        title="Undo · Ctrl Z"><Undo2 size={16} /></button
      ><button
        class="icon-button"
        disabled={!redoHistory.length || locked}
        onclick={() => undo(true)}
        title="Redo · Ctrl Shift Z"><Redo2 size={16} /></button
      ><span class="toolbar-divider"></span><button
        class="button toolbar-button"
        onclick={() => fileInput!.click()}
        ><Upload size={14} /><span>Import</span></button
      ><button class="button toolbar-button" disabled={!lab} onclick={exported}
        ><Download size={14} /><span>Export</span></button
      ><button class="button toolbar-button" onclick={() => (newModal = true)}
        ><Plus size={15} /><span>New lab</span></button
      ><span class="toolbar-divider"></span><button
        class="button run-button"
        class:stop-button={locked}
        disabled={busy || !lab || phase === 'stopping'}
        onclick={run}
        >{#if busy || ['preparing', 'booting', 'connecting', 'stopping'].includes(phase)}<LoaderCircle
            size={15}
            class="spin"
          />{:else if locked}<Square
            size={13}
            fill="currentColor"
          />{:else}<Play size={14} fill="currentColor" />{/if}{phase ===
        'stopping'
          ? 'Stopping…'
          : locked
            ? 'Stop lab'
            : 'Run lab'}</button
      >
    </div>
  </div>
  <input
    class="hidden"
    type="file"
    accept=".json,.fiberlab"
    bind:this={fileInput}
    onchange={imported}
  />
  <main class="workspace">
    <aside class="device-library">
      <div class="panel-heading">
        <span>DEVICE LIBRARY</span><Layers size={14} />
      </div>
      <div class="library-search">
        <Search size={14} /><input
          aria-label="Search devices"
          bind:value={paletteQuery}
          bind:this={librarySearch}
          placeholder="Find a device…"
        /><kbd>/</kbd>
      </div>
      <div class="library-scroll">
        {#each palette as group}<section class="palette-group">
            <h3>{group.name}</h3>
            {#each group.kinds.filter((kind) => !paletteQuery || `${kindNames[kind]} ${descriptions[kind]}`
                  .toLowerCase()
                  .includes(paletteQuery.toLowerCase())) as kind}<button
                class="palette-device"
                disabled={locked}
                draggable={!locked}
                ondragstart={(e) => {
                  e.dataTransfer?.setData('application/fiberlab-device', kind);
                  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copy';
                }}
                onclick={() => add(kind)}
                title={`Add ${kindNames[kind]}`}
                ><span class="palette-symbol kind-{kind}"
                  ><DeviceGlyph {kind} size={22} /></span
                ><span
                  ><strong>{kindNames[kind]}</strong><small
                    >{descriptions[kind]}</small
                  ></span
                ><Plus size={13} class="palette-add" /></button
              >{/each}
          </section>{/each}
        <div class="library-divider"></div>
        <section class="fleet-section">
          <h3>IN THIS LAB <span>{lab?.nodes.length || 0}</span></h3>
          {#each fleet.slice(0, paletteQuery ? 80 : 12) as node}<button
              class="fleet-device"
              class:active={selectedId === node.id}
              onclick={() => selectNode(node.id, true)}
              ><DeviceGlyph kind={node.kind} size={14} /><span
                >{node.label}</span
              ><i
                class="status-dot status-{runtime?.nodes[node.id]?.status ||
                  'ready'}"
              ></i></button
            >{/each}{#if fleet.length > (paletteQuery ? 80 : 12)}<span
              class="fleet-more"
              >+{fleet.length - (paletteQuery ? 80 : 12)} devices · search to find</span
            >{/if}
        </section>
      </div>
      <div class="library-footer">
        <span class="library-footer-icon"><CableIcon size={17} /></span>
        <p>Drag devices onto the canvas.<br />Connect matching ports.</p>
      </div>
    </aside>
    <section class="workspace-center">
      <div
        class="canvas-area"
        bind:this={canvas}
        bind:clientWidth={canvasWidth}
        bind:clientHeight={canvasHeight}
        ondragover={(e) => {
          e.preventDefault();
          if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
        }}
        ondrop={drop}
        role="application"
        aria-label="FTTH network topology canvas"
      >
        <div class="canvas-heading">
          <span class="workspace-label"
            >ACCESS NETWORK <span>/</span>
            {lab?.nodes.find((n) => n.kind === 'olt')
              ? 'GPON'
              : 'TOPOLOGY'}</span
          >
          <div class="canvas-live">
            <span
              class="status-pill"
              class:status-online={running}
              class:status-ready={!running}
              ><i></i>{running
                ? 'Live runtime'
                : ['stopped', 'error'].includes(phase)
                  ? 'Topology preview'
                  : phase}</span
            >{#if faultCount}<button
                class="fault-pill"
                onclick={() => (scenarios = true)}
                ><AlertTriangle size={12} />{faultCount} active {faultCount ===
                1
                  ? 'fault'
                  : 'faults'}</button
              >{/if}
          </div>
        </div>
        {#if loading}<div class="canvas-loading">
            <LoaderCircle class="spin" size={28} /><span
              >Opening your workspace…</span
            >
          </div>{/if}
        {#if lab}
          <SvelteFlow
            bind:nodes
            bind:edges
            {nodeTypes}
            oninit={() => (flowReady = true)}
            connectionLineType={ConnectionLineType.SmoothStep}
            minZoom={0.08}
            maxZoom={2}
            onlyRenderVisibleElements={fittedViewport === fitKey}
            nodesConnectable={!locked}
            deleteKey={locked ? null : ['Delete', 'Backspace']}
            isValidConnection={validConnection}
            onconnect={connect}
            onnodeclick={({ node }) => selectNode(node.id)}
            onedgeclick={({ edge }) => {
              selectedLinkId = edge.id;
              selectedId = null;
              graph();
            }}
            onpaneclick={() => {
              selectedId = null;
              selectedLinkId = null;
              graph();
            }}
            onnodedragstart={() => (dragging = true)}
            onnodedragstop={({ nodes: movedNodes }) => moved(movedNodes)}
            ondelete={({ nodes: deletedNodes, edges: deletedEdges }) => {
              if (locked) return;
              for (const n of deletedNodes) remove('node', n.id);
              for (const e of deletedEdges)
                if (lab?.links.some((l) => l.id === e.id)) remove('link', e.id);
            }}
          >
            <Background
              gap={22}
              size={1}
              patternColor="#d2dfd8"
              bgColor="#f5f8f5"
            />
            <Controls position="bottom-left" showLock={false} />
            <MiniMap
              position="bottom-right"
              pannable
              zoomable
              nodeColor={(n) =>
                (n.data as DeviceData).device.kind === 'router'
                  ? '#668ec0'
                  : (n.data as DeviceData).device.kind === 'olt'
                    ? '#3d9d86'
                    : (n.data as DeviceData).device.kind === 'onu'
                      ? '#ae9cc1'
                      : '#c6b789'}
              maskColor="rgba(236,243,238,.65)"
            />
          </SvelteFlow>
          {#if lab.nodes.length === 0}<div class="empty-canvas">
              <span class="empty-canvas-icon"><Network size={37} /></span>
              <h2>Your network starts here.</h2>
              <p>
                Drag a MikroTik and an OLT from the library,<br />then connect
                their Ethernet ports.
              </p>
              <button class="button primary" onclick={() => add('router')}
                ><Plus size={15} />Add a MikroTik</button
              >
            </div>{/if}
          <div class="canvas-hint">
            <span class="hint-dot"></span>{locked
              ? 'Select a cable to inject a real network fault'
              : 'Drag between ports to connect devices'}<span
              class="hint-separator">·</span
            ><button
              onclick={() => flow.fitView({ duration: 300, padding: 0.16 })}
              >Fit view <kbd>F</kbd></button
            >
          </div>
          {#if ['preparing', 'booting', 'connecting'].includes(phase)}<div
              class="runtime-progress"
            >
              <LoaderCircle size={16} class="spin" />
              <div>
                <strong>{runtime?.message}</strong>
                <div>
                  <span style={`width:${runtime?.progress || 0}%`}></span>
                </div>
              </div>
              <span class="mono">{runtime?.progress || 0}%</span>
            </div>{/if}
        {/if}
      </div>
      {#if lab}<BottomPanel
          {lab}
          {runtime}
          {events}
          bind:tab
          {selectedId}
          onselect={(id) => selectNode(id, true)}
          onaction={action}
          onsetup={() => (settings = true)}
          {history}
        />{/if}
    </section>
    {#if lab}<Inspector
        {lab}
        {runtime}
        node={selectedNode}
        link={selectedLink}
        onclose={() => {
          selectedId = null;
          selectedLinkId = null;
          graph();
        }}
        onnode={(id, config, label) =>
          mutate((l) => {
            const n = l.nodes.find((n) => n.id === id);
            if (n) {
              Object.assign(n.config, config);
              if (label !== undefined) n.label = label;
            }
          })}
        onlink={(id, patch) =>
          mutate((l) => {
            const e = l.links.find((e) => e.id === id);
            if (e) Object.assign(e, patch);
          })}
        onaction={action}
        onfault={fault}
        onrepair={repair}
        onremove={remove}
        onterminal={terminal}
        oncapture={capture}
        onrename={(name) => mutate((l) => (l.name = name))}
        onsetup={() => (settings = true)}
        onintegrate={integrate}
      />{/if}
  </main>
  <footer class="app-statusbar">
    <div>
      <span
        class="status-dot"
        class:status-online={system?.helperOnline}
        class:status-ready={!system?.helperOnline}
      ></span><button onclick={() => (settings = true)}
        >Network helper {system?.helperOnline
          ? 'connected'
          : 'not running'}</button
      ><span class="footer-divider"></span><span
        >{lab?.nodes.length || 0} devices</span
      ><span>{lab?.links.length || 0} links</span><span class="footer-divider"
      ></span><span
        ><strong>{runtime?.metrics.activeSessions || 0}</strong> / {lab
          ?.subscribers.length || 0} PPPoE sessions</span
      >
    </div>
    <div>
      <span>IPv4 · GPON · PPPoE</span><span class="footer-divider"
      ></span><button onclick={() => (shortcuts = true)}
        ><Keyboard size={12} />Shortcuts</button
      ><span class="version-tag">v0.1</span>
    </div>
  </footer>
</div>

{#if error}<div class="toast error-toast" role="alert">
    <AlertTriangle size={19} />
    <div><strong>That action could not finish</strong><span>{error}</span></div>
    <button
      class="icon-button"
      onclick={() => (error = '')}
      title="Dismiss error"><X size={15} /></button
    >
  </div>{:else if notice}<div class="toast notice-toast" role="status">
    <Check size={18} /><span>{notice}</span>
  </div>{/if}
{#if settings && lab}<SettingsDialog
    {lab}
    {locked}
    onclose={() => (settings = false)}
    onsave={saveRadius}
    onimage={saveImage}
    onacs={saveACS}
    onerror={problem}
  />{/if}
{#if newModal}<div
    class="modal-backdrop"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) newModal = false;
    }}
  >
    <div
      class="modal new-lab-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="new-title"
      tabindex="-1"
    >
      <header class="modal-header">
        <div>
          <span class="eyebrow">A FRESH START</span>
          <h2 id="new-title">Create a network lab</h2>
          <p>Choose a ready-to-wire topology or start with an empty canvas.</p>
        </div>
        <button
          class="icon-button"
          onclick={() => (newModal = false)}
          title="Close new lab"><X size={20} /></button
        >
      </header>
      <div class="modal-body form-stack">
        <label
          >Lab name<input
            bind:value={newName}
            maxlength="120"
            placeholder="Kampung fiber"
          /></label
        ><span class="field-label">STARTING TOPOLOGY</span>
        <div class="preset-grid">
          {#each [{ count: 0, name: 'Blank canvas', detail: 'Your design, from scratch' }, { count: 8, name: 'Small neighborhood', detail: '8 ONUs · 2 branches' }, { count: 32, name: 'Access lab', detail: '32 ONUs · functional tests' }, { count: 100, name: 'Growing network', detail: '100 real PPPoE clients' }, { count: 500, name: 'Load test', detail: '500 ONUs · 8 PON ports' }] as preset}<button
              class:chosen={newCount === preset.count}
              onclick={() => (newCount = preset.count)}
              ><span
                >{preset.count
                  ? String(preset.count).padStart(2, '0')
                  : '+'}</span
              >
              <div>
                <strong>{preset.name}</strong><small>{preset.detail}</small>
              </div>
              {#if newCount === preset.count}<Check size={16} />{/if}</button
            >{/each}
        </div>
        <div class="modal-actions">
          <button class="button" onclick={() => (newModal = false)}
            >Cancel</button
          ><button
            class="button primary"
            disabled={busy || !newName.trim()}
            onclick={create}
            >{#if busy}<LoaderCircle class="spin" size={15} />{:else}<Plus
                size={15}
              />{/if}Create lab</button
          >
        </div>
      </div>
    </div>
  </div>{/if}
{#if integration && lab}<div
    class="modal-backdrop"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) integration = false;
    }}
  >
    <div
      class="modal integration-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="integration-title"
      tabindex="-1"
    >
      <header class="modal-header">
        <div>
          <span class="eyebrow">BRING YOUR APPLICATION</span>
          <h2 id="integration-title">Device connection details</h2>
          <p>
            Add these endpoints to your incident management and billing
            application.
          </p>
        </div>
        <button
          class="icon-button"
          onclick={() => (integration = false)}
          title="Close integration"><X size={20} /></button
        >
      </header>
      <div class="modal-body">
        <div class="integration-state">
          <span
            class="status-pill"
            class:status-online={running}
            class:status-ready={!running}
            ><i></i>{running
              ? 'Runtime active'
              : 'Endpoints available when lab runs'}</span
          ><button
            class="button small-button"
            onclick={() => {
              reveal = !reveal;
            }}>{reveal ? 'Hide credentials' : 'Show credentials'}</button
          >
        </div>
        {#if connections?.activeLab && connections.activeLab.id !== lab.id}
          <div class="connection-warning" role="status">
            <p>
              <strong>{connections.activeLab.name}</strong> is the active lab. Management
              IPs are shared between labs, but their passwords differ.
            </p>
            <button
              class="button small-button"
              onclick={async () => {
                const activeID = connections?.activeLab?.id;
                if (!activeID) return;
                try {
                  labs = await api<Lab[]>('/labs');
                  await load(activeID);
                  await integrate();
                } catch (e) {
                  problem((e as Error).message);
                }
              }}>Switch to active lab</button
            >
          </div>
        {/if}
        <p class="field-hint">
          Use the IP address and credentials for this lab. The router password
          is generated per device; it is different from your Linux password.
        </p>
        {#each connections?.devices || [] as device}<div
            class="connection-card"
          >
            <div class="connection-heading">
              <span class="palette-symbol kind-{device.kind}"
                ><DeviceGlyph kind={device.kind as Kind} size={23} /></span
              >
              <div>
                <strong>{device.name}</strong><span>{device.profile}</span>
              </div>
              <button
                class="icon-button"
                title="Copy device connection JSON"
                onclick={() => {
                  copy(JSON.stringify(device, null, 2));
                  toast('Device connection copied');
                }}><Copy size={15} /></button
              >
            </div>
            <div class="connection-fields">
              <div class="connection-field">
                MANAGEMENT IP<code>{device.ip}</code>
              </div>
              <div class="connection-field">
                USERNAME<code>{device.username}</code>
              </div>
              <div class="connection-field">
                PASSWORD<code>{reveal ? device.password : '••••••••••••'}</code>
              </div>
            </div>
            <div class="connection-actions">
              {#if device.kind === 'router'}
                <span
                  >Winbox <code
                    >{device.ip}:{device.services.winbox?.port || 8291}</code
                  ></span
                >
                <button
                  class="button small-button"
                  aria-label={`Copy Winbox address for ${device.name}`}
                  onclick={async () => {
                    await copy(
                      `${device.ip}:${device.services.winbox?.port || 8291}`
                    );
                    toast('Winbox address copied');
                  }}><Copy size={13} />Copy address</button
                >
              {/if}
              <button
                class="button small-button"
                aria-label={`Copy password for ${device.name}`}
                onclick={async () => {
                  await copy(device.password);
                  toast('Device password copied');
                }}><Copy size={13} />Copy password</button
              >
            </div>
            <div class="service-chips">
              {#each Object.entries(device.services) as [name, service]}<span
                  >{name}
                  <code>{service.port}</code>{#if service.community}<small
                      >{reveal
                        ? service.community
                        : 'community configured'}</small
                    >{/if}</span
                >{/each}
            </div>
          </div>{/each}
        {#if connections?.onus?.length}
          <div class="connection-card">
            <div class="connection-heading">
              <span class="palette-symbol kind-onu"
                ><DeviceGlyph kind="onu" size={23} /></span
              >
              <div>
                <strong>ONU identities for FTTH</strong><span
                  >Vendor HSGQ · SNMP v2c · UDP 161</span
                >
              </div>
              <button
                class="icon-button"
                title="Copy ONU identities JSON"
                onclick={async () => {
                  await copy(JSON.stringify(connections?.onus, null, 2));
                  toast('ONU identities copied');
                }}><Copy size={15} /></button
              >
            </div>
            <p class="field-hint">
              The FTTH HSGQ adapter uses the MAC identity as the customer serial
              number. GPON and ACS use the GPON serial.
            </p>
            <div class="onu-identity-table">
              <table>
                <thead
                  ><tr
                    ><th>ONU / PON</th><th>FTTH identity (MAC)</th><th
                      >GPON / ACS serial</th
                    ></tr
                  ></thead
                >
                <tbody
                  >{#each connections.onus as onu}<tr>
                      <td
                        >{onu.name}<small>{onu.oltId} · PON{onu.pon}</small></td
                      >
                      <td
                        ><button
                          class="button small-button"
                          aria-label={`Copy FTTH identity for ${onu.name}`}
                          onclick={async () => {
                            await copy(onu.ftthIdentity);
                            toast('FTTH identity copied');
                          }}
                          ><code>{onu.ftthIdentity}</code><Copy
                            size={13}
                          /></button
                        ></td
                      >
                      <td><code>{onu.serial}</code></td>
                    </tr>{/each}</tbody
                >
              </table>
            </div>
          </div>
        {/if}
        <div class="connection-card">
          <div class="connection-heading">
            <span class="palette-symbol kind-onu"
              ><DeviceGlyph kind="onu" size={23} /></span
            >
            <div>
              <strong>ONU ACS · TR-069</strong><span
                >{lab.acs?.enabled
                  ? 'CWMP enabled'
                  : 'Configure in Runtime settings → ONU ACS'}</span
              >
            </div>
          </div>
          {#if lab.acs?.enabled}<div class="connection-fields">
              <div class="connection-field">
                ACS SERVER<code>{lab.acs.url}</code>
              </div>
              <div class="connection-field">
                REGISTERED ONUs<code
                  >{Object.values(runtime?.acs || {}).filter(
                    (s) => s.lastInform
                  ).length} / {lab.subscribers.length}</code
                >
              </div>
              <div class="connection-field">
                INFORM INTERVAL<code>{lab.acs.periodicInformSeconds}s</code>
              </div>
            </div>{/if}
        </div>
        <div class="reference-note">
          <PlugZap size={18} />
          <div>
            <strong>Same host. Native protocols.</strong>
            <p>{connections?.note}</p>
          </div>
        </div>
        <div class="modal-actions">
          <button
            class="button"
            onclick={() =>
              download(
                'fiberlab-connections.json',
                JSON.stringify(connections, null, 2)
              )}><Download size={14} />Export connection details</button
          ><button
            class="button primary"
            onclick={() => {
              integration = false;
              settings = true;
            }}>Configure runtime<ArrowUpRight size={14} /></button
          >
        </div>
      </div>
    </div>
  </div>{/if}
{#if scenarios && lab}<div
    class="modal-backdrop"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) scenarios = false;
    }}
  >
    <div
      class="modal scenarios-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="scenarios-title"
      tabindex="-1"
    >
      <header class="modal-header">
        <div>
          <span class="eyebrow">CONTROLLED CHAOS</span>
          <h2 id="scenarios-title">Fault scenarios</h2>
          <p>
            Introduce a failure and watch your application detect the
            consequences.
          </p>
        </div>
        <button
          class="icon-button"
          onclick={() => (scenarios = false)}
          title="Close scenarios"><X size={20} /></button
        >
      </header>
      <div class="modal-body">
        <div class="scenario-guide">
          <CableIcon size={22} />
          <div>
            <strong>Cable cuts & optical loss</strong>
            <p>
              Select a cable on the canvas to cut it or add attenuation. Select
              a device to turn it off or reboot an ONU.
            </p>
          </div>
          <button
            class="button"
            onclick={() => {
              scenarios = false;
              toast('Click any cable to open its fault controls');
            }}>Select a cable</button
          >
        </div>
        <div class="scenario-radius">
          <h3>RADIUS FAILURE</h3>
          <div class="action-grid">
            <button
              class="button"
              disabled={lab.radius.mode !== 'builtin' ||
                lab.faults.some((f) => f.kind === 'radius_timeout')}
              onclick={() => fault('radius_timeout', 'radius', 'radius')}
              ><AlertTriangle size={14} />Server timeout</button
            ><button
              class="button"
              disabled={lab.radius.mode !== 'builtin' ||
                lab.faults.some((f) => f.kind === 'radius_reject')}
              onclick={() => fault('radius_reject', 'radius', 'radius')}
              ><Scissors size={14} />Reject new logins</button
            >
          </div>
          <p class="field-hint">
            Affects new authentication requests. Existing sessions remain
            connected until they disconnect or expire.
          </p>
        </div>
        <h3 class="section-label">ACTIVE FAULTS · {lab.faults.length}</h3>
        {#each lab.faults as f}<div class="scenario-active">
            <span class="fault-icon"><AlertTriangle size={17} /></span>
            <div>
              <strong>{f.kind.replaceAll('_', ' ')}</strong><span
                >{f.targetId}{f.kind === 'attenuation'
                  ? ` · +${f.value} dB`
                  : ''}</span
              >
            </div>
            <button class="button" onclick={() => repair(f.id)}
              >Repair<Check size={13} /></button
            >
          </div>{/each}{#if !lab.faults.length}<div class="empty-faults">
            <Check size={23} /><strong>No faults injected</strong><span
              >Your topology is in its configured baseline state.</span
            >
          </div>{/if}
      </div>
    </div>
  </div>{/if}
{#if shortcuts}<div
    class="modal-backdrop"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) shortcuts = false;
    }}
  >
    <div
      class="modal shortcuts-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="shortcuts-title"
      tabindex="-1"
    >
      <header class="modal-header">
        <div>
          <span class="eyebrow">LESS CLICKING, MORE BUILDING</span>
          <h2 id="shortcuts-title">Canvas shortcuts</h2>
        </div>
        <button
          class="icon-button"
          onclick={() => (shortcuts = false)}
          title="Close shortcuts"><X size={18} /></button
        >
      </header>
      <div class="modal-body">
        <dl class="shortcut-list">
          <div>
            <dt>Fit the whole network</dt>
            <dd><kbd>F</kbd></dd>
          </div>
          <div>
            <dt>Undo topology edit</dt>
            <dd><kbd>Ctrl</kbd> <kbd>Z</kbd></dd>
          </div>
          <div>
            <dt>Redo topology edit</dt>
            <dd><kbd>Ctrl</kbd> <kbd>Shift</kbd> <kbd>Z</kbd></dd>
          </div>
          <div>
            <dt>Remove selection</dt>
            <dd><kbd>Delete</kbd></dd>
          </div>
          <div>
            <dt>Close dialog / clear selection</dt>
            <dd><kbd>Esc</kbd></dd>
          </div>
          <div>
            <dt>Pan / zoom</dt>
            <dd>Drag canvas / scroll</dd>
          </div>
        </dl>
      </div>
    </div>
  </div>{/if}
