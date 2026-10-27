package help

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Scan actual application reads (including the injected getenv seam), rather
// than maintaining a second hand-written list of runtime variable names.
func environmentReads(t *testing.T, name string, src any) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		switch strings.ToLower(name) {
		case "getenv", "lookupenv":
			if len(call.Args) != 1 {
				t.Fatalf("%s: environment read must have one literal name", name)
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("%s: dynamic environment read needs explicit reference coverage", name)
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			found[value] = true
		case "userhomedir":
			found["HOME"] = true
		case "environ", "expandenv", "userconfigdir", "usercachedir", "tempdir":
			t.Fatalf("%s: bulk/implicit environment reader needs explicit reference coverage", name)
		}
		return true
	})
	return found
}

func TestEnvironmentReferenceCoversApplicationReads(t *testing.T) {
	documented := map[string]bool{}
	runtimeSection, _, _ := strings.Cut(EnvironmentReference, "### Testing against an isolated service")
	for _, match := range regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]*)` ").FindAllStringSubmatch(runtimeSection, -1) {
		documented[match[1]] = true
	}
	reads := map[string]bool{}
	for _, dir := range []string{"../../cmd", "../../internal", "../../pkg"} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for name := range environmentReads(t, path, nil) {
				reads[name] = true
				if !documented[name] {
					t.Errorf("%s reads undocumented environment variable %s", path, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name := range documented {
		if !reads[name] {
			t.Errorf("reference lists runtime variable %s that application does not read", name)
		}
	}
	// The same scanner recognizes a newly added variable at each supported seam.
	for _, source := range []string{`package p;func f(){os.Getenv("NEW_SETTING")}`, `package p;func f(){r.env.Getenv("NEW_SETTING")}`, `package p;func f(){getenv("NEW_SETTING")}`, `package p;func f(){os.LookupEnv("NEW_SETTING")}`} {
		if !environmentReads(t, "fixture.go", source)["NEW_SETTING"] {
			t.Fatal("scanner missed a new read")
		}
	}
}

func TestEnvironmentReferenceSurfacesStayInSync(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(readme), "<!-- environment:start -->\n")
	if !ok {
		t.Fatal("missing README environment reference")
	}
	block, _, ok = strings.Cut(block, "<!-- environment:end -->")
	if !ok || block != EnvironmentReference {
		t.Fatal("README environment reference differs from installed reference")
	}
	serve, err := CommandHelp("comms", "serve")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"help": CLIUsage("comms"), "serve help": serve, "instructions": InstructionsText(), "JSON instructions": AgentInstructions().Environment} {
		if !strings.Contains(text, EnvironmentReference) {
			t.Errorf("%s omits the shared environment reference", name)
		}
	}
}

func TestEnvironmentReferenceCoversReleaseInputs(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)\s*\?=`).FindAllStringSubmatch(string(data), -1) {
		if !strings.Contains(EnvironmentReference, "| `"+match[1]+"` |") {
			t.Errorf("undocumented Make input %s", match[1])
		}
	}
	scripts, err := filepath.Glob("../../scripts/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range scripts {
		if strings.HasSuffix(path, "-test.sh") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Uppercase shell expansions are operator inputs; lowercase names are
		// locals. BASH_SOURCE, BASH_REMATCH and RANDOM are shell-maintained special variables.
		for _, match := range regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)`).FindAllStringSubmatch(string(data), -1) {
			name := match[1]
			if name == "BASH_SOURCE" || name == "BASH_REMATCH" || name == "RANDOM" {
				continue
			}
			if !strings.Contains(EnvironmentReference, "| `"+name+"` |") {
				t.Errorf("%s reads undocumented release input %s", path, name)
			}
		}
	}
}
