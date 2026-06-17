package schema

// Package schema parses Qase *_schemas.yaml files from the rancher-tests
// validation tree and builds AutomationTestName → Qase case metadata indexes.
//
// Schema file format (YAML, one or more documents per file):
//
//	- projects: [RANCHERINT]
//	  suite: SomeSuite
//	  cases:
//	    - title: "Case title"
//	      custom_field:
//	        "15": "GoTestFunctionName"
//
// Custom field 15 is the AutomationTestName field (see actions/qase/defaults.go).

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// AutomationTestNameFieldKey is the YAML key used for the AutomationTestName
// custom field inside schema files (custom_field."15").
const AutomationTestNameFieldKey = "15"

// CaseMeta holds the metadata extracted from a schema file for a single case.
type CaseMeta struct {
	// AutomationTestName is the value of custom_field "15", i.e. the Go test name.
	AutomationTestName string
	// Title is the human-readable Qase case title.
	Title string
	// Projects lists the Qase project codes this case belongs to.
	Projects []string
	// Suite is the Qase suite name from the schema.
	Suite string
	// SchemaFile is the relative path of the schema file this was loaded from.
	SchemaFile string
	// CaseID is the Qase numeric case ID resolved from the API.
	// It is 0 until ResolveIDs is called with a populated project→name→ID map.
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

// LoadDir walks basePath recursively and parses every file whose name contains
// "_schemas.yaml". It returns a flat slice of CaseMeta for all cases found.
func LoadDir(basePath string) ([]CaseMeta, error) {
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

		cases, parseErr := parseFile(path, basePath)
		if parseErr != nil {
			logrus.Warnf("schema: skipping %s: %v", path, parseErr)
			return nil // non-fatal; keep walking
		}
		all = append(all, cases...)
		return nil
	})

	return all, err
}

// ResolveIDs stamps the CaseID field on each CaseMeta by looking up the
// automation test name (or title, as fallback) in projectMaps.
//
// projectMaps maps Qase project code → (automation-test-name → case ID),
// which is the same structure built by qase.Client.GetAutomationNameMap.
// Each CaseMeta is matched against every project in its Projects list;
// the first hit wins. Cases that cannot be resolved are left with CaseID == 0.
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

// BuildAutomationIndex returns a map of AutomationTestName → []CaseMeta built
// from the provided cases slice. A test name may appear in multiple schema
// files (though it shouldn't); all entries are kept.
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
func parseFile(path, basePath string) ([]CaseMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// The reporter normalises "custom_field" to "customfield" for a different
	// YAML library; we keep the canonical key here since we use gopkg.in/yaml.v2
	// which handles the underscore fine.
	var suites []rawSuiteSchema
	if err := yaml.Unmarshal(data, &suites); err != nil {
		return nil, err
	}

	rel, _ := filepath.Rel(basePath, path)

	var cases []CaseMeta
	for _, suite := range suites {
		for _, rc := range suite.Cases {
			automationName := rc.CustomField[AutomationTestNameFieldKey]
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
