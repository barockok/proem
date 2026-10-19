// Client-compatibility smoke: runs the real Claude Code CLI in headless
// gateway mode with a session JWT the driver obtained through the device flow,
// then checks that the request reached the mock upstream with the pool
// member's credential and was attributed to alice.
import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'

const mock = process.env.E2E_MOCK ?? 'https://mock-upstream:8443'
const metrics = process.env.E2E_PROEM_METRICS ?? 'http://proem:9090/metrics'
const mockKey = process.env.E2E_MOCK_UPSTREAM_KEY ?? 'sk-ant-api03-e2e-mock-upstream'

function fail(msg) {
  console.error(`claude smoke FAILED: ${msg}`)
  process.exit(1)
}

async function aliceRequests() {
  const text = await (await fetch(metrics)).text()
  const line = text.split('\n').find((l) =>
    l.startsWith('proem_requests_total{') && l.includes('client="alice"') && l.includes('code="200"'))
  return line ? Number(line.split(' ').pop()) : 0
}

const jwt = readFileSync('/shared/jwt', 'utf8').trim()
if (!jwt) fail('no JWT from the driver')

await fetch(`${mock}/_requests`, { method: 'DELETE' })
const before = await aliceRequests()

const run = spawnSync('claude', ['-p', 'reply with ok'], {
  encoding: 'utf8',
  timeout: 120_000,
  env: {
    ...process.env,
    CLAUDE_CODE_USE_GATEWAY: '1',
    ANTHROPIC_BASE_URL: 'https://proem:8443',
    ANTHROPIC_AUTH_TOKEN: jwt,
    NODE_EXTRA_CA_CERTS: '/certs/ca.pem',
    DISABLE_AUTOUPDATER: '1',
    DISABLE_TELEMETRY: '1',
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
  },
})
console.log(run.stdout)
console.error(run.stderr)
if (run.error) fail(run.error.message)
if (run.status !== 0) fail(`claude exited ${run.status}`)
if (!run.stdout.toLowerCase().includes('ok')) fail('unexpected output')

const reqs = await (await fetch(`${mock}/_requests`)).json()
const msgs = reqs.filter((r) => r.path === '/v1/messages')
if (msgs.length === 0) fail('mock upstream received no /v1/messages request')
for (const r of msgs) {
  if (r.x_api_key !== mockKey) fail(`upstream credential was ${JSON.stringify(r.x_api_key)}`)
  if (r.authorization) fail('the caller credential reached the upstream')
}
const after = await aliceRequests()
if (after <= before) fail(`requests not attributed to alice (before=${before} after=${after})`)
console.log(`claude smoke OK: ${msgs.length} upstream request(s), attributed to alice`)
