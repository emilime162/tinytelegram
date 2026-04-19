import ws from 'k6/ws';
import { check } from 'k6';

// 10-minute sustained test: 5 VUs × 10 msg/s = 50 msg/s against the local
// gateway, which fans out to local message-service, which hits cloud RDS +
// ElastiCache through the SSM bastion tunnel. Phase 1 exit criterion (a).
export const options = {
  scenarios: {
    chat: {
      executor: 'constant-vus',
      vus: 5,
      duration: __ENV.DURATION || '10m',
    },
  },
  thresholds: {
    ws_connecting: ['p(95)<2000'],
    checks:        ['rate>0.99'],
  },
};

const GATEWAY = __ENV.GATEWAY || 'ws://localhost:8080/ws';

export default function () {
  const userId = `u${__VU}`;
  const url = `${GATEWAY}?user_id=${userId}`;
  const res = ws.connect(url, null, (socket) => {
    socket.setInterval(function () {
      const partner = `u${(__VU % 5) + 1}`;
      socket.send(JSON.stringify({
        receiver_id: partner,
        content: `hello from VU ${__VU} at ${Date.now()}`,
      }));
    }, 100);

    const totalMs = (typeof __ENV.DURATION_MS === 'string' && __ENV.DURATION_MS)
      ? parseInt(__ENV.DURATION_MS, 10)
      : 10 * 60 * 1000;
    socket.setTimeout(() => socket.close(), totalMs);

    socket.on('error', (e) => console.error(`WS error VU${__VU}: ${e}`));
  });
  check(res, { 'status is 101': (r) => r && r.status === 101 });
}
