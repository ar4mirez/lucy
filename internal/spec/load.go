package spec

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Limits on one config directory.
const (
	MaxFiles      = 1024     // config files in one directory tree
	MaxTotalBytes = 16 << 20 // their total size
	MaxDirDepth   = 8        // directory levels below the root
)

// ErrLoad wraps errors that stop a directory from being loaded at all.
var ErrLoad = errors.New("spec: cannot load config")

// Set is the outcome of loading one directory: a result per document, plus one per file nothing
// could be read from, in path order.
type Set struct {
	Dir     string   `json:"dir"`
	Results []Result `json:"results"`
}

// Admitted returns the resources that passed, in path order.
func (s Set) Admitted() []Resource {
	var out []Resource
	for _, r := range s.Results {
		if r.Resource != nil {
			out = append(out, *r.Resource)
		}
	}
	return out
}

// Invalid counts the results that were refused.
func (s Set) Invalid() int {
	n := 0
	for _, r := range s.Results {
		if !r.Valid() {
			n++
		}
	}
	return n
}

// IsConfigFile reports whether lucy reads a file name as config: .yaml, .yml or .json, not hidden.
func IsConfigFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// LoadDir reads every config file under dir and admits each document in it (Admit). Hidden files
// and directories are skipped, as are files that are not .yaml, .yml or .json. Symlinks are never
// followed: a symlinked config file is refused, so config cannot be pulled in from outside the
// directory. Problems with single files or documents are results, not errors; the error is for a
// directory that cannot be read or exceeds the limits.
//
// Duplicate resources (the same namespace, kind and name) are all refused but the first.
func LoadDir(dir string) (Set, error) {
	set := Set{Dir: dir}
	fi, err := os.Stat(dir)
	if err != nil {
		return set, fmt.Errorf("%w: %w", ErrLoad, err)
	}
	if !fi.IsDir() {
		return set, fmt.Errorf("%w: %s is not a directory", ErrLoad, dir)
	}
	files, total := 0, int64(0)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			if p == dir {
				return err
			}
			set.Results = append(set.Results, fileProblem(rel, ReasonUnreadable, clip(err.Error())))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == dir {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if strings.Count(rel, "/")+1 > MaxDirDepth {
				set.Results = append(set.Results, fileProblem(rel, ReasonTooLarge, fmt.Sprintf("more than %d directory levels below the config directory", MaxDirDepth)))
				return fs.SkipDir
			}
			return nil
		}
		if !IsConfigFile(name) {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			set.Results = append(set.Results, fileProblem(rel, ReasonSymlink, "symlinks are not followed; copy the file into the config directory"))
			return nil
		}
		if !d.Type().IsRegular() {
			set.Results = append(set.Results, fileProblem(rel, ReasonUnreadable, "not a regular file"))
			return nil
		}
		files++
		if files > MaxFiles {
			return fmt.Errorf("%w: more than %d config files under %s", ErrLoad, MaxFiles, dir)
		}
		data, err := readCapped(p)
		if err != nil {
			reason := ReasonUnreadable
			if errors.Is(err, errTooLarge) {
				reason = ReasonTooLarge
			}
			set.Results = append(set.Results, fileProblem(rel, reason, clip(err.Error())))
			return nil
		}
		total += int64(len(data))
		if total > MaxTotalBytes {
			return fmt.Errorf("%w: config files under %s exceed %d bytes in total", ErrLoad, dir, MaxTotalBytes)
		}
		set.Results = append(set.Results, LoadFile(rel, data)...)
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrLoad) {
			err = fmt.Errorf("%w: %w", ErrLoad, err)
		}
		return set, err
	}
	markDuplicates(set.Results)
	sortResults(set.Results)
	return set, nil
}

// LoadFile admits every document in one file's contents; name is how results name the file.
func LoadFile(name string, data []byte) []Result {
	docs, err := Decode(data)
	if err != nil {
		reason := ReasonDecodeError
		if len(data) > MaxFileBytes {
			reason = ReasonTooLarge
		}
		return []Result{fileProblem(name, reason, strings.TrimPrefix(err.Error(), "spec: "))}
	}
	if len(docs) == 0 {
		return []Result{fileProblem(name, ReasonNotAnObject, "the file holds no resource")}
	}
	out := make([]Result, 0, len(docs))
	for i, doc := range docs {
		out = append(out, Admit(doc, Source{File: name, Doc: i + 1}))
	}
	return out
}

func fileProblem(file, reason, message string) Result {
	r := Result{Source: Source{File: file}}
	r.refuse(reason, message)
	return r
}

var errTooLarge = errors.New("too large")

// readCapped reads a regular file of at most MaxFileBytes, refusing it if it changed into a
// symlink or something else since the walk saw it.
func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	lfi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || !os.SameFile(fi, lfi) {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes; the limit is %d", errTooLarge, fi.Size(), MaxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", errTooLarge, MaxFileBytes)
	}
	return data, nil
}

// markDuplicates refuses every admitted resource whose identity an earlier one (in walk order)
// already has.
func markDuplicates(rs []Result) {
	first := map[string]Source{}
	for i := range rs {
		r := &rs[i]
		if r.Resource == nil {
			continue
		}
		id := r.Resource.ID()
		if src, seen := first[id]; seen {
			r.refuse(ReasonDuplicate, fmt.Sprintf("%s %s/%s is already defined in %s", r.Kind, r.Namespace, r.Name, src))
			continue
		}
		first[id] = r.Source
	}
}
