<script lang="ts">
  import { onMount } from 'svelte';
  import { TerminalSquare, ArrowRight, LoaderCircle } from 'lucide-svelte';
  import { api } from '../lib/api';
  import type { Lab, Device, Runtime } from '../lib/types';
  import '@xterm/xterm/css/xterm.css';
  let {
    lab,
    node,
    runtime,
    onsetup,
  }: { lab: Lab; node?: Device; runtime: Runtime | null; onsetup: () => void } =
    $props();
  let host = $state<HTMLDivElement>();
  let command = $state('ping 198.18.0.1');
  let output = $state('');
  let busy = $state(false);
  let connectionError = $state('');
  let outputElement = $state<HTMLPreElement>();
  async function execute() {
    if (!node || busy || !command.trim()) return;
    const value = command;
    command = '';
    busy = true;
    output += `\n${node.id} $ ${value}\n`;
    try {
      const result = await api<{ output: string }>(
        `/labs/${lab.id}/exec`,
        'POST',
        { nodeId: node.id, command: value }
      );
      output += result.output + '\n';
    } catch (e) {
      output += (e as Error).message + '\n';
    } finally {
      if (output.length > 100000) output = output.slice(-80000);
      busy = false;
      setTimeout(() => {
        if (outputElement) outputElement.scrollTop = outputElement.scrollHeight;
      }, 0);
    }
  }
  onMount(() => {
    let alive = true;
    let socket: WebSocket | undefined;
    let terminal: import('@xterm/xterm').Terminal | undefined;
    let observer: ResizeObserver | undefined;
    if (
      node &&
      (node.kind === 'router' || node.kind === 'olt') &&
      runtime?.phase === 'running'
    ) {
      const target = node;
      (async () => {
        try {
          const [{ Terminal }, { FitAddon }] = await Promise.all([
            import('@xterm/xterm'),
            import('@xterm/addon-fit'),
          ]);
          if (!alive || !host) return;
          terminal = new Terminal({
            fontFamily: '"SFMono-Regular", Consolas, monospace',
            fontSize: 12,
            cursorBlink: true,
            convertEol: true,
            scrollback: 1500,
            theme: {
              background: '#13211f',
              foreground: '#d9ece5',
              cursor: '#9ae0bd',
              selectionBackground: '#376051',
            },
          });
          const fit = new FitAddon();
          terminal.loadAddon(fit);
          terminal.open(host);
          fit.fit();
          socket = new WebSocket(
            `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/labs/${lab.id}/terminal?node=${encodeURIComponent(target.id)}`
          );
          socket.binaryType = 'arraybuffer';
          const resize = () => {
            if (!alive) return;
            fit.fit();
            if (socket?.readyState === WebSocket.OPEN && terminal)
              socket.send(
                JSON.stringify({
                  type: 'resize',
                  cols: terminal.cols,
                  rows: terminal.rows,
                })
              );
          };
          socket.onopen = () => {
            resize();
            terminal?.focus();
          };
          socket.onmessage = (e) => {
            if (e.data instanceof ArrayBuffer)
              terminal?.write(new Uint8Array(e.data));
            else terminal?.write(e.data);
          };
          socket.onerror = () => {
            connectionError =
              'SSH connection failed. Check the device power and runtime state.';
          };
          socket.onclose = () =>
            terminal?.writeln('\r\n[SSH connection closed]');
          terminal.onData((data) => {
            if (socket?.readyState === WebSocket.OPEN)
              socket.send(JSON.stringify({ type: 'input', data }));
          });
          observer = new ResizeObserver(resize);
          observer.observe(host);
        } catch (e) {
          connectionError = (e as Error).message;
        }
      })();
    }
    return () => {
      alive = false;
      observer?.disconnect();
      socket?.close();
      terminal?.dispose();
    };
  });
</script>

{#if !node}<div class="panel-empty">
    <TerminalSquare size={27} />
    <div>
      <strong>Pick a device to open its console</strong><span
        >RouterOS and OLT use SSH. ONU clients run commands in their own network
        namespace.</span
      >
    </div>
  </div>
{:else if runtime?.phase !== 'running'}<div class="panel-empty">
    <TerminalSquare size={27} />
    <div>
      <strong>The network runtime is stopped</strong><span
        >Start the lab to connect to {node.label}.</span
      >
    </div>
    <button class="button" onclick={onsetup}
      >Runtime settings<ArrowRight size={14} /></button
    >
  </div>
{:else if node.kind === 'onu'}<div class="client-terminal">
    <pre bind:this={outputElement}>{output ||
        `Fiberlab · ${node.label}\nActual customer network namespace. Type help for commands.\nTry: ping 198.18.0.1  •  http  •  ip addr  •  ppp status`}</pre>
    <form
      onsubmit={(e) => {
        e.preventDefault();
        execute();
      }}
    >
      <span class="mono">{node.id} $</span><input
        aria-label="Client terminal command"
        bind:value={command}
        placeholder="ping 198.18.0.1"
        autocomplete="off"
        spellcheck="false"
      /><button
        class="terminal-run"
        disabled={busy}
        aria-label="Execute command"
        >{#if busy}<LoaderCircle class="spin" size={15} />{:else}<ArrowRight
            size={16}
          />{/if}</button
      >
    </form>
  </div>
{:else if node.kind === 'router' || node.kind === 'olt'}<div
    class="ssh-terminal"
    bind:this={host}
  ></div>
  {#if connectionError}<div class="terminal-error">{connectionError}</div>{/if}
{:else}<div class="panel-empty">
    <TerminalSquare size={27} />
    <div>
      <strong>This is passive infrastructure</strong><span
        >Select a MikroTik, OLT, or ONU to open a console.</span
      >
    </div>
  </div>{/if}
