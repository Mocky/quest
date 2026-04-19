package output

import (
	"bytes"
	"encoding/json"
)

// OrderedRow preserves column order when emitting a dynamic-column
// JSON object. quest list --columns determines the field set and
// order at runtime, and json.Marshal on a map[string]any sorts keys
// alphabetically — so a custom MarshalJSON that iterates Columns in
// the supplied order is the only way to honor the spec §quest list
// row-shape rules.
//
// Columns names each emitted key in output order; Values maps each
// column name to its encoded value. Keys listed in Columns but
// absent from Values emit JSON null, matching the spec's "null /
// [] / {} never omitted" contract.
type OrderedRow struct {
	Columns []string
	Values  map[string]any
}

// MarshalJSON emits a flat JSON object with keys in Columns order.
func (r OrderedRow) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, col := range r.Columns {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyBytes, err := json.Marshal(col)
		if err != nil {
			return nil, err
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')
		valBytes, err := json.Marshal(r.Values[col])
		if err != nil {
			return nil, err
		}
		buf.Write(valBytes)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
