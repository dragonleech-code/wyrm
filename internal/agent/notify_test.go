package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestNotificationFormatting(t *testing.T) {
	n := Notification{
		State:       StateBlocked,
		PaneID:      "%1",
		SessionName: "myproj",
		WindowName:  "code",
	}

	if got := n.FormattedTitle(); got != "Wyrm Agent: myproj" {
		t.Errorf("FormattedTitle = %q, want %q", got, "Wyrm Agent: myproj")
	}

	msg := n.FormattedMessage()
	if !strings.Contains(msg, "needs confirmation or input") || !strings.Contains(msg, "code (%1)") {
		t.Errorf("unexpected FormattedMessage: %q", msg)
	}

	nIdle := Notification{
		State:       StateIdle,
		PaneID:      "%2",
		SessionName: "",
		WindowName:  "",
	}
	if got := nIdle.FormattedTitle(); got != "Wyrm Agent Alert" {
		t.Errorf("FormattedTitle = %q, want %q", got, "Wyrm Agent Alert")
	}
	if got := nIdle.FormattedMessage(); !strings.Contains(got, "finished its turn") {
		t.Errorf("unexpected FormattedMessage: %q", got)
	}
}

func TestSanitizeTerminalString(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"normal text", "normal text"},
		{"hello\x1b]0;hacked\x07world", "hello]0;hackedworld"},
		{"line\nfeed\rand\ttab", "linefeedandtab"},
		{"delete\x7fchar", "deletechar"},
		{"bell\a alert", "bell alert"},
		{"\x00\x01\x02\x1fclean", "clean"},
	}

	for _, tc := range cases {
		got := SanitizeTerminalString(tc.input)
		if got != tc.expected {
			t.Errorf("SanitizeTerminalString(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestDispatchBellAndOSC(t *testing.T) {
	var buf bytes.Buffer
	n := Notification{
		State:       StateBlocked,
		PaneID:      "%1",
		SessionName: "test-sess",
		WindowName:  "w0",
	}

	cfg := NotifyConfig{
		Enabled:   true,
		Bell:      true,
		OSC:       true,
		OnBlocked: true,
	}

	if err := Dispatch(n, cfg, &buf); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "\a") {
		t.Errorf("expected bell in output: %q", out)
	}
	if !strings.Contains(out, "\x1b]777;notify;Wyrm Agent: test-sess;") {
		t.Errorf("expected OSC 777 in output: %q", out)
	}
	if !strings.Contains(out, "\x1b]9;") {
		t.Errorf("expected OSC 9 in output: %q", out)
	}
}

func TestDispatchOSCSanitization(t *testing.T) {
	var buf bytes.Buffer
	n := Notification{
		Title:   "Malicious\x1b]0;Title\x07",
		Message: "Payload\x1b\\Escape\r\nTest",
		State:   StateBlocked,
	}
	cfg := NotifyConfig{
		Enabled:   true,
		OSC:       true,
		OnBlocked: true,
	}

	if err := Dispatch(n, cfg, &buf); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	out := buf.String()
	// Should not contain raw internal escape or carriage return / line feed
	if strings.Contains(out, "\r") || strings.Contains(out, "\n") || strings.Contains(out, "\x07") {
		t.Errorf("OSC output contains unsanitized control characters: %q", out)
	}
	if !strings.Contains(out, "Malicious]0;Title") {
		t.Errorf("Sanitized title missing in OSC output: %q", out)
	}
	if !strings.Contains(out, "Payload\\EscapeTest") {
		t.Errorf("Sanitized message missing in OSC output: %q", out)
	}
}

func TestDispatchDisabled(t *testing.T) {
	var buf bytes.Buffer
	n := Notification{State: StateBlocked}
	cfg := NotifyConfig{Enabled: false, Bell: true}
	if err := Dispatch(n, cfg, &buf); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if buf.Len() > 0 {
		t.Errorf("expected no output when disabled, got %q", buf.String())
	}
}

func TestDispatchFiltering(t *testing.T) {
	var buf bytes.Buffer
	nBlocked := Notification{State: StateBlocked}
	cfgNoBlocked := NotifyConfig{Enabled: true, Bell: true, OnBlocked: false}
	_ = Dispatch(nBlocked, cfgNoBlocked, &buf)
	if buf.Len() > 0 {
		t.Errorf("expected no output when OnBlocked is false, got %q", buf.String())
	}

	nIdle := Notification{State: StateIdle}
	cfgNoIdle := NotifyConfig{Enabled: true, Bell: true, OnIdle: false}
	_ = Dispatch(nIdle, cfgNoIdle, &buf)
	if buf.Len() > 0 {
		t.Errorf("expected no output when OnIdle is false, got %q", buf.String())
	}
}

func TestDispatchDesktop(t *testing.T) {
	var desktopTitle, desktopMsg string
	oldSend := SendDesktopNotification
	defer func() { SendDesktopNotification = oldSend }()
	SendDesktopNotification = func(title, msg string) {
		desktopTitle = title
		desktopMsg = msg
	}

	n := Notification{
		State:       StateBlocked,
		PaneID:      "%5",
		SessionName: "proj",
		WindowName:  "dev",
	}
	cfg := NotifyConfig{
		Enabled:   true,
		Desktop:   true,
		OnBlocked: true,
	}

	if err := Dispatch(n, cfg, nil); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	if desktopTitle != "Wyrm Agent: proj" {
		t.Errorf("desktopTitle = %q, want %q", desktopTitle, "Wyrm Agent: proj")
	}
	if !strings.Contains(desktopMsg, "dev (%5)") {
		t.Errorf("desktopMsg = %q, want mentioning dev (%%5)", desktopMsg)
	}
}

func TestBuildWindowsToastCommand_SafeEnvironmentVariables(t *testing.T) {
	maliciousTitle := `test'); Start-Process calc.exe; ('`
	maliciousMsg := `msg"; Invoke-Item C:\; "`

	cmd := BuildWindowsToastCommand(maliciousTitle, maliciousMsg)

	// Command line arguments must be static and not contain user input directly
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "calc.exe") {
			t.Errorf("PowerShell args contained unescaped payload: %s", arg)
		}
		if strings.Contains(arg, maliciousTitle) {
			t.Errorf("PowerShell args contained raw title: %s", arg)
		}
	}

	// Environment variables must safely carry the values
	var foundTitle, foundMsg bool
	for _, env := range cmd.Env {
		if env == "WYRM_NOTIFY_TITLE="+maliciousTitle {
			foundTitle = true
		}
		if env == "WYRM_NOTIFY_MSG="+maliciousMsg {
			foundMsg = true
		}
	}

	if !foundTitle {
		t.Errorf("WYRM_NOTIFY_TITLE not properly set in environment")
	}
	if !foundMsg {
		t.Errorf("WYRM_NOTIFY_MSG not properly set in environment")
	}
}

func TestBuildWindowsToastCommand_SpecialCharacters(t *testing.T) {
	specialCases := []struct {
		name  string
		title string
		msg   string
	}{
		{"single quotes", "It's a test", "Don't break 'PowerShell'"},
		{"double quotes", `He said "hello"`, `Nested "quotes"`},
		{"semicolons and pipes", "title; echo pwned | cat", "msg & echo hi"},
		{"variable expansion", "Value is $HOME and $env:SECRET", "Payload `$((Get-Process).Count)"},
		{"backticks", "Use `code` blocks", "tick ` ` `"},
	}

	for _, tc := range specialCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := BuildWindowsToastCommand(tc.title, tc.msg)
			var foundTitle, foundMsg bool
			for _, env := range cmd.Env {
				if env == "WYRM_NOTIFY_TITLE="+tc.title {
					foundTitle = true
				}
				if env == "WYRM_NOTIFY_MSG="+tc.msg {
					foundMsg = true
				}
			}
			if !foundTitle || !foundMsg {
				t.Errorf("special characters not preserved in environment for case %s", tc.name)
			}
		})
	}
}

func TestBuildCustomNotifyCommand_SafeEnvironmentVariables(t *testing.T) {
	n := Notification{
		State:       StateBlocked,
		PaneID:      "%1",
		SessionName: "test-sess",
		WindowName:  "w0",
	}
	maliciousTitle := `title; rm -rf /; echo `
	maliciousMsg := `msg && touch /tmp/pwned`

	cmd := BuildCustomNotifyCommand("echo $WYRM_NOTIFY_TITLE: $WYRM_NOTIFY_MESSAGE", n, maliciousTitle, maliciousMsg)

	// Command argument should match the configured command verbatim without string injection
	if len(cmd.Args) < 3 || cmd.Args[2] != "echo $WYRM_NOTIFY_TITLE: $WYRM_NOTIFY_MESSAGE" {
		t.Errorf("Command string was unexpectedly altered: %v", cmd.Args)
	}

	var foundTitle, foundMsg, foundState, foundSession, foundPane bool
	for _, env := range cmd.Env {
		if env == "WYRM_NOTIFY_TITLE="+maliciousTitle {
			foundTitle = true
		}
		if env == "WYRM_NOTIFY_MESSAGE="+maliciousMsg {
			foundMsg = true
		}
		if env == "WYRM_NOTIFY_STATE="+n.State.String() {
			foundState = true
		}
		if env == "WYRM_NOTIFY_SESSION="+n.SessionName {
			foundSession = true
		}
		if env == "WYRM_NOTIFY_PANE="+n.PaneID {
			foundPane = true
		}
	}

	if !foundTitle || !foundMsg || !foundState || !foundSession || !foundPane {
		t.Errorf("Missing expected environment variables in cmd.Env: %v", cmd.Env)
	}
}

func TestSanitizeTerminalStringStripsC1(t *testing.T) {
	in := "ok\u009b31m\u009dtitle\x1b[0m\xffend"
	if got := SanitizeTerminalString(in); got != "ok31mtitle[0mend" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeTerminalString("héllo ⏸"); got != "héllo ⏸" {
		t.Errorf("printable non-ASCII altered: %q", got)
	}
}

func TestBuildMacNotifyCommandPassesTextAsArguments(t *testing.T) {
	cmd := BuildMacNotifyCommand(`a "quoted" title`, "-e do shell script \"id\"")
	args := cmd.Args
	if len(args) != 5 || args[1] != "-e" || !strings.Contains(args[2], "item 1 of argv") {
		t.Fatalf("args = %q", args)
	}
	if args[3] != ` -e do shell script "id"` || args[4] != `a "quoted" title` {
		t.Errorf("message/title not passed verbatim as data: %q", args[3:])
	}
}
