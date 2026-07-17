# Agentic QA Workflow

Run the resumable workflow with:

```bash
agentic-qa --config agentic-qa.yaml run
```

Relative paths are resolved from the config file directory. On first run,
missing `pipelineEnv`, `triageFramework`, `additionalContext`, and `chartsDir`
inputs are optional. Missing feature and trigger mappings are generated from
`validationDir` and `jjbDir`; their output paths must be configured.

Preflight validates required credentials, source PR, repository directories,
and output paths before creating Qase runs or triggering Jenkins jobs.

The workflow checkpoints these stages in `pipeline_state.json`:

1. `identify`
2. `plan-environment`
3. `setup-env`
4. `trigger`
5. `wait`
6. `analyze`
7. `defects`
8. `config-failures`
9. `cleanup`, when enabled

`setup-env` publishes each generated cattle config through the configured local
or S3 backend. `trigger` sends the selected tests to `jenkins.environmentJob`.
That job checks out `tests.repoUrl` at `tests.branch` and `qaInfra.repoUrl` at
`qaInfra.branch`, then owns infrastructure, Rancher, downstream-cluster setup,
test execution, and teardown.
Local publications are written to `paths.outputs.publishedEnvironments`.

Agentic QA does not provision Rancher directly. It plans topology and passes the
appropriate parameters to the configured Jenkins job, which runs
qa-infra-automation.

Secrets remain in environment variables or Jenkins credentials. Do not put them
in the YAML config or published bundles.
