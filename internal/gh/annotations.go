package gh

import (
	"context"
	"fmt"

	"github.com/akira-toriyama/cifail/internal/model"
)

// apiAnnotation is the Checks-API annotation shape.
type apiAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
}

// Annotations fetches the Checks annotations for a job. A workflow job maps to a
// check run of the SAME id, so the check-run annotations endpoint takes the job
// id directly. These are free and tiny but sparse (present only when a problem
// matcher fired), so this is best-effort: on any error it returns nil rather
// than failing the whole extraction.
func (c *Client) Annotations(ctx context.Context, jobID int64) []model.Annotation {
	var raw []apiAnnotation
	if err := c.getJSON(ctx, c.repoPath(fmt.Sprintf("/check-runs/%d/annotations", jobID)), &raw); err != nil {
		return nil
	}
	out := make([]model.Annotation, 0, len(raw))
	for _, a := range raw {
		out = append(out, model.Annotation{
			Level:     a.AnnotationLevel,
			Message:   a.Message,
			Path:      a.Path,
			StartLine: a.StartLine,
			EndLine:   a.EndLine,
			Title:     a.Title,
		})
	}
	return out
}
