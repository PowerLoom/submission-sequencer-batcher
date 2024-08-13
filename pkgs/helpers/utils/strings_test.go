package utils

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestSortStringsByValue(t *testing.T) {
	tests := []struct {
		name     string
		keys     []string
		index    int
		order    int
		response []string
	}{
		{
			name:     "Valid keys index 1 Descending",
			keys:     []string{"Prefix.31", "Prefix.27", "Prefix.17", "Prefix.21", "Prefix.1"},
			index:    1,
			order:    DESCENDING,
			response: []string{"Prefix.31", "Prefix.27", "Prefix.21", "Prefix.17", "Prefix.1"},
		},
		{
			name:     "Valid keys index 2 - duplicate Descending",
			keys:     []string{"Prefix.x.31", "Prefix.x.27", "Prefix.x.17", "Prefix.x.27", "Prefix.x.1"},
			index:    2,
			order:    DESCENDING,
			response: []string{"Prefix.x.31", "Prefix.x.27", "Prefix.x.27", "Prefix.x.17", "Prefix.x.1"},
		},
		{
			name:     "Valid keys index 1 Ascending",
			keys:     []string{"Prefix.31", "Prefix.27", "Prefix.17", "Prefix.21", "Prefix.1"},
			index:    1,
			order:    ASCENDING,
			response: []string{"Prefix.1", "Prefix.17", "Prefix.21", "Prefix.27", "Prefix.31"},
		},
		{
			name:     "Valid keys index 2 - duplicate Ascending",
			keys:     []string{"Prefix.x.31", "Prefix.x.27", "Prefix.x.17", "Prefix.x.27", "Prefix.x.1"},
			index:    2,
			order:    ASCENDING,
			response: []string{"Prefix.x.1", "Prefix.x.17", "Prefix.x.27", "Prefix.x.27", "Prefix.x.31"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SortKeysByValue(tt.keys, tt.index, tt.order)
			assert.EqualValues(t, tt.response, tt.keys)
		})
	}
}
