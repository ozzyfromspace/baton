package config

import "strings"

// DefaultReviewModel is the model of baton's reviewer of background tasks when review_model is not set: Sonnet,
// at its API's default effort, high, so that the reviewer can judge a task rather than hedge on it.
const DefaultReviewModel = "sonnet"

// ReviewsTasks reports task_review: the environment's BATON_TASK_REVIEW (0, false, off; or 1, true, on) wins,
// then the file, then on.
func (c Config) ReviewsTasks(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env("BATON_TASK_REVIEW"))) {
	case "0", "false", "off", "no":
		return false
	case "1", "true", "on", "yes":
		return true
	}
	return c.TaskReview == nil || *c.TaskReview
}

// ReviewModelFor reports review_model: the environment's BATON_REVIEW_MODEL wins, then the file, then
// DefaultReviewModel.
func (c Config) ReviewModelFor(env func(string) string) string {
	if m := strings.TrimSpace(env("BATON_REVIEW_MODEL")); m != "" {
		return m
	}
	if m := strings.TrimSpace(c.ReviewModel); m != "" {
		return m
	}
	return DefaultReviewModel
}
