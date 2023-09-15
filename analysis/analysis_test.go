package analysis

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/konveyor/go-konveyor-tests/hack/addon"
	"github.com/konveyor/go-konveyor-tests/hack/uniq"
	"github.com/konveyor/tackle2-hub/api"
	"github.com/konveyor/tackle2-hub/binding"
	"github.com/konveyor/tackle2-hub/test/assert"
)

// Test application analysis
func TestApplicationAnalysis(t *testing.T) {
	// Find right test cases for given Tier (include Tier0 always).
	testCases := Tier0TestCases
	_, tier1 := os.LookupEnv("TIER1")
	if tier1 {
		testCases = Tier1TestCases
	}
	_, tier2 := os.LookupEnv("TIER2")
	if tier2 {
		testCases = Tier2TestCases
	}
	// Run test cases.
	for _, testcase := range testCases {
		t.Run(testcase.Name, func(t *testing.T) {
			// Prepare parallel execution if env variable PARALLEL is set.
			tc := testcase
			_, parallel := os.LookupEnv("PARALLEL")
			if parallel {
				t.Parallel()
			}

			// Create the application.
			uniq.ApplicationName(&tc.Application)
			assert.Should(t, RichClient.Application.Create(&tc.Application))

			// Prepare custom rules.
			for i := range tc.CustomRules {
				r := &tc.CustomRules[i]
				uniq.RuleSetName(r)
				// ruleFiles := []api.File{}
				rules := []api.Rule{}
				for _, rule := range r.Rules {
					ruleFile, err := RichClient.File.Put(rule.File.Name)
					assert.Should(t, err)
					rules = append(rules, api.Rule{
						File: &api.Ref{
							ID: ruleFile.ID,
						},
					})
					// ruleFiles = append(ruleFiles, *ruleFile)
				}
				r.Rules = rules
				assert.Should(t, RichClient.RuleSet.Create(r))
			}

			// Prepare and submit the analyze task.
			// tc.Task.Addon = analyzerAddon
			tc.Task.Application = &api.Ref{ID: tc.Application.ID}
			taskData := tc.Task.Data.(addon.Data)
			//for _, r := range tc.CustomRules {
			//	taskData.Rules = append(taskData.Rules, api.Ref{ID: r.ID, Name: r.Name})
			//}
			if len(tc.Sources) > 0 {
				taskData.Sources = tc.Sources
			}
			if len(tc.Targets) > 0 {
				taskData.Targets = tc.Targets
			}
			if len(tc.Labels.Included) > 0 || len(tc.Labels.Excluded) > 0 {
				taskData.Rules.Labels = tc.Labels
			}
			if tc.Rules.Path != "" { // TODO: better rules handling
				taskData.Rules = tc.Rules
			}
			if tc.WithDeps == true {
				taskData.Mode.WithDeps = true
			}
			tc.Task.Data = taskData
			assert.Should(t, RichClient.Task.Create(&tc.Task))

			// Wait until task finishes
			var task *api.Task
			var err error
			for i := 0; i < Retry; i++ {
				task, err = RichClient.Task.Get(tc.Task.ID)
				if err != nil || task.State == "Succeeded" || task.State == "Failed" {
					break
				}
				time.Sleep(Wait)
			}

			if task.State != "Succeeded" {
				t.Errorf("Analyze Task failed. Details: %+v", task)
			}

			var gotAppAnalyses []api.Analysis
			var gotAnalysis api.Analysis

			// Get LSP analysis directly form Hub API
			analysisPath := binding.Path(api.AppAnalysesRoot).Inject(binding.Params{api.ID: tc.Application.ID})
			assert.Should(t, Client.Get(analysisPath, &gotAppAnalyses))
			analysisDetailPath := binding.Path(api.AnalysisRoot).Inject(binding.Params{api.ID: gotAppAnalyses[len(gotAppAnalyses)-1].ID})
			assert.Should(t, Client.Get(analysisDetailPath, &gotAnalysis))

			_, debug := os.LookupEnv("DEBUG")
			if debug {
				DumpAnalysis(t, tc, gotAnalysis)
			}

			// Check the analysis result (effort, issues, etc).
			if gotAnalysis.Effort != tc.Analysis.Effort {
				t.Errorf("Different effort error. Got %d, expected %d", gotAnalysis.Effort, tc.Analysis.Effort)
			}

			// Check the analysis issues
			if len(gotAnalysis.Issues) != len(tc.Analysis.Issues) {
				t.Errorf("Different amount of issues error. Got %d, expected %d.", len(gotAnalysis.Issues), len(tc.Analysis.Issues))
			}

			// Ensure stable order of Issues.
			sort.SliceStable(gotAnalysis.Issues, func(a, b int) bool { return gotAnalysis.Issues[a].Rule < gotAnalysis.Issues[b].Rule })
			sort.SliceStable(tc.Analysis.Issues, func(a, b int) bool { return tc.Analysis.Issues[a].Rule < tc.Analysis.Issues[b].Rule })

			for i, got := range gotAnalysis.Issues {
				expected := tc.Analysis.Issues[i]
				if got.Category != expected.Category || got.RuleSet != expected.RuleSet || got.Rule != expected.Rule || got.Effort != expected.Effort || !strings.HasPrefix(got.Description, expected.Description) {
					t.Errorf("\nDifferent issue error. Got %+v, expected %+v.\n\n", got, expected)
				}

				// Incidents check.
				if len(expected.Incidents) == 0 {
					t.Log("Skipping empty expected Incidents check.")
					break
				}
				if len(got.Incidents) != len(expected.Incidents) {
					t.Errorf("Different amount of incident error. Got %d, expected %d.", len(got.Incidents), len(expected.Incidents))
				} else {
					// Ensure stable order of Incidents.
					sort.SliceStable(got.Incidents, func(a, b int) bool { return got.Incidents[a].File < got.Incidents[b].File })
					sort.SliceStable(expected.Incidents, func(a, b int) bool { return expected.Incidents[a].File < expected.Incidents[b].File })
					for j, gotInc := range got.Incidents {
						expectedInc := expected.Incidents[j]
						if gotInc.File != expectedInc.File || gotInc.Line != expectedInc.Line || !strings.HasPrefix(gotInc.Message, expectedInc.Message) {
							t.Errorf("\nDifferent incident error. Got %+v, expected %+v.\n\n", gotInc, expectedInc)
						}
					}
				}
			}

			// Check the dependencies.
			if len(gotAnalysis.Dependencies) != len(tc.Analysis.Dependencies) {
				t.Errorf("Different amount of dependencies error. Got %d, expected %d.", len(gotAnalysis.Dependencies), len(tc.Analysis.Dependencies))
			}

			// Ensure stable order of Issues.
			sort.SliceStable(gotAnalysis.Dependencies, func(a, b int) bool { return gotAnalysis.Dependencies[a].Name < gotAnalysis.Dependencies[b].Name })
			sort.SliceStable(tc.Analysis.Dependencies, func(a, b int) bool { return tc.Analysis.Dependencies[a].Name < tc.Analysis.Dependencies[b].Name })

			for i, got := range gotAnalysis.Dependencies {
				expected := tc.Analysis.Dependencies[i]
				if got.Name != expected.Name || got.Version != expected.Version || got.Provider != expected.Provider {
					t.Errorf("\nDifferent dependency error. Got %+v, expected %+v.\n\n", got, expected)
				}
			}

			// Check analysis-created Tags.
			gotApp, _ := RichClient.Application.Get(tc.Application.ID)
			for _, expected := range tc.AnalysisTags {
				found := false
				for _, got := range gotApp.Tags {
					if got.Name == expected.Name && got.Source == "Analysis" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Missing expected tag '%s'.\n", expected.Name)
				}
			}

			if debug {
				DumpTags(t, tc, *gotApp)
			}


			// TODO(maufart): analysis tagger creates duplicate tags, not sure if it is expected, check later.
			//if len(tc.AnalysisTags) != len(gotApp.Tags) {
			//	t.Errorf("Different Tags amount error. Got: %d, expected: %d.\n", len(gotApp.Tags), len(tc.AnalysisTags))
			//}
			//found, gotAnalysisTags := 0, 0
			//for _, t := range gotApp.Tags {
			//	if t.Source == "Analysis" {
			//		gotAnalysisTags = gotAnalysisTags + 1
			//		for _, expectedTag := range tc.AnalysisTags {
			//			if expectedTag.Name == t.Name {
			//				found = found + 1
			//				break
			//			}
			//		}
			//	}
			//}
			//if found != len(tc.AnalysisTags) || found < gotAnalysisTags {
			//	t.Errorf("Analysis Tags don't match. Got:\n  %v\nexpected:\n  %v\n", gotApp.Tags, tc.AnalysisTags)
			//}

			// Allow skip cleanup to keep applications and analysis results for debugging etc.
			_, keep := os.LookupEnv("KEEP")
			if keep {
				return
			}
			// Cleanup Application.
			assert.Must(t, RichClient.Application.Delete(tc.Application.ID))

			// Cleanup custom rules and their files.
			for _, r := range tc.CustomRules {
				assert.Should(t, RichClient.RuleSet.Delete(r.ID))
				for _, rl := range r.Rules {
					assert.Should(t, RichClient.File.Delete(rl.File.ID))
				}
			}
		})
	}
}
