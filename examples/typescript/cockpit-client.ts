// Minimal Cockpit API client in TypeScript with no dependencies.
//
//   COCKPIT_API_KEY=$(cat my.key) node cockpit-client.ts
//
// Node 23.6+ runs TypeScript directly (type stripping); older Node 22
// needs --experimental-strip-types. Reads COCKPIT_API_URL (default
// http://127.0.0.1:7878) and COCKPIT_API_KEY. The key needs metrics:read.

type Availability = "available" | "unsupported" | "permission_denied" | "temporarily_unavailable" | "not_configured";

interface Section<T> {
  status: Availability;
  collected_at: string | null;
  stale: boolean;
  source: string;
  reason?: string;
  data: T | null;
}

interface HealthCheck {
  id: string;
  status: "ok" | "warning" | "critical" | "unknown";
  summary?: string;
  reason?: string;
}

interface Health {
  status: string;
  checks: HealthCheck[];
}

interface Metrics {
  cpu?: Section<{ usage_percent: number | null; logical_cores: number }>;
  memory?: Section<{ used_percent: number; total_bytes: number }>;
}

interface ErrorEnvelope {
  error: { code: string; message: string; request_id: string };
}

const base = (process.env.COCKPIT_API_URL ?? "http://127.0.0.1:7878").replace(/\/$/, "") + "/api/v1";
const key = process.env.COCKPIT_API_KEY ?? "";

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(base + path, { headers: { Authorization: `Bearer ${key}`, Accept: "application/json" } });
  if (!resp.ok) {
    const e = (await resp.json()) as ErrorEnvelope;
    throw new Error(`${path}: HTTP ${resp.status} ${e.error.code}: ${e.error.message}`);
  }
  return (await resp.json()) as T;
}

function data<T>(s: Section<T> | undefined): T | null {
  if (!s || s.status !== "available" || s.data === null) {
    console.log(`  (${s?.status ?? "missing"}: ${s?.reason ?? ""})`);
    return null;
  }
  return s.data;
}

async function main(): Promise<void> {
  if (!key) {
    console.error("Set COCKPIT_API_KEY");
    process.exit(1);
  }
  const health = await get<Health>("/health");
  console.log(`health: ${health.status}`);
  for (const c of health.checks) {
    console.log(`  ${c.id.padEnd(18)} ${c.status.padEnd(8)} ${c.summary ?? c.reason ?? ""}`);
  }
  const m = await get<Metrics>("/metrics?sections=cpu,memory");
  console.log("cpu:");
  const cpu = data(m.cpu);
  if (cpu && cpu.usage_percent !== null) {
    console.log(`  usage ${cpu.usage_percent.toFixed(1)}% of ${cpu.logical_cores} cores`);
  }
  console.log("memory:");
  const mem = data(m.memory);
  if (mem) {
    console.log(`  used ${mem.used_percent.toFixed(1)}% of ${(mem.total_bytes / 2 ** 30).toFixed(1)} GiB`);
  }
}

main().catch((e: unknown) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});
