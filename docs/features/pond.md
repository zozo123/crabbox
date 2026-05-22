# Pond

A small group of crabbox boxes connected on a private network, addressable by name, that live and die together. Same idea as a tidepool — a contained body of water where a few creatures coexist for a while, then it dries up.

A `--pond` of one is the default; existing single-box flows are unchanged.

## Usage

```
crabbox warmup --pond NAME --slug ROLE --provider PROVIDER ...
crabbox run    --id LEASE_ID -- COMMAND
crabbox list   --pond NAME
crabbox doctor --pond NAME
crabbox pond peers   --pond NAME
crabbox pond connect NAME [--export]
crabbox release --pond NAME
```

Each lease in a pond is addressable by its `--slug` from any other member of the same pond.

## Three transport planes

| Provider class                                                              | Plane     | What you get                                              |
| --------------------------------------------------------------------------- | --------- | --------------------------------------------------------- |
| Managed Linux (Hetzner, Azure, GCP)                                         | Tailscale | true peer-to-peer mesh, `<slug>.cbx` DNS                  |
| Delegated URL (Islo, E2B, Railway)                                          | Bridge    | HTTPS endpoints between pond members                      |
| SSH-only (Proxmox, RunPod, exe.dev, Daytona, Sprites, Namespace, Semaphore) | SSH-mesh  | operator-side `ssh -L` tunnels via `pond connect --export` |
| macOS, Windows                                                              | —         | not yet covered                                           |

`crabbox pond peers` returns a transport hint per member: `tailnet` / `url` / `ssh` / `pending` / `unsupported` / `none`.

## Three simple use cases

1. **Per-PR isolated E2E env.** Every PR gets its own staging; pond dies with PR:
   ```
   crabbox warmup --pond pr-$PR --slug api/web/db --provider hetzner --tailscale
   # ... E2E ...
   crabbox release --pond pr-$PR
   ```

2. **API + GPU + DB integration test.** Vendor-mix in 4 lines — CPU on Hetzner, GPU on Modal, DB on Hetzner — talking by name:
   ```
   crabbox warmup --pond it-$SHA --slug api --provider hetzner --tailscale
   crabbox warmup --pond it-$SHA --slug ml  --provider modal   --class a10g
   crabbox warmup --pond it-$SHA --slug db  --provider hetzner --tailscale
   crabbox run --id it-$SHA-api -- "ML_HOST=ml.cbx DB_HOST=db.cbx go test ./..."
   ```

3. **Per-PR build farm.** 30-helper Islo pond per PR, dies with the PR — useful for slow C++/Bazel/Rust builds:
   ```
   crabbox warmup --pond build-$PR --slug coord --provider hetzner
   for i in $(seq 1 30); do
     crabbox warmup --pond build-$PR --slug helper-$i --provider islo &
   done; wait
   ```

## When not to use it

- **Tightly-coupled HPC** (MPI / NCCL) across providers — public-internet latency is too high. Keep tightly-coupled jobs on one provider + region.
- **macOS / Windows peer reachability** — gap, tracked separately.
- **Untrusted multi-tenant** — default-allow within a pond. For agent-isolation cases, see `--isolation per-slug` (future).

## Security

Setting `TS_API_KEY` in your shell empowers `crabbox run` to mutate your operator's Tailscale ACL policy (the auto-bootstrap path). The broker never sees Tailscale credentials.

## API stability

`pond` is **preview** for v0.x. The reserved `pond=` label key stays; the flag shape may evolve. Stable from v1.0.
