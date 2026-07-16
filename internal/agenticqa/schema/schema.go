// Package schema indexes Qase schema cases by AutomationTestName.
package schema

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// AutomationTestNameFieldKey is the Qase automation-name field key.
const AutomationTestNameFieldKey = "15"

// CaseMeta holds the metadata extracted from a schema file for a single case.
type CaseMeta struct {
	AutomationTestName string
	Title              string
	Projects           []string
	Suite              string
	SchemaFile         string
	// CaseID is zero until resolved through Qase.
	CaseID int
}

// rawSuiteSchema mirrors the YAML structure of a schema file entry.
type rawSuiteSchema struct {
	Projects []string  `yaml:"projects"`
	Suite    string    `yaml:"suite"`
	Cases    []rawCase `yaml:"cases"`
}

type rawCase struct {
	Title       string            `yaml:"title"`
	CustomField map[string]string `yaml:"custom_field"`
}

// LoadDir parses *_schemas.yaml files below basePath.
// atnFieldKey selects the AutomationTestName custom field.
func LoadDir(basePath, atnFieldKey string) ([]CaseMeta, error) {
	if atnFieldKey == "" {
		atnFieldKey = AutomationTestNameFieldKey
	}
	var all []CaseMeta

	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.Contains(info.Name(), "_schemas.yaml") {
			return nil
		}

		cases, parseErr := parseFile(path, basePath, atnFieldKey)
		if parseErr != nil {
			logrus.Warnf("schema: skipping %s: %v", path, parseErr)
			return nil // non-fatal; keep walking
		}
		all = append(all, cases...)
		return nil
	})

	return all, err
}

// ResolveIDs fills case IDs from project name maps, with title fallback.
func ResolveIDs(cases []CaseMeta, projectMaps map[string]map[string]int) {
	for i := range cases {
		c := &cases[i]
		atn := c.AutomationTestName
		if atn == "" {
			// Hostbusters schemas use the title as the automation test name.
			atn = c.Title
		}
		for _, project := range c.Projects {
			nameMap, ok := projectMaps[project]
			if !ok {
				continue
			}
			if id, ok := nameMap[atn]; ok {
				c.CaseID = id
				break
			}
			// Title fallback for cases where ATN differs from title.
			if id, ok := nameMap[c.Title]; ok {
				c.CaseID = id
				break
			}
		}
	}
}

// BuildAutomationIndex groups cases by automation test name.
func BuildAutomationIndex(cases []CaseMeta) map[string][]CaseMeta {
	idx := make(map[string][]CaseMeta, len(cases))
	for _, c := range cases {
		if c.AutomationTestName != "" {
			idx[c.AutomationTestName] = append(idx[c.AutomationTestName], c)
		}
	}
	return idx
}

// parseFile reads and parses a single schema YAML file.
func parseFile(path, basePath, atnFieldKey string) ([]CaseMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Use the schema's canonical custom_field key.
	var suites []rawSuiteSchema
	if err := yaml.Unmarshal(data, &suites); err != nil {
		return nil, err
	}

	rel, _ := filepath.Rel(basePath, path)

	var cases []CaseMeta
	for _, suite := range suites {
		for _, rc := range suite.Cases {
			automationName := rc.CustomField[atnFieldKey]
			cases = append(cases, CaseMeta{
				AutomationTestName: automationName,
				Title:              rc.Title,
				Projects:           suite.Projects,
				Suite:              suite.Suite,
				SchemaFile:         rel,
			})
		}
	}
	return cases, nil
}
