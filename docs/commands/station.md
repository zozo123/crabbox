# station

`crabbox station` starts and supervises one long-running workload on a kept
SSH-backed lease. It is the first Station phase: lifecycle, status, logs, and
stop behavior only. It does not deliver model credentials or add agent logic to
Crabbox core.

```sh
crabbox station start --command "scripts/watch-tests.sh" --ttl 10h --idle-timeout 45m
crabbox station start --station-profile default -- scripts/agent-loop.sh
crabbox station status stn_abcdef1234567890
crabbox station logs stn_abcdef1234567890 --tail 100
crabbox station stop stn_abcdef1234567890
```

## Behavior

`station start` reuses the mature `run --sync-only --keep` path to acquire or
reuse a lease, hydrate/sync the workspace, and discover the remote workdir. It
then writes a local durable station record and starts a tiny remote supervisor
under `.crabbox/stations/<station-id>/` in the synced workdir.

The supervisor launches the workload, tees output to `station.log`, writes
heartbeat and status JSON files, records the command pid, and terminates the
process group on `station stop`. TTL and idle timeout are passed through to the
lease bootstrap and also enforced by the station supervisor when possible.

## Flags

- `--command <script>` runs a shell command as the station workload.
- `-- <command...>` runs an argv-style command as the station workload.
- `--shell` treats the trailing command as a shell script.
- `--station-profile <name>` records the profile selector, default `default`.
- `--job <name>` records the repo job name for callers that map jobs to station
  commands.
- `--harness <path>` and `--plan <path>` record local SHA-256 hashes when the
  files exist; compliance evaluation is not part of this phase.
- Run/lease flags accepted by `crabbox run`, such as `--provider`, `--id`,
  `--class`, `--ttl`, `--idle-timeout`, `--full-resync`, and network flags, are
  forwarded to the bootstrap sync step.

## Boundary

Station v1 is SSH-only. Delegated-run providers are intentionally unsupported
until they expose a station-capable contract. Model or tool credentials must not
be passed with ordinary repo env allow lists; future `modelAccess` needs a
separate scoped, auditable, redacted, and revocable delivery path.
