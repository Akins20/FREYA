package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// forbiddenPattern is a command shape that is refused regardless of approval.
type forbiddenPattern struct {
	Name    string
	Detail  string
	Matches func(cmdline string, action Action) bool
}

// Anchors used by several patterns.
var (
	// rmRootPattern catches recursive deletion of the filesystem root, allowing
	// for flag order and the /* form.
	rmRootPattern = regexp.MustCompile(`\brm\b[^|;&]*\s-[a-zA-Z]*[rR][a-zA-Z]*\s[^|;&]*\s?/(\s|\*|$)`)
	// ddToDevice catches writes onto a raw block device.
	ddToDevice = regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/(sd[a-z]|nvme\d|mmcblk\d|vd[a-z]|hd[a-z])`)
	// mkfsPattern catches any filesystem creation.
	mkfsPattern = regexp.MustCompile(`\b(mkfs(\.\w+)?|mke2fs|mkswap)\b`)
	// diskWipe catches partition-table and low-level wipes.
	diskWipe = regexp.MustCompile(`\b(wipefs|sgdisk|fdisk|parted|blkdiscard|shred)\b[^|;&]*/dev/`)
	// forkBomb catches the classic shell fork bomb and near variants.
	forkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{.*\|.*&.*\}\s*;?\s*:`)
	// pipeToShell catches downloading code and executing it unseen.
	pipeToShell = regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|k|da)?sh\b`)
	// recursivePermsOnRoot catches chmod/chown -R against system roots.
	// Longer paths lead the alternation so /etc is not shadowed by a bare /.
	// The R is deliberately uppercase-only: that is the recursive flag.
	recursivePermsOnRoot = regexp.MustCompile(
		`\b(chmod|chown|chgrp)\b[^|;&]*\s-{1,2}[a-zA-Z-]*R[a-zA-Z]*\s[^|;&]*?\s?` +
			`(/etc|/usr|/bin|/sbin|/boot|/lib\S*|/var|/opt|/home|/)(\s|$)`)
	// overwriteDevice catches shell redirection onto a device node.
	overwriteDevice = regexp.MustCompile(`>\s*/dev/(sd[a-z]|nvme\d|mmcblk\d)`)
	// historyWipe catches attempts to erase the audit trail.
	auditTamper = regexp.MustCompile(`\b(rm|truncate|shred|>)\b[^|;&]*(audit\.jsonl|\.bash_history|/var/log)`)
)

// forbidden lists the commands no confirmation can authorise.
//
// Each entry answers: is there any circumstance where a personal assistant
// should do this on a single-disk laptop with no verified backups? If the
// answer is no, it belongs here rather than behind a prompt.
var forbidden = []forbiddenPattern{
	{
		Name:   "rm-root",
		Detail: "recursive deletion of the filesystem root",
		Matches: func(c string, _ Action) bool {
			return rmRootPattern.MatchString(c) || strings.Contains(c, "rm -rf /*") ||
				strings.Contains(c, "rm -rf --no-preserve-root")
		},
	},
	{
		Name:    "dd-to-disk",
		Detail:  "writing directly onto a block device, which destroys the partition",
		Matches: func(c string, _ Action) bool { return ddToDevice.MatchString(c) },
	},
	{
		Name:    "mkfs",
		Detail:  "creating a filesystem, which erases everything on the target",
		Matches: func(c string, _ Action) bool { return mkfsPattern.MatchString(c) },
	},
	{
		Name:    "disk-wipe",
		Detail:  "low-level disk or partition-table modification",
		Matches: func(c string, _ Action) bool { return diskWipe.MatchString(c) },
	},
	{
		Name:    "fork-bomb",
		Detail:  "a fork bomb, which will hang the machine",
		Matches: func(c string, _ Action) bool { return forkBomb.MatchString(c) },
	},
	{
		Name:    "pipe-to-shell",
		Detail:  "downloading and executing code without inspecting it",
		Matches: func(c string, _ Action) bool { return pipeToShell.MatchString(c) },
	},
	{
		Name:    "recursive-perms-on-system",
		Detail:  "recursively changing permissions on system directories",
		Matches: func(c string, _ Action) bool { return recursivePermsOnRoot.MatchString(c) },
	},
	{
		Name:    "overwrite-device",
		Detail:  "redirecting output onto a raw device node",
		Matches: func(c string, _ Action) bool { return overwriteDevice.MatchString(c) },
	},
	{
		Name:   "catastrophic-target",
		Detail: "operates on the system root or the entire home directory",
		Matches: func(c string, _ Action) bool {
			_, _, found := detectCatastrophic(c)
			return found
		},
	},
	{
		Name:    "audit-tamper",
		Detail:  "erasing logs or the audit trail",
		Matches: func(c string, _ Action) bool { return auditTamper.MatchString(c) },
	},
}

// systemPaths must never be deleted or moved and are high risk to write.
var systemPaths = []string{
	"/", "/boot", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64",
	"/var", "/opt", "/proc", "/sys", "/dev", "/run", "/srv",
}

// secretPathFragments identify files whose loss or exposure is serious.
var secretPathFragments = []string{
	"/.ssh", "/.gnupg", "/.password-store", "/.aws", "/.kube",
	"/.config/google-chrome", "/.mozilla", "/.local/share/keyrings",
	"voiceprint.json", ".env", "id_rsa", "id_ed25519", ".pem",
}

// destructiveCommands mutate or remove data.
var destructiveCommands = map[string]string{
	"rm":        "deletes files",
	"rmdir":     "removes directories",
	"shred":     "irrecoverably overwrites files",
	"truncate":  "empties files",
	"dd":        "writes raw data, easily to the wrong target",
	"mv":        "moves files, overwriting the destination",
	"chown":     "changes ownership",
	"chmod":     "changes permissions",
	"kill":      "terminates processes",
	"pkill":     "terminates processes by name",
	"killall":   "terminates processes by name",
	"systemctl": "changes system services",
	"apt":       "installs or removes packages",
	"apt-get":   "installs or removes packages",
	"dpkg":      "installs or removes packages",
	"snap":      "installs or removes packages",
	"pip":       "installs or removes packages",
	"npm":       "installs or removes packages",
	"docker":    "manages containers and images",
	"crontab":   "changes scheduled jobs",
	"usermod":   "changes user accounts",
	"userdel":   "deletes user accounts",
	"passwd":    "changes passwords",
	"mount":     "changes mounted filesystems",
	"umount":    "unmounts filesystems",
	"swapoff":   "disables swap",
	"iptables":  "changes firewall rules",
	"ufw":       "changes firewall rules",
}

// riskyGitSubcommands throw away uncommitted work.
var riskyGitSubcommands = map[string]string{
	"reset":         "discards commits or staged changes",
	"clean":         "deletes untracked files",
	"checkout":      "can overwrite local modifications",
	"restore":       "can overwrite local modifications",
	"push":          "publishes to a remote",
	"rebase":        "rewrites history",
	"filter-branch": "rewrites history",
}

// shellMetacharacters allow one command to smuggle in another.
var shellMetacharacters = []string{"&&", "||", ";", "|", "`", "$(", ">", ">>", "\n"}

// assess is the core evaluation. It is deliberately conservative: when a signal
// is ambiguous it raises the risk rather than lowering it.
func assess(action Action, extraProtected []string) Assessment {
	a := Assessment{Risk: RiskNone, Reversible: true}

	// Matching runs on the raw command line, never a lowercased copy. Flag case
	// is load-bearing: `chmod -r` clears the read bit while `chmod -R` recurses
	// through every file beneath the target. Folding case once let
	// `chmod -R 777 /` through as merely "medium" risk.
	cmdline := commandLine(action)

	// Forbidden patterns win over everything, including explicit approval.
	for _, f := range forbidden {
		if f.Matches(cmdline, action) {
			return Assessment{
				Risk:       RiskForbidden,
				Rule:       f.Name,
				Reasons:    []string{f.Detail},
				Reversible: false,
				Preview:    "refused before execution",
			}
		}
	}

	// A read-only action cannot destroy anything, so heuristics that exist to
	// warn about *what will be changed* do not apply to it.
	readOnly := false
	if action.Shell != "" {
		readOnly = pipelineIsReadOnly(action.Shell)
	} else if action.Kind == KindExec && action.Command != "" {
		readOnly = isReadOnlySegment(commandLine(action))
	}

	raise := func(r Risk, rule, reason string) {
		if r > a.Risk {
			a.Risk = r
			a.Rule = rule
		}
		a.Reasons = append(a.Reasons, reason)
	}

	// Running as root turns ordinary mistakes into system-wide ones.
	if action.Elevated {
		raise(RiskHigh, "elevated", "runs with root privileges")
	}

	// A raw shell string can chain commands past any single-command check.
	if action.Shell != "" {
		// A pipeline of purely observational commands is not made dangerous by
		// the pipe itself. Assess each segment; only escalate if one of them
		// can actually change something.
		if pipelineIsReadOnly(action.Shell) {
			a.Preview = "read-only: " + action.Shell
		} else {
			for _, meta := range shellMetacharacters {
				if strings.Contains(action.Shell, meta) {
					raise(RiskHigh, "shell-chaining",
						fmt.Sprintf("shell string contains %q and at least one segment can modify state", meta))
					break
				}
			}
			if a.Risk < RiskMedium {
				raise(RiskMedium, "shell", "runs through a shell rather than direct execution")
			}
		}
	}

	switch action.Kind {
	case KindDelete:
		raise(RiskHigh, "delete", "removes files")
		a.Reversible = false

	case KindMove:
		raise(RiskMedium, "move", "relocates files, possibly overwriting the destination")

	case KindWrite:
		raise(RiskMedium, "write", "creates or modifies files")

	case KindInput:
		// Synthetic input is unbounded: it can type anything into any focused
		// window, including a terminal or a password field.
		raise(RiskMedium, "synthetic-input",
			"sends keystrokes or clicks to whatever window has focus")

	case KindSystem:
		raise(RiskMedium, "system", "changes system state")

	case KindBrowser:
		raise(RiskLow, "browser", "drives the browser")

	case KindExec:
		base := filepath.Base(action.Command)
		if why, ok := destructiveCommands[base]; ok {
			r := RiskHigh
			if base == "mv" || base == "chmod" || base == "chown" {
				r = RiskMedium
			}
			raise(r, "command:"+base, base+" "+why)
			if base == "rm" || base == "shred" || base == "dd" || base == "truncate" {
				a.Reversible = false
			}
		}
		if base == "git" && len(action.Args) > 0 {
			if why, ok := riskyGitSubcommands[action.Args[0]]; ok {
				raise(RiskMedium, "git:"+action.Args[0], "git "+action.Args[0]+" "+why)
				if action.Args[0] == "clean" || action.Args[0] == "reset" {
					a.Reversible = false
				}
			}
		}
		if base == "sudo" || base == "pkexec" || base == "doas" || base == "su" {
			raise(RiskHigh, "privilege-escalation", "escalates to root")
		}
	}

	// Path analysis applies to every kind: what is touched matters more than
	// which verb is used.
	protected := append(append([]string{}, systemPaths...), extraProtected...)
	for _, p := range action.Paths {
		abs := absPath(p)

		for _, sp := range protected {
			if abs == sp {
				if action.Kind == KindDelete || action.Kind == KindMove {
					return Assessment{
						Risk:       RiskForbidden,
						Rule:       "system-path",
						Reasons:    []string{fmt.Sprintf("%s is a system directory and must not be removed", abs)},
						Reversible: false,
						Preview:    "refused before execution",
					}
				}
				raise(RiskHigh, "system-path", abs+" is a system directory")
			} else if sp != "/" && strings.HasPrefix(abs, sp+string(os.PathSeparator)) {
				// Removable media mounts live under /run but hold user data,
				// not system files. Treating them as system paths made every
				// operation in the user's own workspace look destructive.
				if isUserMedia(abs) {
					continue
				}
				raise(RiskHigh, "system-path", abs+" is inside the system directory "+sp)
			}
		}

		for _, frag := range secretPathFragments {
			if strings.Contains(abs, frag) {
				raise(RiskHigh, "secret-path",
					fmt.Sprintf("%s holds credentials or keys", abs))
				a.Reversible = false
				break
			}
		}

		// A home directory or whole mounted volume is not a normal target.
		if home, err := os.UserHomeDir(); err == nil && abs == home {
			raise(RiskHigh, "home-root", "targets the entire home directory")
			a.Reversible = false
		}
		if strings.HasPrefix(abs, "/run/media/") && strings.Count(abs, "/") <= 4 {
			raise(RiskHigh, "mount-root", "targets an entire mounted volume")
			a.Reversible = false
		}

		// Wildcards mean the destroyed set is unknown until expansion — a
		// concern only when something is actually being changed. In a
		// read-only pipeline a wildcard is just a search pattern.
		if !readOnly && strings.ContainsAny(p, "*?[") {
			raise(RiskMedium, "glob", "uses a wildcard, so the exact targets are resolved at run time")
		}
	}

	if a.Risk >= RiskMedium {
		a.Preview = describeEffect(action)
	}
	return a
}

// commandLine renders an action as a single string for pattern matching.
func commandLine(action Action) string {
	if action.Shell != "" {
		return action.Shell
	}
	parts := append([]string{action.Command}, action.Args...)
	return strings.Join(parts, " ")
}

func absPath(p string) string {
	if abs, err := filepath.Abs(os.ExpandEnv(p)); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// --- canonical-form analysis -------------------------------------------------
//
// Regexes alone cannot cover the ways a dangerous command can be spelled.
// `rm -rf /`, `rm --recursive --force "/"`, `rm -rf ~` and `rm -rf $HOME` are
// the same instruction wearing different clothes, and all four were reaching
// merely "medium" risk. Canonicalising first, then reasoning over tokens,
// closes that whole family rather than one spelling at a time.

var tildePattern = regexp.MustCompile(`(^|\s)~(/|\s|$)`)

// normalizeCommand rewrites a command line into a canonical form for analysis.
// Quotes are stripped and shell variables expanded because they change how a
// command *looks* without changing what it *does*.
func normalizeCommand(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(s)

	if home, err := os.UserHomeDir(); err == nil {
		s = strings.ReplaceAll(s, "${HOME}", home)
		s = strings.ReplaceAll(s, "$HOME", home)
		s = tildePattern.ReplaceAllString(s, "$1"+home+"$2")
	}
	return strings.Join(strings.Fields(s), " ")
}

// deleteVerbs remove file content outright.
var deleteVerbs = map[string]bool{
	"rm": true, "shred": true, "unlink": true, "rmdir": true,
}

// catastrophicPaths are targets whose recursive removal ends the system or the
// user's entire data set.
func catastrophicPaths() []string {
	out := append([]string{}, systemPaths...)
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Clean(home))
	}
	return out
}

// isCatastrophicTarget reports whether a path argument names one of them,
// accounting for the `/*` form that expands to a directory's whole contents.
func isCatastrophicTarget(arg string) bool {
	p := arg
	if strings.HasSuffix(p, "/*") {
		p = strings.TrimSuffix(p, "/*")
		if p == "" {
			p = "/"
		}
	}
	if p == "*" {
		// A bare wildcard depends on the working directory, which we cannot
		// see from here. Treat it as unknown-but-serious.
		return false
	}
	p = filepath.Clean(p)
	for _, c := range catastrophicPaths() {
		if p == c {
			return true
		}
	}
	return false
}

// skipCommandPrefixes advances past env assignments and privilege wrappers to
// reach the real verb, so `sudo FOO=bar rm -rf /` is analysed as `rm`.
func skipCommandPrefixes(fields []string) int {
	i := 0
	for i < len(fields) {
		f := fields[i]
		switch {
		case strings.Contains(f, "=") && !strings.HasPrefix(f, "-"):
			i++
		case f == "sudo" || f == "doas" || f == "pkexec" || f == "env" || f == "command" || f == "nohup":
			i++
		default:
			return i
		}
	}
	return i
}

// detectCatastrophic inspects a canonicalised command for operations that
// destroy the system or the whole home directory.
func detectCatastrophic(raw string) (rule, detail string, found bool) {
	cmd := normalizeCommand(raw)
	fields := strings.Fields(cmd)
	i := skipCommandPrefixes(fields)
	if i >= len(fields) {
		return "", "", false
	}

	base := filepath.Base(fields[i])
	args := fields[i+1:]

	positional := func(list []string) []string {
		var out []string
		for _, a := range list {
			if strings.HasPrefix(a, "-") {
				continue
			}
			out = append(out, a)
		}
		return out
	}

	switch {
	case deleteVerbs[base]:
		for _, a := range positional(args) {
			if isCatastrophicTarget(a) {
				return "catastrophic-delete",
					fmt.Sprintf("%s would delete %s, which is the system root or your entire home directory", base, a), true
			}
		}

	case base == "mv":
		// Everything except the final argument is a source.
		p := positional(args)
		if len(p) > 1 {
			for _, a := range p[:len(p)-1] {
				if isCatastrophicTarget(a) {
					return "catastrophic-move",
						fmt.Sprintf("moving %s would relocate the system root or your entire home directory", a), true
				}
			}
		}

	case base == "find":
		destructive := false
		for _, a := range args {
			if a == "-delete" || a == "-exec" || a == "-execdir" {
				destructive = true
				break
			}
		}
		if destructive {
			for _, a := range positional(args) {
				if isCatastrophicTarget(a) {
					return "find-delete",
						fmt.Sprintf("find would recursively delete from %s", a), true
				}
				break // only the first positional is the search root
			}
		}
	}
	return "", "", false
}

// --- pipeline segment analysis ----------------------------------------------
//
// A guard that flags `find . -name "*.go" | wc -l` as destructive gets turned
// off within a day, and a disabled guard protects nothing. The presence of a
// pipe says a command *could* chain something dangerous, not that it does.
// So each segment is assessed on its own and the pipeline takes the worst.

// readOnlyCommands observe the system without changing it. Membership here is
// deliberately conservative: anything that can write, fetch or execute
// arbitrary code is excluded, even when it is usually harmless.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "column": true,
	"echo": true, "printf": true, "pwd": true, "whoami": true, "id": true,
	"date": true, "uptime": true, "free": true, "df": true, "du": true,
	"ps": true, "which": true, "type": true, "file": true, "stat": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"env": true, "printenv": true, "hostname": true, "uname": true,
	"lsblk": true, "lscpu": true, "lsusb": true, "lspci": true, "lsof": true,
	"diff": true, "cmp": true, "md5sum": true, "sha256sum": true,
	"jq": true, "yq": true, "nl": true, "rev": true, "seq": true, "true": true,
	"nproc": true, "groups": true, "locale": true, "tty": true,
}

// conditionallyReadOnly commands only observe when used without certain flags.
var conditionallyReadOnly = map[string][]string{
	// find writes only when told to act on what it finds
	"find": {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fls", "-fprint"},
	// sed edits in place only with -i
	"sed": {"-i", "--in-place"},
	// perl and awk can open files for writing, but only with explicit scripts
	"awk":  {">", ">>"},
	"tar":  {"-x", "--extract", "-c", "--create", "-f"},
	"sort": {"-o", "--output"},
}

// readSubcommands are the safe verbs of otherwise-capable tools.
var readSubcommands = map[string]map[string]bool{
	"git": {
		"status": true, "log": true, "diff": true, "show": true, "branch": true,
		"remote": true, "describe": true, "blame": true, "shortlog": true,
		"ls-files": true, "rev-parse": true, "config": true, "tag": true,
	},
	"go":     {"version": true, "env": true, "list": true, "vet": true, "doc": true},
	"docker": {"ps": true, "images": true, "logs": true, "inspect": true, "version": true},
	"systemctl": {"status": true, "list-units": true, "is-active": true,
		"is-enabled": true, "show": true, "cat": true},
	"apt":     {"list": true, "search": true, "show": true, "policy": true},
	"apt-get": {},
	"npm":     {"ls": true, "list": true, "view": true, "outdated": true},
	"pip":     {"list": true, "show": true, "freeze": true},
}

// segmentSeparators split a shell string into independently-run commands.
var segmentSeparators = regexp.MustCompile(`\|\||&&|;|\||\n`)

// isReadOnlySegment reports whether one pipeline segment only observes state.
func isReadOnlySegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return true
	}
	// Redirection writes to a file, whatever the command is.
	if strings.Contains(seg, ">") {
		return false
	}
	// Command substitution hides an arbitrary command inside.
	if strings.Contains(seg, "$(") || strings.Contains(seg, "`") {
		return false
	}

	fields := strings.Fields(seg)
	i := skipCommandPrefixes(fields)
	if i >= len(fields) {
		return true
	}
	// Anything running as root is not "merely reading" for our purposes.
	for _, f := range fields[:i] {
		if f == "sudo" || f == "doas" || f == "pkexec" {
			return false
		}
	}

	base := filepath.Base(fields[i])
	args := fields[i+1:]

	if banned, ok := conditionallyReadOnly[base]; ok {
		for _, a := range args {
			for _, b := range banned {
				if a == b || strings.HasPrefix(a, b+"=") {
					return false
				}
			}
		}
		return true
	}

	if subs, ok := readSubcommands[base]; ok {
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			return subs[a]
		}
		return false // no subcommand given: cannot assume it is safe
	}

	// xargs inherits whatever it is asked to run.
	if base == "xargs" {
		return len(args) > 0 && isReadOnlySegment(strings.Join(args, " "))
	}

	return readOnlyCommands[base]
}

// pipelineIsReadOnly reports whether every segment only observes state.
func pipelineIsReadOnly(shell string) bool {
	for _, seg := range segmentSeparators.Split(shell, -1) {
		if !isReadOnlySegment(seg) {
			return false
		}
	}
	return true
}

// removableMediaPrefix is where udisks mounts user volumes. Paths beneath it
// are user data, not system files, even though they sit under /run — and on
// this machine the user's entire workspace lives there.
const removableMediaPrefix = "/run/media/"

// isUserMedia reports whether a path is on user-mounted removable storage.
func isUserMedia(p string) bool { return strings.HasPrefix(p, removableMediaPrefix) }
