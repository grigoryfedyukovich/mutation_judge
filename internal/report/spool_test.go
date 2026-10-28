package report

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/resultspool"
)

func TestSpoolRenderEquivalentJSONAndText(t *testing.T) {
	cases := []int{0, 1, 5}
	for _, n := range cases {
		t.Run(strings.Repeat("r", n+1), func(t *testing.T) {
			r := model.Report{SchemaVersion: model.SchemaVersion, ToolVersion: "test", Complete: true, Patterns: []string{"."}, Bounds: map[string]any{"completed_mutants": n}, EffectiveConfig: map[string]any{}, Results: []model.Result{}}
			spool, err := resultspool.New()
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			for i := 0; i < n; i++ {
				v := model.VerdictSurvived
				if i%2 == 0 {
					v = model.VerdictKilled
				}
				x := model.Result{Verdict: v, Mutation: model.Mutation{ID: string(rune('a' + i)), Span: model.Span{File: "p.go", StartLine: i + 1, StartCol: 1}, Description: "special <> \\n and ☺", Diff: "--- a/p.go\n+++ b/p.go\n"}, Diagnostic: model.Diagnostic{Evidence: map[string]any{"message": "quoted \" x"}}}
				r.Results = append(r.Results, x)
				if err := spool.Append(x); err != nil {
					t.Fatal(err)
				}
			}
			for _, format := range []string{"json", "text"} {
				var regular, streamed bytes.Buffer
				if err := Render(&regular, format, r); err != nil {
					t.Fatal(err)
				}
				if err := renderSpool(&streamed, format, r, spool); err != nil {
					t.Fatal(err)
				}
				if format == "text" {
					if regular.String() != streamed.String() {
						t.Fatalf("text diverged\n%s\n\n%s", regular.String(), streamed.String())
					}
				} else {
					var got, want any
					if err := json.Unmarshal(regular.Bytes(), &want); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(streamed.Bytes(), &got); err != nil {
						t.Fatalf("invalid streamed JSON: %v\n%s", err, streamed.String())
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("JSON semantics differ: got %s want %s", streamed.String(), regular.String())
					}
				}
			}
		})
	}
}
