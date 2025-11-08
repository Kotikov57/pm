package updater

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pm/internal/config"
	"pm/internal/packager"
	"pm/internal/sshcmd"
)

type UpdateOptions struct {
	RemoteDir string
	LocalDir  string
	SSH       sshcmd.Config
}

type Result struct {
	PackageName string
	Version     string
	ArchivePath string
	ExtractedTo string
	Manifest    string
}

func Update(spec *config.UpdateSpec, opts UpdateOptions) ([]Result, error) {
	entries, err := listRemoteArchives(opts.SSH, opts.RemoteDir)
	if err != nil {
		return nil, err
	}

	available := map[string][]remotePackage{}
	for _, pkg := range entries {
		available[pkg.Name] = append(available[pkg.Name], pkg)
	}

	for k := range available {
		sortPackages(available[k])
	}

	var results []Result
	installed := map[string]Version{}
	for _, dep := range spec.Packages {
		if err := installPackage(dep, available, opts, installed, &results); err != nil {
			return nil, err
		}
	}
	return results, nil
}

type remotePackage struct {
	Name    string
	Version Version
	Path    string
}

func listRemoteArchives(cfg sshcmd.Config, dir string) ([]remotePackage, error) {
	if dir == "" {
		dir = "."
	}
	out, err := sshcmd.RunSSH(cfg, fmt.Sprintf("ls -1 %s", sshcmd.ShellEscape(dir)))
	if err != nil {
		return nil, err
	}
	var pkgs []remotePackage
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, version, ok := parseArchiveName(line)
		if !ok {
			continue
		}
		pkgs = append(pkgs, remotePackage{
			Name:    name,
			Version: version,
			Path:    path.Join(dir, line),
		})
	}
	return pkgs, nil
}

func parseArchiveName(filename string) (string, Version, bool) {
	if !strings.HasSuffix(filename, ".tar.gz") {
		return "", Version{}, false
	}
	trimmed := strings.TrimSuffix(filename, ".tar.gz")
	parts := strings.Split(trimmed, "-")
	if len(parts) < 2 {
		return "", Version{}, false
	}
	versionStr := parts[len(parts)-1]
	name := strings.Join(parts[:len(parts)-1], "-")
	version, err := ParseVersion(versionStr)
	if err != nil {
		return "", Version{}, false
	}
	return name, version, true
}

func sortPackages(pkgs []remotePackage) {
	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].Version.GreaterThan(pkgs[j].Version)
	})
}

func selectVersion(pkgs []remotePackage, constraint string) (*remotePackage, error) {
	if constraint == "" {
		return &pkgs[0], nil
	}
	c, err := ParseConstraint(constraint)
	if err != nil {
		return nil, err
	}
	for _, pkg := range pkgs {
		if c.Matches(pkg.Version) {
			p := pkg
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no versions of %s satisfy constraint %s", pkgs[0].Name, constraint)
}

type Version struct {
	parts    []int
	original string
}

func ParseVersion(s string) (Version, error) {
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	segments := strings.Split(s, ".")
	parts := make([]int, len(segments))
	for i, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return Version{}, fmt.Errorf("invalid version segment in %q", s)
		}
		value, err := strconv.Atoi(seg)
		if err != nil {
			return Version{}, fmt.Errorf("invalid version segment %q", seg)
		}
		parts[i] = value
	}
	return Version{parts: parts, original: s}, nil
}

func (v Version) Compare(other Version) int {
	maxLen := len(v.parts)
	if len(other.parts) > maxLen {
		maxLen = len(other.parts)
	}
	for i := 0; i < maxLen; i++ {
		a := 0
		if i < len(v.parts) {
			a = v.parts[i]
		}
		b := 0
		if i < len(other.parts) {
			b = other.parts[i]
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
	}
	return 0
}

func (v Version) GreaterThan(other Version) bool {
	return v.Compare(other) > 0
}

func (v Version) String() string {
	if v.original != "" {
		return v.original
	}
	segments := make([]string, len(v.parts))
	for i, part := range v.parts {
		segments[i] = strconv.Itoa(part)
	}
	return strings.Join(segments, ".")
}

type Constraint struct {
	op      string
	version Version
}

func ParseConstraint(input string) (Constraint, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Constraint{}, fmt.Errorf("empty constraint")
	}
	operators := []string{"<=", ">=", "<", ">", "==", "="}
	op := ""
	for _, candidate := range operators {
		if strings.HasPrefix(input, candidate) {
			op = candidate
			input = strings.TrimSpace(input[len(candidate):])
			break
		}
	}
	if op == "" {
		op = "="
	}
	version, err := ParseVersion(input)
	if err != nil {
		return Constraint{}, err
	}
	return Constraint{op: op, version: version}, nil
}

func (c Constraint) Matches(v Version) bool {
	cmp := v.Compare(c.version)
	switch c.op {
	case "=", "==":
		return cmp == 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false
	}
}

func extractArchive(path, dest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dest, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tr); err != nil {
				file.Close()
				return err
			}
			file.Close()
		default:
			// ignore other types
		}
	}
	return nil
}

func ensureManifestUnique(dir, pkgName, version string) (string, error) {
	src := filepath.Join(dir, "manifest.json")
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, expected file", src)
	}
	target := filepath.Join(dir, manifestFilename(pkgName, version))
	if err := os.Rename(src, target); err != nil {
		return "", err
	}
	return target, nil
}

func manifestFilename(pkgName, version string) string {
	sanitize := func(input string) string {
		var b strings.Builder
		for _, r := range input {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r)
			case r >= 'A' && r <= 'Z':
				b.WriteRune(r)
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			case r == '-', r == '_', r == '.':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		return b.String()
	}
	return fmt.Sprintf("manifest-%s-%s.json", sanitize(pkgName), sanitize(version))
}

func installPackage(dep config.DependencySpec, available map[string][]remotePackage, opts UpdateOptions, installed map[string]Version, results *[]Result) error {
	if installedVersion, ok := installed[dep.Name]; ok {
		if dep.Version == "" {
			return nil
		}
		c, err := ParseConstraint(dep.Version)
		if err != nil {
			return err
		}
		if c.Matches(installedVersion) {
			return nil
		}
		return fmt.Errorf("package %s already installed with version %s which does not satisfy constraint %s", dep.Name, installedVersion.String(), dep.Version)
	}

	candidates := available[dep.Name]
	if len(candidates) == 0 {
		return fmt.Errorf("package %s not found on remote", dep.Name)
	}

	selected, err := selectVersion(candidates, dep.Version)
	if err != nil {
		return err
	}

	localArchive, err := sshcmd.DownloadFile(opts.SSH, selected.Path, opts.LocalDir)
	if err != nil {
		return err
	}

	extractDir := opts.LocalDir
	if extractDir == "" {
		extractDir = "."
	}
	if err := extractArchive(localArchive, extractDir); err != nil {
		return err
	}

	manifestPath, err := ensureManifestUnique(extractDir, dep.Name, selected.Version.String())
	if err != nil {
		return err
	}

	installed[dep.Name] = selected.Version

	res := Result{
		PackageName: dep.Name,
		Version:     selected.Version.String(),
		ArchivePath: localArchive,
		ExtractedTo: extractDir,
		Manifest:    manifestPath,
	}
	*results = append(*results, res)

	deps, err := loadManifestDependencies(manifestPath)
	if err != nil {
		return err
	}
	for _, child := range deps {
		if err := installPackage(child, available, opts, installed, results); err != nil {
			return err
		}
	}
	return nil
}

func loadManifestDependencies(manifestPath string) ([]config.DependencySpec, error) {
	if manifestPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest packager.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return manifest.Dependencies, nil
}
