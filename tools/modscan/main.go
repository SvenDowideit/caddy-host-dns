// Command modscan reproduces the Caddy module-registry scanner's package load,
// so we can tell whether a package path is scannable *before* registering it at
// https://caddyserver.com/account/register-package.
//
// It mirrors what github.com/caddyserver/moduledoc does when the registry scans
// a package:
//
//	go get <pattern>
//	packages.Load(<pattern>) with CGO_ENABLED=0
//
// then reports the loaded packages, the Caddy modules they register (by finding
// `caddy.RegisterModule` calls), and any load errors. A path that loads with no
// errors is one the registry can scan; errors mean it is not claimable, and the
// messages tell you why.
//
// Usage:
//
//	modscan github.com/SvenDowideit/caddy-host-dns
//	modscan github.com/SvenDowideit/caddy-host-dns/dnsrec
//
// The module is loaded into a throwaway workspace under the system temp dir, so
// it never disturbs the current module.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/packages"
)

func main() {
	keep := flag.Bool("keep", false, "keep the temporary workspace")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: modscan <package-or-module-path>")
		os.Exit(2)
	}
	pattern := flag.Arg(0)

	dir, err := os.MkdirTemp("", "modscan-")
	if err != nil {
		fatal("creating workspace: %v", err)
	}
	defer func() {
		if !*keep {
			_ = os.RemoveAll(dir)
		}
	}()

	// A minimal workspace module so `go get` has somewhere to resolve into.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module modscan.workspace\n\ngo 1.21\n"), 0o644); err != nil {
		fatal("writing workspace go.mod: %v", err)
	}

	fmt.Printf("workspace: %s\npattern:   %s\n\n", dir, pattern)

	fmt.Println("== go get", pattern)
	get := exec.Command("go", "get", pattern)
	get.Dir = dir
	get.Stdout = os.Stdout
	get.Stderr = os.Stderr
	if err := get.Run(); err != nil {
		fatal("go get %s: %v", pattern, err)
	}
	fmt.Println()

	cfg := &packages.Config{
		Dir: dir,
		Mode: packages.NeedName |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedModule |
			packages.NeedTypesInfo,
		// The registry forces this; without it, Linux fails with
		// "could not import C (no metadata for C)".
		Env: append(os.Environ(), "CGO_ENABLED=0"),
	}

	fmt.Println("== packages.Load", pattern)
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		fatal("packages.Load: %v", err)
	}

	var errCount int
	modPaths := map[string]bool{}
	for _, pkg := range pkgs {
		name := pkg.PkgPath
		if name == "" {
			name = pkg.ID
		}
		for _, e := range pkg.Errors {
			errCount++
			fmt.Printf("  ERROR  %s: %s\n", name, e)
		}
		if pkg.Module != nil {
			modPaths[pkg.Module.Path] = true
		}
	}

	registrations := findRegistrations(pkgs, targetModules(pkgs))

	fmt.Println()
	fmt.Printf("loaded %d package(s), %d error(s)\n", len(pkgs), errCount)

	if len(registrations) > 0 {
		sort.Strings(registrations)
		fmt.Println("packages (in the scanned module) that call caddy.RegisterModule:")
		for _, p := range registrations {
			fmt.Printf("  %s\n", p)
		}
	} else {
		fmt.Println("packages that call caddy.RegisterModule: none found")
	}

	fmt.Println("packages:")
	for _, pkg := range pkgs {
		mod := ""
		if pkg.Module != nil {
			mod = pkg.Module.Path + "@" + pkg.Module.Version
		}
		status := "ok"
		if len(pkg.Errors) > 0 {
			status = "ERRORS"
		}
		fmt.Printf("  %-6s %s  (%s)\n", status, pkg.PkgPath, mod)
	}

	fmt.Println()
	if errCount > 0 {
		fmt.Println("RESULT: NOT scannable — the registry would fail with the errors above.")
		os.Exit(1)
	}
	fmt.Println("RESULT: scannable (no load errors).")
}

// targetModules returns the set of module paths of the packages matched by the
// load pattern (i.e. the module(s) the user asked about), so registrations can
// be reported for those and not for Caddy core.
func targetModules(pkgs []*packages.Package) map[string]bool {
	out := map[string]bool{}
	for _, p := range pkgs {
		if p.Module != nil {
			out[p.Module.Path] = true
		}
	}
	return out
}

// findRegistrations walks the full import graph and returns the sorted package
// paths that contain a caddy.RegisterModule call, limited to packages whose
// module is in keep.
func findRegistrations(pkgs []*packages.Package, keep map[string]bool) []string {
	seen := map[string]bool{}
	visited := map[*packages.Package]bool{}
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if p == nil || visited[p] {
			return
		}
		visited[p] = true
		if p.Module != nil && (len(keep) == 0 || keep[p.Module.Path]) {
			for _, file := range p.Syntax {
				ast.Inspect(file, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "RegisterModule" {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					obj := p.TypesInfo.Uses[id]
					if obj == nil || obj.Pkg() == nil {
						return true
					}
					if obj.Pkg().Path() == "github.com/caddyserver/caddy/v2" {
						seen[p.PkgPath] = true
					}
					return true
				})
			}
		}
		for _, imp := range p.Imports {
			walk(imp)
		}
	}
	for _, p := range pkgs {
		walk(p)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "modscan: "+format+"\n", args...)
	os.Exit(1)
}
