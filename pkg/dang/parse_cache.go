package dang

import (
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"sync"
	"unsafe"
)

// parsedFiles remembers each successfully parsed .dang file by its content.
// Parsing is most of the fixed cost of running a module, and a host such as
// Dagger runs the same unchanged source on every call. A cached tree is never
// handed out: inference writes into AST nodes, so each caller gets its own
// deep copy, whose source locations name the caller's path.
var parsedFiles = &parseCache{max: 256, entries: map[[sha256.Size]byte]parsedFile{}}

type parsedFile struct {
	path string // the path the cached tree's locations name
	tree *FileBlock
}

type parseCache struct {
	mu      sync.Mutex
	max     int
	entries map[[sha256.Size]byte]parsedFile
	order   [][sha256.Size]byte // oldest first
}

func (c *parseCache) get(key [sha256.Size]byte) (parsedFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.entries[key]
	return f, ok
}

func (c *parseCache) add(key [sha256.Size]byte, f parsedFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		return
	}
	for len(c.order) >= c.max {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	c.entries[key] = f
	c.order = append(c.order, key)
}

// parseSourceFile parses a .dang file as ParseFileWithRecovery does, reusing
// an earlier parse of the same content.
func parseSourceFile(filePath string) (*FileBlock, error) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(src)
	if f, ok := parsedFiles.get(key); ok {
		return copyFileBlock(f.tree, f.path, filePath), nil
	}
	parsed, err := ParseWithRecovery(filePath, src, GlobalStore("filePath", filePath))
	if err != nil {
		return nil, err
	}
	block, ok := parsed.(*FileBlock)
	if !ok {
		return nil, fmt.Errorf("parsed result for %s is not a FileBlock", filePath)
	}
	parsedFiles.add(key, parsedFile{path: filePath, tree: copyFileBlock(block, filePath, filePath)})
	return block, nil
}

// copyFileBlock deep-copies a parsed tree, keeping pointers that the tree
// shares shared in the copy, and renames source locations from one path to
// another.
func copyFileBlock(tree *FileBlock, from, to string) *FileBlock {
	c := &treeCopier{from: from, to: to, seen: map[seenKey]reflect.Value{}}
	return c.copy(reflect.ValueOf(tree)).Interface().(*FileBlock)
}

var sourceLocationType = reflect.TypeOf(SourceLocation{})

type seenKey struct {
	ptr unsafe.Pointer
	typ reflect.Type
}

type treeCopier struct {
	from, to string
	seen     map[seenKey]reflect.Value
}

func (c *treeCopier) copy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := seenKey{v.UnsafePointer(), v.Type()}
		if n, ok := c.seen[key]; ok {
			return n
		}
		n := reflect.New(v.Type().Elem())
		c.seen[key] = n
		setValue(n.Elem(), c.copy(v.Elem()))
		return n
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.New(v.Type()).Elem()
		n.Set(c.copy(v.Elem()))
		return n
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			setValue(n.Index(i), c.copy(v.Index(i)))
		}
		return n
	case reflect.Array:
		n := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			setValue(n.Index(i), c.copy(v.Index(i)))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			n.SetMapIndex(c.copy(iter.Key()), c.copy(iter.Value()))
		}
		return n
	case reflect.Struct:
		src := v
		if !src.CanAddr() {
			src = reflect.New(v.Type()).Elem()
			src.Set(v)
		}
		n := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			setValue(n.Field(i), c.copy(readable(src.Field(i))))
		}
		if v.Type() == sourceLocationType {
			loc := n.Addr().Interface().(*SourceLocation)
			if loc.Filename == c.from {
				loc.Filename = c.to
			}
		}
		return n
	default:
		// Scalars are copied by value; functions and channels are shared.
		return v
	}
}

// readable makes an addressable value readable even if it's unexported.
func readable(v reflect.Value) reflect.Value {
	if v.CanInterface() {
		return v
	}
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
}

// setValue sets an addressable value, even if it's unexported.
func setValue(dst, v reflect.Value) {
	if !dst.CanSet() {
		dst = reflect.NewAt(dst.Type(), unsafe.Pointer(dst.UnsafeAddr())).Elem()
	}
	if v.Kind() == reflect.Invalid {
		dst.Set(reflect.Zero(dst.Type()))
		return
	}
	dst.Set(v)
}
