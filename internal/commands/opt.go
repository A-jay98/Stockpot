package commands

import "encoding/json"

// Opt is an optional, nullable payload field: absent leaves the column as
// it is, null clears it, a value sets it. (A plain pointer can't tell the
// first two apart.)
type Opt[T any] struct {
	Set   bool
	Value *T // nil when set to null
}

// UnmarshalJSON implements json.Unmarshaler; it only runs for present fields.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// MarshalJSON renders null for an unset or cleared field.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}
