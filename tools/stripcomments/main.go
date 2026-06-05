// stripcomments removes all Go comments from .go files using the AST,
// preserving //go: compiler directives (build constraints, generate, embed, etc).
// Usage: go run ./tools/stripcomments [file.go ...]
// With no args, walks the repo root and strips all .go files.
package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root, _ := os.Getwd()
	args := os.Args[1:]

	var files []string
	if len(args) == 0 {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)

			if info.IsDir() {
				base := info.Name()
				if base == ".git" || base == "vendor" || base == "bin" ||
					base == "downloads" || base == "www" || base == "tmp" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(rel, ".go") {
				files = append(files, path)
			}
			return nil
		})
	} else {
		files = args
	}

	changed := 0
	for _, f := range files {
		ok, err := stripFile(f)
		if err != nil {
			os.Stderr.WriteString("stripcomments: " + f + ": " + err.Error() + "\n")
			continue
		}
		if ok {
			changed++
		}
	}
	if changed > 0 {
		os.Stdout.WriteString("[stripcomments] stripped comments from " +
			itoa(changed) + " file(s)\n")
	}
}

// stripFile removes all comments from path except //go: directives.
// Returns true if the file was modified.
func stripFile(path string) (bool, error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, orig, parser.ParseComments)
	if err != nil {

		return false, nil
	}

	// Keep only //go: compiler directives. Everything else is stripped.
	var keep []*ast.CommentGroup
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			t := strings.TrimSpace(c.Text)
			if strings.HasPrefix(t, "//go:") || strings.HasPrefix(t, "// +build") {
				keep = append(keep, cg)
				break
			}
		}
	}
	f.Comments = keep

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return false, err
	}

	result := buf.Bytes()
	if bytes.Equal(orig, result) {
		return false, nil
	}
	return true, os.WriteFile(path, result, 0o644)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
