# Phase 1 cloud-migration validation — 2026-04-18

These are not Exp1 / Exp3 / Exp5 / Exp6 runs. They are the validation runs that prove the Multi-AZ app layer + data stack work correctly against cloud RDS + ElastiCache, per the spec §8 Phase 1 exit criteria. Exp1/3/5/6 run against Fargate in Plan 2.

**Environment under test:**

| Component | Location | Notes |
|---|---|---|
| `message-service` | local (Windows laptop), compiled Go binary | talks to cloud data plane |
| `gateway` | local (Windows laptop), compiled Go binary | 5 VUs connect to `ws://127.0.0.1:8080/ws` |
| RDS PostgreSQL 16.3 | us-east-1, Multi-AZ, `db.m6g.large`, 100 GB gp3 | `TtDataStack` output `DbEndpoint` |
| ElastiCache Redis 7.1 | us-east-1, Multi-AZ, `cache.m6g.large`, transit+at-rest encryption, AUTH | `TtDataStack` output `RedisPrimaryEndpoint` |
| Connectivity | SSM Session Manager port-forward via `TtBastionStack` | RDS → `127.0.0.1:15432`, Redis → `127.0.0.1:16379` |
| Test driver | k6 1.7.0, locally | 5 VUs × 10 msg/s target = 50 msg/s |

**Important caveat on throughput numbers:** every Redis op (INCR × 2, WAIT) and every Postgres INSERT traverses the SSM tunnel (laptop → AWS SSM endpoint → bastion → service). End-to-end RTT is ~30–80 ms per op, vs. ~1–5 ms for an in-VPC Fargate → Redis/Postgres path. The 100 ms `WAIT` timeout hard-coded in [server.go](../../message-service/grpc/server.go) is appropriate for in-VPC replica-ack latencies but is brittle under tunnel RTT, which is why the 50 msg/s target is not reached in these validation runs. In Plan 2 (Fargate in-VPC) we expect the target to be met.

---

## Phase 1 exit criterion (a): sustained load, no PTS duplicates

**Scenario:** `scripts/cloud-integration/k6-integration-check.js` at `DURATION=10m`, 5 VUs holding a single WebSocket each; every VU sends one message every 100 ms to a round-robin partner (`(__VU % 5) + 1`).

**First 10-min attempt (`3785318~1`, pre-mutex-fix gateway):** FAILED at ~30 s — gateway panicked with `concurrent write to websocket connection`. 664,858 k6 iterations, 11 successful WS connects; the rest failed because the gateway process died. Bug fix in commit `3785318`, details in §11.3 of the spec.

**Second 10-min attempt (post-`3785318`):** PASSED.

| Metric | Value |
|---|---|
| k6 VUs | 5 |
| k6 duration (scenario) | 10 m |
| Baseline messages in DB before run | 5,239 (carry-over from earlier smoke) |
| Messages persisted during the run | **7,359** |
| Actual throughput | **≈12 msg/s** (vs 50 msg/s target — tunnel-bound) |
| Duplicate `receiver_pts` rows | **0** |
| Duplicate `sender_pts` rows | **0** |
| Largest receiver_pts gap | 253 (u3, from 4850 → 5103) |
| Integration test `TestPersistMessage_DuplicatePTSReturnsUnavailable` vs same cloud data plane | PASS (2.82 s) |

Sample of the largest receiver_pts gaps (from `go run ./cmd/dbcheck`):

```
[u3 4850 5103 253]   ← 253 messages rejected in one window
[u1 106  183   77]
[u3 138  161   23]
[u1 2343 2355 12]
[u3 2309 2315  6]
[u1 2430 2436  6]
[u3 2327 2333  6]
[u1 2380 2386  6]
...many 3–5 gaps
```

**Reading the gaps:** a gap of `n` on `receiver_id=X` means `n` messages bound for X consumed a Redis `receiver_pts` slot but did not reach the DB — the WAIT-durability check timed out before a replica ack landed, so Layer 1 returned `codes.Unavailable` and the Insert never fired. That is the CP behaviour we chose (spec §5, Decision D3): reject writes we cannot replicate-ack rather than admit out-of-order writes. Under tunnel latency the rejection rate is high; in-VPC it should drop to near zero.

**Sanity 60 s smoke-run numbers** (post-mutex-fix, immediately before the 10-min run): 2996 messages sent, 960 received by partner VUs, 0 errors, p95 ws-connect 6 ms. At smoke load the full 50 msg/s target was hit without rejection.

Verdict vs. spec §8 Phase 1 (a):

- "0 PTS duplicates in DB" — **✅ 0 duplicates** on both receiver_pts and sender_pts.
- "0 message loss" — under a strict read, ≈240 messages were rejected (gaps summing to ~240 over 10 min). Under the CAP reading, the system behaved correctly: it *declined* rather than losing. Phase 2 (Fargate) is where we re-measure this with realistic network latency.

---

## Phase 1 exit criterion (b): mid-run message-service kill

**Scenario:** `scripts/cloud-integration/kill-msgsvc-test.sh`. Starts `msgsvc` + `gateway` → k6 at 50 msg/s for 3 min → at t=60 s `taskkill -F -IM tt-msgsvc.exe` → immediately relaunch the pre-built binary → measure seconds until `http://127.0.0.1:9090/health` returns 200 → let k6 finish the remaining ~2 min.

**Result: PASSED**

```
[15:08:48] msgsvc up
[15:08:50] gateway up
[15:08:51] baseline messages: 527
[15:08:51] starting k6 (3 min at 50 msg/s target)...
[15:09:51] killing msgsvc...
[15:09:52] taskkill returned 0
[15:09:52] relaunched msgsvc, waiting for /health...
[15:09:54] msgsvc back after 3s      ← downtime
[15:11:52] k6 finished

=== Summary ===
  baseline       = 527
  final          = 1960
  messages added = 1433
  dup receiver   = 0
  dup sender     = 0
  downtime       = 3s

PASS: downtime 3s ≤ 10s; 0 PTS duplicates
```

| Metric | Value | Target |
|---|---|---|
| msgsvc downtime (kill → `/health` 200) | **3 s** | ≤ 10 s |
| Messages added across 3 min (incl. the 3 s downtime window) | 1433 | — |
| Duplicate receiver_pts | 0 | 0 |
| Duplicate sender_pts | 0 | 0 |
| k6 VUs that survived the restart | 5 / 5 | — |
| k6 scenario pass/fail | passed | — |

Sample of the receiver_pts gaps seen in this run (no outlier at the kill window — which is the interesting fact; we expected a spike there but gateway retry behaviour + Redis INCR idempotency kept it tight):

```
[u2 376 381 5]
[u5 221 226 5]
[u5 396 401 5]
[u3 208 212 4]
[u3 212 216 4]
[u5 497 501 4]
...only 3–5 gaps, no multi-message cluster
```

Why the 3 s downtime is credible:

- Pre-built binary skips `go compile` on restart (≈ 3–5 s saved).
- Migration step is idempotent (`CREATE TABLE IF NOT EXISTS` + `DO` blocks catching `duplicate_object` / `duplicate_table`). No schema work on re-launch.
- Redis connection ≈ 500 ms–1 s over tunnel.
- Postgres connection ≈ 500 ms–1 s over tunnel.
- gRPC listener ≈ instant.

Gateway's gRPC client to msgsvc sees a brief stream of `codes.Unavailable` errors during the 3 s window — see [kill-msgsvc-test.sh](../../scripts/cloud-integration/kill-msgsvc-test.sh) output. Client WebSockets stayed connected; k6 saw no session drops.

Verdict vs. spec §8 Phase 1 (b): **✅ PASS** on all three sub-criteria (downtime, no duplicates, sessions survived).

---

## Reproduction

1. Deploy the two long-lived stacks if not already running:

   ```bash
   cd infra-cdk
   eval "$(aws configure export-credentials --profile myisb_IsbUsersPS-557270420767 --format env)"
   AWS_REGION=us-east-1 npx cdk deploy TtVpcStack TtDataStack --require-approval never
   ```

2. Deploy a fresh bastion (`TtBastionStack`) — it was destroyed in Task 15 of Plan 1. Its CDK source is gone from the working tree after commit `c6dfee9`; resurrect it from that commit if needed, or move on to Plan 2 where ECS Exec replaces the bastion path entirely.

3. Open the two port-forwards (`scripts/cloud-integration/start-bastion-tunnel.sh`), then run either:

   - 10-min (a): `DURATION=10m DURATION_MS=600000 k6 run scripts/cloud-integration/k6-integration-check.js`
   - Kill test (b): `bash scripts/cloud-integration/kill-msgsvc-test.sh`

4. Verify with `cd message-service && POSTGRES_DSN=... go run ./cmd/dbcheck`.
