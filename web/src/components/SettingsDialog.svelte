<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import {
    X,
    Check,
    AlertTriangle,
    Copy,
    Download,
    LoaderCircle,
    HardDrive,
    RadioTower,
    ShieldCheck,
    Server,
    ExternalLink,
  } from 'lucide-svelte';
  import { api, copy } from '../lib/api';
  import {
    bytes,
    defaultACS,
    type ACS,
    type Lab,
    type Radius,
    type System,
    type ImageStatus,
  } from '../lib/types';
  let {
    lab,
    locked,
    onclose,
    onsave,
    onimage,
    onacs,
    onerror,
  }: {
    lab: Lab;
    locked: boolean;
    onclose: () => void;
    onsave: (radius: Radius, trap: string) => Promise<void>;
    onimage: (id: string) => Promise<void>;
    onacs: (acs: ACS) => Promise<void>;
    onerror: (message: string) => void;
  } = $props();
  let section = $state<'runtime' | 'radius' | 'acs'>('runtime');
  let system = $state<System | null>(null);
  let images = $state<ImageStatus | null>(null);
  let version = $state('7.20.8');
  let radius = $state<Radius>(untrack(() => ({ ...lab.radius })));
  let trap = $state(untrack(() => lab.trapAddress || ''));
  let acs = $state<ACS>(untrack(() => ({ ...(lab.acs || defaultACS()) })));
  let copied = $state(false);
  let saving = $state(false);
  let fetchBusy = $state(false);
  let saved = $state(false);
  async function refresh() {
    try {
      [system, images] = await Promise.all([
        api<System>('/system'),
        api<ImageStatus>('/images'),
      ]);
    } catch (e) {
      onerror((e as Error).message);
    }
  }
  onMount(() => {
    refresh();
    const id = setInterval(refresh, 2500);
    return () => clearInterval(id);
  });
  async function fetchImage() {
    fetchBusy = true;
    try {
      await api('/images/fetch', 'POST', { version });
      await refresh();
    } catch (e) {
      onerror((e as Error).message);
    } finally {
      fetchBusy = false;
    }
  }
  async function save() {
    saving = true;
    try {
      if (section === 'acs') await onacs(acs);
      else await onsave(radius, trap);
      saved = true;
      setTimeout(() => (saved = false), 2000);
    } catch (e) {
      saved = false;
      onerror((e as Error).message);
    } finally {
      saving = false;
    }
  }
</script>

<div
  class="modal-backdrop"
  role="presentation"
  onclick={(e) => {
    if (e.target === e.currentTarget) onclose();
  }}
>
  <div
    class="modal setup-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="setup-title"
    tabindex="-1"
  >
    <header class="modal-header">
      <div>
        <span class="eyebrow">MAKE IT REAL</span>
        <h2 id="setup-title">Runtime & services</h2>
        <p>
          The editor runs on its own. Real packets need the network helper and a
          CHR image.
        </p>
      </div>
      <button class="icon-button" onclick={onclose} title="Close settings"
        ><X size={20} /></button
      >
    </header>
    <div class="modal-tabs">
      <button
        class:active={section === 'runtime'}
        onclick={() => (section = 'runtime')}
        ><HardDrive size={15} />Network runtime</button
      ><button
        class:active={section === 'radius'}
        onclick={() => (section = 'radius')}
        ><RadioTower size={15} />RADIUS & events</button
      ><button
        class:active={section === 'acs'}
        onclick={() => (section = 'acs')}><Server size={15} />ONU ACS</button
      >
    </div>
    <div class="modal-body">
      {#if section === 'runtime'}
        <div class="setup-block">
          <div class="setup-block-title">
            <span class="step-number">1</span>
            <div>
              <h3>Network helper</h3>
              <p>
                Runs the privileged network operations; the web app stays a
                normal user process.
              </p>
            </div>
            <span
              class="status-pill"
              class:status-online={system?.helperOnline}
              class:status-ready={!system?.helperOnline}
              ><i></i>{system?.helperOnline ? 'Connected' : 'Not running'}</span
            >
          </div>
          {#if system && !system.helperOnline}<div class="command-box">
              <code>{system.helperCommand}</code><button
                class="icon-button"
                title="Copy helper command"
                onclick={async () => {
                  await copy(system!.helperCommand);
                  copied = true;
                  setTimeout(() => (copied = false), 1800);
                }}
                >{#if copied}<Check size={16} />{:else}<Copy
                    size={16}
                  />{/if}</button
              >
            </div>
            <p class="field-hint">
              Run this in a local terminal. It creates only the lab's named
              bridges, TAP devices, and customer namespaces.
            </p>{/if}
          <div class="dependency-grid">
            {#each system?.checks || [] as check}<div
                class="dependency"
                title={check.detail}
              >
                <span class:ok={check.ok}
                  >{#if check.ok}<Check size={13} />{:else}<AlertTriangle
                      size={13}
                    />{/if}</span
                ><span>{check.name}</span>
              </div>{/each}
          </div>
        </div>
        <div class="setup-block">
          <div class="setup-block-title">
            <span class="step-number">2</span>
            <div>
              <h3>RouterOS CHR image</h3>
              <p>
                Official MikroTik image, stored locally with a pinned version
                and SHA-256 digest.
              </p>
            </div>
          </div>
          {#if images?.images.length}<div class="image-options">
              {#each images.images as image}<button
                  class="image-option"
                  class:selected={lab.imageId === image.id}
                  disabled={locked}
                  onclick={() =>
                    onimage(image.id).catch((e) => onerror(e.message))}
                  ><span class="image-icon"><Server size={21} /></span>
                  <div>
                    <strong>RouterOS {image.version}</strong><span class="mono"
                      >{bytes(image.size)} · SHA {image.sha256.slice(
                        0,
                        12
                      )}</span
                    >
                  </div>
                  {#if lab.imageId === image.id}<Check size={18} />{:else}<span
                      class="small-text">Select</span
                    >{/if}</button
                >{/each}
            </div>{:else}<div class="empty-image">
              <HardDrive size={25} /><span>No CHR image installed yet.</span>
            </div>{/if}
          <div class="download-row">
            <label
              >Exact version<input
                aria-label="CHR version"
                class="mono"
                bind:value={version}
                placeholder="7.20.8"
              /></label
            ><button
              class="button primary"
              disabled={fetchBusy || images?.job.status === 'downloading'}
              onclick={fetchImage}
              >{#if fetchBusy || images?.job.status === 'downloading'}<LoaderCircle
                  class="spin"
                  size={15}
                />Downloading{:else}<Download size={15} />Download CHR{/if}</button
            >
          </div>
          {#if images?.job.status === 'downloading'}<div
              class="download-progress"
            >
              <span
                style={`width:${images.job.total > 0 ? (images.job.received / images.job.total) * 100 : 12}%`}
              ></span>
            </div>
            <p class="field-hint">
              {bytes(images.job.received)}
              {images.job.total > 0
                ? `/ ${bytes(images.job.total)}`
                : 'downloaded'} from download.mikrotik.com
            </p>{/if}
          {#if images?.job.status === 'error'}<div class="inline-error">
              <AlertTriangle size={16} />{images.job.error}
            </div>{/if}
          <p class="field-hint">
            CHR's free license supports functional testing with an upload limit
            of 1 Mbps per interface. <a
              href="https://help.mikrotik.com/docs/spaces/ROS/pages/18350234/Cloud+Hosted+Router+CHR"
              target="_blank"
              rel="noreferrer">License details <ExternalLink size={10} /></a
            >
          </p>
        </div>
        <div class="setup-footnote">
          <ShieldCheck size={16} /><span
            >Management: <code>10.203.0.0/24</code> · Test origin:
            <code>198.18.0.1:8080</code><br />The helper checks for route
            conflicts before changing the host network.</span
          >
        </div>
      {:else if section === 'acs'}
        <div class="setup-block-title">
          <span class="step-number"><Server size={17} /></span>
          <div>
            <h3>Connect ONUs to your ACS</h3>
            <p>
              TR-069 / CWMP with a TR-098 parameter tree. Works with GenieACS.
            </p>
          </div>
        </div>
        <div class="radius-mode-options">
          <button
            class:chosen={!acs.enabled}
            disabled={locked}
            onclick={() => (acs.enabled = false)}
            ><ShieldCheck size={22} /><strong>Disabled</strong><span
              >Keep ONU management local.</span
            ></button
          >
          <button
            class:chosen={acs.enabled}
            disabled={locked}
            onclick={() => (acs.enabled = true)}
            ><Server size={22} /><strong>Connect to ACS</strong><span
              >Register each connected ONU with your server.</span
            ></button
          >
        </div>
        <div class="form-grid">
          <label
            >ACS URL<input
              class="mono"
              type="url"
              bind:value={acs.url}
              disabled={locked}
              placeholder="http://192.168.1.10:7547"
            /></label
          >
          <label
            >Periodic Inform · seconds<input
              type="number"
              min="10"
              max="86400"
              bind:value={acs.periodicInformSeconds}
              disabled={locked}
            /></label
          >
          <label
            >ACS username<input
              autocomplete="off"
              bind:value={acs.username}
              disabled={locked}
              placeholder="Optional"
            /></label
          >
          <label
            >ACS password<input
              type="password"
              autocomplete="new-password"
              bind:value={acs.password}
              disabled={locked}
              placeholder="Optional"
            /></label
          >
        </div>
        <div class="setup-block-title acs-subheading">
          <div>
            <h3>Connection requests from the ACS</h3>
            <p>
              Lets GenieACS contact an ONU immediately when you queue a task.
            </p>
          </div>
        </div>
        <div class="form-grid">
          <label
            >Connection-request listen address<input
              class="mono"
              bind:value={acs.connectionRequestListen}
              disabled={locked}
              placeholder="127.0.0.1:7548"
            /></label
          >
          <label
            >Connection-request public URL<input
              class="mono"
              type="url"
              bind:value={acs.connectionRequestUrl}
              disabled={locked}
              placeholder="http://192.168.1.20:7548"
            /></label
          >
          <label
            >Connection-request username<input
              autocomplete="off"
              bind:value={acs.connectionRequestUsername}
              disabled={locked}
            /></label
          >
          <label
            >Connection-request password<input
              type="password"
              autocomplete="new-password"
              bind:value={acs.connectionRequestPassword}
              disabled={locked}
            /></label
          >
        </div>
        <div class="reference-note">
          <ShieldCheck size={18} />
          <div>
            <strong>One connection identity per ONU</strong>
            <p>
              For GenieACS on this laptop, keep the loopback defaults. For a
              remote ACS, listen on 0.0.0.0:7548 and set the public URL to this
              laptop's reachable IP. HTTP Digest protects connection requests.
              Outbound CWMP uses the laptop's network and pauses when an ONU
              loses its PPP service.
            </p>
          </div>
        </div>
        <p class="field-hint">
          Set matching credentials in GenieACS under cwmp.connectionRequestAuth,
          or provision them from the ACS. Password parameters are write-only.
        </p>
        <button
          class="button small-button"
          onclick={() =>
            copy(
              `AUTH(${JSON.stringify(acs.connectionRequestUsername)}, ${JSON.stringify(acs.connectionRequestPassword)})`
            ).catch((e) => onerror(e.message))}
          ><Copy size={14} />Copy GenieACS connection auth</button
        >
        <p class="field-hint">
          ONU serials include the lab ID so different labs remain separate in
          GenieACS. Wi-Fi settings are simulated; WAN counters come from the
          real PPP session.
        </p>
        {#if locked}<p class="field-hint">
            Stop the runtime before changing ACS service settings.
          </p>{/if}
        <div class="modal-actions">
          <button
            class="button primary"
            disabled={saving || locked}
            onclick={save}
            >{#if saving}<LoaderCircle class="spin" size={15} />{:else}<Check
                size={15}
              />{/if}{saved ? 'Saved' : 'Save ACS settings'}</button
          >
        </div>
      {:else}
        <div class="radius-mode-options">
          <button
            class:chosen={radius.mode === 'builtin'}
            disabled={locked}
            onclick={() => {
              radius.mode = 'builtin';
              radius.address = '10.203.0.1';
            }}
            ><RadioTower size={22} /><strong>Built-in reference</strong><span
              >Local accounts, PAP/CHAP, accounting, and disconnects.</span
            ></button
          ><button
            class:chosen={radius.mode === 'external'}
            disabled={locked}
            onclick={() => (radius.mode = 'external')}
            ><Server size={22} /><strong>Your RADIUS server</strong><span
              >Authenticate and account through your billing application's
              server.</span
            ></button
          >
        </div>
        <div class="form-grid">
          <label
            >Server IPv4<input
              class="mono"
              bind:value={radius.address}
              disabled={locked || radius.mode === 'builtin'}
            /></label
          ><label
            >Shared secret<input
              type="password"
              bind:value={radius.secret}
              disabled={locked}
            /></label
          ><label
            >Authentication port<input
              type="number"
              min="1"
              max="65535"
              bind:value={radius.authPort}
              disabled={locked}
            /></label
          ><label
            >Accounting port<input
              type="number"
              min="1"
              max="65535"
              bind:value={radius.accountingPort}
              disabled={locked}
            /></label
          ><label
            >Interim updates · seconds<input
              type="number"
              min="10"
              max="3600"
              bind:value={radius.interimSeconds}
              disabled={locked}
            /></label
          ><label
            >SNMP trap receiver<input
              class="mono"
              bind:value={trap}
              placeholder="127.0.0.1:1162 (optional)"
            /></label
          >
        </div>
        <div class="reference-note">
          <ShieldCheck size={18} />
          <div>
            <strong
              >{radius.mode === 'external'
                ? 'Connect from the router’s network'
                : 'Independent optical and billing state'}</strong
            >
            <p>
              {radius.mode === 'external'
                ? 'For a RADIUS server on this host, listen on 10.203.0.1 or all interfaces. Register the MikroTik management IP as a NAS with this shared secret. Subscriber policy is then managed by your application.'
                : 'Suspending an account rejects future logins and disconnects the active session. The ONU keeps its optical connection and does not become LOS.'}
            </p>
          </div>
        </div>
        {#if locked}<p class="field-hint">
            Stop the runtime before changing RADIUS configuration.
          </p>{/if}
        <div class="modal-actions">
          <button
            class="button primary"
            disabled={saving || locked}
            onclick={save}
            >{#if saving}<LoaderCircle class="spin" size={15} />{:else}<Check
                size={15}
              />{/if}{saved ? 'Saved' : 'Save service settings'}</button
          >
        </div>
      {/if}
    </div>
  </div>
</div>
