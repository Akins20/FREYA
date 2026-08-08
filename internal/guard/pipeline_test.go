package guard

import "testing"

func TestPipelineRiskCalibration(t *testing.T) {
	readOnly := []string{
		`find . -name "*.go" | wc -l`,
		"ls -la | grep test",
		"cat README.md | head -20",
		"git log --oneline | head -5",
		"ps aux | grep freya",
		"df -h | sort -k5 -r",
		"du -sh * | sort -rh | head",
		"grep -r TODO . | wc -l",
	}
	for _, c := range readOnly {
		a := assess(Action{Kind: KindExec, Shell: c}, nil, "")
		if a.Risk > RiskLow {
			t.Errorf("OVER-BLOCKED %q -> %s (%s)", c, a.Risk, a.Rule)
		}
	}

	mustEscalate := []string{
		"ls && rm -rf ./build",
		"cat f > /etc/hosts",
		"find . -name '*.tmp' -delete",
		"sed -i 's/a/b/' file.txt",
		"echo $(whoami)",
		"find / -type f | xargs rm -f",
		"sudo ls",
	}
	for _, c := range mustEscalate {
		a := assess(Action{Kind: KindExec, Shell: c}, nil, "")
		if a.Risk < RiskMedium {
			t.Errorf("UNDER-BLOCKED %q -> %s", c, a.Risk)
		}
	}
}

func TestUserMediaIsNotSystemPath(t *testing.T) {
	p := "/run/media/akins/Akins Drive1/Development/JARVIS/main.go"
	a := assess(Action{Kind: KindWrite, Paths: []string{p}}, nil, "")
	for _, r := range a.Reasons {
		if r == p+" is inside the system directory /run" {
			t.Errorf("user media flagged as system path: %s", r)
		}
	}
	if a.Risk > RiskMedium {
		t.Errorf("writing to the user's own workspace got risk %s", a.Risk)
	}
	// But a genuine /run system path must still escalate.
	sys := assess(Action{Kind: KindWrite, Paths: []string{"/run/systemd/system/x.service"}}, nil, "")
	if sys.Risk < RiskHigh {
		t.Errorf("real /run system path got risk %s, want destructive", sys.Risk)
	}
}
