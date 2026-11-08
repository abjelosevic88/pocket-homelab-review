package server

import (
	"reflect"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

func TestMentioned(t *testing.T) {
	people := []store.Participant{
		{UserID: 1, Name: "Aleksandar Bjelosevic", Email: "ab@example.com"},
		{UserID: 2, Name: "Sani Hodžić", Email: "sani@example.com"},
		{UserID: 3, Name: "Ana Kovač", Email: "ana@example.com"},
		{UserID: 4, Name: "Ana Petrović", Email: "ana.p@example.com"},
	}
	cases := []struct {
		text string
		want []int64
	}{
		{"no mention here", nil},
		{"@sani can you check?", []int64{2}},
		{"Thanks @Sani Hodžić, and @aleksandar.", []int64{2, 1}},
		{"@Ana Petrović, not @Anastasia", []int64{4}},
		{"@ana is her email name", []int64{3}},
		{"mail sani@example.com about it", nil},
		{"@sanity check", nil},
		{"@everyone please look", []int64{1, 2, 3, 4}},
		{"(@ana.p) and @ab", []int64{4, 1}},
	}
	for _, c := range cases {
		if got := mentioned(c.text, people); !reflect.DeepEqual(got, c.want) {
			t.Errorf("mentioned(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
