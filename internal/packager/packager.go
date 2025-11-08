package packager

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pm/internal/config"
)

type Manifest struct {
	Name         string                  `json:"name"`
	Version      string                  `json:"version"`
	CreatedAt    time.Time               `json:"created_at"`
	Dependencies []config.DependencySpec `json:"dependencies"`
	Files        []string                `json:"files"`
}

type CreateOptions struct {
	WorkingDir string
	OutputPath string
}

func Create(spec *config.PackageSpec, opts CreateOptions) (string, *Manifest, error) {
	if opts.WorkingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		opts.WorkingDir = cwd
	}

	files, err := collectFiles(spec, opts.WorkingDir)
	if err != nil {
		return "", nil, err
	}

	output := opts.OutputPath
	if output == "" {
		filename := fmt.Sprintf("%s-%s.tar.gz", spec.Name, spec.Version)
		output = filepath.Join(opts.WorkingDir, filename)
	}

	if err := writeArchive(output, opts.WorkingDir, files, spec); err != nil {
		return "", nil, err
	}

	manifest := &Manifest{
		Name:         spec.Name,
		Version:      spec.Version,
		CreatedAt:    time.Now().UTC(),
		Dependencies: spec.Packages,
		Files:        files,
	}

	return output, manifest, nil
}

func collectFiles(spec *config.PackageSpec, baseDir string) ([]string, error) {
	seen := map[string]struct{}{}
	var files []string

	for _, target := range spec.Targets {
		matches, err := globMatches(baseDir, target.Pattern)
		if err != nil {
			return nil, err
		}
		excludes := target.Exclude

		for _, match := range matches {
			if shouldExclude(match, excludes) {
				continue
			}
			if _, exists := seen[match]; exists {
				continue
			}
			seen[match] = struct{}{}
			files = append(files, match)
		}
	}

	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no files matched targets")
	}
	return files, nil
}

func shouldExclude(relPath string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") {
			if matchPattern(pattern, relPath) {
				return true
			}
			continue
		}
		if ok, err := path.Match(pattern, filepath.Base(relPath)); err == nil && ok {
			return true
		}
	}
	return false
}

func globMatches(baseDir, pattern string) ([]string, error) {
	cleaned := strings.TrimPrefix(pattern, "./")
	cleaned = strings.TrimPrefix(cleaned, baseDir+"/")
	cleaned = filepath.ToSlash(cleaned)

	var matches []string
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if matchPattern(cleaned, rel) {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func matchPattern(pattern, target string) bool {
	pattern = filepath.ToSlash(pattern)
	target = filepath.ToSlash(target)

	if pattern == "" {
		return target == ""
	}

	pSegs := strings.Split(pattern, "/")
	tSegs := strings.Split(target, "/")
	return matchSegments(pSegs, tSegs)
}

func matchSegments(patternSegs, targetSegs []string) bool {
	if len(patternSegs) == 0 {
		return len(targetSegs) == 0
	}

	if patternSegs[0] == "**" {
		if matchSegments(patternSegs[1:], targetSegs) {
			return true
		}
		for i := 0; i < len(targetSegs); i++ {
			if matchSegments(patternSegs, targetSegs[i+1:]) {
				return true
			}
		}
		return false
	}

	if len(targetSegs) == 0 {
		return false
	}

	ok, err := path.Match(patternSegs[0], targetSegs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(patternSegs[1:], targetSegs[1:])
}

func writeArchive(output, baseDir string, files []string, spec *config.PackageSpec) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}

	f, err := os.Create(output)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifest := Manifest{
		Name:         spec.Name,
		Version:      spec.Version,
		CreatedAt:    time.Now().UTC(),
		Dependencies: spec.Packages,
		Files:        files,
	}

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	if err := addFile(tw, "manifest.json", manifestData, 0o644); err != nil {
		return err
	}

	for _, file := range files {
		abs := filepath.Join(baseDir, file)
		data, err := os.Open(abs)
		if err != nil {
			return err
		}

		info, err := data.Stat()
		if err != nil {
			data.Close()
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			data.Close()
			return err
		}
		header.Name = file

		if err := tw.WriteHeader(header); err != nil {
			data.Close()
			return err
		}

		if _, err := io.Copy(tw, data); err != nil {
			data.Close()
			return err
		}
		data.Close()
	}

	return nil
}

func addFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	header := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
