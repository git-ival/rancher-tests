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
or S3 backend. Existing Jenkins jobs receive the config as inline `CONFIG` or
`CATTLE_TEST_CONFIG`. Jobs may instead declare URL and checksum bindings.
Local publications are written to `paths.outputs.publishedEnvironments`.

The current qa-infra integration generates upstream Terraform and Ansible input
files but does not execute them. The reusable in-repo provisioner requires a
`testing.T` and does not return the state needed for resumable cleanup. Production
provisioning must remain in the Jenkins provisioner jobs until qa-infra exposes a
context-based provision/destroy API with a persistent resource handle.

Secrets remain in environment variables or Jenkins credentials. Do not put them
in the YAML config or published bundles.
