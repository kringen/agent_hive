# Testing NATS JetStream High Availability

This walks through verifying that the 3-node NATS JetStream cluster
(`agent-platform/docker-compose.yml`) actually tolerates losing a node
without losing data or availability — the same steps used to validate the
clustering setup during development.

Commands below use `podman compose` / `podman`. Substitute `docker compose`
/ `docker` if that's your container runtime — the compose file and NATS
config don't care which one runs them.

## 1. Start just the NATS cluster

```bash
cd agent-platform
podman compose up -d nats-1 nats-2 nats-3
sleep 8
podman compose ps
```

All three containers should show `Up`. Ports: `nats-1` on 4222/8222,
`nats-2` on 4223/8223, `nats-3` on 4224/8224.

## 2. Confirm the cluster actually formed

```bash
curl -s http://localhost:8222/routez | python3 -c \
  "import json,sys; d=json.load(sys.stdin); print('num_routes:', d.get('num_routes')); [print(r.get('remote_id'), r.get('ip'), r.get('port')) for r in d.get('routes',[])]"
```

You should see `num_routes: 8` (each node maintains multiple route
connections to its two peers) with **two distinct `remote_id` values** —
one per peer node. If you only see one distinct ID, or the count is 0, the
nodes aren't finding each other — check `agent-platform/nats/nats-server.conf`
routes and that all three containers are on the same compose network.

## 3. Create a replicated stream

Use the official `nats-box` image so you don't need the `nats` CLI
installed on the host:

```bash
podman run --rm --network agent-platform_default docker.io/natsio/nats-box:latest \
  nats --server nats://nats-1:4222,nats://nats-2:4222,nats://nats-3:4222 \
  stream add AGENT_TASKS --subjects "agent.tasks.>" --replicas 3 \
  --storage file --retention limits --defaults
```

(The router creates this same stream automatically on startup via
`CreateOrUpdateStream` with `Replicas: 3` — this manual step is only
needed if you want to inspect the cluster before/without running the
router.)

Check its status:

```bash
podman run --rm --network agent-platform_default docker.io/natsio/nats-box:latest \
  nats --server nats://nats-1:4222,nats://nats-2:4222,nats://nats-3:4222 \
  stream info AGENT_TASKS
```

Look for a `Cluster Information` block listing a `Leader` and two
`Replica` entries, both `current`.

## 4. Kill the leader and confirm zero-downtime failover

Find which node is currently the leader from the `stream info` output
above (e.g. `nats-1`), then stop it:

```bash
podman stop agent-platform_nats-1_1
sleep 5
```

Query the stream again — **using the two surviving nodes** as the server
list:

```bash
podman run --rm --network agent-platform_default docker.io/natsio/nats-box:latest \
  nats --server nats://nats-2:4222,nats://nats-3:4222 \
  stream info AGENT_TASKS
```

Expected result: a **new leader** was auto-elected from the two survivors
within a few seconds, the stream is still fully readable/writable, and the
dead node shows as `OFFLINE`/`not seen` in the replica list. This is the
core HA guarantee — losing any single node (1 of 3) doesn't take down the
task queue.

## 5. Bring the node back and confirm it rejoins

```bash
podman start agent-platform_nats-1_1
sleep 6
podman run --rm --network agent-platform_default docker.io/natsio/nats-box:latest \
  nats --server nats://nats-1:4222,nats://nats-2:4222,nats://nats-3:4222 \
  stream info AGENT_TASKS
```

The previously-stopped node should rejoin as a `current` replica (it
catches up any missed data automatically).

## 6. Clean up

```bash
podman compose down -v
```

The `-v` flag also removes the `nats1_data`/`nats2_data`/`nats3_data`
volumes — drop it if you want to keep the JetStream data around.

## What this does *not* yet cover

- **Router/dashboard-api HA**: those services still run as a single
  instance each with local SQLite files, so killing the `router` container
  currently does interrupt task processing (NATS will simply hold/redeliver
  queued messages until a router instance reconnects). Multi-replica
  router/dashboard-api with shared storage is tracked in the main README
  roadmap.
- **Split-brain / network partition testing** — the steps above simulate a
  clean node crash (`stop`), not a network partition between nodes. NATS
  JetStream uses Raft consensus so a minority partition can't accept writes,
  but this isn't exercised here.
- **Data durability across a full cluster restart** — covered implicitly
  since the volumes persist, but not explicitly tested step-by-step above.
