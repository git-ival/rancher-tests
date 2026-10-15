# Agentic QA

`agentic-qa` selects tests for a Rancher pull request, plans their environments,
starts Jenkins test jobs, and processes results. Jenkins' configured environment
job owns infrastructure setup, test execution, and teardown. The CLI coordinates
the workflow; it does not provision Rancher directly.

## Build and configure

From the repository root, build the CLI:

```sh
go build -o agentic-qa ./cmd/agentic-qa
```

Copy [`agentic-qa.example.yaml`](../../agentic-qa.example.yaml) to a private
working location and configure the pull request, service URLs, repository
branches, input and output paths, and execution options. Relative paths resolve
from the configuration file's directory. Keep credentials in environment
variables or Jenkins credentials, not in YAML.

The CLI uses `GITHUB_TOKEN`, `QASE_API_TOKEN`, `JENKINS_TOKEN`, and credentials
for the selected LLM provider. Vertex AI uses
`GOOGLE_APPLICATION_CREDENTIALS` and a Vertex project; Claude Direct uses
`CLAUDE_API_KEY`. Jenkins URL and user can come from the configuration or
`JENKINS_URL` and `JENKINS_USER`.

Check service access before running:

```sh
./agentic-qa --config agentic-qa.yaml validate
```

See [`AGENTIC_QA_WORKFLOW.md`](../../AGENTIC_QA_WORKFLOW.md) for configuration
and preflight details.

## Expected sequence

For a standard run, use `run`:

```sh
./agentic-qa --config agentic-qa.yaml run
```

It checks inputs and credentials, generates missing feature and trigger maps
when configured source directories exist, then runs these stages in order:

1. **Identify** — compare pull request changes with the feature map and select
   relevant tests.
2. **Plan environment** — infer required cluster topology and generate the
   environment inputs for selected tests.
3. **Setup environment** — publish generated environment configuration for
   Jenkins to consume. This stage does not create clusters.
4. **Trigger** — create Qase test runs and start the configured Jenkins
   environment job with selected tests and environment references.
5. **Wait** — poll Jenkins until builds finish and record results.
6. **Analyze** — classify failures using known patterns and LLM analysis.
7. **Handle outcomes** — `defects` processes product and test defects;
   `config-failures` handles environment failures, including reruns or guard
   generation.
8. **Cleanup** — run only when `execution.cleanupAfterRun` is enabled. Cleanup
   removes tracked artifacts and Qase records and closes tracked GitHub issues
   and pull requests.

The workflow saves stage status in `paths.outputs.state`. A later `run` skips
successful stages with valid output. Use `--from <stage>` to rerun that stage and
later stages, or `--force` to rerun all stages. Do not combine these flags.

## Commands

| Command | What it does |
| --- | --- |
| `generate-pipeline-env` | Write a template of organization-specific settings for planning, Qase, tags, and Jenkins. Review and customize before use. |
| `generate-feature-map` | Scan validation tests and schemas, resolve Qase cases, and map test metadata to features. |
| `generate-trigger-map` | Read Jenkins Job Builder YAML and map test tags to Jenkins jobs. |
| `validate` | Check connectivity and credentials for GitHub, Qase, Jenkins, and the selected LLM provider. |
| `identify` | Analyze a pull request and select relevant tests using the feature map and LLM. |
| `plan-environment` | Determine environment requirements for selected tests and generate environment inputs. |
| `setup-env` | Publish planned environment inputs and save references for Jenkins. Does not provision clusters. |
| `trigger` | Create Qase test run(s) and start the mapped Jenkins environment job. |
| `wait` | Poll Jenkins builds until completion and record their results. |
| `analyze` | Classify failures using known patterns and LLM analysis. |
| `defects` | Process product and test defects; options can create GitHub issues, Qase defects, and fix pull requests. |
| `config-failures` | Process environment/configuration failures, such as rerunning tests or generating guard changes. |
| `cleanup` | Remove pipeline-tracked artifacts and Qase records, and close tracked GitHub issues and pull requests. |
| `run` | Run or resume workflow stages in order using configuration and saved state. |
| `completion` | Generate shell completion scripts supported by Cobra. |

Commands can also run individually for debugging or reprocessing. Each stage
consumes files produced by earlier stages; use configured paths or pass command
flags. Use `./agentic-qa --help` or `./agentic-qa <command> --help` for flags and
command-specific options.

## Outputs and execution modes

Configured output paths hold intermediate JSON files, generated environment
inputs, state, and final summary. Jenkins checks out the configured tests and
QA infrastructure repositories, provisions the required infrastructure, runs
tests, and tears infrastructure down.

`--dry-run` suppresses external side effects where supported. `--local-test`
creates marked test artifacts and skips Jenkins triggers. Check command help
for exact behavior. Use `cleanup` with care: outside dry-run mode, it deletes or
closes resources tracked by pipeline state.
