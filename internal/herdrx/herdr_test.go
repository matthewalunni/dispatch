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

func TestCreateSessionWorktreeOpensAWorkspaceAndCleansUpTheBareTab(t *testing.T) {
	binary, logPath := stubHerdr(t, `case "$*" in
  *"worktree open"*)
    echo '{"id":"x","result":{"type":"worktree_opened","already_open":false,"workspace":{"workspace_id":"w6","label":"engineer: fix-login","worktree":{"checkout_path":"/wt/fix-login","repo_root":"/src/proj","repo_name":"proj","is_linked_worktree":true}},"tab":{"tab_id":"w6:t1"},"root_pane":{"pane_id":"w6:p1"},"worktree":{"path":"/wt/fix-login","branch":"dispatch/fix-login","is_linked_worktree":true}}}' ;;
  *"tab create"*)
    echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w6:t2","workspace_id":"w6","label":"engineer: fix-login"},"root_pane":{"pane_id":"w6:p2"}}}' ;;
  *"tab close"*)
    echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	session, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout:   LayoutWorkspace,
		CWD:      "/wt/fix-login",
		RepoRoot: "/src/proj",
		Worktree: true,
		Label:    "engineer: fix-login",
		Env:      []string{"DISPATCH_TASK_ID=task_1"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if session.WorkspaceID != "w6" || session.Layout != LayoutWorkspace {
		t.Errorf("session = %+v", session)
	}
	// The agent goes in the tab that carries the environment, not the bare
	// shell tab herdr opened with the worktree.
	if session.PaneID != "w6:p2" || session.TabID != "w6:t2" {
		t.Errorf("session should point at the env-carrying tab: %+v", session)
	}
	if session.WorktreeBranch != "dispatch/fix-login" || session.WorktreePath != "/wt/fix-login" {
		t.Errorf("worktree details lost: %+v", session)
	}
	if !session.OwnsWorkspace() {
		t.Error("dispatch created this workspace and must own it")
	}

	args := readLog(t, logPath)
	for _, want := range []string{
		"worktree open --cwd /src/proj --path /wt/fix-login",
		"--env DISPATCH_TASK_ID=task_1",
		"tab close w6:t1", // the bare shell tab must not linger
	} {
		if !strings.Contains(args, want) {
			t.Errorf("herdr invocations missing %q, got:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--trust-repository") {
		t.Error("git trust must not be granted unless configured")
	}
}

func TestCreateSessionPlainWorkspaceCarriesEnvOnItsRootPane(t *testing.T) {
	binary, logPath := stubHerdr(t, `echo '{"id":"x","result":{"type":"workspace_created","workspace":{"workspace_id":"w7","label":"reviewer: review-diff"},"tab":{"tab_id":"w7:t1"},"root_pane":{"pane_id":"w7:p1"}}}'`)

	session, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout: LayoutWorkspace,
		CWD:    "/src/proj",
		Label:  "reviewer: review-diff",
		Env:    []string{"DISPATCH_TASK_ID=task_2"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.WorkspaceID != "w7" || session.PaneID != "w7:p1" {
		t.Errorf("session = %+v", session)
	}

	args := readLog(t, logPath)
	if !strings.Contains(args, "workspace create --cwd /src/proj") {
		t.Errorf("args = %s", args)
	}
	if !strings.Contains(args, "--env DISPATCH_TASK_ID=task_2") {
		t.Errorf("env not set on the workspace root pane: %s", args)
	}
	// No worktree to open, so no extra tab is needed.
	if strings.Contains(args, "tab create") {
		t.Errorf("a plain workspace should not need a second tab: %s", args)
	}
}

func TestCreateSessionTabLayoutUsesTabCreate(t *testing.T) {
	binary, logPath := stubHerdr(t, `echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w1:t3","workspace_id":"w1","label":"engineer: x"},"root_pane":{"pane_id":"w1:p3"}}}'`)

	session, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout:      LayoutTab,
		WorkspaceID: "w1",
		CWD:         "/wt/x",
		Label:       "engineer: x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Layout != LayoutTab || session.OwnsWorkspace() {
		t.Errorf("a tab lives in someone else's workspace: %+v", session)
	}
	args := readLog(t, logPath)
	if !strings.Contains(args, "tab create --workspace w1") {
		t.Errorf("args = %s", args)
	}
	if strings.Contains(args, "worktree open") || strings.Contains(args, "workspace create") {
		t.Errorf("tab layout must not create a workspace: %s", args)
	}
}

func TestCreateSessionTrustRepositoryIsPassedOnlyWhenAsked(t *testing.T) {
	binary, logPath := stubHerdr(t, `case "$*" in
  *"worktree open"*) echo '{"id":"x","result":{"type":"worktree_opened","workspace":{"workspace_id":"w8"},"tab":{"tab_id":"w8:t1"},"root_pane":{"pane_id":"w8:p1"},"worktree":{"path":"/wt/x","branch":"b"}}}' ;;
  *"tab create"*) echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w8:t2","workspace_id":"w8"},"root_pane":{"pane_id":"w8:p2"}}}' ;;
  *) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	if _, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout: LayoutWorkspace, CWD: "/wt/x", RepoRoot: "/src/proj",
		Worktree: true, TrustRepository: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, logPath), "--trust-repository") {
		t.Error("trust_repository was configured but not passed through")
	}
}

func TestCreateSessionClosesTheWorkspaceIfItsTabCannotBeMade(t *testing.T) {
	binary, logPath := stubHerdr(t, `case "$*" in
  *"worktree open"*) echo '{"id":"x","result":{"type":"worktree_opened","already_open":false,"workspace":{"workspace_id":"w9"},"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"},"worktree":{"path":"/wt/x","branch":"b"}}}' ;;
  *"tab create"*) echo '{"id":"x","error":{"code":"tab_create_failed","message":"no"}}' >&2; exit 1 ;;
  *) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	_, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout: LayoutWorkspace, CWD: "/wt/x", RepoRoot: "/src/proj", Worktree: true,
	})
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	// A half-built workspace in the sidebar is worse than none.
	if !strings.Contains(readLog(t, logPath), "workspace close w9") {
		t.Errorf("the workspace was not cleaned up:\n%s", readLog(t, logPath))
	}
}

func TestCreateSessionKeepsAWorkspaceThatWasAlreadyOpen(t *testing.T) {
	binary, logPath := stubHerdr(t, `case "$*" in
  *"worktree open"*) echo '{"id":"x","result":{"type":"worktree_opened","already_open":true,"workspace":{"workspace_id":"w9"},"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"},"worktree":{"path":"/wt/x","branch":"b"}}}' ;;
  *"tab create"*) echo '{"id":"x","error":{"code":"tab_create_failed","message":"no"}}' >&2; exit 1 ;;
  *) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	if _, err := NewCLI(binary, "").CreateSession(context.Background(), CreateSessionRequest{
		Layout: LayoutWorkspace, CWD: "/wt/x", RepoRoot: "/src/proj", Worktree: true,
	}); err == nil {
		t.Fatal("expected the failure to surface")
	}
	// It was open before dispatch asked, so it is not dispatch's to close.
	if strings.Contains(readLog(t, logPath), "workspace close") {
		t.Error("dispatch closed a workspace that already existed")
	}
}

func TestCloseSessionMatchesWhatWasCreated(t *testing.T) {
	binary, logPath := stubHerdr(t, `echo '{"id":"x","result":{"type":"ok"}}'`)
	client := NewCLI(binary, "")

	if err := client.CloseSession(context.Background(), Session{
		WorkspaceID: "w6", TabID: "w6:t2", PaneID: "w6:p2", Layout: LayoutWorkspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseSession(context.Background(), Session{
		WorkspaceID: "w1", TabID: "w1:t3", PaneID: "w1:p3", Layout: LayoutTab,
	}); err != nil {
		t.Fatal(err)
	}

	args := readLog(t, logPath)
	if !strings.Contains(args, "workspace close w6") {
		t.Errorf("a workspace dispatch owns should be closed whole: %s", args)
	}
	if !strings.Contains(args, "tab close w1:t3") {
		t.Errorf("a tab should close only its tab: %s", args)
	}
	if strings.Contains(args, "workspace close w1") {
		t.Errorf("dispatch must not close a workspace it does not own: %s", args)
	}
}
