package bridge

import "slices"

// AnswerCut is the most of a model's answer a complete command's ack carries, in characters, as the mod
// cuts it (ANSWER_CUT in plugin/hooks/baton.js); a longer one ends in "…".
const AnswerCut = 4000

// MaxAnswers bounds the acks of complete commands the record keeps for the hosts that issued them.
const MaxAnswers = 50

// Usage is what one model call used, as the API counts it ($.model.complete's usage).
type Usage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

// Answer takes the ack of instance's complete command id, once the mod has made it: the model's answer, or
// why there is none. ok is false while there is none yet. The record is looked at without the lock first,
// so that a host waiting for an answer takes the lock only to take it.
func (b *Bridge) Answer(instance string, id int) (rep Report, ok bool, err error) {
	is := func(a Report) bool { return a.Command == id && a.Instance == instance }
	rec, err := b.Peek()
	if err != nil || !slices.ContainsFunc(rec.Answers, is) {
		return Report{}, false, err
	}
	_, err = b.update(func(r *Record) error {
		i := slices.IndexFunc(r.Answers, is)
		if ok = i >= 0; ok {
			rep = r.Answers[i]
			r.Answers = slices.Delete(r.Answers, i, i+1)
		}
		return nil
	})
	if err != nil {
		return Report{}, false, err
	}
	return rep, ok, nil
}
