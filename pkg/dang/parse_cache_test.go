package dang

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const parseCacheModule = `type Thing {
  pub name: String! = "thing"

  pub greet(who: String!): String! {
    "hello, " + who + " from " + name
  }
}

let sortedNames(items: [String!]!): [String!]! {
  items.sorted
}

let greeting = Thing.greet(who: sortedNames(["b", "a"]).join(","))
`

func writeModuleDir(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.dang"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// dumpTree lists every visited node's type and location, then the formatted
// source.
func dumpTree(block *FileBlock) string {
	var sb strings.Builder
	block.Walk(func(n Node) bool {
		if loc := n.GetSourceLocation(); loc != nil {
			fmt.Fprintf(&sb, "%T %s %d:%d+%d\n", n, loc.Filename, loc.Line, loc.Column, loc.Length)
		} else {
			fmt.Fprintf(&sb, "%T\n", n)
		}
		return true
	})
	sb.WriteString(Format(block))
	return sb.String()
}

func TestParseSourceFileReusesParse(t *testing.T) {
	src := parseCacheModule + "# " + t.Name() + "\n"
	key := sha256.Sum256([]byte(src))
	first := filepath.Join(writeModuleDir(t, src), "main.dang")
	second := filepath.Join(writeModuleDir(t, src), "main.dang")

	if _, ok := parsedFiles.get(key); ok {
		t.Fatal("content already cached before the first parse")
	}
	if _, err := parseSourceFile(first); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsedFiles.get(key); !ok {
		t.Fatal("first parse was not cached")
	}

	reused, err := parseSourceFile(second)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := ParseFileWithRecovery(second, GlobalStore("filePath", second))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dumpTree(reused), dumpTree(fresh.(*FileBlock)); got != want {
		t.Fatalf("reused tree differs from a fresh parse at the second path:\n--- reused\n%s\n--- fresh\n%s", got, want)
	}
	if strings.Contains(dumpTree(reused), first) {
		t.Fatal("reused tree still names the first path")
	}
}

func TestParseCacheCopiesAreIndependent(t *testing.T) {
	src := parseCacheModule + "# " + t.Name() + "\n"
	key := sha256.Sum256([]byte(src))
	for i := range 3 {
		dir := writeModuleDir(t, src)
		env, err := RunDir(context.Background(), dir, false)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !env.Has("Thing") || !env.Has("greeting") {
			t.Fatalf("run %d: module bindings missing", i)
		}
	}
	cached, ok := parsedFiles.get(key)
	if !ok {
		t.Fatal("module was not cached")
	}
	cached.tree.Walk(func(n Node) bool {
		if n.GetInferredType() != nil {
			t.Fatalf("cached tree was written by inference: %T", n)
		}
		return true
	})
}

func TestParseSourceFileDoesNotCacheErrors(t *testing.T) {
	src := "type {{{ # " + t.Name() + "\n"
	key := sha256.Sum256([]byte(src))
	path := filepath.Join(writeModuleDir(t, src), "main.dang")
	if _, err := parseSourceFile(path); err == nil {
		t.Fatal("expected a parse error")
	}
	if _, ok := parsedFiles.get(key); ok {
		t.Fatal("a failed parse was cached")
	}
}

// BenchmarkParseSourceFile compares a fresh parse of $DANG_BENCH_FILE (or a
// small module) with reusing it, which costs a deep copy.
func BenchmarkParseSourceFile(b *testing.B) {
	path := os.Getenv("DANG_BENCH_FILE")
	if path == "" {
		dir := b.TempDir()
		path = filepath.Join(dir, "main.dang")
		if err := os.WriteFile(path, []byte(parseCacheModule), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("fresh", func(b *testing.B) {
		for range b.N {
			if _, err := ParseFileWithRecovery(path, GlobalStore("filePath", path)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("reused", func(b *testing.B) {
		if _, err := parseSourceFile(path); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for range b.N {
			if _, err := parseSourceFile(path); err != nil {
				b.Fatal(err)
			}
		}
	})
}
