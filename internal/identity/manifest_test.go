package identity

import (
	"strings"
	"testing"
)

const validManifest = `name: Zii
role: Discord AI assistant
description: Questions and information organization
current_version: "1.2"
capabilities:
  - id: conversation
    description: Questions and recent conversation history
  - id: search
    description: Search MCP
    requires_tools: [search_local, search_web, fetch_page]
releases:
  - {version: "1.0", status: released, summary: Baseline}
  - {version: "1.2", status: released, summary: Identity}
  - {version: "1.5", status: planned, summary: Agent}
`

func TestParseManifest(t *testing.T) {
	m, err := Parse([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Zii" || m.CurrentVersion != "1.2" || len(m.Releases) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestManifestRejectsInvalidData(t *testing.T) {
	for name, data := range map[string]string{
		"yaml":                 "name: [",
		"unknown field":        validManifest + "endpoint: private\n",
		"multiple documents":   validManifest + "---\nname: Other\n",
		"empty identity":       strings.Replace(validManifest, "name: Zii", "name: ' '", 1),
		"empty role":           strings.Replace(validManifest, "role: Discord AI assistant", "role: ''", 1),
		"empty description":    strings.Replace(validManifest, "description: Questions and information organization", "description: ''", 1),
		"empty summary":        strings.Replace(validManifest, "summary: Baseline", "summary: ''", 1),
		"empty capability":     strings.Replace(validManifest, "description: Search MCP", "description: ''", 1),
		"duplicate capability": strings.Replace(validManifest, "id: search", "id: conversation", 1),
		"duplicate tool":       strings.Replace(validManifest, "search_local, search_web", "search_local, search_local", 1),
		"unknown tool":         strings.Replace(validManifest, "fetch_page", "shell", 1),
		"duplicate version":    strings.Replace(validManifest, "version: \"1.0\"", "version: \"1.2\"", 1),
		"unknown status":       strings.Replace(validManifest, "status: planned", "status: shipped", 1),
		"missing current":      strings.Replace(validManifest, "current_version: \"1.2\"", "current_version: \"4.0\"", 1),
		"planned current":      strings.Replace(validManifest, `version: "1.2", status: released`, `version: "1.2", status: planned`, 1),
		"bad version":          strings.Replace(validManifest, "version: \"1.0\"", "version: 'first'", 1),
		"no releases":          "name: Zii\nrole: assistant\ndescription: test\ncurrent_version: '1.2'\ncapabilities: []\nreleases: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestEmbeddedManifest(t *testing.T) {
	m := Default()
	if m.CurrentVersion != "1.2" || len(m.Releases) != 6 {
		t.Fatalf("manifest = %+v", m)
	}
	m.Releases[0].Status = "corrupted"
	if Default().Releases[0].Status != "released" {
		t.Fatal("default manifest is mutable shared state")
	}
}
