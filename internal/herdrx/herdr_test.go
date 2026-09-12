package herdrx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubHerdr writes a fake `herdr` onto PATH that replays canned responses.
//
// The script echoes its arguments to a log so a test can assert on exactly
// what dispatch asked herdr to do, which is the part that has to stay correct
// against the installed CLI.
func stubHerdr(t *testing.T, script string) (binary string, logPath string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "herdr")
	logPath = filepath.Join(dir, "args.log")

	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" + script
	if err := os.WriteFile(binary, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestCreateTabParsesTheRootPaneID(t *testing.T) {
	binary, logPath := stubHerdr(t, `cat <<'JSON'
{"id":"cli:tab:create","result":{"type":"tab_created","tab":{"tab_id":"w1:t3","workspace_id":"w1","label":"engineer: fix-login"},"root_pane":{"pane_id":"w1:p3","cwd":"/wt/fix-login"}}}
JSON`)

	tab, err := NewCLI(binary, "").CreateTab(context.Background(), CreateTabRequest{
		WorkspaceID: "w1", CWD: "/wt/fix-login", Label: "engineer: fix-login",
	})
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if tab.TabID != "w1:t3" || tab.PaneID != "w1:p3" || tab.WorkspaceID != "w1" {
		t.Errorf("tab = %+v", tab)
	}

	args := readLog(t, logPath)
	for _, want := range []string{"tab create", "--workspace w1", "--cwd /wt/fix-login", "--no-focus"} {
		if !strings.Contains(args, want) {
			t.Errorf("herdr invocation missing %q, got: %s", want, args)
		}
	}
}

func TestCreateTabWithoutARootPaneIsAnError(t *testing.T) {
	binary, _ := stubHerdr(t, `echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w1:t3"}}}'`)
	_, err := NewCLI(binary, "").CreateTab(context.Background(), CreateTabRequest{})
	if err == nil || !strings.Contains(err.Error(), "root pane") {
		t.Errorf("err = %v, want a complaint about the missing pane id", err)
	}
}

func TestStartAgentSendsKindPaneAndArgs(t *testing.T) {
	binary, logPath := stubHerdr(t, `cat <<'JSON'
{"id":"cli:agent:start","result":{"type":"agent_started","agent":{"name":"fix-login","agent":"claude","agent_status":"idle","pane_id":"w1:p3","tab_id":"w1:t3","workspace_id":"w1","terminal_id":"term_x","cwd":"/wt/fix-login","interactive_ready":true},"argv":["claude"]}}
JSON`)

	agent, err := NewCLI(binary, "").StartAgent(context.Background(), StartAgentRequest{
		Name: "fix-login", Kind: "claude", PaneID: "w1:p3", TimeoutMS: 60000,
		Args: []string{"--model", "opus"},
	})
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if agent.Name != "fix-login" || agent.Status != StatusIdle || !agent.InteractiveReady {
		t.Errorf("agent = %+v", agent)
	}

	args := readLog(t, logPath)
	for _, want := range []string{"agent start fix-login", "--kind claude", "--pane w1:p3", "--timeout 60000", "-- --model opus"} {
		if !strings.Contains(args, want) {
			t.Errorf("herdr invocation missing %q, got: %s", want, args)
		}
	}
}

func TestSessionFlagIsPassedBeforeTheSubcommand(t *testing.T) {
	binary, logPath := stubHerdr(t, `echo '{"id":"x","result":{"type":"agent_list","agents":[]}}'`)
	if _, err := NewCLI(binary, "work").ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := strings.TrimSpace(readLog(t, logPath))
	if !strings.HasPrefix(args, "--session work agent list") {
		t.Errorf("args = %q, want the session flag first", args)
	}
}

func TestListAgentsParsesLiveState(t *testing.T) {
	binary, _ := stubHerdr(t, `cat <<'JSON'
{"id":"cli:agent:list","result":{"type":"agent_list","agents":[
  {"name":"fix-login","agent":"claude","agent_status":"working","pane_id":"w1:p3","tab_id":"w1:t3","workspace_id":"w1","terminal_id":"t1","focused":false},
  {"name":"review-diff","agent":"codex","agent_status":"blocked","pane_id":"w1:p4","tab_id":"w1:t4","workspace_id":"w1","terminal_id":"t2","focused":true}
]}}
JSON`)

	agents, err := NewCLI(binary, "").ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("agents = %d", len(agents))
	}
	if agents[0].Status != StatusWorking || !agents[0].Status.Live() {
		t.Errorf("agent[0] = %+v", agents[0])
	}
	if !agents[1].Status.NeedsAttention() {
		t.Error("a blocked agent should need attention")
	}
}

func TestErrorsOnStderrBecomeTypedErrors(t *testing.T) {
	binary, _ := stubHerdr(t, `echo '{"id":"cli:agent:get","error":{"code":"agent_not_found","message":"agent target ghost not found"}}' >&2
exit 1`)

	_, err := NewCLI(binary, "").GetAgent(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsCode(err, CodeAgentNotFound) {
		t.Errorf("err = %v, want agent_not_found", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should carry herdr's message: %v", err)
	}
}

func TestServerNotRunningIsDetected(t *testing.T) {
	binary, _ := stubHerdr(t, `echo '{"id":"x","error":{"code":"server_not_running","message":"no herdr server is running at /tmp/herdr.sock"}}' >&2
exit 1`)

	_, err := NewCLI(binary, "").Workspaces(context.Background())
	if !IsCode(err, CodeServerNotRunning) {
		t.Errorf("err = %v, want server_not_running", err)
	}
}

func TestMissingBinaryIsReportedClearly(t *testing.T) {
	_, err := NewCLI(filepath.Join(t.TempDir(), "no-such-herdr"), "").ListAgents(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("err = %v, want ErrNotInstalled", err)
	}
}

func TestNonJSONOutputIsReportedWithoutPanicking(t *testing.T) {
	binary, _ := stubHerdr(t, `echo 'this is not json'`)
	_, err := NewCLI(binary, "").ListAgents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected output") {
		t.Errorf("err = %v", err)
	}
}

func TestHealthReadsStatusJSON(t *testing.T) {
	binary, _ := stubHerdr(t, `cat <<'JSON'
{"client":{"version":"0.9.0"},"server":{"status":"running","running":true,"version":"0.9.0","socket":"/tmp/herdr.sock","compatible":true}}
JSON`)

	health := NewCLI(binary, "").Health(context.Background())
	if !health.Installed || !health.ServerRunning || !health.Compatible {
		t.Errorf("health = %+v", health)
	}
	if health.ServerVersion != "0.9.0" || health.Socket != "/tmp/herdr.sock" {
		t.Errorf("health = %+v", health)
	}
}

func TestHealthReportsAStoppedServerWithAFix(t *testing.T) {
	binary, _ := stubHerdr(t, `cat <<'JSON'
{"client":{"version":"0.9.0"},"server":{"status":"not_running","running":false,"socket":"/tmp/herdr.sock"}}
JSON`)

	health := NewCLI(binary, "").Health(context.Background())
	if !health.Installed {
		t.Error("Installed should be true when the binary exists")
	}
	if health.ServerRunning {
		t.Error("ServerRunning should be false")
	}
	if health.Detail == "" {
		t.Error("Detail should explain what to do")
	}
}

func TestHealthOnAMissingBinary(t *testing.T) {
	health := NewCLI(filepath.Join(t.TempDir(), "absent"), "").Health(context.Background())
	if health.Installed {
		t.Error("Installed should be false")
	}
	if health.Detail == "" {
		t.Error("Detail should say the binary was not found")
	}
}

func TestStopAgentClosesTheHostingPane(t *testing.T) {
	// herdr 0.9.0 has no `agent stop`; dispatch closes the pane it created.
	binary, logPath := stubHerdr(t, `case "$*" in
  *"agent get"*) echo '{"id":"x","result":{"type":"agent_info","agent":{"name":"fix-login","pane_id":"w1:p3","agent_status":"idle","tab_id":"w1:t3","workspace_id":"w1","terminal_id":"t1"}}}' ;;
  *"pane close"*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	if err := NewCLI(binary, "").StopAgent(context.Background(), "fix-login"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	args := readLog(t, logPath)
	if !strings.Contains(args, "pane close w1:p3") {
		t.Errorf("expected the agent's pane to be closed, got: %s", args)
	}
}

func TestAttachCommandTargetsTheAgent(t *testing.T) {
	argv := NewCLI("herdr", "work").AttachCommand("fix-login")
	want := []string{"herdr", "--session", "work", "agent", "attach", "fix-login"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v", argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", argv, want)
		}
	}
}

func TestStatusHelpers(t *testing.T) {
	if !StatusBlocked.NeedsAttention() {
		t.Error("blocked needs attention")
	}
	for _, status := range []Status{StatusIdle, StatusWorking, StatusDone, StatusUnknown} {
		if status.NeedsAttention() {
			t.Errorf("%q should not need attention", status)
		}
	}
	if !StatusWorking.Live() || StatusIdle.Live() {
		t.Error("only working counts as live")
	}
}
