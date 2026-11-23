// k6 load test for the reminder API.
//
// Usage:
//   k6 run -e BASE_URL=http://127.0.0.1:8906 -e API_KEY=<your-api-key> scripts/loadtest/reminder.js
//
// The script mixes the three hot read paths (health, today summary, list page)
// with one write path (create reminder) so SQLite writer contention and the
// scheduler-backed delivery enqueue are exercised, not just static files.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8906';
const API_KEY = __ENV.API_KEY || '';
const WRITE_RATIO = Number(__ENV.WRITE_RATIO || '0.1');

const writeLatency = new Trend('write_latency', true);
const readLatency = new Trend('read_latency', true);
const failures = new Rate('business_failures');

export const options = {
  scenarios: {
    steady: {
      executor: 'ramping-vus',
      startVUs: 1,
      stages: [
        { duration: '30s', target: 20 },
        { duration: '1m', target: 50 },
        { duration: '30s', target: 0 },
      ],
    },
  },
  thresholds: {
    // Read paths should stay interactive; writes are slower because each one
    // opens a transaction and enqueues delivery jobs.
    'http_req_failed': ['rate<0.01'],
    'read_latency': ['p(95)<300'],
    'write_latency': ['p(95)<800'],
    'business_failures': ['rate<0.01'],
  },
};

const headers = {
  'Content-Type': 'application/json',
  'X-API-Key': API_KEY,
};

function authed(path) {
  return `${BASE_URL}${path}`;
}

export default function () {
  // Read 1: liveness/readiness (includes DB ping + scheduler snapshot).
  const health = http.get(authed('/api/health'));
  readLatency.add(health.timings.duration);
  check(health, { 'health 200': (r) => r.status === 200 });

  // Read 2: today summary.
  const summary = http.get(authed('/api/reminder/summary'), { headers });
  readLatency.add(summary.timings.duration);
  check(summary, { 'summary ok': (r) => r.status === 200 });

  // Read 3: paginated list (page 1, 20 rows).
  const list = http.get(authed('/api/reminder/items?page=1&page_size=20'), { headers });
  readLatency.add(list.timings.duration);
  check(list, { 'list ok': (r) => r.status === 200 });

  // Optional write: create a reminder due in one hour (no channel => no push).
  if (Math.random() < WRITE_RATIO) {
    const due = new Date(Date.now() + 3600 * 1000).toISOString();
    const payload = JSON.stringify({
      title: `压测提醒 ${__VU}-${__ITER}`,
      due_at: due,
      repeat_rule: 'none',
      channels: [],
    });
    const create = http.post(authed('/api/reminder/items'), payload, { headers });
    writeLatency.add(create.timings.duration);
    const ok = check(create, { 'create 200': (r) => r.status === 200 });
    failures.add(!ok);
  }

  sleep(0.2 + Math.random() * 0.3);
}
