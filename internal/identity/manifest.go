// Package identity owns Zii's public application facts, independently of LLM profiles.
package identity

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name           string       `yaml:"name" json:"name"`
	Role           string       `yaml:"role" json:"role"`
	Description    string       `yaml:"description" json:"description"`
	CurrentVersion string       `yaml:"current_version" json:"current_version"`
	Capabilities   []Capability `yaml:"capabilities" json:"capabilities"`
	Releases       []Release    `yaml:"releases" json:"releases"`
}
type Capability struct {
	ID            string   `yaml:"id" json:"id"`
	Description   string   `yaml:"description" json:"description"`
	RequiresTools []string `yaml:"requires_tools,omitempty" json:"requires_tools,omitempty"`
}
type Release struct {
	Version string `yaml:"version" json:"version"`
	Status  string `yaml:"status" json:"status"`
	Summary string `yaml:"summary" json:"summary"`
}

//go:embed manifest.yaml
var embedded []byte

// Validate the embedded source at process initialization. A broken release must
// fail at startup, never silently substitute outdated application facts.
func init() {
	if _, err := Parse(embedded); err != nil {
		panic(err)
	}
}

// Default returns a fresh copy of the single embedded source.
func Default() Manifest {
	m, err := Parse(embedded)
	if err != nil {
		panic(err)
	}
	return m
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("identity: invalid manifest: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("identity: expected exactly one YAML document")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m Manifest) Validate() error {
	for _, value := range []string{m.Name, m.Role, m.Description, m.CurrentVersion} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("identity: identity fields must be nonempty")
		}
	}
	if !versionPattern.MatchString(m.CurrentVersion) {
		return fmt.Errorf("identity: invalid current version")
	}
	if len(m.Capabilities) == 0 || len(m.Releases) == 0 {
		return fmt.Errorf("identity: capabilities and releases are required")
	}
	capabilities := map[string]bool{}
	for _, c := range m.Capabilities {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Description) == "" || capabilities[c.ID] {
			return fmt.Errorf("identity: invalid or duplicate capability")
		}
		capabilities[c.ID] = true
		seen := map[string]bool{}
		for _, tool := range c.RequiresTools {
			if (tool != "search_local" && tool != "search_web" && tool != "fetch_page") || seen[tool] {
				return fmt.Errorf("identity: invalid or duplicate required tool")
			}
			seen[tool] = true
		}
	}
	versions := map[string]bool{}
	current := false
	for _, r := range m.Releases {
		if !versionPattern.MatchString(r.Version) || versions[r.Version] || strings.TrimSpace(r.Summary) == "" {
			return fmt.Errorf("identity: invalid or duplicate release")
		}
		versions[r.Version] = true
		if r.Status != "released" && r.Status != "planned" {
			return fmt.Errorf("identity: unknown release status")
		}
		if r.Version == m.CurrentVersion {
			if r.Status != "released" {
				return fmt.Errorf("identity: current version must be released")
			}
			current = true
		}
	}
	if !current {
		return fmt.Errorf("identity: current version is missing")
	}
	return nil
}
