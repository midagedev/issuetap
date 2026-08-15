<script lang="ts">
  type Overview = {
    product: string
    dialect: string
    locale: string
    seed: number
    counts: Record<string, number>
    faults: { name: string; status: number; seen: number; applied: number }[]
    ui: boolean
  }

  type Trace = {
    id: number
    at: string
    method: string
    path: string
    query?: string
    dialect: string
    status: number
    latencyMs: number
    fault?: string
    request?: string
    response?: string
  }

  type IssueRow = {
    Key: string
    Summary: string
    Status: string
    StatusID: string
    Type: string
    TypeID: string
    Project: string
    Comments: number
    Histories: number
  }

  type Data = {
    projects: { key: string; name: string }[]
    issues: IssueRow[]
    pages: { ID: string; Title: string; Space: string; Comments: number }[]
    users: { AccountID: string; DisplayName: string; Email: string }[]
    statuses: { id: string; name: string; statusCategory: { key: string } }[]
  }

  type Tab = 'requests' | 'data' | 'diff' | 'scenarios' | 'diagnose'

  let tab = $state<Tab>('requests')
  let overview = $state<Overview | null>(null)
  let traces = $state<Trace[]>([])
  let data = $state<Data | null>(null)
  let diff = $state<unknown>(null)
  let error = $state('')
  let selected = $state<Trace | null>(null)

  async function load() {
    error = ''
    try {
      const [o, r, d, f] = await Promise.all([
        fetch('/api/overview').then((x) => x.json()),
        fetch('/api/requests?limit=200').then((x) => x.json()),
        fetch('/api/data').then((x) => x.json()),
        fetch('/api/diff').then((x) => x.json()),
      ])
      overview = o
      traces = r.requests ?? []
      data = d
      diff = f
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  $effect(() => {
    void load()
    const id = setInterval(() => void load(), 2000)
    return () => clearInterval(id)
  })

  function tone(status: number): string {
    if (status >= 500) return 'bad'
    if (status >= 400) return 'warn'
    return 'ok'
  }
</script>

<div class="shell">
  <header>
    <div>
      <strong>issuetap</strong>
      <span class="muted">lab instrument — not an issue tracker</span>
    </div>
    {#if overview}
      <div class="meta">
        dialect={overview.dialect}
        locale={overview.locale}
        seed={overview.seed}
        issues={overview.counts.issues ?? 0}
        pages={overview.counts.pages ?? 0}
      </div>
    {/if}
    <div class="actions">
      <button type="button" onclick={() => void load()}>Refresh</button>
      <a class="btn" href="/api/diagnostics">Export diagnostics</a>
    </div>
  </header>

  <nav>
    <button type="button" class:on={tab === 'requests'} onclick={() => (tab = 'requests')}>Requests</button>
    <button type="button" class:on={tab === 'data'} onclick={() => (tab = 'data')}>Data</button>
    <button type="button" class:on={tab === 'diff'} onclick={() => (tab = 'diff')}>Diff</button>
    <button type="button" class:on={tab === 'scenarios'} onclick={() => (tab = 'scenarios')}>Scenarios</button>
    <button type="button" class:on={tab === 'diagnose'} onclick={() => (tab = 'diagnose')}>Diagnose</button>
  </nav>

  {#if error}
    <p class="err">{error}</p>
  {/if}

  <main>
    {#if tab === 'requests'}
      <table>
        <thead>
          <tr>
            <th>#</th>
            <th>method</th>
            <th>path</th>
            <th>status</th>
            <th>ms</th>
            <th>fault</th>
          </tr>
        </thead>
        <tbody>
          {#each traces as t (t.id)}
            <tr class:sel={selected?.id === t.id} onclick={() => (selected = t)}>
              <td>{t.id}</td>
              <td>{t.method}</td>
              <td><code>{t.path}{t.query ? '?' + t.query : ''}</code></td>
              <td class={tone(t.status)}>{t.status}</td>
              <td>{t.latencyMs}</td>
              <td>{t.fault ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if selected}
        <section class="split">
          <div>
            <h3>Request</h3>
            <pre>{selected.request || '(empty)'}</pre>
          </div>
          <div>
            <h3>Response (first 2 KiB)</h3>
            <pre>{selected.response || '(empty)'}</pre>
          </div>
        </section>
      {/if}
    {:else if tab === 'data'}
      {#if data}
        <h3>Issues</h3>
        <table>
          <thead>
            <tr>
              <th>key</th>
              <th>summary</th>
              <th>status (id)</th>
              <th>type (id)</th>
              <th>comments</th>
              <th>history</th>
            </tr>
          </thead>
          <tbody>
            {#each data.issues ?? [] as iss (iss.Key)}
              <tr>
                <td><code>{iss.Key}</code></td>
                <td>{iss.Summary}</td>
                <td>{iss.Status} <code>{iss.StatusID}</code></td>
                <td>{iss.Type} <code>{iss.TypeID}</code></td>
                <td>{iss.Comments}</td>
                <td>{iss.Histories}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        <h3>Pages</h3>
        <table>
          <thead>
            <tr><th>id</th><th>title</th><th>space</th><th>comments</th></tr>
          </thead>
          <tbody>
            {#each data.pages ?? [] as p (p.ID)}
              <tr>
                <td><code>{p.ID}</code></td>
                <td>{p.Title}</td>
                <td>{p.Space}</td>
                <td>{p.Comments}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        <h3>Statuses (key on id, not name)</h3>
        <table>
          <thead>
            <tr><th>id</th><th>name</th><th>category.key</th></tr>
          </thead>
          <tbody>
            {#each data.statuses ?? [] as st (st.id)}
              <tr>
                <td><code>{st.id}</code></td>
                <td>{st.name}</td>
                <td><code>{st.statusCategory?.key}</code></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {:else if tab === 'diff'}
      <p class="muted">Last request vs what the endpoint documented. Issuetap does not invent provider fields the fixture did not supply.</p>
      <pre>{JSON.stringify(diff, null, 2)}</pre>
    {:else if tab === 'scenarios'}
      <p>Run from the CLI. Reports are JSON.</p>
      <pre>issuetap scenario run examples/scenarios/locale-ko-name-trap.yaml
issuetap scenario run examples/scenarios/credential-revoked.yaml
issuetap scenario run examples/scenarios/rate-limit-burst.yaml
issuetap scenario run examples/scenarios/confluence-401-stops-watch.yaml</pre>
      {#if overview?.faults?.length}
        <h3>Active faults</h3>
        <table>
          <thead>
            <tr><th>name</th><th>status</th><th>seen</th><th>applied</th></tr>
          </thead>
          <tbody>
            {#each overview.faults as f (f.name)}
              <tr>
                <td>{f.name}</td>
                <td>{f.status}</td>
                <td>{f.seen}</td>
                <td>{f.applied}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {:else}
      <p>Download the zip an agent or CI can read: traces, snapshot, compatibility table, and a likely-cause note.</p>
      <p><a class="btn" href="/api/diagnostics">GET /api/diagnostics</a></p>
      <p class="muted">Or <code>issuetap diagnose --addr 127.0.0.1:8080 --out issuetap-diagnose.zip</code></p>
    {/if}
  </main>
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  header {
    display: flex;
    gap: 16px;
    align-items: baseline;
    justify-content: space-between;
    padding: 12px 16px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
  }
  .muted {
    color: var(--muted);
    margin-left: 8px;
  }
  .meta {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
    color: var(--muted);
  }
  .actions {
    display: flex;
    gap: 8px;
  }
  nav {
    display: flex;
    gap: 4px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--line);
  }
  nav button.on {
    border-color: var(--accent);
    color: var(--accent);
  }
  main {
    flex: 1;
    overflow: auto;
    padding: 12px 16px 32px;
  }
  .err {
    color: var(--bad);
    padding: 0 16px;
  }
  .ok {
    color: var(--ok);
  }
  .bad {
    color: var(--bad);
  }
  .warn {
    color: var(--warn);
  }
  tr.sel {
    background: #d5dde8;
  }
  tr {
    cursor: pointer;
  }
  .split {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    margin-top: 16px;
  }
  h3 {
    margin: 16px 0 8px;
    font-size: 13px;
  }
</style>
