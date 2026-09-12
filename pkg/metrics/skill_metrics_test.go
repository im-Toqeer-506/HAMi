/*
Copyright 2026 The HAMi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The skill documents under skill/ name the metrics an operator is told to
// query, but metric names are plain string literals in the collectors, so
// renaming one leaves the documents stale with nothing to catch it. That has
// happened twice already, in #2685 and #2761.
//
// These tests compare the two sides. They live in this package rather than
// beside the documents because hack/unit-test.sh runs
// `go test $(go list ./pkg/... ./cmd/...)`, which does not reach skill/ or
// dashboards/.

// repoRoot is this package's path back to the repository root.
const repoRoot = "../.."

var (
	// documentedMetricPattern matches a metric name written as inline code.
	// Metric names in the skill documents are always in backticks, which is
	// what separates them from the skill's own identifier in the YAML front
	// matter and from family wildcards such as `hami_host_gpu_*`, whose
	// trailing character stops the name short.
	documentedMetricPattern = regexp.MustCompile("`(hami_[a-z0-9_]+)`")

	// registeredMetricPattern matches a metric name written as a Go string
	// literal, which is how every collector passes it to prometheus.NewDesc.
	registeredMetricPattern = regexp.MustCompile(`"(hami_[a-z0-9_]+)"`)
)

// documentedMetrics returns every metric name referenced by a skill document,
// mapped to the documents that reference it.
func documentedMetrics(t *testing.T) map[string][]string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(repoRoot, "skill", "*", "SKILL.md"))
	if err != nil {
		t.Fatalf("glob skill documents: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no skill documents found under %s/skill; has the directory moved?", repoRoot)
	}

	documented := map[string][]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		name := filepath.Base(filepath.Dir(path))
		for _, match := range documentedMetricPattern.FindAllStringSubmatch(string(data), -1) {
			metric := match[1]
			if !slices.Contains(documented[metric], name) {
				documented[metric] = append(documented[metric], name)
			}
		}
	}
	return documented
}

// registeredMetrics returns every metric name that appears as a string literal
// in non-test Go sources under cmd/ and this package, which is where the
// collectors declare them.
func registeredMetrics(t *testing.T) map[string]string {
	t.Helper()

	registered := map[string]string{}
	for _, dir := range []string{filepath.Join(repoRoot, "cmd"), filepath.Join(repoRoot, "pkg", "metrics")} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, match := range registeredMetricPattern.FindAllStringSubmatch(string(data), -1) {
				if _, seen := registered[match[1]]; !seen {
					registered[match[1]] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(registered) == 0 {
		t.Fatalf("no metric names found in the collectors; have they moved?")
	}
	return registered
}

// TestSkillDocsOnlyReferenceRegisteredMetrics fails when a skill document names
// a metric no collector declares, which is what a rename leaves behind.
func TestSkillDocsOnlyReferenceRegisteredMetrics(t *testing.T) {
	documented := documentedMetrics(t)
	registered := registeredMetrics(t)

	for _, metric := range slices.Sorted(maps.Keys(documented)) {
		if _, ok := registered[metric]; !ok {
			t.Errorf("skill document(s) %s reference metric %q, which no collector declares; "+
				"update the document, or the metric name if it was renamed",
				strings.Join(documented[metric], ", "), metric)
		}
	}
}

// TestRegisteredMetricsAreDocumented reports metrics no skill document mentions.
// It does not fail: not every metric belongs in an operator-facing summary, and
// failing here would block unrelated work that happens to add one.
func TestRegisteredMetricsAreDocumented(t *testing.T) {
	documented := documentedMetrics(t)
	registered := registeredMetrics(t)

	var undocumented []string
	for _, metric := range slices.Sorted(maps.Keys(registered)) {
		if _, ok := documented[metric]; !ok {
			undocumented = append(undocumented, metric)
		}
	}
	if len(undocumented) > 0 {
		t.Logf("%d registered metric(s) are not mentioned by any skill document: %s",
			len(undocumented), strings.Join(undocumented, ", "))
	}
}

// TestDocumentedMetricPattern pins the extraction rule against the two shapes
// that are not metric references. Both appear in
// skill/hami-vgpu-metrics-summary/SKILL.md today and a bare hami_[a-z_]+ scan
// reports both as undeclared metrics.
func TestDocumentedMetricPattern(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "inline code reference",
			input: "- `hami_gpu_shared_count`\n",
			want:  []string{"hami_gpu_shared_count"},
		},
		{
			name:  "table cell reference",
			input: "| `hami_vgpu_memory_used_bytes` / `vGPU_device_memory_usage_in_bytes` | used | runtime |\n",
			want:  []string{"hami_vgpu_memory_used_bytes"},
		},
		{
			name:  "front matter skill name is not a metric",
			input: "---\nname: hami_vgpu_metrics_summarizer\n---\n",
			want:  nil,
		},
		{
			name:  "family wildcard is not a metric name",
			input: "| only `hami_host_gpu_*` present | runtime monitor metrics only |\n",
			want:  nil,
		},
		{
			name:  "prose mention without backticks is ignored",
			input: "The hami_gpu_shared_count value shows sharing density.\n",
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, match := range documentedMetricPattern.FindAllStringSubmatch(test.input, -1) {
				got = append(got, match[1])
			}
			if len(got) != len(test.want) {
				t.Fatalf("extracted %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("extracted[%d] = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}

// TestRegisteredMetricPattern pins the collector-side rule: a name counts as
// declared only where it appears as a Go string literal, so a mention in a
// comment does not make a stale document pass.
func TestRegisteredMetricPattern(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "prometheus desc argument",
			input: "prometheus.NewDesc(\n\t\"hami_gpu_memory_limit_bytes\",\n\t\"Device memory limit\",\n)",
			want:  []string{"hami_gpu_memory_limit_bytes"},
		},
		{
			name:  "unquoted comment mention is ignored",
			input: "// hami_gpu_shared_count is emitted per device.\n",
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, match := range registeredMetricPattern.FindAllStringSubmatch(test.input, -1) {
				got = append(got, match[1])
			}
			if len(got) != len(test.want) {
				t.Fatalf("extracted %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("extracted[%d] = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}
