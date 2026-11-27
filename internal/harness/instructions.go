package harness

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Caps on the repository instructions a session is given.
const (
	MaxInstructionFile = 64 << 10 // a larger file is dropped whole
	// MaxInstructionTotal keeps the instructions, with the adapter's own system prompt around them,
	// well inside Linux's 128 KiB cap on one argument; a file that would take the total past it is
	// dropped whole.
	MaxInstructionTotal = 96 << 10
)

// InstructionFiles are the files Instructions reads, in order of preference: the first one
// present is the repository's instructions.
var InstructionFiles = []string{"CLAUDE.md", "AGENTS.md"}

// Instructions returns the agent instructions of the repository in dir: its CLAUDE.md, or its
// AGENTS.md when there is no CLAUDE.md, followed by each file it imports with an "@path" word.
// Every file is read through dir as a root, so an import or a symlink that leads out of dir is
// dropped. Imports are followed one level deep: an imported file's own imports are not. A file
// over MaxInstructionFile, or one that would take the total over MaxInstructionTotal, is dropped
// whole, as is any file that cannot be read. No instruction file is not an error: it returns "".
func Instructions(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	var main, text string
	for _, name := range InstructionFiles {
		if t, ok := readCapped(root, name); ok {
			main, text = name, t
			break
		}
	}
	if main == "" {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(text)
	seen := map[string]bool{main: true}
	for _, word := range strings.Fields(text) {
		name, ok := importPath(word)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		t, ok := readCapped(root, name)
		if !ok || b.Len()+len("\n\n")+len(t) > MaxInstructionTotal {
			continue
		}
		b.WriteString("\n\n" + t)
	}
	return b.String(), nil
}

// importPath returns the file an "@path" word imports, relative to the repository, or false when
// the word is not an import lucy follows: an absolute or home path is never inside the repository.
func importPath(word string) (string, bool) {
	p, ok := strings.CutPrefix(word, "@")
	if !ok || p == "" || strings.HasPrefix(p, "~") || filepath.IsAbs(p) {
		return "", false
	}
	return filepath.Clean(p), true
}

// readCapped reads name through root, refusing a file over MaxInstructionFile.
func readCapped(root *os.Root, name string) (string, bool) {
	f, err := root.Open(name)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxInstructionFile+1))
	if err != nil || len(b) > MaxInstructionFile {
		return "", false
	}
	return string(b), true
}
