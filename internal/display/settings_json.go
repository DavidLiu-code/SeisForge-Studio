package display

import "encoding/json"

// A missing format uses today's default. An explicitly saved legacy zero
// remains AutoFormat; decoding must not infer user intent from its number.
func (a *Axis) UnmarshalJSON(data []byte) error {
	type wire Axis
	v := wire{Format: DefaultNumberFormat}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*a = Axis(v)
	return nil
}

func (m *ColorMapping) UnmarshalJSON(data []byte) error {
	type wire ColorMapping
	v := wire{Format: DefaultNumberFormat}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*m = ColorMapping(v)
	return nil
}
