package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/schema"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	genFeatureMapValidationDirFlag = "validation-dir"
	genFeatureMapActionsDirFlag    = "actions-dir"
)

var (
	genFeatureMapValidationDir string
	genFeatureMapOutputFile    string
	genFeatureMapActionsDir    string
	genFeatureMapQaseProjects  []string
)

func init() {
	f := genFeatureMapCmd.Flags()
	f.StringVar(&genFeatureMapValidationDir, genFeatureMapValidationDirFlag, "validation", "Path to rancher-tests validation/ directory")
	f.StringVar(&genFeatureMapOutputFile, outputFileFlag, "./agentic-qa/feature_test_mapping.json", "Output path for feature_test_mapping.json")
	f.StringVar(&genFeatureMapActionsDir, genFeatureMapActionsDirFlag, "actions", "Path to rancher-tests actions/ directory")
	f.StringSliceVar(&genFeatureMapQaseProjects, qaseProjectsFlag, nil,
		"Qase project codes to scan for title-based fallback matching (comma-separated); defaults to qase_projects from --pipeline-env")

	rootCmd.AddCommand(genFeatureMapCmd)
}

var genFeatureMapCmd = &cobra.Command{
	Use:   "generate-feature-map",
	Short: "Generate feature_test_mapping.json from validation/ test files and schemas",
	Long: `Walks the validation/ directory to discover test files, extracts build tags,
test suites, and test functions. Parses *_schemas.yaml files to find Qase case
metadata. Queries the Qase REST API to resolve case IDs for each automation test
name. Outputs feature_test_mapping.json with complete qase_cases entries including
title, id, and automation_test_name.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		return runGenerateFeatureMap(ctx)
	},
}

func runGenerateFeatureMap(ctx context.Context) error {
	qaseToken := os.Getenv(qaseApiTokenEnvVar)
	if qaseToken == "" {
		return fmt.Errorf("%s environment variable is required", qaseApiTokenEnvVar)
	}

	env := activePipelineEnv()

	// Resolve the Qase project list: prefer explicit --qase-projects flag,
	// then pipeline_env.json, then the generated defaults.
	fallbackProjects := genFeatureMapQaseProjects
	if len(fallbackProjects) == 0 {
		fallbackProjects = env.QaseProjects
	}

	logrus.Info("Discovering test files...")
	testFiles, err := discoverTestFiles(genFeatureMapValidationDir)
	if err != nil {
		return fmt.Errorf("discovering test files: %w", err)
	}
	logrus.Infof("Found %d test files", len(testFiles))

	logrus.Info("Loading schema files...")
	atnFieldKey := fmt.Sprintf("%d", env.QaseATNFieldID)
	schemaCases, err := schema.LoadDir(genFeatureMapValidationDir, atnFieldKey)
	if err != nil {
		return fmt.Errorf("loading schema files: %w", err)
	}
	logrus.Infof("Loaded %d cases from schema files", len(schemaCases))

	// Collect all unique Qase projects across all schema cases for API resolution.
	projectSet := map[string]struct{}{}
	for _, c := range schemaCases {
		for _, p := range c.Projects {
			projectSet[p] = struct{}{}
		}
	}

	// Fetch automation name → case ID maps from Qase API, then stamp CaseID onto
	// every CaseMeta entry before associating them with test files.
	qaseClient := qase.NewClientWithATNFieldID(qaseToken, env.QaseATNFieldID)
	projectMaps := map[string]map[string]int{}
	for project := range projectSet {
		logrus.Infof("Fetching Qase cases for project %s...", project)
		nameMap, err := qaseClient.GetAutomationNameMap(ctx, project)
		if err != nil {
			return fmt.Errorf("fetching Qase cases for project %s: %w", project, err)
		}
		projectMaps[project] = nameMap
		logrus.Infof("  %s: %d cases indexed", project, len(nameMap))
	}

	// Stamp resolved case IDs onto every CaseMeta entry before association so that
	// associateSchemas can propagate CaseMeta.CaseID directly into QaseCase.ID.
	schema.ResolveIDs(schemaCases, projectMaps)

	// Build index: directory path → schema cases in that directory.
	schemaByDir := buildSchemaByDir(schemaCases, genFeatureMapValidationDir)

	// Associate schema cases (now with CaseIDs populated) with test files.
	associateSchemas(testFiles, schemaByDir, genFeatureMapValidationDir)

	totalResolved := 0
	totalUnresolved := 0
	for _, c := range schemaCases {
		if c.CaseID != 0 {
			totalResolved++
		}
	}
	for _, tf := range testFiles {
		if len(tf.QaseCases) == 0 {
			totalUnresolved++
		}
	}
	logrus.Infof("Resolved %d case IDs from schemas; %d test files have no Qase cases yet", totalResolved, totalUnresolved)

	// Fallback: for test files that still have no QaseCases, try to match their
	// test function names against Qase case titles in all configured projects.
	if totalUnresolved > 0 {
		fallbackResolved := resolveByTitleFallback(ctx, qaseClient, testFiles, fallbackProjects)
		logrus.Infof("Title fallback resolved %d additional cases across unmapped test files", fallbackResolved)
		totalResolved += fallbackResolved
	}

	// Group test files into feature areas.
	featureAreas := groupFeatureAreas(testFiles, genFeatureMapValidationDir, genFeatureMapActionsDir, env.TagToJob)

	// Build output.
	mapping := types.FeatureTestMapping{
		Metadata: types.MappingMetadata{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GeneratedBy: "agentic-qa " + genFeatureMapCommandName,
			Version:     mappingFileVersion,
		},
		FeatureAreas: featureAreas,
	}

	data, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling output: %w", err)
	}
	if err := ensureOutputParent(genFeatureMapOutputFile); err != nil {
		return err
	}
	if err := os.WriteFile(genFeatureMapOutputFile, data, 0644); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	logrus.Infof("Wrote %s (%d feature areas, %d test files, %d resolved cases)",
		genFeatureMapOutputFile, len(featureAreas), len(testFiles), totalResolved)
	return nil
}

// discoveredTestFile holds parsed information about a single _test.go file.
type discoveredTestFile struct {
	RelPath       string
	BuildTags     []string
	TestSuite     string
	TestFunctions []string
	QaseSchema    *string
	QaseProjects  []string
	QaseCases     []types.QaseCase
	// schemaCases holds the raw CaseMeta entries associated with this file.
	schemaCases []schema.CaseMeta
}

// discoverTestFiles walks the validation directory and parses each *_test.go file.
func discoverTestFiles(validationDir string) ([]*discoveredTestFile, error) {
	var files []*discoveredTestFile

	err := filepath.Walk(validationDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		tf, parseErr := parseTestFile(path, validationDir)
		if parseErr != nil {
			logrus.Warnf("Skipping %s: %v", path, parseErr)
			return nil
		}
		files = append(files, tf)
		return nil
	})

	return files, err
}

// parseTestFile extracts build tags, suite name, and test functions from a Go test file.
func parseTestFile(path, baseDir string) (*discoveredTestFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	rel, _ := filepath.Rel(baseDir, path)

	tf := &discoveredTestFile{
		RelPath: rel,
	}

	// Extract build tags from //go:build lines.
	tf.BuildTags = extractBuildTags(string(content))

	// Parse the Go file to extract function names.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		// If parsing fails (e.g. due to build constraints), extract function names with regex.
		tf.TestFunctions = extractTestFunctionsRegex(string(content))
		tf.TestSuite = extractSuiteNameRegex(string(content))
		return tf, nil
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") {
			tf.TestFunctions = append(tf.TestFunctions, name)
		}
	}

	// Extract suite name from suite.Run calls.
	tf.TestSuite = extractSuiteName(f)

	return tf, nil
}

// extractBuildTags extracts tags from //go:build directives.
var buildTagRegex = regexp.MustCompile(`//go:build\s+(.+)`)

func extractBuildTags(content string) []string {
	lines := strings.Split(content, "\n")
	tagSet := map[string]struct{}{}

	for _, line := range lines {
		if strings.HasPrefix(line, "package ") {
			break // stop at package declaration
		}
		matches := buildTagRegex.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}
		expr := matches[1]
		// Extract individual tag identifiers from the boolean expression.
		tokens := tokenizeBuildExpr(expr)
		for _, tok := range tokens {
			if tok != "" && tok != "!" {
				tagSet[tok] = struct{}{}
			}
		}
	}

	var tags []string
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

// tokenizeBuildExpr extracts identifiers from a Go build constraint expression.
var tagIdentRegex = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_.]*`)

func tokenizeBuildExpr(expr string) []string {
	return tagIdentRegex.FindAllString(expr, -1)
}

// extractSuiteName finds suite.Run calls and returns the suite struct name.
func extractSuiteName(f *ast.File) string {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			exprStmt, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := exprStmt.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if sel.Sel.Name == "Run" && len(call.Args) >= 2 {
				// Second arg is typically &SuiteName{} or new(SuiteName).
				switch arg := call.Args[1].(type) {
				case *ast.UnaryExpr: // &SuiteName{}
					if comp, ok := arg.X.(*ast.CompositeLit); ok {
						if ident, ok := comp.Type.(*ast.Ident); ok {
							return ident.Name
						}
					}
				case *ast.CallExpr: // new(SuiteName)
					if len(arg.Args) == 1 {
						if ident, ok := arg.Args[0].(*ast.Ident); ok {
							return ident.Name
						}
					}
				}
			}
		}
	}
	return ""
}

// extractTestFunctionsRegex is a fallback for files that can't be parsed by go/ast.
var testFuncRegex = regexp.MustCompile(`func\s+(Test\w+)\s*\(`)

func extractTestFunctionsRegex(content string) []string {
	matches := testFuncRegex.FindAllStringSubmatch(content, -1)
	var fns []string
	for _, m := range matches {
		fns = append(fns, m[1])
	}
	return fns
}

// extractSuiteNameRegex is a fallback suite name extractor.
var suiteRunRegex = regexp.MustCompile(`suite\.Run\([^,]+,\s*(?:new\((\w+)\)|&(\w+)\{\})`)

func extractSuiteNameRegex(content string) string {
	matches := suiteRunRegex.FindStringSubmatch(content)
	if len(matches) > 1 && matches[1] != "" {
		return matches[1]
	}
	if len(matches) > 2 && matches[2] != "" {
		return matches[2]
	}
	return ""
}

// buildSchemaByDir groups schema CaseMeta entries by their directory path
// relative to the validation root.
func buildSchemaByDir(cases []schema.CaseMeta, validationDir string) map[string][]schema.CaseMeta {
	byDir := map[string][]schema.CaseMeta{}
	for _, c := range cases {
		// SchemaFile is relative to validationDir; extract directory.
		dir := filepath.Dir(c.SchemaFile)
		// Normalize: remove trailing "/schemas" since test files are in parent.
		dir = strings.TrimSuffix(dir, "/schemas")
		dir = strings.TrimSuffix(dir, "\\schemas")
		byDir[dir] = append(byDir[dir], c)
	}
	return byDir
}

// associateSchemas links schema cases to test files based on their directory.
func associateSchemas(files []*discoveredTestFile, schemaByDir map[string][]schema.CaseMeta, validationDir string) {
	for _, tf := range files {
		dir := filepath.Dir(tf.RelPath)
		cases, ok := schemaByDir[dir]
		if !ok {
			continue
		}

		tf.schemaCases = cases

		// Determine the schema file path and projects.
		projectSet := map[string]struct{}{}
		var schemaFile string
		for _, c := range cases {
			for _, p := range c.Projects {
				projectSet[p] = struct{}{}
			}
			if schemaFile == "" {
				schemaFile = c.SchemaFile
			}
		}

		if schemaFile != "" {
			tf.QaseSchema = &schemaFile
		}
		for p := range projectSet {
			tf.QaseProjects = append(tf.QaseProjects, p)
		}
		sort.Strings(tf.QaseProjects)

		// Build QaseCases from schema metadata. CaseMeta.CaseID is already populated
		// by schema.ResolveIDs() before associateSchemas is called.
		for _, c := range cases {
			atn := c.AutomationTestName
			if atn == "" {
				// For hostbusters schemas, title IS the automation test name.
				atn = c.Title
			}
			tf.QaseCases = append(tf.QaseCases, types.QaseCase{
				ID:                 c.CaseID,
				Title:              c.Title,
				AutomationTestName: atn,
			})
		}
	}
}

// resolveByTitleFallback iterates test files that have no QaseCases after schema
// association and attempts to match each TestFunction name against Qase case
// titles across all provided projects. Returns the total number of cases resolved.
//
// For each test file without QaseCases:
//   - Each test function name is looked up in every project's title→ID map.
//   - The first project match wins (projects are tried in the order given).
//   - Matched cases are appended to QaseCases with project association.
func resolveByTitleFallback(ctx context.Context, qaseClient *qase.Client, files []*discoveredTestFile, projects []string) int {
	if len(projects) == 0 {
		return 0
	}

	// Fetch title maps for all projects (lazily, only if we actually need them).
	titleMaps := map[string]map[string]int{}
	fetchTitleMap := func(project string) map[string]int {
		if m, ok := titleMaps[project]; ok {
			return m
		}
		logrus.Infof("Fetching Qase title map for project %s (title fallback)...", project)
		m, err := qaseClient.GetTitleMap(ctx, project)
		if err != nil {
			logrus.Warnf("Failed to fetch title map for project %s: %v", project, err)
			titleMaps[project] = map[string]int{} // cache empty to avoid re-fetch
			return titleMaps[project]
		}
		titleMaps[project] = m
		logrus.Infof("  %s: %d case titles indexed", project, len(m))
		return m
	}

	totalResolved := 0
	for _, tf := range files {
		if len(tf.QaseCases) > 0 || len(tf.TestFunctions) == 0 {
			continue
		}

		// Try each test function name against each project's title map.
		projectSet := map[string]struct{}{}
		for _, fn := range tf.TestFunctions {
			for _, project := range projects {
				tMap := fetchTitleMap(project)
				if caseID, ok := tMap[fn]; ok {
					tf.QaseCases = append(tf.QaseCases, types.QaseCase{
						ID:                 caseID,
						Title:              fn,
						AutomationTestName: fn,
					})
					projectSet[project] = struct{}{}
					totalResolved++
					break // first project match wins for this function
				}
			}
		}

		// Populate QaseProjects from matches found.
		if len(projectSet) > 0 {
			for p := range projectSet {
				tf.QaseProjects = appendUnique(tf.QaseProjects, p)
			}
			sort.Strings(tf.QaseProjects)
			logrus.Infof("Title fallback matched %d functions in %s → projects %v",
				len(tf.QaseCases), tf.RelPath, tf.QaseProjects)
		}
	}

	return totalResolved
}

// groupFeatureAreas organizes discovered test files into feature areas keyed
// by a normalized directory-based name (e.g. "nodescaling_rke2").
// tagToJob is the tag→Jenkins-job mapping from the active PipelineEnv.
func groupFeatureAreas(files []*discoveredTestFile, validationDir, actionsDir string, tagToJob map[string]string) map[string]types.FeatureArea {
	areas := map[string]*types.FeatureArea{}

	for _, tf := range files {
		areaKey := featureAreaKey(tf.RelPath)

		area, ok := areas[areaKey]
		if !ok {
			area = &types.FeatureArea{
				Description: featureAreaDescription(areaKey),
			}
			areas[areaKey] = area
		}

		testFile := types.TestFile{
			Path:          filepath.Join("validation", tf.RelPath),
			BuildTags:     tf.BuildTags,
			TestSuite:     tf.TestSuite,
			TestFunctions: tf.TestFunctions,
			QaseSchema:    tf.QaseSchema,
			QaseProjects:  tf.QaseProjects,
			QaseCases:     tf.QaseCases,
		}
		area.TestFiles = append(area.TestFiles, testFile)

		// Determine Jenkins jobs from build tags.
		for _, tag := range tf.BuildTags {
			area.PITTags = appendUnique(area.PITTags, tag)
		}
	}

	// Resolve actions packages and Jenkins jobs for each area.
	for key, area := range areas {
		area.ActionsPackages = findActionsPackages(key, actionsDir)
		area.JenkinsJobs = inferJenkinsJobs(area.PITTags, tagToJob)
		sort.Strings(area.PITTags)
	}

	// Convert to non-pointer map.
	result := make(map[string]types.FeatureArea, len(areas))
	for k, v := range areas {
		result[k] = *v
	}
	return result
}

// featureAreaKey returns a normalized feature area key from a relative test file path.
// e.g. "nodescaling/rke2/scaling_test.go" → "nodescaling_rke2"
func featureAreaKey(relPath string) string {
	dir := filepath.Dir(relPath)
	// Replace path separators with underscores.
	key := strings.ReplaceAll(dir, string(filepath.Separator), "_")
	key = strings.ReplaceAll(key, "/", "_")
	// Clean up.
	key = strings.Trim(key, "_")
	if key == "." || key == "" {
		key = "root"
	}
	return key
}

// featureAreaDescription generates a human-readable description from the area key.
func featureAreaDescription(key string) string {
	parts := strings.Split(key, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ") + " validation tests"
}

// findActionsPackages looks for matching action packages for a feature area.
func findActionsPackages(areaKey, actionsDir string) []string {
	if actionsDir == "" {
		return nil
	}

	// Try to find an actions/ subdirectory matching the first component.
	parts := strings.Split(areaKey, "_")
	var packages []string

	for _, part := range parts {
		candidate := filepath.Join(actionsDir, part)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			packages = appendUnique(packages, "actions/"+part+"/")
		}
	}

	return packages
}

// inferJenkinsJobs returns the Jenkins job names that match the given build
// tags, using the tag→job mapping from the active PipelineEnv.
func inferJenkinsJobs(tags []string, tagToJob map[string]string) []string {
	jobSet := map[string]struct{}{}
	for _, tag := range tags {
		if job, ok := tagToJob[tag]; ok {
			jobSet[job] = struct{}{}
		}
	}
	var jobs []string
	for j := range jobSet {
		jobs = append(jobs, j)
	}
	sort.Strings(jobs)
	return jobs
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	if slices.Contains(slice, s) {
		return slice
	}
	return append(slice, s)
}
