package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const stationRecordVersion = 1

type stationRecord struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	Slug           string    `json:"slug,omitempty"`
	Profile        string    `json:"stationProfile"`
	State          string    `json:"state"`
	DesiredState   string    `json:"desiredState"`
	Attempt        int       `json:"attempt"`
	Provider       string    `json:"provider"`
	LeaseID        string    `json:"leaseId"`
	LeaseSlug      string    `json:"leaseSlug,omitempty"`
	RepoPath       string    `json:"repoPath,omitempty"`
	Workdir        string    `json:"workdir"`
	Command        []string  `json:"command"`
	CommandDisplay string    `json:"commandDisplay"`
	CommandHash    string    `json:"commandHash"`
	ShellMode      bool      `json:"shellMode,omitempty"`
	Job            string    `json:"job,omitempty"`
	HarnessPath    string    `json:"harnessPath,omitempty"`
	HarnessHash    string    `json:"harnessHash,omitempty"`
	PlanPath       string    `json:"planPath,omitempty"`
	PlanHash       string    `json:"planHash,omitempty"`
	TTL            string    `json:"ttl,omitempty"`
	IdleTimeout    string    `json:"idleTimeout,omitempty"`
	RemoteDir      string    `json:"remoteDir"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	LastObservedAt time.Time `json:"lastObservedAt,omitempty"`
	StopReason     string    `json:"stopReason,omitempty"`
}

type stationRemoteStatus struct {
	State           string `json:"state,omitempty"`
	DesiredState    string `json:"desiredState,omitempty"`
	StopReason      string `json:"stopReason,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	SupervisorPID   string `json:"supervisorPid,omitempty"`
	CommandPID      string `json:"commandPid,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	LastHeartbeatAt string `json:"lastHeartbeatAt,omitempty"`
	LastOutputAt    string `json:"lastOutputAt,omitempty"`
	LogBytes        int64  `json:"logBytes,omitempty"`
}

func (a App) station(ctx context.Context, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		a.printStationHelp()
		return nil
	}
	switch args[0] {
	case "start":
		return a.stationStart(ctx, args[1:])
	case "status":
		return a.stationStatus(ctx, args[1:])
	case "logs":
		return a.stationLogs(ctx, args[1:])
	case "stop":
		return a.stationStop(ctx, args[1:])
	default:
		a.printStationHelp()
		return exit(2, "unknown station command %q", args[0])
	}
}

func (a App) printStationHelp() {
	fmt.Fprintln(a.Stdout, `Usage: crabbox station start|status|logs|stop [flags]

Start a supervised long-running workload on an SSH-backed lease.

Commands:
  crabbox station start [run flags] -- <command...>
  crabbox station start --command "scripts/agent-loop.sh"
  crabbox station status <station-id-or-slug> [--json]
  crabbox station logs <station-id-or-slug> [--tail N]
  crabbox station stop <station-id-or-slug>`)
}

func (a App) stationStart(ctx context.Context, args []string) error {
	opts, runArgs, err := parseStationStartArgs(args)
	if err != nil {
		return err
	}
	if len(opts.Command) == 0 {
		return exit(2, "usage: crabbox station start [flags] -- <command...>")
	}
	repo, err := findRepo()
	if err != nil {
		return err
	}
	timing, err := a.stationBootstrapSync(ctx, runArgs)
	if err != nil {
		return err
	}
	if strings.TrimSpace(timing.LeaseID) == "" || strings.TrimSpace(timing.Workdir) == "" {
		return exit(5, "station bootstrap did not return a lease id and workdir")
	}
	cfg, target, err := a.stationResolveTarget(ctx, timing.Provider, timing.LeaseID)
	if err != nil {
		return err
	}
	if isWindowsNativeTarget(target) {
		return exit(2, "station start currently supports Linux/macOS SSH targets; native Windows stations are not implemented")
	}
	now := time.Now().UTC()
	id, err := newStationID()
	if err != nil {
		return err
	}
	commandScript := stationCommandScript(opts.Command, opts.ShellMode)
	record := stationRecord{
		Version:        stationRecordVersion,
		ID:             id,
		Slug:           firstNonBlank(opts.Slug, timing.Slug),
		Profile:        firstNonBlank(opts.Profile, "default"),
		State:          "starting",
		DesiredState:   "running",
		Attempt:        1,
		Provider:       firstNonBlank(timing.Provider, cfg.Provider),
		LeaseID:        timing.LeaseID,
		LeaseSlug:      timing.Slug,
		RepoPath:       firstNonBlank(timing.RepoPath, repo.Root),
		Workdir:        timing.Workdir,
		Command:        opts.Command,
		CommandDisplay: runCommandDisplay(opts.Command, opts.ShellMode),
		CommandHash:    sha256Hex(commandScript),
		ShellMode:      opts.ShellMode,
		Job:            opts.Job,
		HarnessPath:    opts.Harness,
		PlanPath:       opts.Plan,
		TTL:            opts.TTL,
		IdleTimeout:    firstNonBlank(opts.IdleTimeout, timing.IdleTimeout),
		RemoteDir:      stationRemoteDir(timing.Workdir, id),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	record.HarnessHash, err = optionalFileSHA256(repo.Root, record.HarnessPath)
	if err != nil {
		return err
	}
	record.PlanHash, err = optionalFileSHA256(repo.Root, record.PlanPath)
	if err != nil {
		return err
	}
	if err := a.stationStartRemoteSupervisor(ctx, target, record, commandScript); err != nil {
		return err
	}
	record.State = "running"
	record.LastObservedAt = now
	record.UpdatedAt = now
	if err := writeStationRecord(record); err != nil {
		return err
	}
	if opts.JSON {
		return json.NewEncoder(a.Stdout).Encode(record)
	}
	fmt.Fprintf(a.Stdout, "station %s state=running profile=%s lease=%s slug=%s workdir=%s\n", record.ID, record.Profile, record.LeaseID, blank(record.Slug, "-"), record.Workdir)
	fmt.Fprintf(a.Stdout, "status: crabbox station status %s\n", record.ID)
	return nil
}

type stationStartOptions struct {
	Command     []string
	ShellMode   bool
	JSON        bool
	Profile     string
	Job         string
	Harness     string
	Plan        string
	Slug        string
	TTL         string
	IdleTimeout string
}

func parseStationStartArgs(args []string) (stationStartOptions, []string, error) {
	var opts stationStartOptions
	runArgs := make([]string, 0, len(args)+3)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.Command = append([]string{}, args[i+1:]...)
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--command":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--command requires a value")
				}
				value = args[i]
			}
			opts.Command = []string{value}
			opts.ShellMode = true
		case "--shell":
			opts.ShellMode = true
		case "--json":
			opts.JSON = true
		case "--station-profile":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--station-profile requires a value")
				}
				value = args[i]
			}
			opts.Profile = strings.TrimSpace(value)
		case "--job":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--job requires a value")
				}
				value = args[i]
			}
			opts.Job = strings.TrimSpace(value)
		case "--harness":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--harness requires a value")
				}
				value = args[i]
			}
			opts.Harness = strings.TrimSpace(value)
		case "--plan":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--plan requires a value")
				}
				value = args[i]
			}
			opts.Plan = strings.TrimSpace(value)
		case "--station-slug":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--station-slug requires a value")
				}
				value = args[i]
			}
			opts.Slug = strings.TrimSpace(value)
		case "--ttl":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--ttl requires a value")
				}
				value = args[i]
			}
			opts.TTL = strings.TrimSpace(value)
			runArgs = append(runArgs, "--ttl", value)
		case "--idle-timeout":
			if !hasValue {
				i++
				if i >= len(args) {
					return opts, nil, exit(2, "--idle-timeout requires a value")
				}
				value = args[i]
			}
			opts.IdleTimeout = strings.TrimSpace(value)
			runArgs = append(runArgs, "--idle-timeout", value)
		default:
			runArgs = append(runArgs, arg)
		}
	}
	runArgs = append(runArgs, "--keep", "--sync-only", "--timing-json")
	return opts, runArgs, nil
}

func (a App) stationBootstrapSync(ctx context.Context, runArgs []string) (timingReport, error) {
	var stderr bytes.Buffer
	runApp := a
	runApp.Stderr = io.MultiWriter(a.Stderr, &stderr)
	if err := runApp.runCommand(ctx, runArgs); err != nil {
		return timingReport{}, err
	}
	report, ok := lastTimingReport(stderr.String())
	if !ok {
		return timingReport{}, exit(5, "station bootstrap did not emit timing JSON")
	}
	if report.ExitCode != 0 {
		return timingReport{}, ExitError{Code: report.ExitCode, Message: fmt.Sprintf("station bootstrap exited %d", report.ExitCode)}
	}
	return report, nil
}

func lastTimingReport(output string) (timingReport, bool) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	lines := []string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], "{") {
			continue
		}
		var report timingReport
		if err := json.Unmarshal([]byte(lines[i]), &report); err == nil && report.Provider != "" {
			return report, true
		}
	}
	return timingReport{}, false
}

func (a App) stationResolveTarget(ctx context.Context, provider, leaseID string) (Config, SSHTarget, error) {
	defaults := defaultConfig()
	fs := newFlagSet("station target", a.Stderr)
	providerFlag := fs.String("provider", firstNonBlank(provider, defaults.Provider), providerHelpSSH())
	providerFlags := registerProviderFlags(fs, defaults)
	targetFlags := registerTargetFlags(fs, defaults)
	networkFlags := registerNetworkModeFlag(fs, defaults)
	cfg, err := loadSSHCommandConfig(fs, *providerFlag, providerFlags, targetFlags, networkFlags, leaseTargetConfigOptions{LeaseID: leaseID})
	if err != nil {
		return Config{}, SSHTarget{}, err
	}
	server, target, _, err := a.resolveNetworkLeaseTargetForRepo(ctx, cfg, leaseID, false, false)
	if err != nil {
		return Config{}, SSHTarget{}, err
	}
	applyResolvedServerConfig(&cfg, server)
	return cfg, target, nil
}

func (a App) stationStartRemoteSupervisor(ctx context.Context, target SSHTarget, record stationRecord, commandScript string) error {
	ttlSecs := durationSeconds(record.TTL)
	idleSecs := durationSeconds(record.IdleTimeout)
	remote := stationRemoteStartCommand(record.RemoteDir, record.Workdir, record.ID, record.Attempt, commandScript, ttlSecs, idleSecs)
	if out, err := runSSHCombinedOutput(ctx, target, remote); err != nil {
		if strings.TrimSpace(out) != "" {
			return exit(7, "start station supervisor: %v: %s", err, strings.TrimSpace(out))
		}
		return exit(7, "start station supervisor: %v", err)
	}
	return nil
}

func stationRemoteStartCommand(remoteDir, workdir, stationID string, attempt int, commandScript string, ttlSecs, idleSecs int64) string {
	script := `set -eu
remote_dir=` + shellQuote(remoteDir) + `
workdir=` + shellQuote(workdir) + `
station_id=` + shellQuote(stationID) + `
attempt=` + strconv.Itoa(attempt) + `
command_script=` + shellQuote(commandScript) + `
ttl_secs=` + strconv.FormatInt(ttlSecs, 10) + `
idle_secs=` + strconv.FormatInt(idleSecs, 10) + `
mkdir -p "$remote_dir"
chmod 700 "$remote_dir" 2>/dev/null || true
log="$remote_dir/station.log"
status="$remote_dir/status.json"
heartbeat="$remote_dir/heartbeat"
printf '%s\n' "$command_script" > "$remote_dir/command.sh"
chmod 700 "$remote_dir/command.sh"
now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
write_status() {
  state="$1"; reason="${2:-}"; exit_code="${3:-}"
  ts="$(now_utc)"
  log_bytes=0
  if [ -f "$log" ]; then log_bytes="$(wc -c < "$log" | tr -d ' ')"; fi
  command_pid="$(cat "$remote_dir/command.pid" 2>/dev/null || true)"
  supervisor_pid="$(cat "$remote_dir/supervisor.pid" 2>/dev/null || true)"
  last_output="$(cat "$remote_dir/last_output_at" 2>/dev/null || true)"
  {
    printf '{"stationId":%s,"attempt":%s,"state":%s,"desiredState":%s' "$(json_string "$station_id")" "$attempt" "$(json_string "$state")" "$(json_string running)"
    printf ',"supervisorPid":%s,"commandPid":%s' "$(json_string "$supervisor_pid")" "$(json_string "$command_pid")"
    printf ',"updatedAt":%s,"lastHeartbeatAt":%s,"lastOutputAt":%s,"logBytes":%s' "$(json_string "$ts")" "$(json_string "$ts")" "$(json_string "$last_output")" "$log_bytes"
    if [ -n "$reason" ]; then printf ',"stopReason":%s' "$(json_string "$reason")"; fi
    if [ -n "$exit_code" ]; then printf ',"exitCode":%s' "$exit_code"; fi
    printf '}\n'
  } > "$status.tmp"
  mv "$status.tmp" "$status"
  printf '%s\n' "$ts" > "$heartbeat"
}
json_string() {
  python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1" 2>/dev/null || printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"
}
write_status starting
(
  set +e
  printf '[station] id=%s attempt=%s starting at %s\n' "$station_id" "$attempt" "$(now_utc)" >> "$log"
  cd "$workdir" || { printf '[station] workdir unavailable: %s\n' "$workdir" >> "$log"; write_status failed workdir_unavailable 127; exit 127; }
  if command -v setsid >/dev/null 2>&1; then
    setsid "$remote_dir/command.sh" >> "$log" 2>&1 &
  else
    "$remote_dir/command.sh" >> "$log" 2>&1 &
  fi
  child=$!
  printf '%s\n' "$child" > "$remote_dir/command.pid"
  printf '%s\n' "$(now_utc)" > "$remote_dir/last_output_at"
  start_epoch="$(date +%s)"
  last_size=0
  stop_reason=""
  write_status running
  while kill -0 "$child" 2>/dev/null; do
    now_epoch="$(date +%s)"
    size=0
    if [ -f "$log" ]; then size="$(wc -c < "$log" | tr -d ' ')"; fi
    if [ "$size" != "$last_size" ]; then
      printf '%s\n' "$(now_utc)" > "$remote_dir/last_output_at"
      printf '%s\n' "$now_epoch" > "$remote_dir/last_output_epoch"
      last_size="$size"
    fi
    if [ "$ttl_secs" -gt 0 ] && [ $((now_epoch - start_epoch)) -ge "$ttl_secs" ]; then
      stop_reason=ttl_expired
      break
    fi
    last_output_epoch="$(cat "$remote_dir/last_output_epoch" 2>/dev/null || printf '%s' "$start_epoch")"
    if [ "$idle_secs" -gt 0 ] && [ $((now_epoch - last_output_epoch)) -ge "$idle_secs" ]; then
      stop_reason=idle_expired
      break
    fi
    write_status running
    sleep 5
  done
  if [ -n "$stop_reason" ] && kill -0 "$child" 2>/dev/null; then
    kill -TERM -- "-$child" 2>/dev/null || kill -TERM "$child" 2>/dev/null || true
    sleep 10
    kill -KILL -- "-$child" 2>/dev/null || kill -KILL "$child" 2>/dev/null || true
  fi
  wait "$child"
  code=$?
  if [ -z "$stop_reason" ]; then
    if [ "$code" -eq 0 ]; then state=succeeded; else state=failed; fi
  else
    state="$stop_reason"
  fi
  printf '[station] id=%s attempt=%s finished state=%s exit=%s at %s\n' "$station_id" "$attempt" "$state" "$code" "$(now_utc)" >> "$log"
  write_status "$state" "$stop_reason" "$code"
) >/dev/null 2>&1 &
printf '%s\n' "$!" > "$remote_dir/supervisor.pid"
write_status running
`
	return "bash -lc " + shellQuote(script)
}

func (a App) stationStatus(ctx context.Context, args []string) error {
	fs := newFlagSet("station status", a.Stderr)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	id := ""
	if fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		return exit(2, "usage: crabbox station status <station-id-or-slug>")
	}
	record, err := readStationRecord(id)
	if err != nil {
		return err
	}
	status, err := a.observeStation(ctx, &record)
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: station remote status unavailable: %v\n", err)
	}
	if *jsonOut {
		return json.NewEncoder(a.Stdout).Encode(map[string]any{"station": record, "remote": status})
	}
	fmt.Fprintf(a.Stdout, "%s slug=%s profile=%s state=%s desired=%s attempt=%d lease=%s workdir=%s last_observed=%s stop_reason=%s\n",
		record.ID, blank(record.Slug, "-"), record.Profile, record.State, record.DesiredState, record.Attempt, record.LeaseID, record.Workdir, formatStationTime(record.LastObservedAt), blank(record.StopReason, "-"))
	if status.LogBytes > 0 {
		fmt.Fprintf(a.Stdout, "remote supervisor=%s command=%s heartbeat=%s log_bytes=%d\n", blank(status.SupervisorPID, "-"), blank(status.CommandPID, "-"), blank(status.LastHeartbeatAt, "-"), status.LogBytes)
	}
	return nil
}

func (a App) observeStation(ctx context.Context, record *stationRecord) (stationRemoteStatus, error) {
	if record == nil {
		return stationRemoteStatus{}, exit(2, "station record is required")
	}
	cfg, target, err := a.stationResolveTarget(ctx, record.Provider, record.LeaseID)
	if err != nil {
		return stationRemoteStatus{}, err
	}
	_ = cfg
	out, err := runSSHOutput(ctx, target, stationRemoteStatusCommand(record.RemoteDir))
	if err != nil {
		return stationRemoteStatus{}, err
	}
	var status stationRemoteStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &status); err != nil {
		return stationRemoteStatus{}, err
	}
	if status.State != "" {
		record.State = status.State
	}
	if status.DesiredState != "" {
		record.DesiredState = status.DesiredState
	}
	if status.StopReason != "" {
		record.StopReason = status.StopReason
	}
	record.LastObservedAt = time.Now().UTC()
	record.UpdatedAt = record.LastObservedAt
	if err := writeStationRecord(*record); err != nil {
		return status, err
	}
	return status, nil
}

func stationRemoteStatusCommand(remoteDir string) string {
	script := `set -eu
status=` + shellQuote(remoteDir) + `/status.json
if [ ! -f "$status" ]; then
  printf '{"state":"lost","desiredState":"running","stopReason":"missing_status"}\n'
  exit 0
fi
cat "$status"
`
	return "bash -lc " + shellQuote(script)
}

func (a App) stationLogs(ctx context.Context, args []string) error {
	fs := newFlagSet("station logs", a.Stderr)
	tail := fs.Int("tail", 0, "print only the last N log lines")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *tail < 0 {
		return exit(2, "tail must be >= 0")
	}
	id := ""
	if fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		return exit(2, "usage: crabbox station logs <station-id-or-slug>")
	}
	record, err := readStationRecord(id)
	if err != nil {
		return err
	}
	_, target, err := a.stationResolveTarget(ctx, record.Provider, record.LeaseID)
	if err != nil {
		return err
	}
	out, err := runSSHOutput(ctx, target, stationRemoteLogsCommand(record.RemoteDir, *tail))
	if err != nil {
		return err
	}
	fmt.Fprint(a.Stdout, out)
	return nil
}

func stationRemoteLogsCommand(remoteDir string, tail int) string {
	logPath := remoteDir + "/station.log"
	if tail > 0 {
		return "tail -n " + strconv.Itoa(tail) + " " + shellQuote(logPath)
	}
	return "cat " + shellQuote(logPath)
}

func (a App) stationStop(ctx context.Context, args []string) error {
	fs := newFlagSet("station stop", a.Stderr)
	release := fs.Bool("release", false, "release the lease after stopping the station")
	grace := fs.Duration("grace", 10*time.Second, "grace period before SIGKILL")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	id := ""
	if fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		return exit(2, "usage: crabbox station stop <station-id-or-slug>")
	}
	record, err := readStationRecord(id)
	if err != nil {
		return err
	}
	_, target, err := a.stationResolveTarget(ctx, record.Provider, record.LeaseID)
	if err != nil {
		return err
	}
	if out, err := runSSHCombinedOutput(ctx, target, stationRemoteStopCommand(record.RemoteDir, int(grace.Seconds()))); err != nil {
		if strings.TrimSpace(out) != "" {
			return exit(7, "stop station: %v: %s", err, strings.TrimSpace(out))
		}
		return exit(7, "stop station: %v", err)
	}
	record.State = "stopped"
	record.DesiredState = "stopped"
	record.StopReason = firstNonBlank(record.StopReason, "user")
	record.LastObservedAt = time.Now().UTC()
	record.UpdatedAt = record.LastObservedAt
	if err := writeStationRecord(record); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "station %s stopped lease=%s\n", record.ID, record.LeaseID)
	if *release {
		return a.stop(ctx, []string{"--provider", record.Provider, "--id", record.LeaseID})
	}
	return nil
}

func stationRemoteStopCommand(remoteDir string, graceSeconds int) string {
	if graceSeconds < 0 {
		graceSeconds = 0
	}
	script := `set -eu
remote_dir=` + shellQuote(remoteDir) + `
grace=` + strconv.Itoa(graceSeconds) + `
command_pid="$(cat "$remote_dir/command.pid" 2>/dev/null || true)"
supervisor_pid="$(cat "$remote_dir/supervisor.pid" 2>/dev/null || true)"
now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
if [ -z "$command_pid" ] && [ -z "$supervisor_pid" ]; then
  exit 0
fi
if [ -n "$command_pid" ] && kill -0 "$command_pid" 2>/dev/null; then
  kill -TERM -- "-$command_pid" 2>/dev/null || kill -TERM "$command_pid" 2>/dev/null || true
  sleep "$grace"
  kill -KILL -- "-$command_pid" 2>/dev/null || kill -KILL "$command_pid" 2>/dev/null || true
fi
if [ -n "$supervisor_pid" ] && kill -0 "$supervisor_pid" 2>/dev/null; then
  kill -TERM "$supervisor_pid" 2>/dev/null || true
fi
printf '{"state":"stopped","desiredState":"stopped","stopReason":"user","updatedAt":"%s"}\n' "$(now_utc)" > "$remote_dir/status.json"
`
	return "bash -lc " + shellQuote(script)
}

func stationCommandScript(command []string, shellMode bool) string {
	if shellMode {
		return strings.Join(command, " ")
	}
	return shellScriptFromArgv(command)
}

func stationRemoteDir(workdir, stationID string) string {
	return strings.TrimRight(workdir, "/") + "/.crabbox/stations/" + stationID
}

func writeStationRecord(record stationRecord) error {
	dir, err := stationStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, record.ID+".json"), record)
}

func readStationRecord(id string) (stationRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return stationRecord{}, exit(2, "station id is required")
	}
	dir, err := stationStateDir()
	if err != nil {
		return stationRecord{}, err
	}
	paths := []string{filepath.Join(dir, safeStationRecordName(id)+".json")}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return stationRecord{}, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		record, err := readStationRecordPath(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return stationRecord{}, err
		}
		if record.ID == id || record.Slug == id || record.LeaseID == id || record.LeaseSlug == id {
			return record, nil
		}
	}
	return stationRecord{}, exit(2, "station %s not found", id)
}

func readStationRecordPath(path string) (stationRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stationRecord{}, err
	}
	var record stationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return stationRecord{}, err
	}
	return record, nil
}

func stationStateDir() (string, error) {
	dir, err := crabboxStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "stations"), nil
}

func safeStationRecordName(value string) string {
	return safeWebVNCDaemonName(value)
}

func newStationID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "stn_" + hex.EncodeToString(b[:]), nil
}

func optionalFileSHA256(repoRoot, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, filepath.FromSlash(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", exit(2, "read %s: %v", path, err)
	}
	return sha256Hex(string(data)), nil
}

func durationSeconds(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0
	}
	return int64(duration.Round(time.Second) / time.Second)
}

func formatStationTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func listStationRecords() ([]stationRecord, error) {
	dir, err := stationStateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]stationRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := readStationRecordPath(filepath.Join(dir, entry.Name()))
		if err == nil {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}
