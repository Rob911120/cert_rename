package server

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/v2/ai"
	"cert-renamer/internal/v2/monitorsync"
)

// judge bygger AI-parbedömaren för en sync-körning, eller nil utan API-nyckel
// (domen fylls i när nyckeln finns — syncen i övrigt fungerar ändå).
func (s *Server) judge() monitorsync.Judge {
	cfg := s.Config()
	if cfg.ApiKey == "" {
		return nil
	}
	client := anthropic.NewClient(option.WithAPIKey(cfg.ApiKey))
	return &claudeJudge{client: &client, log: s}
}

type claudeJudge struct {
	client *anthropic.Client
	log    ai.Logger
}

func (j *claudeJudge) ClassifyUpcoming(ctx context.Context, in ai.UpcomingClassifyInput) (*ai.UpcomingClassification, error) {
	return ai.ClassifyUpcoming(ctx, j.log, j.client, in)
}

func (j *claudeJudge) ParseRequirements(ctx context.Context, in ai.RequirementsInput) (*ai.ArticleRequirements, error) {
	return ai.ParseRequirements(ctx, j.log, j.client, in)
}
