package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
)

// Finding out whether the code she just wrote is code.
//
// # The gap
//
// She had no way to check her own output. Everything needed was present — she
// can run any command — but nothing told her that writing a file is only half of
// writing a file, and nothing made the check cheap enough to be automatic. So a
// Go file with a missing brace, a JSON config with a trailing comma, or a page
// with an unclosed tag was written, reported as done, and discovered later by
// the person who tried to use it.
//
// This is the same shape as the embedded-JavaScript problem found in
// internal/browser today: seven blocks of script that Go compiled happily and
// nothing ever parsed. The fix there was a test running node --check. The fix
// here is to give her the same move for anything she writes.
//
// # Why a dedicated tool rather than telling her to run the compiler
//
// Because the instruction is the part that does not work. "Verify your output"
// has been in the playbooks for weeks; what was missing was a single call that
// knows which checker belongs to which extension, so the decision is "check it"
// rather than "remember whether this project uses tsc or node, and what the
// invocation is, and whether it is installed".
//
// It reports the checker it used, so a pass means something specific rather than
// a blanket assurance — and when nothing is installed for that language it says
// so plainly instead of returning a green tick it has not earned.

// checkers maps an extension to the command that will parse it, cheapest first.
// Each entry is a syntax check, never a full build: the question is whether the
// file is well-formed, and waiting on a link step to find a missing brace is a
// worse trade than it sounds.
var checkers = map[string][][]string{
	".go":   {{"gofmt", "-e", "-l"}},
	".js":   {{"node", "--check"}},
	".mjs":  {{"node", "--check"}},
	".cjs":  {{"node", "--check"}},
	".json": {{"python3", "-c", "import json,sys; json.load(open(sys.argv[1]))"}},
	".py":   {{"python3", "-m", "py_compile"}},
	".sh":   {{"bash", "-n"}},
	".bash": {{"bash", "-n"}},
	".yaml": {{"python3", "-c", "import yaml,sys; yaml.safe_load(open(sys.argv[1]))"}},
	".yml":  {{"python3", "-c", "import yaml,sys; yaml.safe_load(open(sys.argv[1]))"}},
	".css":  {{"node", "-e", "require('fs').readFileSync(process.argv[1],'utf8')"}},
	".ts":   {{"tsc", "--noEmit", "--allowJs"}, {"node", "--check"}},
	".html": {{"python3", "-c", "import html.parser,sys\n" +
		"class P(html.parser.HTMLParser):\n" +
		" def __init__(s):\n" +
		"  super().__init__(); s.stack=[]\n" +
		" def handle_starttag(s,t,a):\n" +
		"  if t not in ('br','img','hr','meta','link','input','source','col'): s.stack.append(t)\n" +
		" def handle_endtag(s,t):\n" +
		"  if s.stack and s.stack[-1]==t: s.stack.pop()\n" +
		"  elif t in s.stack: s.stack.remove(t)\n" +
		"p=P(); p.feed(open(sys.argv[1]).read())\n" +
		"print('unclosed: '+', '.join(p.stack)) if p.stack else None\n" +
		"sys.exit(1 if p.stack else 0)"}},
}

// RegisterSyntax adds the tool that checks a file parses.
func RegisterSyntax(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "code_check",
			Description: "Check that a file you wrote actually parses.\n\n" +
				"Run it after writing or editing any code, config or markup — Go, " +
				"JavaScript, TypeScript, Python, JSON, YAML, shell, HTML, CSS. It picks " +
				"the right checker from the extension and reports what it used, so a " +
				"pass means something specific.\n\n" +
				"Writing a file and reporting it done is half the job: a missing brace, " +
				"a trailing comma or an unclosed tag costs nothing to find now and is " +
				"found by the user later otherwise. This is fast and worth doing every " +
				"time.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "The file to check."},
			}, "path"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := expandIn(ctx, argString(args, "path"))
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			ext := strings.ToLower(filepath.Ext(path))
			cmds, known := checkers[ext]
			if !known {
				return fmt.Sprintf("No syntax checker for %s files, so this is unverified — "+
					"say so rather than reporting it as checked.", ext), nil
			}

			var tried []string
			for _, cmd := range cmds {
				if !have(cmd[0]) {
					tried = append(tried, cmd[0]+" (not installed)")
					continue
				}
				out, err := run(ctx, 30*time.Second, cmd[0], append(cmd[1:], path)...)
				if err == nil {
					// gofmt -l prints the filename when the file is misformatted and
					// says nothing when it is fine; an empty result is the pass.
					if cmd[0] == "gofmt" && strings.TrimSpace(out) != "" {
						return "", fmt.Errorf("%s does not parse cleanly (gofmt):\n%s", path, out)
					}
					return fmt.Sprintf("%s parses cleanly (checked with %s).",
						filepath.Base(path), cmd[0]), nil
				}
				// A real syntax error, reported with the checker's own words — they
				// name the line, which is the whole value.
				return "", fmt.Errorf("%s does not parse (%s):\n%s",
					filepath.Base(path), cmd[0], clip(out+" "+err.Error(), 800))
			}
			return fmt.Sprintf("Could not check %s — none of the checkers for %s is "+
				"installed here (%s). It is unverified; do not report it as checked.",
				filepath.Base(path), ext, strings.Join(tried, ", ")), nil
		},
	})
}
