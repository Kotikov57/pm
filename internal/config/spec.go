package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type PackageSpec struct {
	Name     string           `json:"name" yaml:"name"`
	Version  string           `json:"ver" yaml:"ver"`
	Targets  []TargetSpec     `json:"targets" yaml:"targets"`
	Packages []DependencySpec `json:"packets" yaml:"packets"`
}

type TargetSpec struct {
	Pattern string
	Exclude []string
}

type DependencySpec struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"ver" yaml:"ver"`
}

func (t *TargetSpec) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		t.Pattern = asString
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("target must be string or object: %w", err)
	}

	pathVal, ok := raw["path"].(string)
	if !ok || pathVal == "" {
		return errors.New("target object must have non-empty path")
	}
	t.Pattern = pathVal

	if ex, ok := raw["exclude"]; ok {
		switch v := ex.(type) {
		case string:
			t.Exclude = []string{v}
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return errors.New("exclude entries must be strings")
				}
				t.Exclude = append(t.Exclude, s)
			}
		default:
			return errors.New("exclude must be string or array of strings")
		}
	}
	return nil
}

func LoadPackageSpec(path string) (*PackageSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	spec := &PackageSpec{}
	switch ext := filepath.Ext(path); ext {
	case ".yaml", ".yml":
		jsonData, err := parseYAMLToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
		if err := json.Unmarshal(jsonData, spec); err != nil {
			return nil, err
		}
	default:
		if err := json.Unmarshal(data, spec); err != nil {
			if jsonData, yamlErr := parseYAMLToJSON(data); yamlErr == nil {
				if err := json.Unmarshal(jsonData, spec); err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("failed to parse JSON: %w", err)
			}
		}
	}

	if spec.Name == "" {
		return nil, errors.New("package spec missing name")
	}
	if spec.Version == "" {
		return nil, errors.New("package spec missing version")
	}
	if len(spec.Targets) == 0 {
		return nil, errors.New("package spec must define at least one target")
	}
	return spec, nil
}

type UpdateSpec struct {
	Packages []DependencySpec `json:"packages" yaml:"packages"`
}

func LoadUpdateSpec(path string) (*UpdateSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	spec := &UpdateSpec{}
	switch ext := filepath.Ext(path); ext {
	case ".yaml", ".yml":
		jsonData, err := parseYAMLToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
		if err := json.Unmarshal(jsonData, spec); err != nil {
			return nil, err
		}
	default:
		if err := json.Unmarshal(data, spec); err != nil {
			if jsonData, yamlErr := parseYAMLToJSON(data); yamlErr == nil {
				if err := json.Unmarshal(jsonData, spec); err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("failed to parse JSON: %w", err)
			}
		}
	}

	if len(spec.Packages) == 0 {
		return nil, errors.New("update spec must declare packages")
	}
	return spec, nil
}

type yamlParser struct {
	lines []string
	pos   int
}

func parseYAMLToJSON(data []byte) ([]byte, error) {
	parser := &yamlParser{lines: preprocessYAMLLines(string(data))}
	value, err := parser.parseBlock(0)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("empty YAML content")
	}
	return json.Marshal(value)
}

func preprocessYAMLLines(input string) []string {
	rawLines := strings.Split(input, "\n")
	var lines []string
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		content := line
		if idx := strings.Index(line, "#"); idx != -1 {
			before := line[:idx]
			if strings.TrimSpace(before) == "" {
				continue
			}
			content = before
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		lines = append(lines, content)
	}
	return lines
}

func (p *yamlParser) parseBlock(indent int) (any, error) {
	for p.pos < len(p.lines) {
		lineIndent, trimmed := p.currentLine()
		if trimmed == "" {
			p.pos++
			continue
		}
		if lineIndent < indent {
			return nil, nil
		}
		if strings.HasPrefix(trimmed, "- ") {
			return p.parseList(indent)
		}
		return p.parseMap(indent)
	}
	return nil, nil
}

func (p *yamlParser) parseList(indent int) ([]any, error) {
	var result []any
	for p.pos < len(p.lines) {
		lineIndent, trimmed := p.currentLine()
		if lineIndent < indent {
			break
		}
		if lineIndent > indent {
			return nil, fmt.Errorf("invalid indentation in list")
		}
		if !strings.HasPrefix(strings.TrimSpace(trimmed), "- ") {
			break
		}
		trimmed = strings.TrimSpace(trimmed)[2:]
		trimmed = strings.TrimSpace(trimmed)
		p.pos++

		if trimmed == "" {
			val, err := p.parseBlock(indent + 2)
			if err != nil {
				return nil, err
			}
			result = append(result, val)
			continue
		}

		if strings.Contains(trimmed, ":") {
			key, valStr := splitKeyValue(trimmed)
			entry, err := p.parseInlineMap(key, valStr, indent+2)
			if err != nil {
				return nil, err
			}
			result = append(result, entry)
			continue
		}

		result = append(result, parseScalar(trimmed))
	}
	return result, nil
}

func (p *yamlParser) parseInlineMap(key, valStr string, indent int) (map[string]any, error) {
	result := map[string]any{}
	if valStr == "" {
		val, err := p.parseBlock(indent + 2)
		if err != nil {
			return nil, err
		}
		result[key] = val
	} else {
		result[key] = parseScalar(valStr)
	}

	for p.pos < len(p.lines) {
		lineIndent, trimmed := p.currentLine()
		if lineIndent < indent {
			break
		}
		if lineIndent > indent {
			return nil, fmt.Errorf("invalid indentation in map entry")
		}
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmed, "- ") {
			break
		}
		if trimmed == "" {
			p.pos++
			continue
		}
		if !strings.Contains(trimmed, ":") {
			break
		}
		k, v := splitKeyValue(trimmed)
		p.pos++
		if v == "" {
			val, err := p.parseBlock(indent + 2)
			if err != nil {
				return nil, err
			}
			result[k] = val
		} else {
			result[k] = parseScalar(v)
		}
	}
	return result, nil
}

func (p *yamlParser) parseMap(indent int) (map[string]any, error) {
	result := map[string]any{}
	for p.pos < len(p.lines) {
		lineIndent, trimmed := p.currentLine()
		if lineIndent < indent {
			break
		}
		if lineIndent > indent {
			return nil, fmt.Errorf("invalid indentation in map")
		}
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			p.pos++
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			break
		}
		if !strings.Contains(trimmed, ":") {
			return nil, fmt.Errorf("invalid mapping entry: %s", trimmed)
		}
		key, valStr := splitKeyValue(trimmed)
		p.pos++
		if valStr == "" {
			val, err := p.parseBlock(indent + 2)
			if err != nil {
				return nil, err
			}
			result[key] = val
		} else {
			result[key] = parseScalar(valStr)
		}
	}
	return result, nil
}

func (p *yamlParser) currentLine() (int, string) {
	line := p.lines[p.pos]
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	return indent, line[indent:]
}

func splitKeyValue(input string) (string, string) {
	parts := strings.SplitN(input, ":", 2)
	key := strings.TrimSpace(parts[0])
	value := ""
	if len(parts) > 1 {
		value = strings.TrimSpace(parts[1])
	}
	return key, value
}

func parseScalar(input string) any {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if strings.HasPrefix(input, "\"") && strings.HasSuffix(input, "\"") {
		return strings.Trim(input, "\"")
	}
	if strings.HasPrefix(input, "'") && strings.HasSuffix(input, "'") {
		return strings.Trim(input, "'")
	}
	if input == "true" || input == "false" {
		return input == "true"
	}
	if i, err := strconv.Atoi(input); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(input, 64); err == nil {
		return f
	}
	return input
}
