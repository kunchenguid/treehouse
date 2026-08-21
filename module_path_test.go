package main_test

import (
	"bufio"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const expectedModulePath = "github.com/kunchenguid/treehouse/v2"

func TestModulePath(t *testing.T) {
	modulePath := readModulePath(t)
	if modulePath != expectedModulePath {
		t.Fatalf("module path = %q, want %q", modulePath, expectedModulePath)
	}

	legacyModulePath := strings.TrimSuffix(expectedModulePath, "/v2")
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if importPath != expectedModulePath &&
				!strings.HasPrefix(importPath, expectedModulePath+"/") &&
				(importPath == legacyModulePath || strings.HasPrefix(importPath, legacyModulePath+"/")) {
				t.Errorf("%s imports legacy module path %q", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readModulePath(t *testing.T) string {
	t.Helper()

	file, err := os.Open("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("go.mod has no module directive")
	return ""
}
