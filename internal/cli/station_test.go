package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseStationStartArgsSplitsStationAndRunFlags(t *testing.T) {
	opts, runArgs, err := parseStationStartArgs([]string{
		"--provider", "ssh",
		"--station-profile", "agent",
		"--job", "agent-loop",
		"--harness", "HARNESS.md",
		"--plan", "PLAN.md",
		"--ttl", "10h",
		"--idle-timeout=45m",
		"--command", "scripts/agent-loop.sh",
		"--json",
	})
	if err != nil {
		t.Fatalf("parseStationStartArgs: %v", err)
	}
	if opts.Profile != "agent" || opts.Job != "agent-loop" || opts.Harness != "HARNESS.md" || opts.Plan != "PLAN.md" {
		t.Fatalf("station opts not captured: %#v", opts)
	}
	if !opts.ShellMode || !opts.JSON || !reflect.DeepEqual(opts.Command, []string{"scripts/agent-loop.sh"}) {
		t.Fatalf("command opts not captured: %#v", opts)
	}
	wantRun := []string{"--provider", "ssh", "--ttl", "10h", "--idle-timeout", "45m", "--keep", "--sync-only", "--timing-json"}
	if !reflect.DeepEqual(runArgs, wantRun) {
		t.Fatalf("run args\ngot:  %#v\nwant: %#v", runArgs, wantRun)
	}
}

func TestParseStationStartArgsUsesTrailingCommand(t *testing.T) {
	opts, runArgs, err := parseStationStartArgs([]string{"--id", "blue-lobster", "--", "pnpm", "test", "--watch"})
	if err != nil {
		t.Fatalf("parseStationStartArgs: %v", err)
	}
	if opts.ShellMode {
		t.Fatal("trailing argv command should not imply shell mode")
	}
	if !reflect.DeepEqual(opts.Command, []string{"pnpm", "test", "--watch"}) {
		t.Fatalf("command = %#v", opts.Command)
	}
	wantRun := []string{"--id", "blue-lobster", "--keep", "--sync-only", "--timing-json"}
	if !reflect.DeepEqual(runArgs, wantRun) {
		t.Fatalf("run args\ngot:  %#v\nwant: %#v", runArgs, wantRun)
	}
}

func TestLastTimingReportSkipsProgressLines(t *testing.T) {
	report, ok := lastTimingReport("syncing repo\n{\"provider\":\"ssh\",\"leaseId\":\"cbx_1\",\"workdir\":\"/work/cbx_1/repo\",\"exitCode\":0}\n")
	if !ok {
		t.Fatal("expected timing report")
	}
	if report.Provider != "ssh" || report.LeaseID != "cbx_1" || report.Workdir != "/work/cbx_1/repo" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestStationRemoteStartCommandContainsSupervisorContract(t *testing.T) {
	got := stationRemoteStartCommand("/work/repo/.crabbox/stations/stn_1", "/work/repo", "stn_1", 1, "printf 'hello world'\n", 3600, 60)
	for _, want := range []string{
		"workdir=",
		"/work/repo",
		`cd "$workdir"`,
		"workdir_unavailable",
		"station.log",
		"status.json",
		"heartbeat",
		"command.pid",
		"supervisor.pid",
		"ttl_expired",
		"idle_expired",
		"hello world",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("supervisor command missing %q in:\n%s", want, got)
		}
	}
}
