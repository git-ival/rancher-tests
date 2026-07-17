package cmd

import (
	"testing"

	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestResolveJobDefaultsUsesExactReference(t *testing.T) {
	job := &types.JobMapping{Defaults: "wanted"}
	resolveJobDefaults(job, map[string]*jjbDefaults{
		"other":  {Name: "other", Folder: "wrong", Jenkinsfile: "wrong"},
		"wanted": {Name: "wanted", Folder: "rancher_qa", Jenkinsfile: "validation/Jenkinsfile.e2e"},
	})
	if job.Folder != "rancher_qa" || job.Jenkinsfile != "validation/Jenkinsfile.e2e" {
		t.Fatalf("job = %#v", job)
	}
}
